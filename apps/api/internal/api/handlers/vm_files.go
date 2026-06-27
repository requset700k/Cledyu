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
	if h.denyIfNotSessionOwner(c, sess) {
		return nil, false
	}
	if sess.Provider != "" && sess.Provider != session.ProviderKubeVirt {
		h.err(c, http.StatusConflict, "VM file access is only available for KubeVirt sessions")
		return nil, false
	}
	if sess.Status != "ready" && sess.Status != "active" {
		h.err(c, http.StatusConflict, "session is not ready")
		return nil, false
	}
	return sess, true
}

func (h *Handler) handleVMFileError(c *gin.Context, err error, logMsg string) {
	switch {
	case errors.Is(err, vmfiles.ErrBusy):
		h.err(c, http.StatusTooManyRequests, "VM file access is busy")
	case errors.Is(err, vmfiles.ErrFileNotListed):
		h.err(c, http.StatusNotFound, "file not found")
	default:
		h.log.Warn(logMsg, zap.Error(err))
		h.err(c, http.StatusBadGateway, "VM file access failed")
	}
}
