package artifacts

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	// HumanAccountID and AgentAccountID must match claude.exe's r8 regex
	// (/^[0-9a-fA-F-]{1,64}$/) so comments are never marked degraded.
	HumanAccountID = "00000000-0000-0000-0000-000000000001"
	AgentAccountID = "00000000-0000-0000-0000-000000000002"
)

// WireAuthor matches the Frame API comments schema expected by claude.exe.
type WireAuthor struct {
	Account string `json:"account"`
	Role    string `json:"role,omitempty"`
}

// WireComment is one comment message inside a thread.
type WireComment struct {
	ID         string     `json:"id"`
	Author     WireAuthor `json:"author"`
	Text       string     `json:"text"`
	CreatedAt  string     `json:"created_at,omitempty"`
	ToClaudeAt string     `json:"to_claude_at,omitempty"`
	Source     string     `json:"source,omitempty"`
}

// WireThread is a comment thread returned by GET /api/frame/comments/{slug}.
type WireThread struct {
	ID                string        `json:"id"`
	CreatedAt         string        `json:"created_at,omitempty"`
	ResolvedAt        string        `json:"resolved_at,omitempty"`
	ResolvedBy        string        `json:"resolved_by,omitempty"`
	ClaudeActivatedAt string        `json:"claude_activated_at,omitempty"`
	ClaudeActivatedBy string        `json:"claude_activated_by,omitempty"`
	ClaudeCapability  string        `json:"claude_capability,omitempty"`
	Comments          []WireComment `json:"comments"`
}

