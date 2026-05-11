package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// TODO: Keycloak cledyu-web 클라이언트 등록 후 실 OIDC 흐름으로 교체.
// 실 구현: Login → Keycloak 리다이렉트 → Callback → authorization code 교환 → access_token 쿠키 설정.
func (h *Handler) Login(c *gin.Context) {
	secure := h.cfg.Server.Mode == "release"
	c.SetCookie("access_token", "mock-token", 3600, "/", h.cfg.Keycloak.CookieDomain, secure, true)
	c.Redirect(http.StatusFound, h.cfg.Keycloak.FrontendURL+"/callback")
}
