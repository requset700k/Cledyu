package handlers

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"golang.org/x/oauth2"
)

// 인증 흐름에서 쓰는 쿠키 이름.
const (
	cookieAccessToken  = "access_token"
	cookieIDToken      = "id_token"
	cookieRefreshToken = "refresh_token"
	cookieOAuthState   = "oauth_state"
	cookieOAuthNonce   = "oauth_nonce"
	cookiePKCE         = "oauth_pkce"

	oauthTempTTL = 600 // state/nonce/pkce 임시 쿠키 수명(초)

	// refreshTTLFallback: 토큰 응답에 refresh_expires_in 이 없을 때의 refresh 쿠키 수명.
	// Keycloak SSO Session Idle 기본 30m 과 정렬 — 쿠키가 세션보다 오래 살아도
	// 서버측에서 거부되므로 보안 문제는 없고 UX 만 401 로 끝난다.
	refreshTTLFallback = 1800
)

// randToken은 URL-safe 랜덤 문자열(state/nonce)을 만든다.
func randToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// secure는 release 모드에서만 Secure 쿠키를 쓴다(로컬 http 개발 허용).
func (h *Handler) secureCookie() bool { return h.cfg.Server.Mode == "release" }

// Login은 state/nonce/PKCE 를 생성해 임시 쿠키로 저장하고 Keycloak 인가 URL로 리다이렉트한다.
// GET /api/v1/auth/login — 인증 불필요.
func (h *Handler) Login(c *gin.Context) {
	if h.auth == nil {
		h.err(c, http.StatusServiceUnavailable, "auth provider not configured")
		return
	}

	state := randToken()
	nonce := randToken()
	pkce := oauth2.GenerateVerifier()

	c.SetSameSite(http.SameSiteLaxMode)
	dom := h.cfg.Keycloak.CookieDomain
	sec := h.secureCookie()
	c.SetCookie(cookieOAuthState, state, oauthTempTTL, "/", dom, sec, true)
	c.SetCookie(cookieOAuthNonce, nonce, oauthTempTTL, "/", dom, sec, true)
	c.SetCookie(cookiePKCE, pkce, oauthTempTTL, "/", dom, sec, true)

	// ?screen=register → Keycloak 회원가입 폼으로 딥링크.
	register := c.Query("screen") == "register"
	c.Redirect(http.StatusFound, h.auth.AuthCodeURL(state, nonce, pkce, register))
}

// Callback은 Keycloak이 돌려준 code를 토큰으로 교환하고 검증한 뒤 세션 쿠키를 설정한다.
// GET /api/v1/auth/callback — 인증 불필요.
func (h *Handler) Callback(c *gin.Context) {
	if h.auth == nil {
		h.err(c, http.StatusServiceUnavailable, "auth provider not configured")
		return
	}

	// Keycloak이 에러를 돌려준 경우(사용자 취소 등).
	if e := c.Query("error"); e != "" {
		h.log.Warn("oidc callback error from idp", zap.String("error", e))
		h.err(c, http.StatusUnauthorized, "authentication failed")
		return
	}

	// CSRF: state 쿠키와 쿼리 state 일치 확인.
	state, err := c.Cookie(cookieOAuthState)
	if err != nil || state == "" || state != c.Query("state") {
		h.err(c, http.StatusBadRequest, "invalid oauth state")
		return
	}
	pkce, err := c.Cookie(cookiePKCE)
	if err != nil || pkce == "" {
		h.err(c, http.StatusBadRequest, "missing pkce verifier")
		return
	}
	nonce, _ := c.Cookie(cookieOAuthNonce)

	code := c.Query("code")
	if code == "" {
		h.err(c, http.StatusBadRequest, "missing authorization code")
		return
	}

	token, err := h.auth.Exchange(c.Request.Context(), code, pkce)
	if err != nil {
		h.log.Warn("oidc code exchange failed", zap.Error(err))
		h.err(c, http.StatusUnauthorized, "token exchange failed")
		return
	}

	identity, err := h.auth.VerifyIDToken(c.Request.Context(), token, nonce)
	if err != nil {
		h.log.Warn("oidc id_token verification failed", zap.Error(err))
		h.err(c, http.StatusUnauthorized, "token verification failed")
		return
	}
	// 로그인 성공 — Keycloak 신원을 앱 DB users 로 미러링(프로필·역할·last_login_at 갱신).
	h.upsertUser(identity)

	// 임시 쿠키 정리.
	dom := h.cfg.Keycloak.CookieDomain
	sec := h.secureCookie()
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(cookieOAuthState, "", -1, "/", dom, sec, true)
	c.SetCookie(cookieOAuthNonce, "", -1, "/", dom, sec, true)
	c.SetCookie(cookiePKCE, "", -1, "/", dom, sec, true)

	// 세션 쿠키: access_token + id_token + refresh_token(silent refresh 용).
	h.setSessionCookies(c, token)

	c.Redirect(http.StatusFound, h.cfg.FrontendURL+"/callback")
}

