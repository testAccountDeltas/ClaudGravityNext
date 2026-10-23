// Package accounts is a port of superrelay (v1)'s auth_manager.go +
// selector.go + tiering.go, merged per
// ARCHITECTURE_V2_SPECIFICATION_AND_MIGRATION_PLAN.md Part 6.5: those
// three v1 files independently implement one life cycle (a pool of
// Google accounts -> each with OAuth state and quotas -> pick one for
// an incoming request and a model tier). tiering.go's logic is about
// the REQUEST, not the account pool, so it stays its own subpackage
// (internal/accounts/tiering); the rest merges into this package across
// 8 files by responsibility: pool.go (this file — AccountManager/Record,
// lifecycle), oauth.go (OAuth refresh/exchange), quota.go (quota
// fetch/refresh), projectid.go (GCP project discovery), catalog.go
// (model catalog), selector.go (account selection), affinity.go
// (session-sticky LRU).
//
// Three real dependency fixes, all found by attempting this port, none
// of them scope creep:
//  1. v1's httpClient (server.go's package-level fingerprinted client) is
//     now this package's OWN client, built from the already-ported
//     internal/tlsfingerprint — a legitimate dependency (accounts really
//     does talk to Google directly), unlike the incidental couplings
//     fixed elsewhere in this migration.
//  2. v1's atomicWriteFile (config.go) is now internal/atomicfile — see
//     that package's doc comment.
//  3. v1's globalWSHub.BroadcastAccountDeleted/BroadcastAccounts calls are
//     now routed through a Broadcaster interface (this file), the same
//     dependency-inversion pattern already applied to internal/requestlog
//     in Phase 1 — accounts must not import internal/dashboard (the
//     dashboard displays accounts, not the reverse).
package accounts

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"claudgravity/internal/atomicfile"
	"claudgravity/internal/config"
	"claudgravity/internal/logx"
	"claudgravity/internal/tlsfingerprint"
)

// httpClient is this package's own Google-facing client — see this
// file's doc comment for why it's a legitimate, dedicated dependency on
// internal/tlsfingerprint rather than a shared global reached across
// package boundaries.
var httpClient = &http.Client{
	Transport: tlsfingerprint.NewFingerprintedRoundTripper(),
}

// SetHTTPClientForTest temporarily overrides the package-level httpClient for testing.
func SetHTTPClientForTest(c *http.Client) func() {
	orig := httpClient
	httpClient = c
	return func() { httpClient = orig }
}

const fallbackProjectID = "aicode-consumers"

type QuotaBucket struct {
	BucketID          string  `json:"bucket_id"`
	DisplayName       string  `json:"display_name"`
	Window            string  `json:"window"`
	ResetTime         string  `json:"reset_time"`
	RemainingFraction float64 `json:"remaining_fraction"`
	Description       string  `json:"description"`
}

type QuotaGroup struct {
	DisplayName string        `json:"display_name"`
	Description string        `json:"description"`
	Buckets     []QuotaBucket `json:"buckets"`
}

// Record is a port of v1's AccountRecord, renamed per Go convention once
// it's no longer disambiguated from anything else by an "Account" prefix
// within its own package (callers spell it accounts.Record).
type Record struct {
	Email           string `json:"email"`
	ProjectID       string `json:"project_id"`
	ProjectIDProbed bool   `json:"project_id_probed"` // discovered via loadCodeAssist/onboardUser (or confirmed), not just the generic fallback — never re-probe once true
	// Never serialized: GetAccounts()/GetAccount() snapshots of this struct
	// go straight out over /api/accounts and the dashboard websocket to
	// anyone with the dashboard password — these are long-lived Google
	// OAuth credentials, not display data, and nothing reads them back
	// through JSON (on-disk persistence builds its own anonymous map, it
	// never marshals Record itself).
	RefreshToken  string       `json:"-"`
	AccessToken   string       `json:"-"`
	ExpiresAt     time.Time    `json:"expires_at"`
	Disabled      bool         `json:"disabled"`
	FilePath      string       `json:"-"`
	Quotas        []QuotaGroup `json:"quotas"`
	CooldownUntil time.Time    `json:"cooldown_until"` // do not route requests until this time if quota exhausted
	LastChecked   time.Time    `json:"last_checked"`
	LastError     string       `json:"last_error"`
	ErrorKind     string       `json:"error_kind"` // see health.go
	ErrorDetail   string       `json:"error_detail"`
	ErrorAt       time.Time    `json:"error_at"`
	ErrorUntil    time.Time    `json:"error_until"`

	// ModelCooldowns: per-(account, model) cooldowns from upstream 429s
	// (cooldown.go). hardCooldownUntil: the error-driven part of
	// CooldownUntil that FetchQuota must not clear while still in the future.
	ModelCooldowns    map[string]time.Time `json:"model_cooldowns,omitempty"`
	hardCooldownUntil time.Time

	// refreshMu serializes RefreshAccessToken calls for this one account:
	// without it, N concurrent requests on the same account (e.g. parallel
	// subagents) each saw a pre-refresh AccessToken and all fired their own
	// token-refresh HTTP round-trip. Holding this for the whole call means
	// only the first caller hits the network; the rest block, then see the
	// now-cached token and skip the request entirely.
	refreshMu sync.Mutex
}

