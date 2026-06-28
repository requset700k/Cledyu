package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/requset700k/cledyu/api/internal/session"
	"github.com/requset700k/cledyu/api/internal/vmfiles"
	"go.uber.org/zap"
)

// FilePreview는 Web 파일 미리보기 패널에 반환하는 읽기 전용 텍스트 응답이다.
type FilePreview struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
}

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

// GET /api/v1/sessions/:id/files/preview?path=work/app.log
func (h *Handler) PreviewSessionFile(c *gin.Context) {
	// 미리보기 역시 목록 조회와 같은 세션 gate를 통과해야 한다.
	// path 검증은 vmfiles.Service.Read()가 snapshot 포함 여부로 다시 수행하지만,
	// 빈 path는 여기서 바로 거부해 VM runner까지 내려가지 않게 한다.
	sess, ok := h.readyFileSession(c)
	if !ok {
		return
	}
	relativePath := c.Query("path")
	if relativePath == "" {
		h.err(c, http.StatusBadRequest, "file path is required")
		return
	}

	raw, err := h.vmFiles.Read(c.Request.Context(), sess.ID, relativePath)
	if err != nil {
		h.handleVMFileError(c, err, "preview VM session file")
		return
	}
	preview, err := parseFilePreview(raw, relativePath)
	if err != nil {
		h.log.Warn("invalid VM file preview response", zap.String("session_id", sess.ID), zap.Error(err))
		h.err(c, http.StatusBadGateway, "preview VM session file failed")
		return
	}
	c.JSON(http.StatusOK, preview)
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
	// 세션 상태 모델은 provisioning/ready/failed만 가지므로 VM 파일 접근은 ready 세션으로 제한한다.
	if sess.Status != "ready" {
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

func parseFilePreview(raw []byte, requestedPath string) (FilePreview, error) {
	var preview FilePreview
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&preview); err != nil {
		return FilePreview{}, fmt.Errorf("decode file preview: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err == nil {
		return FilePreview{}, errors.New("decode file preview: multiple JSON values")
	} else if !errors.Is(err, io.EOF) {
		return FilePreview{}, fmt.Errorf("decode file preview trailer: %w", err)
	}
	if preview.Path == "" || preview.Path != requestedPath {
		return FilePreview{}, fmt.Errorf("preview path %q does not match requested path %q", preview.Path, requestedPath)
	}
	return preview, nil
}
