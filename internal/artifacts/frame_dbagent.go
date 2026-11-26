package artifacts

import (
	"encoding/json"
	"net/http"
)

const (
	maxDocuments = 1000
	maxBatchOps  = 50
)

type dbOp struct {
	Slug  string          `json:"slug"`
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	DocID string          `json:"docId"`
	Data  json.RawMessage `json:"data"`
	Limit int             `json:"limit"`
	Ops   []dbOp          `json:"ops"`
}

// dbAgent serves POST /api/frame/db/agent: the artifact's built-in document
// database, addressed as collection path + document id.
func (h *Handler) dbAgent(w http.ResponseWriter, r *http.Request) {
	var req dbOp
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Slug == "" {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "malformed db request")
		return
	}
	if _, err := h.store.Get(req.Slug); err != nil {
		h.storeErr(w, err)
		return
	}
	if req.Op == "batch" {
		h.dbBatch(w, req)
		return
	}
	switch req.Op {
	case "get":
		d, ok, err := h.store.DocGet(req.Slug, req.Path, req.DocID)
		if err != nil {
			h.storeErr(w, err)
			return
		}
		if !ok {
			writeJSON(w, http.StatusOK, map[string]any{"exists": false})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"exists": true, "data": d.Data,
			"version": d.Version, "updatedAt": d.UpdatedAt})
	case "list", "query":
		limit := req.Limit
		if limit <= 0 || limit > 200 {
			limit = 200
		}
		docs, err := h.store.DocList(req.Slug, req.Path, limit)
		if err != nil {
			h.storeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"docs": docs, "nextCursor": nil, "authoredByOthers": false})
	case "set", "update", "delete":
		v, err := h.dbWrite(req)
		if err != nil {
			h.storeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"version": v, "usage": h.usage(req.Slug)})
	default:
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "unsupported db op: "+req.Op)
	}
}

func (h *Handler) dbWrite(op dbOp) (int, error) {
	if op.Op == "delete" {
		return 0, h.store.DocDelete(op.Slug, op.Path, op.DocID)
	}
	if len(op.Data) == 0 {
		op.Data = json.RawMessage(`{}`)
	}
	return h.store.DocSet(op.Slug, op.Path, op.DocID, op.Data, op.Op == "update")
}

func (h *Handler) dbBatch(w http.ResponseWriter, req dbOp) {
	if len(req.Ops) == 0 || len(req.Ops) > maxBatchOps {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "batch needs 1..50 ops")
		return
	}
	results := make([]map[string]any, 0, len(req.Ops))
	for _, op := range req.Ops {
		op.Slug = req.Slug
		v, err := h.dbWrite(op)
		if err != nil {
			h.storeErr(w, err)
			return
		}
		results = append(results, map[string]any{"version": v})
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results, "usage": h.usage(req.Slug)})
}

func (h *Handler) usage(slug string) map[string]any {
	return map[string]any{"documents": h.store.DocCount(slug), "maxDocuments": maxDocuments}
}
