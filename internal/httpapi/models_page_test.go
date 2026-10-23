package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func getModels(t *testing.T, path string) (int, map[string]interface{}) {
	t.Helper()
	cm := newTestConfigManager(t, "k")
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) { handleModels(cm, w, r) })
	mux.HandleFunc("/v1/models/{id}", func(w http.ResponseWriter, r *http.Request) { handleModelByID(cm, w, r) })
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("x-api-key", "k")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var body map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

func TestModelsPagination(t *testing.T) {
	code, body := getModels(t, "/v1/models?limit=2")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	data := body["data"].([]interface{})
	if len(data) != 2 || body["has_more"] != true {
		t.Fatalf("limit=2: %d items has_more=%v", len(data), body["has_more"])
	}
	last := body["last_id"].(string)
	if last != data[1].(map[string]interface{})["id"] || body["first_id"] != data[0].(map[string]interface{})["id"] {
		t.Fatalf("first/last ids wrong: %v", body)
	}
	if data[0].(map[string]interface{})["description"] == "" {
		t.Errorf("missing description")
	}
	_, next := getModels(t, "/v1/models?limit=2&after_id="+last)
	nd := next["data"].([]interface{})
	if len(nd) != 2 || nd[0].(map[string]interface{})["id"] == last {
		t.Fatalf("after_id page wrong: %v", nd)
	}
	// before_id of the second page's first item returns the first page.
	_, prev := getModels(t, "/v1/models?limit=2&before_id="+nd[0].(map[string]interface{})["id"].(string))
	pd := prev["data"].([]interface{})
	if len(pd) != 2 || pd[1].(map[string]interface{})["id"] != last {
		t.Fatalf("before_id page wrong: %v", pd)
	}
	for _, bad := range []string{"limit=0", "limit=1001", "limit=x", "after_id=nope"} {
		if c, _ := getModels(t, "/v1/models?"+bad); c != 400 {
			t.Errorf("%s: status %d, want 400", bad, c)
		}
	}
	_, all := getModels(t, "/v1/models?limit=1000")
	if all["has_more"] != false {
		t.Errorf("limit=1000 should hold the full catalog")
	}
}

func TestModelByIDSynthesisesNormalisedIDs(t *testing.T) {
	for _, id := range []string{"claude-opus-4-1-20250805", "us.anthropic.claude-sonnet-4-5-20250929-v1:0", "claude-sonnet-9-9", "Claude-Opus-4-5", "claude-opus-4-5[1m]", "gemini-9.9-flash"} {
		code, body := getModels(t, "/v1/models/"+id)
		if code != 200 {
			t.Errorf("%s: status %d", id, code)
			continue
		}
		if body["id"] != id || body["type"] != "model" || body["capabilities"] == nil || body["description"] == "" {
			t.Errorf("%s: bad entry %v", id, body)
		}
	}
	for _, id := range []string{"garbage", "does-not-exist", "gpt-4o"} {
		if code, _ := getModels(t, "/v1/models/"+id); code != 404 {
			t.Errorf("%s: status %d, want 404", id, code)
		}
	}
}
