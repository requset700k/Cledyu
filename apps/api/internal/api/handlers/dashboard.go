package handlers

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/requset700k/cledyu/api/internal/content"
	"github.com/requset700k/cledyu/api/internal/store"
	"go.uber.org/zap"
)

// difficultyProgress는 난이도별 완료/전체 진행이다.
type difficultyProgress struct {
	Done  int `json:"done"`
	Total int `json:"total"`
}

// dashboardSummary는 대시보드 상단 요약이다. Score/Rank 는 핸들러가 채운다.
type dashboardSummary struct {
	Score         int                           `json:"score"`
	Rank          int                           `json:"rank"`
	LabsCompleted int                           `json:"labs_completed"`
	TotalLabs     int                           `json:"total_labs"`
	CompletionPct int                           `json:"completion_pct"`
	ByDifficulty  map[string]difficultyProgress `json:"by_difficulty"`
}

// dashboardLab는 랩별 상태 1건이다.
type dashboardLab struct {
	LabID       string     `json:"lab_id"`
	Title       string     `json:"title"`
	Difficulty  string     `json:"difficulty"`
	Status      string     `json:"status"` // completed | in_progress | not_started
	SessionID   string     `json:"session_id,omitempty"`
	CompletedAt *time.Time `json:"completed_at"`
}

// labStatus는 카탈로그가 사용하는 최소 상태다. 랭킹·점수·선호 정보는 포함하지 않는다.
type labStatus struct {
	LabID     string `json:"lab_id"`
	Status    string `json:"status"`
	SessionID string `json:"session_id,omitempty"`
}

