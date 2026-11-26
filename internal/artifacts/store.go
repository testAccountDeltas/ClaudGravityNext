// Package artifacts is the gateway's own backend for Claude Code's Artifact
// tool. claude.exe hardcodes api.anthropic.com for /api/frame/* (it ignores
// ANTHROPIC_BASE_URL for them), so behind a gateway the tool can only work
// when the CONNECT tunnel answers those paths itself. This package stores
// published pages in a local SQLite file and speaks the subset of the Frame
// REST API the client needs to publish, read, list and delete them.
package artifacts

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// DefaultDBPath is the artifacts database, next to the gateway's other
// SQLite files in its working directory.
const DefaultDBPath = "artifacts.db"

// Artifact is one published page.
type Artifact struct {
	Slug         string    `json:"slug"`
	Title        string    `json:"title"`
	Description  string    `json:"description,omitempty"`
	Content      string    `json:"-"`
	Version      int       `json:"-"`
	Contract     string    `json:"contract,omitempty"`
	Capabilities string    `json:"-"` // raw JSON object, "" when none
	Starred      bool      `json:"starred"`
	UpdatedAt    time.Time `json:"-"`
}

// VersionString is the wire form of Version (the client wants a string).
func (a Artifact) VersionString() string { return strconv.Itoa(a.Version) }

// ErrNotFound reports a missing artifact.
var ErrNotFound = errors.New("artifact not found")

// Store is the SQLite-backed artifact storage.
type Store struct {
	db     *sql.DB
	secMu  sync.Mutex
	secret []byte
}

const schema = `
CREATE TABLE IF NOT EXISTS artifacts (
  slug TEXT PRIMARY KEY,
  title TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  content TEXT NOT NULL,
  version INTEGER NOT NULL DEFAULT 1,
  contract TEXT NOT NULL DEFAULT 'latest',
  capabilities TEXT NOT NULL DEFAULT '',
  starred INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS artifact_docs (
  slug TEXT NOT NULL,
  collection TEXT NOT NULL,
  doc_id TEXT NOT NULL,
  data TEXT NOT NULL,
  version INTEGER NOT NULL DEFAULT 1,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (slug, collection, doc_id)
);
CREATE TABLE IF NOT EXISTS artifact_meta (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS artifact_threads (
  id TEXT PRIMARY KEY,
  slug TEXT NOT NULL,
  created_at TEXT NOT NULL,
  resolved_at TEXT NOT NULL DEFAULT '',
  resolved_by TEXT NOT NULL DEFAULT '',
  claude_activated_at TEXT NOT NULL DEFAULT '',
  claude_activated_by TEXT NOT NULL DEFAULT '',
  claude_capability TEXT NOT NULL DEFAULT 'edit'
);
CREATE INDEX IF NOT EXISTS idx_artifact_threads_slug ON artifact_threads(slug);
CREATE TABLE IF NOT EXISTS artifact_comments (
  id TEXT PRIMARY KEY,
  thread_id TEXT NOT NULL,
  slug TEXT NOT NULL,
  author_account TEXT NOT NULL,
  author_role TEXT NOT NULL DEFAULT '',
  text TEXT NOT NULL,
  created_at TEXT NOT NULL,
  to_claude_at TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_artifact_comments_thread ON artifact_comments(thread_id);`

// Open opens (creating if needed) the database at path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("open artifacts db: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init artifacts schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

// PutParams describes one publish.
type PutParams struct {
	Slug         string // empty: mint a new artifact
	Title        string
	Description  string
	Content      string
	Contract     string
	Capabilities string
}

