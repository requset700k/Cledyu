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