// buildDashboard는 카탈로그·완료·진행중을 대조해 요약과 랩별 상태를 만든다(순수 함수).
// Score/Rank 는 채우지 않는다(DB 랭킹 필요 — 핸들러가 설정).
func buildDashboard(labs map[string]content.LabContent, completions []store.Completion, inProgress []store.InProgressLab) (dashboardSummary, []dashboardLab) {
	completedAt := make(map[string]time.Time, len(completions))
	for _, c := range completions {
		completedAt[c.LabID] = c.CompletedAt
	}
	started := make(map[string]string, len(inProgress))
	for _, row := range inProgress {
		started[row.LabID] = row.SessionID
	}

	ids := make([]string, 0, len(labs))
	for id := range labs {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	byDiff := map[string]difficultyProgress{
		"beginner":     {},
		"intermediate": {},
		"advanced":     {},
	}
	rows := make([]dashboardLab, 0, len(ids))
	for _, id := range ids {
		lc := labs[id]
		row := dashboardLab{LabID: id, Title: lc.Title, Difficulty: lc.Difficulty, Status: "not_started"}
		if ts, ok := completedAt[id]; ok {
			row.Status = "completed"
			t := ts
			row.CompletedAt = &t
		} else if sessionID, ok := started[id]; ok {
			row.Status = "in_progress"
			row.SessionID = sessionID
		}
		rows = append(rows, row)

		dp := byDiff[lc.Difficulty]
		dp.Total++
		if row.Status == "completed" {
			dp.Done++
		}
		byDiff[lc.Difficulty] = dp
	}

	summary := dashboardSummary{
		LabsCompleted: len(completions),
		TotalLabs:     len(ids),
		ByDifficulty:  byDiff,
	}
	if summary.TotalLabs > 0 {
		pct := summary.LabsCompleted * 100 / summary.TotalLabs
		if pct > 100 {
			pct = 100
		}
		summary.CompletionPct = pct
	}
	return summary, rows
}

// userRank는 공개 랭킹에서 유저의 순위를 반환한다(없으면 0). GetMyProgress 와 공유.
func (h *Handler) userRank(ctx context.Context, userID string) int {
	allRows, err := h.db.LeaderboardRows(ctx, nil)
	if err != nil {
		return 0
	}
	for _, e := range rankEntries(allRows, h.weightForLab) {
		if e.UserID == userID {
			return e.Rank
		}
	}
	return 0
}

// GetMyDashboard는 학습자 개인 대시보드(요약 + 랩별 상태)를 반환한다. Postgres only.
// GET /api/v1/me/dashboard
func (h *Handler) GetMyDashboard(c *gin.Context) {
	if h.db == nil {
		h.err(c, http.StatusServiceUnavailable, "dashboard store not configured")
		return
	}
	ctx := c.Request.Context()
	uid := c.GetString("user_id")

	completions, err := h.db.ListCompletionsByUser(ctx, uid)
	if err != nil {
		h.log.Error("list completions", zap.Error(err))
		h.err(c, http.StatusInternalServerError, "load dashboard failed")
		return
	}
	// 진행중은 부가 정보 — 실패해도 치명적이지 않으니 빈 목록으로 강등.
	inProgress, err := h.db.ListInProgressLabsByUser(ctx, uid)
	if err != nil {
		h.log.Warn("list in-progress labs", zap.Error(err), zap.String("user_id", uid))
		inProgress = nil
	}

	summary, rows := buildDashboard(h.labs, completions, inProgress)
	for _, comp := range completions {
		summary.Score += h.weightForLab(comp.LabID)
	}
	summary.Rank = h.userRank(ctx, uid)
	hidden, err := h.db.LeaderboardHidden(ctx, uid)
	if err != nil {
		h.log.Error("dashboard preference", zap.Error(err), zap.String("user_id", uid))
		h.err(c, http.StatusInternalServerError, "load dashboard failed")
		return
	}

	recent := completions
	if len(recent) > leaderboardTopN {
		recent = recent[:leaderboardTopN]
	}

	c.JSON(http.StatusOK, gin.H{
		"summary":            summary,
		"labs":               rows,
		"recent_completions": recent,
		"leaderboard_hidden": hidden,
	})
}

// GetMyLabStatuses는 카탈로그용 랩 상태만 반환한다. 전체 랭킹 집계는 실행하지 않는다.
// GET /api/v1/me/lab-statuses
func (h *Handler) GetMyLabStatuses(c *gin.Context) {
	if h.db == nil {
		h.err(c, http.StatusServiceUnavailable, "dashboard store not configured")
		return
	}
	ctx := c.Request.Context()
	uid := c.GetString("user_id")

	completions, err := h.db.ListCompletionsByUser(ctx, uid)
	if err != nil {
		h.log.Error("list completions", zap.Error(err))
		h.err(c, http.StatusInternalServerError, "load lab statuses failed")
		return
	}
	inProgress, err := h.db.ListInProgressLabsByUser(ctx, uid)
	if err != nil {
		h.log.Error("list in-progress labs", zap.Error(err), zap.String("user_id", uid))
		h.err(c, http.StatusInternalServerError, "load lab statuses failed")
		return
	}

	activeProgress := make([]store.InProgressLab, 0, len(inProgress))
	for _, progress := range inProgress {
		if !progress.AllPassed {
			activeProgress = append(activeProgress, progress)
		}
	}

	_, dashboardRows := buildDashboard(h.labs, completions, activeProgress)
	completedSessionByLab := make(map[string]string, len(completions))
	for _, completion := range completions {
		completedSessionByLab[completion.LabID] = completion.SessionID
	}
	activeSessionByLab := make(map[string]string, len(activeProgress))
	for _, progress := range activeProgress {
		activeSessionByLab[progress.LabID] = progress.SessionID
	}
	rows := make([]labStatus, 0, len(dashboardRows))
	for _, row := range dashboardRows {
		status := labStatus{
			LabID:     row.LabID,
			Status:    row.Status,
			SessionID: row.SessionID,
		}
		// 완료 세션과 다른 진행 기록이 있으면 재실행 중이므로 현재 세션을 우선한다.
		if sessionID, ok := activeSessionByLab[row.LabID]; ok && completedSessionByLab[row.LabID] != sessionID {
			status.Status = "in_progress"
			status.SessionID = sessionID
		}
		rows = append(rows, status)
	}
	c.JSON(http.StatusOK, gin.H{"items": rows, "total": len(rows)})
}
