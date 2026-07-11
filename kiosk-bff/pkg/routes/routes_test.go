package routes

import (
	"net/http"
	"net/http/httptest"
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
