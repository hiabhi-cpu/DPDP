package routes

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hiabhi-cpu/kiosk-bff/pkg/handlers"
)

func newTestRouter(upstreamURL string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// Both proxies point at the same fake upstream for routing assertions.
	tp := handlers.StubProvider("test-jwt")
	Setup(r, Deps{
		OTP:       handlers.NewProxy(upstreamURL, tp),
		Consent:   handlers.NewProxy(upstreamURL, tp),
		StaticDir: "",
	})
	return r
}

func TestRoutesReachUpstream(t *testing.T) {
	var hits []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	r := newTestRouter(upstream.URL)

	cases := map[string]string{
		"/kiosk/api/otp/send":        "/api/v1/otp/send",
		"/kiosk/api/otp/verify":      "/api/v1/otp/verify",
		"/kiosk/api/consent/capture": "/api/v1/consent/capture",
	}
	for path, wantUpstream := range cases {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, http.NoBody)
		r.ServeHTTP(w, req)
		if w.Code == http.StatusNotFound {
			t.Fatalf("%s not routed (404)", path)
		}
		found := false
		for _, h := range hits {
			if h == wantUpstream {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s did not reach upstream %s; hits=%v", path, wantUpstream, hits)
		}
	}
}

func TestSPAFallbackWithStaticServing(t *testing.T) {
	tmpDir := t.TempDir()
	indexContent := "<!DOCTYPE html><html><body>Kiosk App</body></html>"
	manifestContent := `{"name":"Kiosk"}`
	assetContent := "console.log('app');"
	if err := os.WriteFile(filepath.Join(tmpDir, "index.html"), []byte(indexContent), 0o644); err != nil {
		t.Fatalf("failed to write index.html: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "manifest.webmanifest"), []byte(manifestContent), 0o644); err != nil {
		t.Fatalf("failed to write manifest.webmanifest: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, "assets"), 0o755); err != nil {
		t.Fatalf("failed to create assets dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "assets", "app.js"), []byte(assetContent), 0o644); err != nil {
		t.Fatalf("failed to write assets/app.js: %v", err)
	}
	// A "secret" file outside StaticDir, used to confirm traversal can't reach it.
	secretContent := "top secret"
	if err := os.WriteFile(filepath.Join(filepath.Dir(tmpDir), "secret"), []byte(secretContent), 0o644); err != nil {
		t.Fatalf("failed to write secret file: %v", err)
	}
	defer os.Remove(filepath.Join(filepath.Dir(tmpDir), "secret"))

	gin.SetMode(gin.TestMode)
	r := gin.New()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	tp := handlers.StubProvider("test-jwt")
	Setup(r, Deps{
		OTP:       handlers.NewProxy(upstream.URL, tp),
		Consent:   handlers.NewProxy(upstream.URL, tp),
		StaticDir: tmpDir,
	})

	get := func(path string) (int, string) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, http.NoBody)
		r.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}

	// GET /kiosk/manifest.webmanifest returns the real manifest file, not index.html.
	if code, body := get("/kiosk/manifest.webmanifest"); code != http.StatusOK || body != manifestContent {
		t.Fatalf("GET /kiosk/manifest.webmanifest = %d %q, want 200 %q", code, body, manifestContent)
	}

	// GET /kiosk/assets/app.js returns the real asset file.
	if code, body := get("/kiosk/assets/app.js"); code != http.StatusOK || body != assetContent {
		t.Fatalf("GET /kiosk/assets/app.js = %d %q, want 200 %q", code, body, assetContent)
	}

	// GET /kiosk/ returns index.html.
	if code, body := get("/kiosk/"); code != http.StatusOK || body != indexContent {
		t.Fatalf("GET /kiosk/ = %d %q, want 200 %q", code, body, indexContent)
	}

	// GET /kiosk/dashboard (SPA client route, no matching file) falls back to index.html.
	if code, body := get("/kiosk/dashboard"); code != http.StatusOK || body != indexContent {
		t.Fatalf("GET /kiosk/dashboard = %d %q, want 200 %q", code, body, indexContent)
	}

	// GET /kiosk/some/client/route (no such file) falls back to index.html.
	if code, body := get("/kiosk/some/client/route"); code != http.StatusOK || body != indexContent {
		t.Fatalf("GET /kiosk/some/client/route = %d %q, want 200 %q", code, body, indexContent)
	}

	// Path-traversal attempt must not escape StaticDir: must not return the secret
	// file's content. It should fall back to index.html (or 404), never the secret.
	if code, body := get("/kiosk/../secret"); body == secretContent {
		t.Fatalf("GET /kiosk/../secret leaked file outside StaticDir: %d %q", code, body)
	}
	if code, body := get("/kiosk/%2e%2e/secret"); body == secretContent {
		t.Fatalf("GET /kiosk/%%2e%%2e/secret leaked file outside StaticDir: %d %q", code, body)
	}

	// GET /kioskxyz returns 404 (not the index, wrong prefix boundary)
	if code, _ := get("/kioskxyz"); code != http.StatusNotFound {
		t.Fatalf("GET /kioskxyz returned %d, want 404", code)
	}
}
