package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/requset700k/cledyu/api/internal/bq"
	"go.uber.org/zap"
)

// fakeBQ는 bqAnalytics 의 테스트 더블이다.
type fakeBQ struct {
	completion []bq.LabCompletionRow
	funnel     []bq.StepFunnelRow
	hints      []bq.HintUsageRow
}

func (f fakeBQ) LabCompletion(context.Context) ([]bq.LabCompletionRow, error) {
	return f.completion, nil
}
func (f fakeBQ) StepFunnel(context.Context) ([]bq.StepFunnelRow, error) { return f.funnel, nil }
func (f fakeBQ) HintUsage(context.Context) ([]bq.HintUsageRow, error)   { return f.hints, nil }

func TestGetInstructorAnalytics_ReturnsViews(t *testing.T) {
	h := &Handler{log: zap.NewNop()}
	h.SetBQAnalytics(fakeBQ{
		completion: []bq.LabCompletionRow{{LabID: "lab-docker-basics", Started: 5, Completed: 3, CompletionRate: 0.6}},
		funnel:     []bq.StepFunnelRow{{LabID: "lab-k8s-basics", StepID: 6, ValidationFailures: 4}},
		hints:      []bq.HintUsageRow{{LabID: "lab-docker-basics", StepID: 3, HintSource: "ai", HintCount: 7}},
	})

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/instructor/analytics", h.GetInstructorAnalytics)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/instructor/analytics", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body struct {
		LabCompletion []struct {
			LabID          string  `json:"lab_id"`
			CompletionRate float64 `json:"completion_rate"`
		} `json:"lab_completion"`
		StepFunnel []struct {
			ValidationFailures int64 `json:"validation_failures"`
		} `json:"step_funnel"`
		HintUsage []struct {
			HintSource string `json:"hint_source"`
		} `json:"hint_usage"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.LabCompletion) != 1 || body.LabCompletion[0].CompletionRate != 0.6 {
		t.Fatalf("lab_completion mismatch: %+v", body.LabCompletion)
	}
	if len(body.StepFunnel) != 1 || body.StepFunnel[0].ValidationFailures != 4 {
		t.Fatalf("step_funnel mismatch: %+v", body.StepFunnel)
	}
	if len(body.HintUsage) != 1 || body.HintUsage[0].HintSource != "ai" {
		t.Fatalf("hint_usage mismatch: %+v", body.HintUsage)
	}
}

func TestGetInstructorAnalytics_503WhenUnconfigured(t *testing.T) {
	h := &Handler{log: zap.NewNop()} // bq nil
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/instructor/analytics", h.GetInstructorAnalytics)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/instructor/analytics", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}
