package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

const apiVersion = "0.1.0"

// Health는 서버 상태와 버전을 반환한다. 인증 불필요.
// GET /health
// Kubernetes liveness 용도로 쓰는 가벼운 엔드포인트라 외부 의존성을 확인하지 않는다.
func (h *Handler) Health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":  "ok",
		"version": apiVersion,
	})
}

// Ready는 Pod readiness와 런타임 의존성 상태를 반환한다. 인증 불필요.
// 트래픽 차단 여부는 앱 내부 초기화 상태(lab content load)를 기준으로 판단하고,
// 외부 의존성(Keycloak/KubeVirt/Kafka)은 checks에 degraded 상태로만 노출한다.
// GET /ready
func (h *Handler) Ready(c *gin.Context) {
	release := h.cfg.Server.Mode == "release"
	checks := gin.H{
		"labs":       readinessCheck(len(h.labs) > 0, true, "loaded", "not_loaded"),
		"keycloak":   readinessCheck(h.auth != nil, release, "connected", "unavailable"),
		"kubevirt":   readinessCheck(h.sessions != nil, release, "configured", "not_configured"),
		"validation": readinessCheck(h.validator != nil, release, "configured", "mock_fallback"),
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
		"version": apiVersion,
		"checks":  checks,
	})
}

func readinessCheck(ok, required bool, okDetail, failDetail string) gin.H {
	if ok {
		return gin.H{"status": "ok", "detail": okDetail}
	}
	if !required {
		return gin.H{"status": "ok", "detail": "optional_in_debug"}
	}
	return gin.H{"status": "degraded", "detail": failDetail}
}