// Put creates the artifact or stores a new version of it and returns the
// stored row. Last write wins: the gateway has a single owner, so the
// client's baseVersion precondition is not enforced.
func (s *Store) Put(p PutParams) (Artifact, error) {
	now := time.Now().UTC()
	if p.Slug == "" {
		p.Slug = uuid.NewString()
	}
	if p.Contract == "" {
		p.Contract = "latest"
	}
	// A title is only defaulted for a brand-new artifact; a redeploy that
	// sends none keeps the stored one (see the CASE in the upsert below).
	if _, err := s.Get(p.Slug); errors.Is(err, ErrNotFound) && p.Title == "" {
		p.Title = "Untitled"
	}
	_, err := s.db.Exec(`
INSERT INTO artifacts (slug,title,description,content,version,contract,capabilities,updated_at)
VALUES (?,?,?,?,1,?,?,?)
ON CONFLICT(slug) DO UPDATE SET
  title=CASE WHEN excluded.title<>'' THEN excluded.title ELSE artifacts.title END,
  description=excluded.description, content=excluded.content,
  contract=excluded.contract, capabilities=excluded.capabilities,
  version=artifacts.version+1, updated_at=excluded.updated_at`,
		p.Slug, p.Title, p.Description, p.Content, p.Contract, p.Capabilities, now.Format(time.RFC3339Nano))
	if err != nil {
		return Artifact{}, err
	}
	return s.Get(p.Slug)
}

const cols = `slug,title,description,content,version,contract,capabilities,starred,updated_at`

func scan(r interface{ Scan(...any) error }) (Artifact, error) {
	var a Artifact
	var starred int
	var ts string
	if err := r.Scan(&a.Slug, &a.Title, &a.Description, &a.Content, &a.Version,
		&a.Contract, &a.Capabilities, &starred, &ts); err != nil {
		return Artifact{}, err
	}
	a.Starred = starred != 0
	a.UpdatedAt, _ = time.Parse(time.RFC3339Nano, ts)
	return a, nil
}

// Get returns one artifact.
func (s *Store) Get(slug string) (Artifact, error) {
	a, err := scan(s.db.QueryRow(`SELECT `+cols+` FROM artifacts WHERE slug=?`, slug))
	if errors.Is(err, sql.ErrNoRows) {
		return Artifact{}, ErrNotFound
	}
	return a, err
}

// List returns up to limit artifacts, newest first.
func (s *Store) List(limit int) ([]Artifact, error) {
	rows, err := s.db.Query(`SELECT `+cols+` FROM artifacts ORDER BY updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Artifact
	for rows.Next() {
		a, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// LiveSecret returns the persistent 32-byte HMAC secret used to sign
// artifact live-channel subscription tokens across gateway restarts.
func (s *Store) LiveSecret() ([]byte, error) {
	s.secMu.Lock()
	defer s.secMu.Unlock()
	if len(s.secret) == 32 {
		return s.secret, nil
	}
	var hexVal string
	err := s.db.QueryRow(`SELECT value FROM artifact_meta WHERE key='live_secret'`).Scan(&hexVal)
	if err == nil {
		if raw, decErr := hex.DecodeString(hexVal); decErr == nil && len(raw) == 32 {
			s.secret = raw
			return s.secret, nil
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	if _, err := s.db.Exec(`INSERT OR REPLACE INTO artifact_meta (key,value) VALUES ('live_secret',?)`, hex.EncodeToString(raw)); err != nil {
		return nil, err
	}
	s.secret = raw
	return s.secret, nil
}

// Delete removes an artifact, its documents, threads and comments.
func (s *Store) Delete(slug string) error {
	res, err := s.db.Exec(`DELETE FROM artifacts WHERE slug=?`, slug)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	for _, q := range []string{
		`DELETE FROM artifact_docs WHERE slug=?`,
		`DELETE FROM artifact_comments WHERE slug=?`,
		`DELETE FROM artifact_threads WHERE slug=?`,
	} {
		if _, err := s.db.Exec(q, slug); err != nil {
			return err
		}
	}
	return nil
}

// SetStarred pins or unpins an artifact.
func (s *Store) SetStarred(slug string, v bool) error {
	n := 0
	if v {
		n = 1
	}
	res, err := s.db.Exec(`UPDATE artifacts SET starred=? WHERE slug=?`, n, slug)
	if err != nil {
		return err
	}
	if c, _ := res.RowsAffected(); c == 0 {
		return ErrNotFound
	}
	return nil
}

// Retitle changes an artifact's title.
func (s *Store) Retitle(slug, title string) error {
	res, err := s.db.Exec(`UPDATE artifacts SET title=? WHERE slug=?`, title, slug)
	if err != nil {
		return err
	}
	if c, _ := res.RowsAffected(); c == 0 {
		return ErrNotFound
	}
	return nil
}
