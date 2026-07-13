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

// leaderboardTestHandler는 실제 임베드 콘텐츠 + fakePersistence 로 핸들러를 구성한다.
func leaderboardTestHandler(t *testing.T, fake *fakePersistence) *Handler {
	t.Helper()
	labs, err := content.Load()
	if err != nil {
		t.Fatalf("load lab content: %v", err)
	}
	return &Handler{log: zap.NewNop(), labs: labs, db: fake}
}

func TestGetLeaderboard_IncludesMeOutsideTopN(t *testing.T) {
	base := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	fake := newFakePersistence()
	// u-top: 2완료(20점), me: 1완료(10점). me 가 2위.
	fake.leaderboard = []store.LeaderboardRow{
		{UserID: "u-top", Name: "Top", LabID: "lab-docker-basics", CompletedAt: base},
		{UserID: "u-top", Name: "Top", LabID: "lab-k8s-basics", CompletedAt: base.Add(time.Hour)},
		{UserID: "me", Name: "Me", LabID: "lab-linux-basics", CompletedAt: base.Add(2 * time.Hour)},
	}
	h := leaderboardTestHandler(t, fake)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/leaderboard", func(c *gin.Context) {
		c.Set("user_id", "me")
		h.GetLeaderboard(c)
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/leaderboard", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body struct {
		HallOfFame []struct {
			Rank int    `json:"rank"`
			Name string `json:"name"`
		} `json:"hall_of_fame"`
		Me struct {
			Rank              int  `json:"rank"`
			Score             int  `json:"score"`
			LabsCompleted     int  `json:"labs_completed"`
			LeaderboardHidden bool `json:"leaderboard_hidden"`
		} `json:"me"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.HallOfFame) != 2 || body.HallOfFame[0].Name != "Top" {
		t.Fatalf("hall_of_fame mismatch: %+v", body.HallOfFame)
	}
	if body.Me.Rank != 2 || body.Me.Score != 10 || body.Me.LabsCompleted != 1 {
		t.Fatalf("me mismatch: %+v", body.Me)
	}
	if body.Me.LeaderboardHidden {
		t.Fatalf("leaderboard_hidden: want false, got true")
	}
}

func TestGetLeaderboard_ReturnsHiddenPreferenceWithoutCompletions(t *testing.T) {
	fake := newFakePersistence()
	fake.hidden["me"] = true
	h := leaderboardTestHandler(t, fake)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/leaderboard", func(c *gin.Context) {
		c.Set("user_id", "me")
		h.GetLeaderboard(c)
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/leaderboard", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body struct {
		Me struct {
			Rank              int  `json:"rank"`
			Score             int  `json:"score"`
			LabsCompleted     int  `json:"labs_completed"`
			LeaderboardHidden bool `json:"leaderboard_hidden"`
		} `json:"me"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Me.Rank != 0 || body.Me.Score != 0 || body.Me.LabsCompleted != 0 {
		t.Fatalf("new user rank mismatch: %+v", body.Me)
	}
	if !body.Me.LeaderboardHidden {
		t.Fatalf("leaderboard_hidden: want true, got false")
	}
}
