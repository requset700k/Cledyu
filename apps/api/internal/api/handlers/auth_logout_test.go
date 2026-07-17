package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/requset700k/cledyu/api/internal/api/handlers"
	"go.uber.org/zap"
)

func logoutRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := handlers.New(newTestConfig(), zap.NewNop(), nil, nil, nil, nil, nil, nil, nil)
	r := gin.New()
	r.GET("/api/v1/auth/logout", h.Logout)
	return r
}

func TestLogoutReturnsToLocalFrontendForLocalDevRequest(t *testing.T) {
	r := logoutRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/logout", nil)
	req.Header.Set("Referer", "http://localhost:3000/")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("status=%d, want 302", w.Code)
	}
	if got := w.Header().Get("Location"); got != "http://localhost:3000/" {
		t.Fatalf("location=%q, want localhost frontend", got)
	}
}

func TestLogoutRejectsUntrustedRefererAsReturnTarget(t *testing.T) {
	r := logoutRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/logout", nil)
	req.Header.Set("Referer", "https://attacker.example/")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if got := w.Header().Get("Location"); got != "https://app.test/" {
		t.Fatalf("location=%q, want configured frontend", got)
	}
}
