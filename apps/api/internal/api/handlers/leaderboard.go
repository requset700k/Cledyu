package handlers

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/requset700k/cledyu/api/internal/store"
	"go.uber.org/zap"
)

// difficultyWeight는 랩 난이도별 완료 점수다(설계 2026-06-26). 새 난이도 추가 시 여기만 바꾼다.
var difficultyWeight = map[string]int{"beginner": 10, "intermediate": 25, "advanced": 50}

// weightForLab은 lab_id 의 완료 점수를 콘텐츠 난이도로 환산한다.
// 콘텐츠에 없는 랩(삭제 등)은 0, 알 수 없는 난이도는 beginner 로 폴백한다.
func (h *Handler) weightForLab(labID string) int {
	lc, ok := h.labs[labID]
	if !ok {
		return 0
	}
	w, ok := difficultyWeight[lc.Difficulty]
	if !ok {
		h.log.Warn("unknown lab difficulty — beginner 가중치로 폴백",
			zap.String("lab_id", labID), zap.String("difficulty", lc.Difficulty))
		return difficultyWeight["beginner"]
	}
	return w
}

// leaderboardEntry는 유저 1명의 집계 결과다.
type leaderboardEntry struct {
	UserID        string
	Name          string
	Score         int
	LabsCompleted int
	LastCompleted time.Time
	Rank          int
}

// rankEntries는 완료 행을 유저별로 집계하고 점수 내림차순(동점 시 마지막 완료 빠른 순)으로
// 정렬한 뒤 Rank(1부터)를 부여한다. weight 는 lab_id → 점수 함수다.
func rankEntries(rows []store.LeaderboardRow, weight func(labID string) int) []leaderboardEntry {
	byUser := map[string]*leaderboardEntry{}
	for _, r := range rows {
		e, ok := byUser[r.UserID]
		if !ok {
			e = &leaderboardEntry{UserID: r.UserID, Name: r.Name}
			byUser[r.UserID] = e
		}
		e.Score += weight(r.LabID)
		e.LabsCompleted++
		if r.CompletedAt.After(e.LastCompleted) {
			e.LastCompleted = r.CompletedAt
		}
	}

	out := make([]leaderboardEntry, 0, len(byUser))
	for _, e := range byUser {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].LastCompleted.Before(out[j].LastCompleted)
	})
	for i := range out {
		out[i].Rank = i + 1
	}
	return out
}

const (
	leaderboardTopN = 10                 // 명예의 전당/급상승 노출 인원
	recentWindow    = 7 * 24 * time.Hour // 급상승 윈도우
)

type leaderboardItem struct {
	Rank          int    `json:"rank"`
	Name          string `json:"name"`
	Score         int    `json:"score"`
	LabsCompleted int    `json:"labs_completed"`
}

type myRank struct {
	Rank          int `json:"rank"` // 0 = 미공개/완료 없음
	Score         int `json:"score"`
	LabsCompleted int `json:"labs_completed"`
}

func toItems(entries []leaderboardEntry, n int) []leaderboardItem {
	if len(entries) > n {
		entries = entries[:n]
	}
	out := make([]leaderboardItem, 0, len(entries))
	for _, e := range entries {
		out = append(out, leaderboardItem{Rank: e.Rank, Name: e.Name, Score: e.Score, LabsCompleted: e.LabsCompleted})
	}
	return out
}

