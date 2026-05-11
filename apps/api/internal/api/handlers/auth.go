package handlers

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

var httpClient = &http.Client{Timeout: 10 * time.Second}

const stateCookieName = "oauth_state"

// CSRF 방지를 위한 state 쿠키를 설정하고 Keycloak 인증 페이지로 리다이렉트.
func (h *Handler) Login(c *gin.Context) {
	state, err := randomState()
	if err != nil {
		h.err(c, http.StatusInternalServerError, "failed to generate state")
		return
	}

	secure := h.cfg.Server.Mode == "release"
	c.SetCookie(stateCookieName, state, 300, "/", "", secure, true)

	authURL := h.cfg.Keycloak.URL + "/realms/" + url.PathEscape(h.cfg.Keycloak.Realm) +
		"/protocol/openid-connect/auth?" + url.Values{
			"client_id":     {h.cfg.Keycloak.ClientID},
			"redirect_uri":  {h.cfg.Keycloak.RedirectURI},
			"response_type": {"code"},
			"scope":         {"openid profile email"},
			"state":         {state},
		}.Encode()

	c.Redirect(http.StatusFound, authURL)
}

// authorization code를 access token으로 교환, HttpOnly 쿠키 설정 후 프론트엔드 /callback으로 리다이렉트.
func (h *Handler) Callback(c *gin.Context) {
	secure := h.cfg.Server.Mode == "release"

	savedState, err := c.Cookie(stateCookieName)
	if err != nil || savedState != c.Query("state") {
		h.err(c, http.StatusBadRequest, "invalid state")
		return
	}
	c.SetCookie(stateCookieName, "", -1, "/", "", secure, true)

	code := c.Query("code")
	if code == "" {
		h.err(c, http.StatusBadRequest, "missing code")
		return
	}

	token, err := h.exchangeCode(code)
	if err != nil {
		h.log.Error("token exchange failed", zap.Error(err))
		h.err(c, http.StatusInternalServerError, "token exchange failed")
		return
	}

	// cookie_domain=".cledyu.local" 으로 설정해 app.cledyu.local에서도 읽힘.
	c.SetCookie("access_token", token.AccessToken, token.ExpiresIn, "/", h.cfg.Keycloak.CookieDomain, secure, true)
	c.Redirect(http.StatusFound, h.cfg.Keycloak.FrontendURL+"/callback")
}

type oidcTokenResponse struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
}

func (h *Handler) exchangeCode(code string) (*oidcTokenResponse, error) {
	tokenURL := h.cfg.Keycloak.URL + "/realms/" + url.PathEscape(h.cfg.Keycloak.Realm) +
		"/protocol/openid-connect/token"

	resp, err := httpClient.PostForm(tokenURL, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {h.cfg.Keycloak.RedirectURI},
		"client_id":     {h.cfg.Keycloak.ClientID},
		"client_secret": {h.cfg.Keycloak.ClientSecret},
	})
	if err != nil {
		return nil, fmt.Errorf("post token endpoint: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("keycloak %d: %s", resp.StatusCode, body)
	}

	var token oidcTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}
	return &token, nil
}

func randomState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}
