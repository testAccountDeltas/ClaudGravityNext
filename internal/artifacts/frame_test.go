package artifacts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func newTestServer(t *testing.T, authorize func(*http.Request) bool) http.Handler {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	mux := http.NewServeMux()
	NewHandler(s, authorize).Register(mux)
	return mux
}

func do(t *testing.T, h http.Handler, method, path, body string) (int, map[string]any, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return rec.Code, m, rec.Body.String()
}

func TestPublishReadListDelete(t *testing.T) {
	h := newTestServer(t, nil)
	code, m, _ := do(t, h, "POST", "/api/frame/deploy/direct?attempt_id=1",
		`{"title":"Dash","content":"<h1>hi</h1>","capabilities":{"db":{}}}`)
	if code != 200 || m["version"] != "1" {
		t.Fatalf("deploy: %d %v", code, m)
	}
	slug, _ := m["slug"].(string)
	if !slugRe.MatchString(slug) {
		t.Fatalf("slug %q is not a UUID", slug)
	}

	// Redeploy bumps the version and keeps the slug.
	_, m, _ = do(t, h, "POST", "/api/frame/deploy/direct", `{"slug":"`+slug+`","content":"<h1>v2</h1>"}`)
	if m["version"] != "2" || m["slug"] != slug {
		t.Fatalf("redeploy: %v", m)
	}

	code, m, _ = do(t, h, "GET", "/api/frame/read/"+slug, "")
	if code != 200 || m["title"] != "Dash" || m["version"] != "2" {
		t.Fatalf("read: %d %v", code, m)
	}
	code, _, body := do(t, h, "GET", "/api/frame/content/"+slug, "")
	if code != 200 || body != "<h1>v2</h1>" {
		t.Fatalf("content: %d %q", code, body)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/frame/content/"+slug+"/index.html", nil))
	if rec.Header().Get("x-frame-content-route") != "served" {
		t.Fatalf("content must carry x-frame-content-route: served, got %v", rec.Header())
	}
	// What the client's `read` and auto-mode share probe request.
	code, m, _ = do(t, h, "GET", "/api/frame/"+slug+"?via=model_read&readback=1", "")
	perm, _ := m["perm"].(map[string]any)
	if code != 200 || m["ver"] != "2" || m["assetToken"] == "" || m["assetToken"] == nil || m["contentVia"] != "frame" ||
		m["mode"] != "owner" || perm["role"] != "owner" || perm["mode"] != "owner" ||
		m["outsideWriter"] != false || m["outsideAudience"] != false || m["cowritten"] != false {
		t.Fatalf("boot: %d %v", code, m)
	}
	_, m, _ = do(t, h, "GET", "/api/frame/frames?limit=10&rel=mine", "")
	if frames, _ := m["frames"].([]any); len(frames) != 1 {
		t.Fatalf("frames: %v", m)
	}
	_, m, _ = do(t, h, "GET", "/api/frame/frames?rel=shared", "")
	if frames, _ := m["frames"].([]any); len(frames) != 0 {
		t.Fatalf("shared frames: %v", m)
	}

	if code, m, _ = do(t, h, "POST", "/api/frame/favorite/"+slug, ""); code != 200 || m["starred"] != true {
		t.Fatalf("favorite: %d %v", code, m)
	}
	if code, m, _ = do(t, h, "DELETE", "/api/frame/"+slug, ""); code != 200 || m["deleted"] != true {
		t.Fatalf("delete: %d %v", code, m)
	}
	if code, _, _ = do(t, h, "GET", "/api/frame/read/"+slug, ""); code != 404 {
		t.Fatalf("read after delete: %d", code)
	}
}

func TestTypesContractAndFallbacks(t *testing.T) {
	h := newTestServer(t, nil)
	_, m, _ := do(t, h, "GET", "/api/frame/types?scopes=anthropic", "")
	if types, ok := m["types"].([]any); !ok || len(types) != 0 {
		t.Fatalf("types must be an empty array (not null): %v", m)
	}
	_, m, _ = do(t, h, "GET", "/api/frame/contract/latest", "")
	if m["version"] != ContractVersion {
		t.Fatalf("contract: %v", m)
	}
	if code, _, _ := do(t, h, "GET", "/api/frame/contract/1.0.0/db.d.ts", ""); code != 404 {
		t.Fatalf("d.ts must 404, got %d", code)
	}
	if code, m, _ := do(t, h, "POST", "/api/artifacts/mcp", `{}`); code != 404 || m["type"] != "error" {
		t.Fatalf("mcp must fail fast with an Anthropic-style 404: %d %v", code, m)
	}
	if code, _, _ := do(t, h, "GET", "/api/frame/nonsense", ""); code != 404 {
		t.Fatalf("unknown path: %d", code)
	}
}

func TestDBAgent(t *testing.T) {
	h := newTestServer(t, nil)
	_, m, _ := do(t, h, "POST", "/api/frame/deploy/direct", `{"title":"t","content":"x"}`)
	slug := m["slug"].(string)
	op := func(body string) map[string]any {
		_, m, raw := do(t, h, "POST", "/api/frame/db/agent", body)
		if m == nil {
			t.Fatalf("bad response %q", raw)
		}
		return m
	}
	if r := op(`{"slug":"` + slug + `","op":"set","path":"tasks","docId":"1","data":{"a":1,"b":2}}`); r["version"] != float64(1) {
		t.Fatalf("set: %v", r)
	}
	op(`{"slug":"` + slug + `","op":"update","path":"tasks","docId":"1","data":{"b":{"__delete__":true},"c":3}}`)
	r := op(`{"slug":"` + slug + `","op":"get","path":"tasks","docId":"1"}`)
	data, _ := json.Marshal(r["data"])
	if r["exists"] != true || string(data) != `{"a":1,"c":3}` || r["version"] != float64(2) {
		t.Fatalf("get after update: %v %s", r, data)
	}
	if r := op(`{"slug":"` + slug + `","op":"list","path":"tasks"}`); len(r["docs"].([]any)) != 1 {
		t.Fatalf("list: %v", r)
	}
	b := op(`{"slug":"` + slug + `","op":"batch","ops":[{"op":"set","path":"tasks","docId":"2","data":{}},{"op":"delete","path":"tasks","docId":"1"}]}`)
	if len(b["results"].([]any)) != 2 {
		t.Fatalf("batch: %v", b)
	}
	if r := op(`{"slug":"` + slug + `","op":"get","path":"tasks","docId":"1"}`); r["exists"] != false {
		t.Fatalf("deleted doc still exists: %v", r)
	}
}

func TestAuthorize(t *testing.T) {
	h := newTestServer(t, func(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer ok" })
	if code, _, _ := do(t, h, "GET", "/api/frame/frames", ""); code != 401 {
		t.Fatalf("unauthenticated must be 401, got %d", code)
	}
	req := httptest.NewRequest("GET", "/api/frame/frames", nil)
	req.Header.Set("Authorization", "Bearer ok")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("authenticated: %d", rec.Code)
	}
}
