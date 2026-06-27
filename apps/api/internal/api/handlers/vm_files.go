package handlers

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/requset700k/cledyu/api/internal/session"
	"github.com/requset700k/cledyu/api/internal/vmfiles"
	"go.uber.org/zap"
)

// GET /api/v1/sessions/:id/files
func (h *Handler) ListSessionFiles(c *gin.Context) {
	// 파일 목록 조회는 세션 VM 내부 `/home/lab`만 대상으로 하는 read-only 보조 기능이다.
	// 실제 파일 시스템 경계는 vmfiles runner/forced command가 한 번 더 검증하지만,
	// HTTP 계층에서는 먼저 "요청자가 이 세션의 소유자인가"와 "KubeVirt 세션인가"를 확인한다.
	sess, ok := h.readyFileSession(c)
	if !ok {
		return
	}

	snapshot, err := h.vmFiles.List(c.Request.Context(), sess.ID)
	if err != nil {
		h.handleVMFileError(c, err, "list VM session files")
		return
	}
	c.JSON(http.StatusOK, snapshot)
}

// readyFileSession은 VM 파일 API가 공통으로 통과해야 하는 HTTP 계층 gate다.
// 이 함수가 true를 반환하기 전에는 KubeVirt port-forward나 VM 내부 SSH 명령을 시작하지 않는다.
func (h *Handler) readyFileSession(c *gin.Context) (*session.Session, bool) {
	if h.sessions == nil {
		h.err(c, http.StatusServiceUnavailable, "kubevirt not configured")
		return nil, false
	}
	if h.vmFiles == nil {
		h.err(c, http.StatusServiceUnavailable, "VM file access is not configured")
		return nil, false
	}

	sess, err := h.sessions.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		if errors.Is(err, session.ErrNotFound) {
			h.err(c, http.StatusNotFound, "session not found")
			return nil, false
		}
		h.log.Error("get session for VM file access", zap.Error(err))
		h.err(c, http.StatusInternalServerError, "get session failed")
		return nil, false
	}
	// 세션 ID는 URL path에서 오므로, 존재하는 세션이라도 소유자가 다르면 404로 숨긴다.
	// console/IDE 경로와 같은 정책을 유지해 타인의 세션 존재 여부를 노출하지 않는다.
	if h.denyIfNotSessionOwner(c, sess) {
		return nil, false
	}
	// 현재 파일 접근 runner는 KubeVirt `lab-{sessionID}/session-vm`에만 붙는다.
	// EC2 overflow 세션을 그대로 넘기면 존재하지 않는 KubeVirt namespace를 port-forward하다
	// 502로 보일 수 있으므로, provider 경계에서 명시적으로 차단한다.
	if sess.Provider != "" && sess.Provider != session.ProviderKubeVirt {
		h.err(c, http.StatusConflict, "VM file access is only available for KubeVirt sessions")
		return nil, false
	}
	// provisioning 단계에서는 VM guest의 forced command/SSH key가 아직 준비되지 않았을 수 있다.
	// runner를 호출하기 전에 ready/active 세션으로 제한해 불필요한 retry와 502를 줄인다.
	if sess.Status != "ready" && sess.Status != "active" {
		h.err(c, http.StatusConflict, "session is not ready")
		return nil, false
	}
	return sess, true
}

func (h *Handler) handleVMFileError(c *gin.Context, err error, logMsg string) {
	switch {
	case errors.Is(err, vmfiles.ErrBusy):
		// 파일 트리 새로고침은 사용자가 반복해서 누를 수 있으므로, 동시성 제한에 걸리면
		// 서버 오류가 아니라 backoff 가능한 429로 돌려준다.
		h.err(c, http.StatusTooManyRequests, "VM file access is busy")
	case errors.Is(err, vmfiles.ErrFileNotListed):
		h.err(c, http.StatusNotFound, "file not found")
	default:
		h.log.Warn(logMsg, zap.Error(err))
		h.err(c, http.StatusBadGateway, "VM file access failed")
	}
}
