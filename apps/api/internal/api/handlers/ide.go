package handlers

import (
	"errors"
	"net/http"
	"net/http/httputil"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/requset700k/cledyu/api/internal/kubevirt"
	"go.uber.org/zap"
)

// ideBackendPort는 세션 VM 안의 code-server 가 listen 하는 포트다.
// Lab DSL init 이 code-server 설정(bind-addr)을 이 포트로 맞춘다(lab-terraform-basics 참고).
const ideBackendPort = "13337"

// IDE는 세션 VM 의 code-server(브라우저 VS Code)로 HTTP/WebSocket 을 리버스 프록시한다.
// code-server 는 --auth none 으로 떠 있고 VM pod IP 는 클러스터 밖에서 접근 불가하므로,
// 인증(JWT)·소유자 확인을 통과한 이 프록시가 유일한 진입점이다.
// ANY /api/v1/sessions/:id/ide/*idepath
func (h *Handler) IDE(c *gin.Context) {
	if h.sessions == nil {
		h.err(c, http.StatusServiceUnavailable, "kubevirt not configured")
		return
	}
	sessionID := c.Param("id")

	sess, err := h.sessions.Get(c.Request.Context(), sessionID)
	if err != nil {
		if errors.Is(err, kubevirt.ErrNotFound) {
			h.err(c, http.StatusNotFound, "session not found")
			return
		}
		h.log.Error("get session for ide", zap.Error(err))
		h.err(c, http.StatusInternalServerError, "get session failed")
		return
	}

	// 소유자 확인 — 다른 학생의 VM IDE 접근 차단(존재 여부도 숨기기 위해 404).
	// debug 모드의 mock 신원/무소유 세션(빈 값)은 로컬 개발을 위해 통과시킨다.
	userID, _ := c.Get("user_id")
	if uid, _ := userID.(string); uid != "" && sess.UserID != "" && uid != sess.UserID {
		h.err(c, http.StatusNotFound, "session not found")
		return
	}

	lc, ok := h.labs[sess.LabID]
	if !ok || !lc.IDE {
		h.err(c, http.StatusNotFound, "ide not available for this lab")
		return
	}

	ip, err := h.sessions.VMIAddress(c.Request.Context(), sessionID)
	if err != nil {
		// VM 부팅/IP 할당 전 — 프론트 IdePane 이 healthz 폴링으로 재시도한다.
		h.err(c, http.StatusServiceUnavailable, "ide not ready")
		return
	}

	prefix := "/api/v1/sessions/" + sessionID + "/ide"
	backendPath := strings.TrimPrefix(c.Request.URL.Path, prefix)
	if backendPath == "" {
		backendPath = "/"
	}

	proxy := &httputil.ReverseProxy{
		Director: func(r *http.Request) {
			r.URL.Scheme = "http"
			r.URL.Host = ip + ":" + ideBackendPort
			r.URL.Path = backendPath
			r.Host = r.URL.Host
			// 플랫폼 세션 쿠키/토큰이 학생 VM(루트 권한 환경)으로 흘러가지 않게 제거.
			r.Header.Del("Cookie")
			r.Header.Del("Authorization")
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			// code-server 설치가 끝나기 전(connection refused)에도 503 — 폴링 재시도 신호.
			h.log.Debug("ide proxy error", zap.String("session_id", sessionID), zap.Error(err))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"ide not ready"}`))
		},
	}
	proxy.ServeHTTP(c.Writer, c.Request)
}
