package handlers

import (
	"encoding/json"
	"errors"
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
	inProgress := []store.InProgressLab{{LabID: "lab-b", SessionID: "s2"}}

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
	if rows[1].SessionID != "s2" {
		t.Fatalf("row1 session_id mismatch: %+v", rows[1])
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
	fake.inProgress["u1"] = []store.InProgressLab{{LabID: "lab-k8s-basics", SessionID: "s2"}}
	fake.leaderboard = []store.LeaderboardRow{
		{UserID: "u1", Name: "U1", LabID: "lab-docker-basics", CompletedAt: at},
	}
	fake.hidden["u1"] = true
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
			LabID     string `json:"lab_id"`
			Status    string `json:"status"`
			SessionID string `json:"session_id"`
		} `json:"labs"`
		LeaderboardHidden bool `json:"leaderboard_hidden"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Summary.Score != 10 || body.Summary.Rank != 1 || body.Summary.LabsCompleted != 1 {
		t.Fatalf("summary mismatch: %+v", body.Summary)
	}
	if !body.LeaderboardHidden {
		t.Fatalf("leaderboard_hidden: want true, got false")
	}
	// 실제 임베드 카탈로그는 6개 랩(lab-helm-advanced 포함).
	if body.Summary.TotalLabs != 6 {
		t.Fatalf("total_labs want 6, got %d", body.Summary.TotalLabs)
	}
	statusByLab := map[string]string{}
	sessionByLab := map[string]string{}
	for _, l := range body.Labs {
		statusByLab[l.LabID] = l.Status
		sessionByLab[l.LabID] = l.SessionID
	}
	if statusByLab["lab-docker-basics"] != "completed" || statusByLab["lab-k8s-basics"] != "in_progress" {
		t.Fatalf("status mismatch: %+v", statusByLab)
	}
	if sessionByLab["lab-k8s-basics"] != "s2" {
		t.Fatalf("session_id mismatch: %+v", sessionByLab)
	}
}

func TestGetMyLabStatuses_DoesNotLoadLeaderboard(t *testing.T) {
	at := time.Date(2026, 6, 25, 9, 0, 0, 0, time.UTC)
	fake := newFakePersistence()
	fake.completions["u1|lab-docker-basics"] = "s1"
	fake.completionAt = map[string]string{"u1|lab-docker-basics": at.Format(time.RFC3339)}
	fake.inProgress["u1"] = []store.InProgressLab{{LabID: "lab-k8s-basics", SessionID: "s2"}}
	h := dashboardTestHandler(t, fake)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/me/lab-statuses", func(c *gin.Context) {
		c.Set("user_id", "u1")
		h.GetMyLabStatuses(c)
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/me/lab-statuses", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if fake.leaderboardCalls != 0 {
		t.Fatalf("leaderboard calls = %d, want 0", fake.leaderboardCalls)
	}
	var body struct {
		Items []labStatus `json:"items"`
		Total int         `json:"total"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Total != len(h.labs) || len(body.Items) != len(h.labs) {
		t.Fatalf("status rows mismatch: total=%d items=%d labs=%d", body.Total, len(body.Items), len(h.labs))
	}
	statusByLab := map[string]string{}
	for _, lab := range body.Items {
		statusByLab[lab.LabID] = lab.Status
	}
	if statusByLab["lab-docker-basics"] != "completed" || statusByLab["lab-k8s-basics"] != "in_progress" {
		t.Fatalf("status mismatch: %+v", statusByLab)
	}
}

func TestGetMyLabStatuses_ActiveRerunTakesPriorityOverCompletion(t *testing.T) {
	fake := newFakePersistence()
	fake.completions["u1|lab-docker-basics"] = "completed-session"
	fake.inProgress["u1"] = []store.InProgressLab{{
		LabID:     "lab-docker-basics",
		SessionID: "rerun-session",
	}}
	h := dashboardTestHandler(t, fake)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/me/lab-statuses", func(c *gin.Context) {
		c.Set("user_id", "u1")
		h.GetMyLabStatuses(c)
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/me/lab-statuses", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body struct {
		Items []labStatus `json:"items"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	for _, item := range body.Items {
		if item.LabID != "lab-docker-basics" {
			continue
		}
		if item.Status != "in_progress" || item.SessionID != "rerun-session" {
			t.Fatalf("active rerun mismatch: %+v", item)
		}
		return
	}
	t.Fatal("lab-docker-basics status not found")
}

func TestGetMyLabStatuses_CompletedSessionProgressRemainsCompleted(t *testing.T) {
	fake := newFakePersistence()
	fake.completions["u1|lab-docker-basics"] = "completed-session"
	fake.inProgress["u1"] = []store.InProgressLab{{
		LabID:     "lab-docker-basics",
		SessionID: "completed-session",
	}}
	h := dashboardTestHandler(t, fake)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/me/lab-statuses", func(c *gin.Context) {
		c.Set("user_id", "u1")
		h.GetMyLabStatuses(c)
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/me/lab-statuses", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body struct {
		Items []labStatus `json:"items"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	for _, item := range body.Items {
		if item.LabID != "lab-docker-basics" {
			continue
		}
		if item.Status != "completed" || item.SessionID != "" {
			t.Fatalf("completed status mismatch: %+v", item)
		}
		return
	}
	t.Fatal("lab-docker-basics status not found")
}

func TestGetMyLabStatuses_CompletedRerunProgressRemainsCompleted(t *testing.T) {
	fake := newFakePersistence()
	fake.completions["u1|lab-docker-basics"] = "first-completed-session"
	fake.inProgress["u1"] = []store.InProgressLab{{
		LabID:     "lab-docker-basics",
		SessionID: "completed-rerun-session",
		AllPassed: true,
	}}
	h := dashboardTestHandler(t, fake)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/me/lab-statuses", func(c *gin.Context) {
		c.Set("user_id", "u1")
		h.GetMyLabStatuses(c)
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/me/lab-statuses", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body struct {
		Items []labStatus `json:"items"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	for _, item := range body.Items {
		if item.LabID != "lab-docker-basics" {
			continue
		}
		if item.Status != "completed" || item.SessionID != "" {
			t.Fatalf("completed rerun status mismatch: %+v", item)
		}
		return
	}
	t.Fatal("lab-docker-basics status not found")
}

func TestGetMyLabStatuses_InProgressLookupErrorReturnsServerError(t *testing.T) {
	fake := newFakePersistence()
	fake.inProgressErr = errors.New("database unavailable")
	h := dashboardTestHandler(t, fake)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/me/lab-statuses", func(c *gin.Context) {
		c.Set("user_id", "u1")
		h.GetMyLabStatuses(c)
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/me/lab-statuses", nil))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
	}
}
