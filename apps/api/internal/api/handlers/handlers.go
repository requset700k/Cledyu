// Package handlers는 도메인별 HTTP 핸들러를 구현한다 (health, lab, user, auth).
package handlers

import (
	"github.com/gin-gonic/gin"
	"github.com/requset700k/cledyu/api/internal/config"
	"github.com/requset700k/cledyu/api/internal/content"
	"go.uber.org/zap"
)

// Handler는 모든 HTTP 핸들러의 공유 의존성을 보관한다.
type Handler struct {
	cfg      *config.Config
	log      *zap.Logger
	labs     map[string]content.LabContent // lab id → DSL 콘텐츠(스텝). GetLab/세션에서 사용.
	sessions *sessionStore                 // STUB: in-memory mock 세션 스토어.
}

// New는 설정과 로거를 받아 Handler를 생성한다.
// 시작 시 임베드된 Lab DSL 콘텐츠를 로드한다. 로드 실패해도 서버는 기동하되,
// 상세 페이지에 스텝이 비게 되므로 에러를 로깅한다.
func New(cfg *config.Config, log *zap.Logger) *Handler {
	labs, err := content.Load()
	if err != nil {
		log.Error("lab content load failed; detail pages will lack steps", zap.Error(err))
		labs = map[string]content.LabContent{}
	}
	return &Handler{
		cfg:      cfg,
		log:      log,
		labs:     labs,
		sessions: newSessionStore(),
	}
}

// 프론트엔드 lib/api.ts의 ApiError 타입과 대응.
type errResp struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
}

func (h *Handler) err(c *gin.Context, status int, msg string) {
	c.JSON(status, errResp{Error: msg})
}
