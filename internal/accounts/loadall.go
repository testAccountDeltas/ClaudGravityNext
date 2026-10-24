// Part of the accounts package — see pool.go for the package doc comment.
// Holds LoadAll: (re)reading the account pool from the auth directory.
package accounts

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"claudgravity/internal/logx"
)

// LoadAll rescans the auth dir. Unreadable, corrupt (non-JSON) and
// email-less files are skipped (corrupt ones are logged); accounts whose
// file no longer exists are dropped from the pool.
func (am *AccountManager) LoadAll() error {
	am.mu.Lock()
	defer am.mu.Unlock()

	_ = os.MkdirAll(am.authDir, 0755)

	seen := make(map[string]bool)
	err := filepath.Walk(am.authDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(info.Name(), ".json") || strings.HasPrefix(info.Name(), ".") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		if !gjson.ValidBytes(data) {
			logx.LogGateway("[AccountManager] Skipping %s: not valid JSON (truncated or corrupt)", path)
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
		if exp := gjson.GetBytes(data, "expires_at").String(); exp != "" {
			if t, perr := time.Parse(time.RFC3339, exp); perr == nil {
				acc.ExpiresAt = t
			}
		}
		seen[email] = true
		am.accounts[email] = acc
		return nil
	})
	if err == nil {
		for email := range am.accounts {
			if !seen[email] {
				delete(am.accounts, email)
			}
		}
	}

	return err
}
