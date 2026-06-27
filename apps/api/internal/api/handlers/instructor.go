package handlers

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/requset700k/cledyu/api/internal/bq"
	"go.uber.org/zap"
)

// bqAnalytics는 D3 강사 분석 BigQuery 조회 의존성이다(*bq.Client 가 구현).
type bqAnalytics interface {
	LabCompletion(ctx context.Context) ([]bq.LabCompletionRow, error)
	StepFunnel(ctx context.Context) ([]bq.StepFunnelRow, error)
	HintUsage(ctx context.Context) ([]bq.HintUsageRow, error)
}

// GetInstructorAnalytics는 D2 BigQuery 뷰(완료율·이탈지점·힌트사용)를 강사에게 반환한다.
// GET /api/v1/instructor/analytics — RequireMinRole("instructor") 게이트(라우터).
func (h *Handler) GetInstructorAnalytics(c *gin.Context) {
	if h.bq == nil {
		h.err(c, http.StatusServiceUnavailable, "analytics store not configured")
		return
	}
	ctx := c.Request.Context()

	completion, err := h.bq.LabCompletion(ctx)
	if err != nil {
		h.log.Error("bq lab_completion", zap.Error(err))
		h.err(c, http.StatusInternalServerError, "load analytics failed")
		return
	}
	funnel, err := h.bq.StepFunnel(ctx)
	if err != nil {
		h.log.Error("bq step_funnel", zap.Error(err))
		h.err(c, http.StatusInternalServerError, "load analytics failed")
		return
	}
	hints, err := h.bq.HintUsage(ctx)
	if err != nil {
		h.log.Error("bq hint_usage", zap.Error(err))
		h.err(c, http.StatusInternalServerError, "load analytics failed")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"lab_completion": completion,
		"step_funnel":    funnel,
		"hint_usage":     hints,
	})
}
