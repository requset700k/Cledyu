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

// newLoginRouter는 newFakeIDP(t) 를 기반으로 실제 auth.Provider 를 연결한
// GET /api/v1/auth/login 라우터를 반환한다.
// newFakeIDP / newTestConfig 는 auth_refresh_test.go / handlers_test.go 에서 공유.
func newLoginRouter(t *testing.T, idp *fakeIDP) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	cfg := newTestConfig()
	var provider *auth.Provider
	if idp != nil {
		cfg.Keycloak = config.KeycloakConfig{
			URL: idp.server.URL, Realm: "test", ClientID: "web",
			CookieDomain: "", RedirectURI: idp.server.URL + "/cb",
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		p, err := auth.NewProvider(ctx, cfg.Keycloak)
		if err != nil {
			t.Fatalf("fake idp discovery failed: %v", err)
		}
		provider = p
	}

	h := handlers.New(cfg, zap.NewNop(), nil, nil, nil, nil, nil, nil, provider)
	r := gin.New()
	r.GET("/api/v1/auth/login", h.Login)
	return r
}

// getLogin은 query 문자열을 붙여 GET /api/v1/auth/login 을 호출한다.
func getLogin(r *gin.Engine, query string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	target := "/api/v1/auth/login"
	if query != "" {
		target += "?" + query
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	r.ServeHTTP(w, req)
	return w
}

// TestLogin_IdPHint_Google: ?idp=google → Location 에 kc_idp_hint=google 포함.
func TestLogin_IdPHint_Google(t *testing.T) {
	idp := newFakeIDP(t)
	defer idp.server.Close()
	r := newLoginRouter(t, idp)

	w := getLogin(r, "idp=google")
	if w.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d: %s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if loc == "" {
		t.Fatal("expected Location header, got empty")
	}
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("Location not a valid URL: %v", err)
	}
	hint := u.Query().Get("kc_idp_hint")
	if hint != "google" {
		t.Errorf("expected kc_idp_hint=google, got %q (Location: %s)", hint, loc)
	}
}

// TestLogin_IdPHint_Rejected: ?idp=evil → Location 에 kc_idp_hint 없음(허용 목록 외 차단).
func TestLogin_IdPHint_Rejected(t *testing.T) {
	idp := newFakeIDP(t)
	defer idp.server.Close()
	r := newLoginRouter(t, idp)

	w := getLogin(r, "idp=evil")
	if w.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d: %s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("Location not a valid URL: %v", err)
	}
	if hint := u.Query().Get("kc_idp_hint"); hint != "" {
		t.Errorf("expected no kc_idp_hint for disallowed idp, got %q (Location: %s)", hint, loc)
	}
}

// TestLogin_NoIdPParam: idp 파라미터 없음 → Location 에 kc_idp_hint 없음.
func TestLogin_NoIdPParam(t *testing.T) {
	idp := newFakeIDP(t)
	defer idp.server.Close()
	r := newLoginRouter(t, idp)

	w := getLogin(r, "")
	if w.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d: %s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("Location not a valid URL: %v", err)
	}
	if hint := u.Query().Get("kc_idp_hint"); hint != "" {
		t.Errorf("expected no kc_idp_hint when idp param absent, got %q (Location: %s)", hint, loc)
	}
}
