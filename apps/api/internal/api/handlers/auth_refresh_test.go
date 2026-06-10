package handlers_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/requset700k/cledyu/api/internal/api/handlers"
	"github.com/requset700k/cledyu/api/internal/auth"
	"github.com/requset700k/cledyu/api/internal/config"
	"go.uber.org/zap"
)

// ── 가짜 Keycloak(IdP) ──────────────────────────────────────────────────────
// discovery + JWKS + token 엔드포인트만 흉내 낸다. refresh grant 의 응답(성공/실패)을
// 테스트가 제어할 수 있도록 refreshStatus 로 스위칭한다.

type fakeIDP struct {
	server        *httptest.Server
	key           *rsa.PrivateKey
	refreshStatus int // 200 이면 새 토큰 발급, 그 외엔 해당 상태코드로 실패
}

func newFakeIDP(t *testing.T) *fakeIDP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIDP{key: key, refreshStatus: http.StatusOK}

	mux := http.NewServeMux()
	mux.HandleFunc("/realms/test/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		issuer := f.server.URL + "/realms/test"
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 issuer,
			"authorization_endpoint": issuer + "/protocol/openid-connect/auth",
			"token_endpoint":         issuer + "/protocol/openid-connect/token",
			"jwks_uri":               issuer + "/protocol/openid-connect/certs",
			"end_session_endpoint":   issuer + "/protocol/openid-connect/logout",
		})
	})
	mux.HandleFunc("/realms/test/protocol/openid-connect/certs", func(w http.ResponseWriter, _ *http.Request) {
		pub := f.key.PublicKey
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]any{{
				"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "test-key",
				"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
			}},
		})
	})
	mux.HandleFunc("/realms/test/protocol/openid-connect/token", func(w http.ResponseWriter, r *http.Request) {
		// Content-Type 이 json 이 아니면 oauth2 가 form 으로 파싱해 access_token 을 못 읽는다.
		w.Header().Set("Content-Type", "application/json")
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "refresh_token" {
			http.Error(w, `{"error":"unsupported_grant_type"}`, http.StatusBadRequest)
			return
		}
		if f.refreshStatus != http.StatusOK {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(f.refreshStatus)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":       f.signJWT(),
			"token_type":         "Bearer",
			"expires_in":         300,
			"refresh_token":      "rotated-refresh-token",
			"refresh_expires_in": 1800,
		})
	})
	f.server = httptest.NewServer(mux)
	return f
}

// signJWT는 가짜 IdP 의 RSA 키로 RS256 access_token JWT 를 서명한다.
func (f *fakeIDP) signJWT() string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"test-key","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString(fmt.Appendf(nil,
		`{"iss":%q,"sub":"user-1","aud":"account","exp":%d,"iat":%d,"preferred_username":"tester"}`,
		f.server.URL+"/realms/test", time.Now().Add(5*time.Minute).Unix(), time.Now().Unix(),
	))
	signingInput := header + "." + payload
	hashed := sha256.Sum256([]byte(signingInput))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, hashed[:])
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func newRefreshRouter(t *testing.T, idp *fakeIDP) *gin.Engine {
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

	h := handlers.New(cfg, zap.NewNop(), nil, nil, nil, provider)
	r := gin.New()
	r.POST("/api/v1/auth/refresh", h.Refresh)
	return r
}

func postRefresh(r *gin.Engine, cookie string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: "refresh_token", Value: cookie})
	}
	r.ServeHTTP(w, req)
	return w
}

// provider 미구성(Keycloak 미가용) 시 503 — 다른 auth 라우트와 동일한 동작.
func TestRefresh_Unavailable_WhenNoProvider(t *testing.T) {
	r := newRefreshRouter(t, nil)
	if w := postRefresh(r, "any"); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}

// refresh_token 쿠키가 없으면 401 + 세션 쿠키 정리.
func TestRefresh_MissingCookie(t *testing.T) {
	idp := newFakeIDP(t)
	defer idp.server.Close()
	r := newRefreshRouter(t, idp)

	w := postRefresh(r, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "refresh_missing") {
		t.Errorf("expected refresh_missing code, got %s", w.Body.String())
	}
}

// 정상 refresh: 204 + access/refresh 쿠키 재발급(회전된 refresh_token 반영).
func TestRefresh_Success_RotatesCookies(t *testing.T) {
	idp := newFakeIDP(t)
	defer idp.server.Close()
	r := newRefreshRouter(t, idp)

	w := postRefresh(r, "valid-refresh-token")
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	var gotAccess, gotRefresh bool
	for _, ck := range cookies {
		switch ck.Name {
		case "access_token":
			gotAccess = ck.Value != "" && ck.MaxAge > 0
		case "refresh_token":
			if ck.Value != "rotated-refresh-token" {
				t.Errorf("expected rotated refresh token, got %q", ck.Value)
			}
			if ck.MaxAge != 1800 {
				t.Errorf("expected refresh cookie maxAge 1800 (refresh_expires_in), got %d", ck.MaxAge)
			}
			gotRefresh = true
		}
	}
	if !gotAccess || !gotRefresh {
		t.Fatalf("expected access+refresh cookies, got %+v", cookies)
	}
}

// IdP 가 invalid_grant 를 돌려주면(만료/폐기/SSO 종료) 401 + 세션 쿠키 만료 처리.
func TestRefresh_Failure_ClearsCookies(t *testing.T) {
	idp := newFakeIDP(t)
	defer idp.server.Close()
	idp.refreshStatus = http.StatusBadRequest
	r := newRefreshRouter(t, idp)

	w := postRefresh(r, "expired-refresh-token")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "refresh_failed") {
		t.Errorf("expected refresh_failed code, got %s", w.Body.String())
	}
	for _, ck := range w.Result().Cookies() {
		if ck.Name == "refresh_token" && ck.MaxAge >= 0 {
			t.Errorf("expected refresh cookie expired, got maxAge %d", ck.MaxAge)
		}
	}
}
