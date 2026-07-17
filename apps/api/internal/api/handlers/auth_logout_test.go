package handlers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/requset700k/cledyu/api/internal/api/handlers"
	"github.com/requset700k/cledyu/api/internal/auth"
	"github.com/requset700k/cledyu/api/internal/config"
	"go.uber.org/zap"
)

func logoutRouter(t *testing.T, provider *auth.Provider) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := handlers.New(newTestConfig(), zap.NewNop(), nil, nil, nil, nil, nil, nil, provider)
	r := gin.New()
	r.GET("/api/v1/auth/logout", h.Logout)
	return r
}

func TestLogoutReturnsToLocalFrontendForLocalDevRequest(t *testing.T) {
	r := logoutRouter(t, nil)
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

func TestLogoutUsesKeycloakEndSessionBeforeReturningToLocalFrontend(t *testing.T) {
	idp := newFakeIDP(t)
	defer idp.server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	provider, err := auth.NewProvider(ctx, config.KeycloakConfig{
		URL: idp.server.URL, Realm: "test", ClientID: "web",
	})
	if err != nil {
		t.Fatalf("fake idp discovery failed: %v", err)
	}

	r := logoutRouter(t, provider)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/logout", nil)
	req.Header.Set("Referer", "http://localhost:3000/")
	req.AddCookie(&http.Cookie{Name: "id_token", Value: "test-id-token"})
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("status=%d, want 302", w.Code)
	}
	location, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse logout location: %v", err)
	}
	idpURL, err := url.Parse(idp.server.URL)
	if err != nil {
		t.Fatalf("parse fake idp url: %v", err)
	}
	if location.Host != idpURL.Host {
		t.Fatalf("location host=%q, want Keycloak host %q", location.Host, idpURL.Host)
	}
	wantPath := "/realms/test/protocol/openid-connect/logout"
	if location.Path != wantPath {
		t.Fatalf("location path=%q, want %q", location.Path, wantPath)
	}
	if got := location.Query().Get("post_logout_redirect_uri"); got != "http://localhost:3000/" {
		t.Fatalf("post_logout_redirect_uri=%q, want localhost frontend", got)
	}
}

func TestLogoutRejectsUntrustedRefererAsReturnTarget(t *testing.T) {
	r := logoutRouter(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/logout", nil)
	req.Header.Set("Referer", "https://attacker.example/")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if got := w.Header().Get("Location"); got != "https://app.test/" {
		t.Fatalf("location=%q, want configured frontend", got)
	}
}
