package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Health는 서버 상태와 버전을 반환한다. 인증 불필요.
// GET /health
// Kubernetes liveness 용도로 쓰는 가벼운 엔드포인트라 외부 의존성을 확인하지 않는다.
func (h *Handler) Health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":  "ok",
		"version": "0.1.0",
	})
}

// Ready는 제품 API를 처리하는 데 필요한 런타임 의존성 초기화 상태를 반환한다. 인증 불필요.
// GET /ready
func (h *Handler) Ready(c *gin.Context) {
	release := h.cfg.Server.Mode == "release"
	checks := gin.H{
		"labs":       readyCheck(len(h.labs) > 0, "loaded", "not_loaded"),
		"keycloak":   dependencyCheck(h.auth != nil, release, "connected", "unavailable"),
		"kubevirt":   dependencyCheck(h.sessions != nil, release, "configured", "not_configured"),
		"validation": dependencyCheck(h.validator != nil, release, "configured", "mock_fallback"),
	}

	ready := len(h.labs) > 0
	status := "ok"
	code := http.StatusOK
	if !ready {
		status = "degraded"
		code = http.StatusServiceUnavailable
	}

	c.JSON(code, gin.H{
		"status":  status,
		"version": "0.1.0",
		"checks":  checks,
	})
}

func readyCheck(ok bool, okDetail, failDetail string) gin.H {
	if ok {
		return gin.H{"status": "ok", "detail": okDetail}
	}
	return gin.H{"status": "degraded", "detail": failDetail}
}

func dependencyCheck(ok, release bool, okDetail, failDetail string) gin.H {
	if ok {
		return gin.H{"status": "ok", "detail": okDetail}
	}
	if !release {
		return gin.H{"status": "ok", "detail": "optional_in_debug"}
	}
	return gin.H{"status": "degraded", "detail": failDetail}
}
