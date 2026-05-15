package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Login은 access_token 쿠키를 설정하고 프론트엔드 /callback으로 리다이렉트한다. 인증 불필요.
// GET /api/v1/auth/login
// TODO: Keycloak cledyu-web 클라이언트 등록 후 실 OIDC 흐름으로 교체.
// 실 구현: Login → Keycloak 리다이렉트 → Callback → authorization code 교환 → access_token 쿠키 설정.
func (h *Handler) Login(c *gin.Context) {
	secure := h.cfg.Server.Mode == "release"
	c.SetCookie("access_token", "mock-token", 3600, "/", h.cfg.Keycloak.CookieDomain, secure, true)
	c.Redirect(http.StatusFound, h.cfg.FrontendURL+"/callback")
}
