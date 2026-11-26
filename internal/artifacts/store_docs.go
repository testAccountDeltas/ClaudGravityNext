package artifacts

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Doc is one document of an artifact's built-in database.
type Doc struct {
	ID        string          `json:"id"`
	Data      json.RawMessage `json:"data"`
	Version   int             `json:"version"`
	UpdatedAt string          `json:"updatedAt"`
}

// DocGet returns one document; ok is false when it does not exist.
func (s *Store) DocGet(slug, collection, id string) (Doc, bool, error) {
	var d Doc
	var data string
	err := s.db.QueryRow(`SELECT doc_id,data,version,updated_at FROM artifact_docs
WHERE slug=? AND collection=? AND doc_id=?`, slug, collection, id).
		Scan(&d.ID, &data, &d.Version, &d.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Doc{}, false, nil
	}
	d.Data = json.RawMessage(data)
	return d, err == nil, err
}

// DocList returns up to limit documents of a collection, ordered by id.
func (s *Store) DocList(slug, collection string, limit int) ([]Doc, error) {
	rows, err := s.db.Query(`SELECT doc_id,data,version,updated_at FROM artifact_docs
WHERE slug=? AND collection=? ORDER BY doc_id LIMIT ?`, slug, collection, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Doc{}
	for rows.Next() {
		var d Doc
		var data string
		if err := rows.Scan(&d.ID, &data, &d.Version, &d.UpdatedAt); err != nil {
			return nil, err
		}
		d.Data = json.RawMessage(data)
		out = append(out, d)
	}
	return out, rows.Err()
}

// DocSet replaces a document (merge=false) or merges the top-level fields of
// data into it (merge=true; a field set to {"__delete__":true} is removed).
// It returns the new document version.
func (s *Store) DocSet(slug, collection, id string, data json.RawMessage, merge bool) (int, error) {
	cur, ok, err := s.DocGet(slug, collection, id)
	if err != nil {
		return 0, err
	}
	next := data
	if merge && ok {
		if next, err = mergeFields(cur.Data, data); err != nil {
			return 0, err
		}
	}
	ver := 1
	if ok {
		ver = cur.Version + 1
	}
	_, err = s.db.Exec(`INSERT INTO artifact_docs (slug,collection,doc_id,data,version,updated_at)
VALUES (?,?,?,?,?,?)
ON CONFLICT(slug,collection,doc_id) DO UPDATE SET data=excluded.data,
version=excluded.version, updated_at=excluded.updated_at`,
		slug, collection, id, string(next), ver, time.Now().UTC().Format(time.RFC3339Nano))
	return ver, err
}

// DocDelete removes a document.
func (s *Store) DocDelete(slug, collection, id string) error {
	_, err := s.db.Exec(`DELETE FROM artifact_docs WHERE slug=? AND collection=? AND doc_id=?`,
		slug, collection, id)
	return err
}

// DocCount is the number of documents stored for an artifact.
func (s *Store) DocCount(slug string) int {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM artifact_docs WHERE slug=?`, slug).Scan(&n)
	return n
}

func mergeFields(base, patch json.RawMessage) (json.RawMessage, error) {
	var b, p map[string]json.RawMessage
	if err := json.Unmarshal(base, &b); err != nil || b == nil {
		b = map[string]json.RawMessage{}
	}
	if err := json.Unmarshal(patch, &p); err != nil {
		return nil, err
	}
	for k, v := range p {
		var marker struct {
			Delete bool `json:"__delete__"`
		}
		if json.Unmarshal(v, &marker) == nil && marker.Delete {
			delete(b, k)
			continue
		}
		b[k] = v
	}
	return json.Marshal(b)
}