type AccountManager struct {
	mu       sync.RWMutex
	accounts map[string]*Record
	authDir  string
}

var globalAccountManager = &AccountManager{
	accounts: make(map[string]*Record),
	authDir:  "auths",
}

// Broadcaster is implemented by internal/dashboard's WSHub. See this
// file's doc comment for why accounts calls it through an interface
// rather than importing dashboard directly.
type Broadcaster interface {
	BroadcastAccountDeleted(email string)
	BroadcastAccounts(accounts []*Record)
}

var broadcaster Broadcaster

// SetBroadcaster wires a dashboard (or any other) subscriber in. Safe to
// leave unset — DeleteAccount/ToggleAccountDisabled simply skip
// broadcasting, as nothing in this package or its tests requires a
// dashboard to exist.
func SetBroadcaster(b Broadcaster) { broadcaster = b }

// InitAccountManager loads the account pool from authDir and applies any
// OAuth overrides from cfg. See this file's doc comment item 1 above —
// v1's config.go directly mutated this package's own globals from
// LoadConfig; that's now this package's own init-time responsibility,
// reading the same ServerConfig fields.
func InitAccountManager(authDir string, cfg config.ServerConfig) error {
	if cfg.GoogleOAuthClientID != "" {
		AntigravityClientID = cfg.GoogleOAuthClientID
	}
	if cfg.GoogleOAuthClientSecret != "" {
		AntigravityClientSecret = cfg.GoogleOAuthClientSecret
	}
	if cfg.AntigravityUserAgent != "" {
		AntigravityUserAgent = cfg.AntigravityUserAgent
	}
	globalAccountManager.authDir = authDir
	return globalAccountManager.LoadAll()
}

func (am *AccountManager) LoadAll() error {
	am.mu.Lock()
	defer am.mu.Unlock()

	_ = os.MkdirAll(am.authDir, 0755)

	err := filepath.Walk(am.authDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(info.Name(), ".json") || strings.HasPrefix(info.Name(), ".") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		email := gjson.GetBytes(data, "email").String()
		if email == "" {
			return nil
		}

		refToken := gjson.GetBytes(data, "refresh_token").String()
		accToken := gjson.GetBytes(data, "access_token").String()
		projectID := gjson.GetBytes(data, "project_id").String()
		projectIDProbed := gjson.GetBytes(data, "project_id_probed").Bool()
		disabled := gjson.GetBytes(data, "disabled").Bool()

		if projectID == "" {
			projectID = fallbackProjectID
		}

		acc := &Record{
			Email:           email,
			ProjectID:       projectID,
			ProjectIDProbed: projectIDProbed,
			RefreshToken:    refToken,
			AccessToken:     accToken,
			Disabled:        disabled,
			FilePath:        path,
		}
		am.accounts[email] = acc
		return nil
	})

	return err
}

// snapshot copies the fields callers outside this package actually read
// (email/tokens/quotas/status), so holders of the returned pointer never
// race against FetchQuota/RefreshAccessToken writing the live *Record
// under am.mu.Lock() from a different goroutine. Deliberately skips
// refreshMu: it's a sync.Mutex, and copying a struct containing one is a
// go vet copylocks violation — field-by-field construction here never
// touches it, and a snapshot is never locked/unlocked anyway.
func (acc *Record) snapshot() *Record {
	if acc == nil {
		return nil
	}
	snap := &Record{
		Email:           acc.Email,
		ProjectID:       acc.ProjectID,
		ProjectIDProbed: acc.ProjectIDProbed,
		RefreshToken:    acc.RefreshToken,
		AccessToken:     acc.AccessToken,
		ExpiresAt:       acc.ExpiresAt,
		Disabled:        acc.Disabled,
		FilePath:        acc.FilePath,
		CooldownUntil:   acc.CooldownUntil,
		LastChecked:     acc.LastChecked,
		LastError:       acc.LastError,
		ErrorKind:       acc.ErrorKind,
		ErrorDetail:     acc.ErrorDetail,
		ErrorAt:         acc.ErrorAt,
		ErrorUntil:      acc.ErrorUntil,

		hardCooldownUntil: acc.hardCooldownUntil,
	}
	if len(acc.ModelCooldowns) > 0 {
		snap.ModelCooldowns = make(map[string]time.Time, len(acc.ModelCooldowns))
		for k, v := range acc.ModelCooldowns {
			snap.ModelCooldowns[k] = v
		}
	}
	if len(acc.Quotas) > 0 {
		snap.Quotas = make([]QuotaGroup, len(acc.Quotas))
		for i, g := range acc.Quotas {
			snap.Quotas[i] = g
			if len(g.Buckets) > 0 {
				snap.Quotas[i].Buckets = make([]QuotaBucket, len(g.Buckets))
				copy(snap.Quotas[i].Buckets, g.Buckets)
			}
		}
	}
	return snap
}

