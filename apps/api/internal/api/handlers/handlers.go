// 핸들러는 도메인 단위로 파일 분리: health, lab, user, auth.
package handlers

import (
	"github.com/gin-gonic/gin"
	"github.com/requset700k/cledyu/api/internal/config"
	"go.uber.org/zap"
)

type Handler struct {
	cfg *config.Config
	log *zap.Logger
}

func New(cfg *config.Config, log *zap.Logger) *Handler {
	return &Handler{cfg: cfg, log: log}
}

// 프론트엔드 lib/api.ts의 ApiError 타입과 대응.
type errResp struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
}

func (h *Handler) err(c *gin.Context, status int, msg string) {
	c.JSON(status, errResp{Error: msg})
}
