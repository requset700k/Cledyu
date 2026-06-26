package handlers

import (
	"testing"
	"time"

	"github.com/requset700k/cledyu/api/internal/content"
	"github.com/requset700k/cledyu/api/internal/store"
	"go.uber.org/zap"
)

func TestRankEntries_SortsByScoreThenRecency(t *testing.T) {
	base := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	// weight: 모든 랩 10점으로 고정해 점수=완료수×10
	weight := func(string) int { return 10 }
	rows := []store.LeaderboardRow{
		{UserID: "u1", Name: "A", LabID: "lab-a", CompletedAt: base},
		{UserID: "u1", Name: "A", LabID: "lab-b", CompletedAt: base.Add(time.Hour)},
		{UserID: "u2", Name: "B", LabID: "lab-a", CompletedAt: base.Add(2 * time.Hour)},
		{UserID: "u2", Name: "B", LabID: "lab-b", CompletedAt: base.Add(3 * time.Hour)},
		{UserID: "u3", Name: "C", LabID: "lab-a", CompletedAt: base},
	}
	got := rankEntries(rows, weight)

	if len(got) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(got))
	}
	// u1, u2 둘 다 20점이나 u1 의 마지막 완료(base+1h)가 u2(base+3h)보다 빨라 u1 이 1위.
	if got[0].UserID != "u1" || got[0].Rank != 1 || got[0].Score != 20 || got[0].LabsCompleted != 2 {
		t.Fatalf("rank1 mismatch: %+v", got[0])
	}
	if got[1].UserID != "u2" || got[1].Rank != 2 {
		t.Fatalf("rank2 mismatch: %+v", got[1])
	}
	if got[2].UserID != "u3" || got[2].Rank != 3 || got[2].Score != 10 {
		t.Fatalf("rank3 mismatch: %+v", got[2])
	}
}

func TestDifficultyWeight_Values(t *testing.T) {
	if difficultyWeight["beginner"] != 10 || difficultyWeight["intermediate"] != 25 || difficultyWeight["advanced"] != 50 {
		t.Fatalf("weight map changed unexpectedly: %+v", difficultyWeight)
	}
}

func TestWeightForLab_Fallbacks(t *testing.T) {
	h := &Handler{
		log: zap.NewNop(),
		labs: map[string]content.LabContent{
			"lab-known": {ID: "lab-known", Difficulty: "intermediate"},
			"lab-weird": {ID: "lab-weird", Difficulty: "expert"},
		},
	}
	if got := h.weightForLab("lab-missing"); got != 0 {
		t.Fatalf("unknown lab_id: want 0, got %d", got)
	}
	if got := h.weightForLab("lab-weird"); got != 10 {
		t.Fatalf("unknown difficulty fallback: want 10, got %d", got)
	}
	if got := h.weightForLab("lab-known"); got != 25 {
		t.Fatalf("known difficulty: want 25, got %d", got)
	}
}