// setSessionCookies는 토큰 응답을 세션 쿠키 3종으로 저장한다.
//   - access_token: JWT 미들웨어 검증용(수명 = 토큰 만료까지)
//   - id_token:     로그아웃 end-session hint 용
//   - refresh_token: POST /auth/refresh 의 silent refresh 용(수명 = refresh_expires_in)
//
// Callback(최초 로그인)과 Refresh(갱신, refresh_token 회전 반영)가 공유한다.
func (h *Handler) setSessionCookies(c *gin.Context, token *oauth2.Token) {
	dom := h.cfg.Keycloak.CookieDomain
	sec := h.secureCookie()
	c.SetSameSite(http.SameSiteLaxMode)

	maxAge := int(time.Until(token.Expiry).Seconds())
	if maxAge <= 0 {
		maxAge = 900 // access_token_lifespan 기본 15m fallback
	}
	c.SetCookie(cookieAccessToken, token.AccessToken, maxAge, "/", dom, sec, true)
	if raw, ok := token.Extra("id_token").(string); ok && raw != "" {
		c.SetCookie(cookieIDToken, raw, maxAge, "/", dom, sec, true)
	}
	if token.RefreshToken != "" {
		refreshTTL := refreshTTLFallback
		// Keycloak 은 refresh_expires_in(초)을 응답에 포함한다(JSON 숫자 → float64).
		if v, ok := token.Extra("refresh_expires_in").(float64); ok && v > 0 {
			refreshTTL = int(v)
		}
		c.SetCookie(cookieRefreshToken, token.RefreshToken, refreshTTL, "/", dom, sec, true)
	}
}

// clearSessionCookies는 세션 쿠키 3종을 만료시킨다(로그아웃/refresh 실패).
func (h *Handler) clearSessionCookies(c *gin.Context) {
	dom := h.cfg.Keycloak.CookieDomain
	sec := h.secureCookie()
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(cookieAccessToken, "", -1, "/", dom, sec, true)
	c.SetCookie(cookieIDToken, "", -1, "/", dom, sec, true)
	c.SetCookie(cookieRefreshToken, "", -1, "/", dom, sec, true)
}

// Refresh는 refresh_token 쿠키로 access_token 을 갱신한다(silent refresh).
// 프론트(lib/api.ts)가 401 응답을 받으면 이 엔드포인트를 한 번 호출하고 원 요청을
// 재시도한다. 실패(만료/폐기/SSO 세션 종료) 시 세션 쿠키를 정리하고 401 을 반환해
// 프론트가 로그인 페이지로 보내게 한다. POST /api/v1/auth/refresh — 인증 불필요.
func (h *Handler) Refresh(c *gin.Context) {
	if h.auth == nil {
		h.err(c, http.StatusServiceUnavailable, "auth provider not configured")
		return
	}

	refresh, err := c.Cookie(cookieRefreshToken)
	if err != nil || refresh == "" {
		h.clearSessionCookies(c)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "no refresh token", "code": "refresh_missing"})
		return
	}

	token, err := h.auth.Refresh(c.Request.Context(), refresh)
	if err != nil {
		h.log.Debug("refresh token grant failed", zap.Error(err))
		h.clearSessionCookies(c)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "session expired", "code": "refresh_failed"})
		return
	}

	// 방어적 검증: Keycloak 이 돌려준 새 access_token 이 실제로 유효한지 확인한다.
	identity, err := h.auth.VerifyAccessToken(c.Request.Context(), token.AccessToken)
	if err != nil {
		h.log.Warn("refreshed access token verification failed", zap.Error(err))
		h.clearSessionCookies(c)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "session expired", "code": "refresh_failed"})
		return
	}
	// 세션 연장도 활동이다 — last_login_at 을 갱신한다.
	h.upsertUser(identity)

	h.setSessionCookies(c, token)
	c.Status(http.StatusNoContent)
}

// Logout은 세션 쿠키를 지우고 Keycloak end-session으로 리다이렉트한다.
// GET /api/v1/auth/logout — 인증 불필요(쿠키만 있으면 됨).
func (h *Handler) Logout(c *gin.Context) {
	idTokenHint, _ := c.Cookie(cookieIDToken)

	h.clearSessionCookies(c)

	postLogout := h.cfg.FrontendURL + "/"
	if h.auth != nil {
		if u := h.auth.LogoutURL(idTokenHint, postLogout); u != "" {
			c.Redirect(http.StatusFound, u)
			return
		}
	}
	c.Redirect(http.StatusFound, postLogout)
}
