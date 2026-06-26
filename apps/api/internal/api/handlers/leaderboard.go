package handlers

import (
	"sort"
	"time"

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