// GetLeaderboard는 누적 명예의 전당 + 최근 7일 급상승 + 본인 순위를 반환한다.
// GET /api/v1/leaderboard
func (h *Handler) GetLeaderboard(c *gin.Context) {
	if h.db == nil {
		h.err(c, http.StatusServiceUnavailable, "leaderboard store not configured")
		return
	}
	ctx := c.Request.Context()
	uid := c.GetString("user_id")

	allRows, err := h.db.LeaderboardRows(ctx, nil)
	if err != nil {
		h.log.Error("leaderboard rows", zap.Error(err))
		h.err(c, http.StatusInternalServerError, "load leaderboard failed")
		return
	}
	ranked := rankEntries(allRows, h.weightForLab)

	since := time.Now().Add(-recentWindow)
	recentRows, err := h.db.LeaderboardRows(ctx, &since)
	if err != nil {
		h.log.Error("leaderboard recent rows", zap.Error(err))
		h.err(c, http.StatusInternalServerError, "load leaderboard failed")
		return
	}
	recent := rankEntries(recentRows, h.weightForLab)

	// 본인 순위 — Top N 밖이어도 항상 포함. 옵트아웃이면 공개 랭킹에 없어 Rank=0.
	me := myRank{}
	for _, e := range ranked {
		if e.UserID == uid {
			me = myRank{Rank: e.Rank, Score: e.Score, LabsCompleted: e.LabsCompleted}
			break
		}
	}
	// 공개 랭킹에 없으면(옵트아웃 등) 본인 완료 기록으로 점수/개수만 채운다.
	if me.Rank == 0 {
		me.Score, me.LabsCompleted = h.userScore(ctx, uid)
	}

	c.JSON(http.StatusOK, gin.H{
		"hall_of_fame":  toItems(ranked, leaderboardTopN),
		"recent_risers": toItems(recent, leaderboardTopN),
		"me":            me,
	})
}

// userScore는 유저의 누적 점수와 완료 수를 반환한다(옵트아웃 무관, 본인 조회용).
func (h *Handler) userScore(ctx context.Context, userID string) (score, count int) {
	if h.db == nil || userID == "" {
		return 0, 0
	}
	completions, err := h.db.ListCompletionsByUser(ctx, userID)
	if err != nil {
		h.log.Warn("user score lookup failed", zap.Error(err), zap.String("user_id", userID))
		return 0, 0
	}
	for _, comp := range completions {
		score += h.weightForLab(comp.LabID)
	}
	return score, len(completions)
}

// GetMyProgress는 본인 학습 현황(점수·순위·완료 수·난이도 분포·최근 완료)을 반환한다.
// GET /api/v1/me/progress
func (h *Handler) GetMyProgress(c *gin.Context) {
	if h.db == nil {
		h.err(c, http.StatusServiceUnavailable, "leaderboard store not configured")
		return
	}
	ctx := c.Request.Context()
	uid := c.GetString("user_id")

	completions, err := h.db.ListCompletionsByUser(ctx, uid)
	if err != nil {
		h.log.Error("list my completions", zap.Error(err))
		h.err(c, http.StatusInternalServerError, "load progress failed")
		return
	}

	score := 0
	byDifficulty := map[string]int{"beginner": 0, "intermediate": 0, "advanced": 0}
	for _, comp := range completions {
		score += h.weightForLab(comp.LabID)
		if lc, ok := h.labs[comp.LabID]; ok {
			byDifficulty[lc.Difficulty]++
		}
	}

	// 순위 — 공개 랭킹에서의 위치(옵트아웃이면 0).
	rank := 0
	if allRows, rerr := h.db.LeaderboardRows(ctx, nil); rerr == nil {
		for _, e := range rankEntries(allRows, h.weightForLab) {
			if e.UserID == uid {
				rank = e.Rank
				break
			}
		}
	}

	recent := completions
	if len(recent) > leaderboardTopN {
		recent = recent[:leaderboardTopN]
	}

	c.JSON(http.StatusOK, gin.H{
		"score":              score,
		"rank":               rank,
		"labs_completed":     len(completions),
		"by_difficulty":      byDifficulty,
		"recent_completions": recent,
	})
}

// SetMyPreferences는 리더보드 노출 옵트아웃을 갱신한다.
// PATCH /api/v1/me/preferences  body: { "leaderboard_hidden": true }
func (h *Handler) SetMyPreferences(c *gin.Context) {
	if h.db == nil {
		h.err(c, http.StatusServiceUnavailable, "leaderboard store not configured")
		return
	}
	var req struct {
		LeaderboardHidden *bool `json:"leaderboard_hidden" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		h.err(c, http.StatusBadRequest, "leaderboard_hidden is required")
		return
	}
	if err := h.db.SetLeaderboardHidden(c.Request.Context(), c.GetString("user_id"), *req.LeaderboardHidden); err != nil {
		h.log.Error("set leaderboard hidden", zap.Error(err))
		h.err(c, http.StatusInternalServerError, "update preferences failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"leaderboard_hidden": *req.LeaderboardHidden})
}
