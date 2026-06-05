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
	cookieAccessToken = "access_token"
	cookieIDToken     = "id_token"
	cookieOAuthState  = "oauth_state"
	cookieOAuthNonce  = "oauth_nonce"
	cookiePKCE        = "oauth_pkce"

	oauthTempTTL = 600 // state/nonce/pkce 임시 쿠키 수명(초)
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

	if _, err := h.auth.VerifyIDToken(c.Request.Context(), token, nonce); err != nil {
		h.log.Warn("oidc id_token verification failed", zap.Error(err))
		h.err(c, http.StatusUnauthorized, "token verification failed")
		return
	}

	// 임시 쿠키 정리.
	dom := h.cfg.Keycloak.CookieDomain
	sec := h.secureCookie()
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(cookieOAuthState, "", -1, "/", dom, sec, true)
	c.SetCookie(cookieOAuthNonce, "", -1, "/", dom, sec, true)
	c.SetCookie(cookiePKCE, "", -1, "/", dom, sec, true)

	// 세션 쿠키: access_token(미들웨어 검증용) + id_token(로그아웃 hint용).
	maxAge := int(time.Until(token.Expiry).Seconds())
	if maxAge <= 0 {
		maxAge = 900 // access_token_lifespan 기본 15m fallback
	}
	c.SetCookie(cookieAccessToken, token.AccessToken, maxAge, "/", dom, sec, true)
	if raw, ok := token.Extra("id_token").(string); ok && raw != "" {
		c.SetCookie(cookieIDToken, raw, maxAge, "/", dom, sec, true)
	}

	c.Redirect(http.StatusFound, h.cfg.FrontendURL+"/callback")
}

// Logout은 세션 쿠키를 지우고 Keycloak end-session으로 리다이렉트한다.
// GET /api/v1/auth/logout — 인증 불필요(쿠키만 있으면 됨).
func (h *Handler) Logout(c *gin.Context) {
	idTokenHint, _ := c.Cookie(cookieIDToken)

	dom := h.cfg.Keycloak.CookieDomain
	sec := h.secureCookie()
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(cookieAccessToken, "", -1, "/", dom, sec, true)
	c.SetCookie(cookieIDToken, "", -1, "/", dom, sec, true)

	postLogout := h.cfg.FrontendURL + "/"
	if h.auth != nil {
		if u := h.auth.LogoutURL(idTokenHint, postLogout); u != "" {
			c.Redirect(http.StatusFound, u)
			return
		}
	}
	c.Redirect(http.StatusFound, postLogout)
}
