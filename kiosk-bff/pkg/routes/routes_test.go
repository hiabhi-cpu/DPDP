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
	if err := os.WriteFile(filepath.Join(tmpDir, "index.html"), []byte(indexContent), 0o644); err != nil {
		t.Fatalf("failed to write index.html: %v", err)
	}

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

	// Test: GET /kiosk/dashboard (SPA route) returns 200 and serves index.html
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/kiosk/dashboard", http.NoBody)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /kiosk/dashboard returned %d, want 200", w.Code)
	}
	if body := w.Body.String(); body != indexContent {
		t.Fatalf("GET /kiosk/dashboard returned %q, want %q", body, indexContent)
	}

	// Test: GET /kioskxyz returns 404 (not the index, wrong prefix boundary)
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/kioskxyz", http.NoBody)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("GET /kioskxyz returned %d, want 404", w.Code)
	}
}
