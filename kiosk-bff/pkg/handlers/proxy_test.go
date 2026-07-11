package handlers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

type stubToken struct {
	tok string
	err error
}

func (s stubToken) Token(context.Context) (string, error) { return s.tok, s.err }

func TestForwardPost_AttachesBearerAndPipesBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var gotAuth, gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"session_id":"sess-1"}`))
	}))
	defer upstream.Close()

	p := NewProxy(upstream.URL, stubToken{tok: "secret-jwt"})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/kiosk/api/x", strings.NewReader(`{"mobile":"9999999999"}`))

	p.ForwardPost(c, "/api/v1/otp/verify")

	if gotAuth != "Bearer secret-jwt" {
		t.Fatalf("upstream Authorization = %q, want Bearer secret-jwt", gotAuth)
	}
	if gotBody != `{"mobile":"9999999999"}` {
		t.Fatalf("upstream body = %q", gotBody)
	}
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", w.Code)
	}
	if strings.Contains(w.Body.String(), "secret-jwt") {
		t.Fatalf("JWT leaked into browser response: %s", w.Body.String())
	}
}

func TestForwardPost_TokenFailureIs502(t *testing.T) {
	gin.SetMode(gin.TestMode)
	p := NewProxy("http://unused", stubToken{err: io.EOF})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/kiosk/api/x", strings.NewReader(`{}`))

	p.ForwardPost(c, "/api/v1/otp/send")

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", w.Code)
	}
}