// AllAccounts exposes the package-level singleton to callers outside
// this package (the dashboard's snapshot/broadcast, once it needs
// accounts) — same package-level-singleton-plus-accessor convention
// used throughout this migration (e.g. requestlog.QuerySessions).
func AllAccounts() []*Record { return globalAccountManager.GetAccounts() }

// Manager exposes the package-level AccountManager singleton to
// internal/orchestrate (account rotation/cooldown/token-refresh during
// the pre-stream failover loop) and internal/ratelimit (quota lookups for
// rate-limit headers) — same accessor convention as loopguard.Global().
func Manager() *AccountManager { return globalAccountManager }

func (am *AccountManager) GetAccount(email string) (*Record, bool) {
	am.mu.RLock()
	defer am.mu.RUnlock()
	acc, ok := am.accounts[email]
	if !ok {
		return nil, false
	}
	return acc.snapshot(), true
}

func (am *AccountManager) GetAccounts() []*Record {
	am.mu.RLock()
	defer am.mu.RUnlock()
	var res []*Record
	for _, acc := range am.accounts {
		res = append(res, acc.snapshot())
	}
	sort.Slice(res, func(i, j int) bool {
		return res[i].Email < res[j].Email
	})
	return res
}

// DeleteAccount permanently removes the account JSON file from disk,
// evicts it from the in-memory pool, unbinds any sticky affinities,
// and notifies live dashboard clients via the Broadcaster.
func (am *AccountManager) DeleteAccount(email string) error {
	am.mu.Lock()
	acc, ok := am.accounts[email]
	if !ok {
		am.mu.Unlock()
		return fmt.Errorf("account %q not found", email)
	}

	filePath := acc.FilePath
	delete(am.accounts, email)
	am.mu.Unlock()

	// Physical removal from disk
	if filePath != "" {
		if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
			logx.LogGateway("[AccountManager] Warning: failed to delete file %s: %v", filePath, err)
		}
	}

	// Remove from sticky/affinity tables
	globalSelector.UnstickAccount(email)

	if broadcaster != nil {
		broadcaster.BroadcastAccountDeleted(email)
		broadcaster.BroadcastAccounts(am.GetAccounts())
	}

	logx.LogGateway("[AccountManager] Account deleted: %s (file: %s)", email, filePath)
	return nil
}

// ToggleAccountDisabled toggles the disabled flag of an account, persists it
// to the JSON credential file on disk, and broadcasts the update.
func (am *AccountManager) ToggleAccountDisabled(email string, disabled bool) error {
	am.mu.Lock()
	acc, ok := am.accounts[email]
	if !ok {
		am.mu.Unlock()
		return fmt.Errorf("account %q not found", email)
	}
	acc.Disabled = disabled
	filePath := acc.FilePath
	am.mu.Unlock()

	if filePath != "" {
		data, err := os.ReadFile(filePath)
		if err == nil {
			if updated, err := sjson.SetBytes(data, "disabled", disabled); err == nil {
				_ = atomicfile.WriteFile(filePath, updated)
			}
		}
	}

	if disabled {
		globalSelector.UnstickAccount(email)
	}

	if broadcaster != nil {
		broadcaster.BroadcastAccounts(am.GetAccounts())
	}
	logx.LogGateway("[AccountManager] Account %s disabled state set to: %v", email, disabled)
	return nil
}

// InvalidateAccessToken clears a cached access token and its expiry so the
// next RefreshAccessToken call is forced to actually fetch a new one instead
// of reusing the cached value. An audit caught that a Google-side HTTP 401
// (token revoked, or some other auth desync) only ever excluded the account
// from the IMMEDIATE request's retry loop — the cached AccessToken/ExpiresAt
// were never touched, so RefreshAccessToken's own cache check kept finding
// the token "still valid" by its stale expiry and handed the same rejected
// token right back out on every subsequent request until it naturally expired.
func (am *AccountManager) InvalidateAccessToken(email string) {
	am.mu.Lock()
	if acc, ok := am.accounts[email]; ok {
		acc.AccessToken = ""
		acc.ExpiresAt = time.Time{}
		logx.LogGateway("[AccountManager] Invalidated cached access token for %s after upstream 401", email)
	}
	am.mu.Unlock()
}
