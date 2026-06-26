package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/requset700k/cledyu/api/internal/content"
	"github.com/requset700k/cledyu/api/internal/store"
	"go.uber.org/zap"
)

func TestBuildDashboard_StatusAndSummary(t *testing.T) {
	at := time.Date(2026, 6, 25, 9, 0, 0, 0, time.UTC)
	labs := map[string]content.LabContent{
		"lab-a": {ID: "lab-a", Title: "A", Difficulty: "beginner"},
		"lab-b": {ID: "lab-b", Title: "B", Difficulty: "beginner"},
		"lab-c": {ID: "lab-c", Title: "C", Difficulty: "intermediate"},
	}
	completions := []store.Completion{{LabID: "lab-a", CompletedAt: at}}
	inProgress := []string{"lab-b"}

	summary, rows := buildDashboard(labs, completions, inProgress)

	if summary.TotalLabs != 3 || summary.LabsCompleted != 1 || summary.CompletionPct != 33 {
		t.Fatalf("summary mismatch: %+v", summary)
	}
	if summary.ByDifficulty["beginner"].Done != 1 || summary.ByDifficulty["beginner"].Total != 2 {
		t.Fatalf("beginner progress mismatch: %+v", summary.ByDifficulty["beginner"])
	}
	if summary.ByDifficulty["intermediate"].Total != 1 || summary.ByDifficulty["intermediate"].Done != 0 {
		t.Fatalf("intermediate progress mismatch: %+v", summary.ByDifficulty["intermediate"])
	}
	// rows 는 lab_id 정렬: lab-a(completed), lab-b(in_progress), lab-c(not_started)
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}
	if rows[0].LabID != "lab-a" || rows[0].Status != "completed" || rows[0].CompletedAt == nil {
		t.Fatalf("row0 mismatch: %+v", rows[0])
	}
	if rows[1].LabID != "lab-b" || rows[1].Status != "in_progress" || rows[1].CompletedAt != nil {
		t.Fatalf("row1 mismatch: %+v", rows[1])
	}
	if rows[2].LabID != "lab-c" || rows[2].Status != "not_started" {
		t.Fatalf("row2 mismatch: %+v", rows[2])
	}
}

func dashboardTestHandler(t *testing.T, fake *fakePersistence) *Handler {
	t.Helper()
	labs, err := content.Load()
	if err != nil {
		t.Fatalf("load lab content: %v", err)
	}
	return &Handler{log: zap.NewNop(), labs: labs, db: fake}
}

func TestGetMyDashboard_PostgresOnly(t *testing.T) {
	at := time.Date(2026, 6, 25, 9, 0, 0, 0, time.UTC)
	fake := newFakePersistence()
	fake.completions["u1|lab-docker-basics"] = "s1"
	fake.completionAt = map[string]string{"u1|lab-docker-basics": at.Format(time.RFC3339)}
	fake.inProgress["u1"] = []string{"lab-k8s-basics"}
	fake.leaderboard = []store.LeaderboardRow{
		{UserID: "u1", Name: "U1", LabID: "lab-docker-basics", CompletedAt: at},
	}
	h := dashboardTestHandler(t, fake)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/me/dashboard", func(c *gin.Context) {
		c.Set("user_id", "u1")
		h.GetMyDashboard(c)
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/me/dashboard", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body struct {
		Summary struct {
			Score         int `json:"score"`
			Rank          int `json:"rank"`
			LabsCompleted int `json:"labs_completed"`
			TotalLabs     int `json:"total_labs"`
		} `json:"summary"`
		Labs []struct {
			LabID  string `json:"lab_id"`
			Status string `json:"status"`
		} `json:"labs"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Summary.Score != 10 || body.Summary.Rank != 1 || body.Summary.LabsCompleted != 1 {
		t.Fatalf("summary mismatch: %+v", body.Summary)
	}
	// 실제 임베드 카탈로그는 6개 랩(lab-helm-advanced 포함).
	if body.Summary.TotalLabs != 6 {
		t.Fatalf("total_labs want 6, got %d", body.Summary.TotalLabs)
	}
	statusByLab := map[string]string{}
	for _, l := range body.Labs {
		statusByLab[l.LabID] = l.Status
	}
	if statusByLab["lab-docker-basics"] != "completed" || statusByLab["lab-k8s-basics"] != "in_progress" {
		t.Fatalf("status mismatch: %+v", statusByLab)
	}
}
