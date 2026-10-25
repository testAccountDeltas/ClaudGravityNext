package translate

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

// plainFetchClient lets tests reach a loopback httptest server (the real
// client refuses it, which is asserted separately) while keeping the same
// redirect limit and timeout policy under test.
func plainFetchClient(t *testing.T) {
	t.Helper()
	orig := urlFetchClient
	urlFetchClient = &http.Client{Timeout: 5 * time.Second, CheckRedirect: orig.CheckRedirect}
	t.Cleanup(func() { urlFetchClient = orig })
}

func TestFetchURLAttachment_TheRealClientNeverReachesInternalTargets(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("an internal server must never be contacted")
	}))
	defer srv.Close()
	if _, _, ok := fetchURLAttachment(srv.URL + "/secret"); ok {
		t.Fatal("loopback target was fetched")
	}
	for _, u := range []string{"file:///etc/passwd", "ftp://example.com/x", "gopher://x", "", "http://[::1", "//example.com/x"} {
		if _, _, ok := fetchURLAttachment(u); ok {
			t.Errorf("%q must be refused", u)
		}
	}
}

func TestFetchURLAttachment_StatusSizeAndRedirects(t *testing.T) {
	plainFetchClient(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("hello"))
	})
	mux.HandleFunc("/missing", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	mux.HandleFunc("/huge", func(w http.ResponseWriter, r *http.Request) {
		chunk := make([]byte, 1<<20)
		for i := 0; i < 21; i++ {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	})
	mux.HandleFunc("/hop/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/hop/"+r.URL.Path[len("/hop/"):]+"x", http.StatusFound)
	})
	mux.HandleFunc("/one", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/ok", http.StatusFound) })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, ct, ok := fetchURLAttachment(srv.URL + "/ok")
	if !ok || string(body) != "hello" || !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("ok: %q %q %v", body, ct, ok)
	}
	if _, _, ok := fetchURLAttachment(srv.URL + "/missing"); ok {
		t.Fatal("a 404 must be refused")
	}
	if _, _, ok := fetchURLAttachment(srv.URL + "/huge"); ok {
		t.Fatal("a body over the cap must be refused, not truncated")
	}
	if body, _, ok := fetchURLAttachment(srv.URL + "/one"); !ok || string(body) != "hello" {
		t.Fatalf("a single redirect must be followed: %q %v", body, ok)
	}
	if _, _, ok := fetchURLAttachment(srv.URL + "/hop/a"); ok {
		t.Fatal("a redirect loop must stop at the limit")
	}
}

func TestFetchAttachmentFromURL_ClassifiesWhatItDownloaded(t *testing.T) {
	plainFetchClient(t)
	png := "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89"
	mux := http.NewServeMux()
	reply := func(path, ct, body string) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if ct != "" {
				w.Header().Set("Content-Type", ct)
			} else {
				w.Header()["Content-Type"] = nil // suppress sniffing by net/http
			}
			_, _ = w.Write([]byte(body))
		})
	}
	reply("/a.png", "image/png", png)
	reply("/opaque", "application/octet-stream", png)
	reply("/noext", "", "%PDF-1.4 minimal")
	reply("/doc.pdf", "application/octet-stream", "%PDF-1.4 minimal")
	reply("/notes.txt", "application/octet-stream", "just words")
	reply("/empty", "text/plain", "")
	reply("/fake.png", "image/png", "this is not an image")
	reply("/blob.bin", "application/octet-stream", "\x00\x01\x02\x03binary")
	srv := httptest.NewServer(mux)
	defer srv.Close()

	for _, c := range []struct {
		name, path, ptype string
		wantBinary        bool
		wantMime          string // for binary results
		wantText          string // substring for text results
	}{
		{"declared image", "/a.png", "image", true, "image/png", ""},
		{"opaque type, bytes decide", "/opaque", "image", true, "image/png", ""},
		{"no content type, pdf by sniffing", "/noext", "document", true, "application/pdf", ""},
		{"opaque type, extension decides", "/doc.pdf", "document", true, "application/pdf", ""},
		{"plain text", "/notes.txt", "document", false, "", "just words"},
		{"empty body", "/empty", "document", false, "", "empty response"},
		{"lying image", "/fake.png", "image", false, "", "not a valid"},
		{"unsupported binary", "/blob.bin", "document", false, "", "unsupported"},
	} {
		t.Run(c.name, func(t *testing.T) {
			part, binary, text, ok := fetchAttachmentFromURL(c.ptype, srv.URL+c.path, "name")
			if !ok || binary != c.wantBinary {
				t.Fatalf("ok=%v binary=%v text=%q", ok, binary, text)
			}
			if c.wantBinary {
				if got := gjson.Get(part, "inlineData.mimeType").String(); got != c.wantMime {
					t.Fatalf("mime = %q", got)
				}
			} else if !strings.Contains(text, c.wantText) {
				t.Fatalf("text = %q, want %q", text, c.wantText)
			}
		})
	}
	if _, _, _, ok := fetchAttachmentFromURL("image", srv.URL+"/gone", ""); ok {
		t.Fatal("a failed download must report ok=false so the caller can say so")
	}
}