// AddCommentThread creates a new comment thread and its opening human comment.
func (s *Store) AddCommentThread(slug, text string, askClaude bool) (WireThread, error) {
	if _, err := s.Get(slug); err != nil {
		return WireThread{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tid := uuid.NewString()
	cid := uuid.NewString()
	toClaudeAt := ""
	activatedAt := ""
	activatedBy := ""
	if askClaude {
		toClaudeAt = now
		activatedAt = now
		activatedBy = HumanAccountID
	}
	tx, err := s.db.Begin()
	if err != nil {
		return WireThread{}, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
INSERT INTO artifact_threads (id,slug,created_at,claude_activated_at,claude_activated_by,claude_capability)
VALUES (?,?,?,?,?,?)`, tid, slug, now, activatedAt, activatedBy, "edit"); err != nil {
		return WireThread{}, err
	}
	if _, err := tx.Exec(`
INSERT INTO artifact_comments (id,thread_id,slug,author_account,author_role,text,created_at,to_claude_at,source)
VALUES (?,?,?,?,?,?,?,?,?)`, cid, tid, slug, HumanAccountID, "", text, now, toClaudeAt, ""); err != nil {
		return WireThread{}, err
	}
	if err := tx.Commit(); err != nil {
		return WireThread{}, err
	}
	return WireThread{
		ID:                tid,
		CreatedAt:         now,
		ClaudeActivatedAt: activatedAt,
		ClaudeActivatedBy: activatedBy,
		ClaudeCapability:  "edit",
		Comments: []WireComment{{
			ID:         cid,
			Author:     WireAuthor{Account: HumanAccountID},
			Text:       text,
			CreatedAt:  now,
			ToClaudeAt: toClaudeAt,
		}},
	}, nil
}

// AddCommentReply appends a comment (from either an assistant or a human viewer)
// to an existing thread.
func (s *Store) AddCommentReply(slug, threadID, text, role string, askClaude bool) (WireComment, error) {
	var existingSlug string
	err := s.db.QueryRow(`SELECT slug FROM artifact_threads WHERE id=?`, threadID).Scan(&existingSlug)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && existingSlug != slug) {
		return WireComment{}, ErrNotFound
	}
	if err != nil {
		return WireComment{}, err
	}

	nowTime := time.Now().UTC()
	// Ensure reply created_at never sorts lexicographically/chronologically before
	// the preceding comment within the same microsecond in fast tests.
	var lastCreated string
	if err := s.db.QueryRow(`SELECT created_at FROM artifact_comments WHERE thread_id=? ORDER BY rowid DESC LIMIT 1`, threadID).Scan(&lastCreated); err == nil {
		if prevT, perr := time.Parse(time.RFC3339Nano, lastCreated); perr == nil && !nowTime.After(prevT) {
			nowTime = prevT.Add(time.Millisecond)
		}
	}
	now := nowTime.Format(time.RFC3339Nano)

	account := HumanAccountID
	toClaudeAt := ""
	source := ""
	if role == "assistant" {
		account = AgentAccountID
		source = "agent"
	} else if askClaude {
		toClaudeAt = now
	}

	cid := uuid.NewString()
	tx, err := s.db.Begin()
	if err != nil {
		return WireComment{}, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
INSERT INTO artifact_comments (id,thread_id,slug,author_account,author_role,text,created_at,to_claude_at,source)
VALUES (?,?,?,?,?,?,?,?,?)`, cid, threadID, slug, account, role, text, now, toClaudeAt, source); err != nil {
		return WireComment{}, err
	}
	if askClaude {
		if _, err := tx.Exec(`
UPDATE artifact_threads
SET claude_activated_at=CASE WHEN claude_activated_at='' THEN ? ELSE claude_activated_at END,
    claude_activated_by=CASE WHEN claude_activated_by='' THEN ? ELSE claude_activated_by END,
    resolved_at='', resolved_by=''
WHERE id=? AND slug=?`, now, HumanAccountID, threadID, slug); err != nil {
			return WireComment{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return WireComment{}, err
	}
	return WireComment{
		ID:         cid,
		Author:     WireAuthor{Account: account, Role: role},
		Text:       text,
		CreatedAt:  now,
		ToClaudeAt: toClaudeAt,
		Source:     source,
	}, nil
}

// ResolveThread marks a comment thread resolved. resolvedBy should be "claude"
// when resolved by the CLI or empty when resolved in the browser viewer.
func (s *Store) ResolveThread(slug, threadID, resolvedBy string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.Exec(`UPDATE artifact_threads SET resolved_at=?, resolved_by=? WHERE id=? AND slug=?`,
		now, resolvedBy, threadID, slug)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListComments returns all comment threads and replies for slug in creation order.
func (s *Store) ListComments(slug string) ([]WireThread, error) {
	if _, err := s.Get(slug); err != nil {
		return nil, err
	}
	tRows, err := s.db.Query(`
SELECT id,created_at,resolved_at,resolved_by,claude_activated_at,claude_activated_by,claude_capability
FROM artifact_threads WHERE slug=? ORDER BY rowid ASC`, slug)
	if err != nil {
		return nil, err
	}
	defer tRows.Close()

	threads := make([]WireThread, 0)
	byID := make(map[string]int)
	for tRows.Next() {
		var t WireThread
		if err := tRows.Scan(&t.ID, &t.CreatedAt, &t.ResolvedAt, &t.ResolvedBy,
			&t.ClaudeActivatedAt, &t.ClaudeActivatedBy, &t.ClaudeCapability); err != nil {
			return nil, err
		}
		t.Comments = []WireComment{}
		byID[t.ID] = len(threads)
		threads = append(threads, t)
	}
	if err := tRows.Err(); err != nil {
		return nil, err
	}
	if len(threads) == 0 {
		return threads, nil
	}

	cRows, err := s.db.Query(`
SELECT id,thread_id,author_account,author_role,text,created_at,to_claude_at,source
FROM artifact_comments WHERE slug=? ORDER BY rowid ASC`, slug)
	if err != nil {
		return nil, err
	}
	defer cRows.Close()

	for cRows.Next() {
		var c WireComment
		var tid string
		if err := cRows.Scan(&c.ID, &tid, &c.Author.Account, &c.Author.Role, &c.Text, &c.CreatedAt, &c.ToClaudeAt, &c.Source); err != nil {
			return nil, err
		}
		if idx, ok := byID[tid]; ok {
			threads[idx].Comments = append(threads[idx].Comments, c)
		}
	}
	return threads, cRows.Err()
}

func (h *Handler) commentsList(w http.ResponseWriter, slug string) {
	if !slugRe.MatchString(slug) {
		writeErr(w, http.StatusNotFound, "not_found_error", "artifact not found")
		return
	}
	threads, err := h.store.ListComments(slug)
	if err != nil {
		h.storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"threads": threads})
}

func (h *Handler) commentReply(w http.ResponseWriter, r *http.Request, slug, threadID string) {
	if !slugRe.MatchString(slug) || !slugRe.MatchString(threadID) {
		writeErr(w, http.StatusNotFound, "not_found_error", "thread not found")
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Text) == "" {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "comment text required")
		return
	}
	c, err := h.store.AddCommentReply(slug, threadID, req.Text, "assistant", false)
	if err != nil {
		h.storeErr(w, err)
		return
	}
	if threads, lerr := h.store.ListComments(slug); lerr == nil {
		h.live.BroadcastComment(slug, threads, false)
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"thread_id":  threadID,
		"comment_id": c.ID,
	})
}

func (h *Handler) commentResolve(w http.ResponseWriter, slug, threadID, resolvedBy string) {
	if !slugRe.MatchString(slug) || !slugRe.MatchString(threadID) {
		writeErr(w, http.StatusNotFound, "not_found_error", "thread not found")
		return
	}
	if err := h.store.ResolveThread(slug, threadID, resolvedBy); err != nil {
		h.storeErr(w, err)
		return
	}
	if threads, lerr := h.store.ListComments(slug); lerr == nil {
		h.live.BroadcastComment(slug, threads, false)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "resolved": true})
}
