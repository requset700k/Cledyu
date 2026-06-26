# 유저별 리더보드 v1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 학습자가 자신의 교육 현황을 보고 상위 학습자가 명예의 전당으로 공시되는 리더보드를 온프렘 Postgres 위에 구현한다.

**Architecture:** 점수는 기존 `lab_completions`(최초 완료 1건/랩)를 랩 콘텐츠의 `difficulty`로 가중 합산해 서버(Go/Gin)에서 계산한다. 집계는 요청 시 수행(소규모). 프론트(Next.js)는 신규 `/leaderboard` 페이지에서 결과를 렌더한다. 신규 인프라/외부 서비스 없음 — 컬럼 1개 + 읽기 쿼리만 추가.

**Tech Stack:** Go 1.x + Gin + pgx/v5(Postgres), Next.js App Router + TanStack Query, node:test(웹 lib 테스트), Go testing.

## Global Constraints

- 문서/주석은 한국어, 코드 식별자·CLI·키는 영어 (repo 규칙).
- GitHub 텍스트(PR/리뷰/커밋)에 이모지 금지. 심각도는 텍스트 라벨.
- 커밋 subject 소문자 시작, Conventional Commits. scope-enum: `api` `web` 등. body 줄당 ≤100자.
- PR 제목: `<type>(<scope>): <소문자 subject>` (action-semantic-pull-request 강제).
- 점수 가중치: `beginner: 10, intermediate: 25, advanced: 50`. 알 수 없는 difficulty → beginner(10) 폴백 + 로그 경고. 콘텐츠에 없는 lab_id → 0점.
- 명예의 전당/급상승 노출 인원 기본 N = 10 (상수).
- 최근 급상승 윈도우 = 7일.
- 옵트아웃 제외는 서버측에서 수행. 타인 정보는 이름만 노출(email·user_id·org 비노출).
- 저장소에 live-DB store 단위테스트가 없다(`go test -race`는 DB 없이 실행). 영속 로직은 `fakePersistence`를 통한 핸들러 테스트로 검증한다. 새 live-DB 테스트 하니스를 만들지 않는다.
- 온프렘 전용 가정(Longhorn 경로/노드 로컬 등)을 코드에 넣지 않는다(클라우드 이관 안전성).
- 검증 게이트(머지 후 CI 안 깨지게): `go test ./... -race`, `golangci-lint run`, `gofmt -l`, `pnpm typecheck`, `pnpm lint`, `node --test`.

**작업 디렉터리:** repo 루트 `/Users/kylekim1223/request700k/cledyu`. Go 명령은 `apps/api`, 웹 명령은 `apps/web`에서 실행. 브랜치: `feat/user-leaderboard` (이미 생성됨).

---

### Task 1: 영속 계층 — 마이그레이션 + store 메서드 + 인터페이스/테스트더블 확장

리더보드 데이터 접근을 위한 스키마 컬럼과 store 메서드를 추가하고, `persistence` 인터페이스와 `fakePersistence` 테스트 더블을 함께 확장해 전체가 컴파일되게 한다. 이 태스크의 산출물은 `go build`/`go vet` 통과다(live-DB 테스트 없음).

**Files:**
- Create: `apps/api/internal/store/migrations/0002_leaderboard.sql`
- Modify: `apps/api/internal/store/store.go` (lab completions 섹션 끝에 추가)
- Modify: `apps/api/internal/api/handlers/progress.go:13-22` (persistence 인터페이스)
- Modify: `apps/api/internal/api/handlers/progress_test.go` (fakePersistence)

**Interfaces:**
- Produces:
  - `store.LeaderboardRow struct { UserID string; Name string; LabID string; CompletedAt time.Time }`
  - `(*store.Store).LeaderboardRows(ctx context.Context, since *time.Time) ([]store.LeaderboardRow, error)`
  - `(*store.Store).SetLeaderboardHidden(ctx context.Context, userID string, hidden bool) error`
  - 위 두 메서드가 `persistence` 인터페이스에 추가됨.

- [ ] **Step 1: 마이그레이션 파일 생성**

Create `apps/api/internal/store/migrations/0002_leaderboard.sql`:

```sql
-- 리더보드 노출 옵트아웃 — 기본 노출(false). 학습자가 명시적으로 숨길 수 있다.
ALTER TABLE users ADD COLUMN IF NOT EXISTS leaderboard_hidden boolean NOT NULL DEFAULT false;
```

- [ ] **Step 2: store.go에 타입과 메서드 추가**

`apps/api/internal/store/store.go`의 `ListCompletionsByUser` 함수 정의 끝(닫는 `}`) 다음 줄에 추가:

```go
// LeaderboardRow는 리더보드 집계용 완료 1건이다(유저 표시명 포함).
type LeaderboardRow struct {
	UserID      string    `json:"user_id"`
	Name        string    `json:"name"`
	LabID       string    `json:"lab_id"`
	CompletedAt time.Time `json:"completed_at"`
}

// LeaderboardRows는 옵트아웃하지 않은 유저의 랩 완료 행을 반환한다.
// since 가 nil 이 아니면 completed_at >= *since 만 포함한다(최근 급상승 윈도우).
// 난이도 가중은 호출부(핸들러)가 in-memory 콘텐츠로 수행하므로 여기선 raw 행만 돌려준다.
// users 와 INNER JOIN — 완료 기록은 로그인(미러 생성)을 전제로 한다.
func (s *Store) LeaderboardRows(ctx context.Context, since *time.Time) ([]LeaderboardRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT c.user_id, u.name, c.lab_id, c.completed_at
		FROM lab_completions c
		JOIN users u ON u.id = c.user_id
		WHERE u.leaderboard_hidden = false
		  AND ($1::timestamptz IS NULL OR c.completed_at >= $1)`, since)
	if err != nil {
		return nil, fmt.Errorf("leaderboard rows: %w", err)
	}
	defer rows.Close()

	out := make([]LeaderboardRow, 0)
	for rows.Next() {
		var r LeaderboardRow
		if err := rows.Scan(&r.UserID, &r.Name, &r.LabID, &r.CompletedAt); err != nil {
			return nil, fmt.Errorf("scan leaderboard row: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetLeaderboardHidden은 유저의 리더보드 노출 여부를 갱신한다(옵트아웃 토글).
func (s *Store) SetLeaderboardHidden(ctx context.Context, userID string, hidden bool) error {
	if _, err := s.pool.Exec(ctx,
		`UPDATE users SET leaderboard_hidden = $2 WHERE id = $1`, userID, hidden); err != nil {
		return fmt.Errorf("set leaderboard hidden: %w", err)
	}
	return nil
}
```

- [ ] **Step 3: persistence 인터페이스 확장**

`apps/api/internal/api/handlers/progress.go`의 `persistence` 인터페이스(13-22행), `RecordCompletion` 줄 다음에 추가:

```go
	LeaderboardRows(ctx context.Context, since *time.Time) ([]store.LeaderboardRow, error)
	SetLeaderboardHidden(ctx context.Context, userID string, hidden bool) error
```

`progress.go` import에 `time`이 이미 있는지 확인(있음 — `dbTimeout` 사용). `store`도 이미 import됨.

- [ ] **Step 4: fakePersistence 확장**

`apps/api/internal/api/handlers/progress_test.go`의 `fakePersistence` struct에 필드 추가(`saves int` 다음):

```go
	leaderboard []store.LeaderboardRow // LeaderboardRows 가 돌려줄 행
	hidden      map[string]bool        // SetLeaderboardHidden 이 기록
```

`newFakePersistence`의 반환 리터럴에 `hidden: map[string]bool{},` 추가.

같은 파일 끝(마지막 메서드 다음)에 추가. import에 `time` 추가 필요:

```go
func (f *fakePersistence) LeaderboardRows(_ context.Context, since *time.Time) ([]store.LeaderboardRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.LeaderboardRow, 0, len(f.leaderboard))
	for _, r := range f.leaderboard {
		if since != nil && r.CompletedAt.Before(*since) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func (f *fakePersistence) SetLeaderboardHidden(_ context.Context, userID string, hidden bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hidden[userID] = hidden
	return nil
}
```

- [ ] **Step 5: 컴파일·vet 확인**

Run: `cd apps/api && go build ./... && go vet ./...`
Expected: 에러 없이 종료(exit 0).

- [ ] **Step 6: gofmt 확인**

Run: `cd apps/api && gofmt -l internal/store/store.go internal/api/handlers/progress.go internal/api/handlers/progress_test.go`
Expected: 출력 없음(이미 포맷됨).

- [ ] **Step 7: 커밋**

```bash
cd /Users/kylekim1223/request700k/cledyu
git add apps/api/internal/store/migrations/0002_leaderboard.sql apps/api/internal/store/store.go apps/api/internal/api/handlers/progress.go apps/api/internal/api/handlers/progress_test.go
git commit -m "feat(api): add leaderboard persistence layer" \
  -m "Add leaderboard_hidden column, LeaderboardRows and SetLeaderboardHidden store
methods, and extend the persistence interface and test double."
```

---

### Task 2: 점수 계산 코어 (가중치 + 랭킹) — TDD

순수 함수로 점수 가중과 랭킹 정렬을 구현한다. 난이도는 핸들러가 콘텐츠에서 조회해 weight 함수로 주입하므로, 랭킹 로직은 콘텐츠/DB와 분리되어 단위테스트가 쉽다.

**Files:**
- Create: `apps/api/internal/api/handlers/leaderboard.go`
- Create: `apps/api/internal/api/handlers/leaderboard_test.go`

**Interfaces:**
- Consumes: `store.LeaderboardRow` (Task 1).
- Produces:
  - `leaderboardEntry struct { UserID string; Name string; Score int; LabsCompleted int; LastCompleted time.Time; Rank int }`
  - `rankEntries(rows []store.LeaderboardRow, weight func(labID string) int) []leaderboardEntry` — 유저별 집계 후 점수 내림차순(동점 시 LastCompleted 빠른 순) 정렬 + Rank(1부터) 부여.
  - `difficultyWeight map[string]int`

- [ ] **Step 1: 실패하는 테스트 작성**

Create `apps/api/internal/api/handlers/leaderboard_test.go`:

```go
package handlers

import (
	"testing"
	"time"

	"github.com/requset700k/cledyu/api/internal/store"
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
```

- [ ] **Step 2: 테스트 실패 확인**

Run: `cd apps/api && go test ./internal/api/handlers/ -run TestRankEntries -v`
Expected: 컴파일 실패 — `undefined: rankEntries`.

- [ ] **Step 3: 최소 구현 작성**

Create `apps/api/internal/api/handlers/leaderboard.go`:

```go
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
```

- [ ] **Step 4: 테스트 통과 확인**

Run: `cd apps/api && go test ./internal/api/handlers/ -run TestRankEntries -v`
Expected: PASS.

- [ ] **Step 5: 가중치 폴백 테스트 추가**

`leaderboard_test.go`에 추가:

```go
func TestDifficultyWeight_Values(t *testing.T) {
	if difficultyWeight["beginner"] != 10 || difficultyWeight["intermediate"] != 25 || difficultyWeight["advanced"] != 50 {
		t.Fatalf("weight map changed unexpectedly: %+v", difficultyWeight)
	}
}
```

Run: `cd apps/api && go test ./internal/api/handlers/ -run 'TestRankEntries|TestDifficultyWeight' -v`
Expected: 둘 다 PASS.

- [ ] **Step 6: 커밋**

```bash
cd /Users/kylekim1223/request700k/cledyu
git add apps/api/internal/api/handlers/leaderboard.go apps/api/internal/api/handlers/leaderboard_test.go
git commit -m "feat(api): add leaderboard scoring and ranking core" \
  -m "Difficulty-weighted completion scoring with rank assignment, tie-broken by
earliest last-completion. Unknown difficulty falls back to beginner."
```

---

### Task 3: 리더보드 API 핸들러 + 라우터 + /me 점수 연동 — TDD

`/leaderboard`, `/me/progress`, `/me/preferences` 핸들러를 추가하고 `/me`의 points 스텁을 실제 점수로 교체한다.

**Files:**
- Modify: `apps/api/internal/api/handlers/leaderboard.go` (핸들러 추가)
- Modify: `apps/api/internal/api/handlers/user.go` (GetMe 점수)
- Modify: `apps/api/internal/api/router.go:80` 부근 (라우트 등록)
- Modify: `apps/api/internal/api/handlers/leaderboard_test.go` (핸들러 테스트)

**Interfaces:**
- Consumes: `rankEntries`, `weightForLab` (Task 2); `h.db` persistence (Task 1); `h.err`, `c.GetString("user_id"/"user_name")` (기존).
- Produces:
  - `(*Handler).GetLeaderboard(c *gin.Context)` — `GET /api/v1/leaderboard`
  - `(*Handler).GetMyProgress(c *gin.Context)` — `GET /api/v1/me/progress`
  - `(*Handler).SetMyPreferences(c *gin.Context)` — `PATCH /api/v1/me/preferences`
  - `(*Handler).userScore(ctx, userID string) int` — GetMe 가 사용.

- [ ] **Step 1: 실패하는 핸들러 테스트 작성**

`leaderboard_test.go`에 추가(상단 import에 `encoding/json`, `net/http`, `net/http/httptest`, `github.com/gin-gonic/gin`, `go.uber.org/zap`, `github.com/requset700k/cledyu/api/internal/content` 필요):

```go
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
			Rank          int `json:"rank"`
			Score         int `json:"score"`
			LabsCompleted int `json:"labs_completed"`
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
}
```

- [ ] **Step 2: 테스트 실패 확인**

Run: `cd apps/api && go test ./internal/api/handlers/ -run TestGetLeaderboard -v`
Expected: 컴파일 실패 — `undefined: (*Handler).GetLeaderboard`.

- [ ] **Step 3: 핸들러 구현**

`leaderboard.go` 상단 import를 다음으로 교체(net/http, time, gin 추가):

```go
import (
	"net/http"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/requset700k/cledyu/api/internal/store"
	"go.uber.org/zap"
)
```

파일 끝에 추가:

```go
const (
	leaderboardTopN = 10               // 명예의 전당/급상승 노출 인원
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
```

참고: `sort`는 Task 2에서 이미 쓰이므로 import 유지. 만약 goimports가 중복/미사용을 지적하면 정리.

- [ ] **Step 4: 테스트 통과 확인**

Run: `cd apps/api && go test ./internal/api/handlers/ -run TestGetLeaderboard -v`
Expected: PASS.

- [ ] **Step 5: GetMe 점수 연동**

`apps/api/internal/api/handlers/user.go`의 `GetMe`를 다음으로 교체:

```go
// GetMe는 검증된 JWT claims(미들웨어가 컨텍스트에 주입)에서 현재 사용자 정보를 반환한다. 인증 필요.
// GET /api/v1/me
// points = 난이도 가중 누적 점수. badges 는 v2 까지 STUB.
func (h *Handler) GetMe(c *gin.Context) {
	points := 0
	if h.db != nil {
		points, _ = h.userScore(c.Request.Context(), c.GetString("user_id"))
	}
	c.JSON(http.StatusOK, gin.H{
		"id":     c.GetString("user_id"),
		"email":  c.GetString("user_email"),
		"name":   c.GetString("user_name"),
		"role":   c.GetString("user_role"),
		"org":    c.GetString("user_org"),
		"points": points,
		"badges": []gin.H{},
	})
}
```

- [ ] **Step 6: 라우터 등록**

`apps/api/internal/api/router.go`의 `v1.GET("/me", h.GetMe)`(80행) 다음 줄들에 추가:

```go
		v1.GET("/me/progress", h.GetMyProgress)
		v1.PATCH("/me/preferences", h.SetMyPreferences)
		v1.GET("/leaderboard", h.GetLeaderboard)
```

- [ ] **Step 7: 전체 핸들러 패키지 테스트 + vet + fmt**

Run: `cd apps/api && go test ./internal/api/handlers/ -v && go vet ./... && gofmt -l internal/api`
Expected: 모든 테스트 PASS, vet 무출력, gofmt 무출력.

- [ ] **Step 8: 커밋**

```bash
cd /Users/kylekim1223/request700k/cledyu
git add apps/api/internal/api/handlers/leaderboard.go apps/api/internal/api/handlers/leaderboard_test.go apps/api/internal/api/handlers/user.go apps/api/internal/api/router.go
git commit -m "feat(api): add leaderboard and personal progress endpoints" \
  -m "GET /leaderboard (hall of fame, recent risers, my rank), GET /me/progress,
PATCH /me/preferences (opt-out), and wire real score into GET /me."
```

---

### Task 4: 웹 타입 + API 클라이언트 + 표시 헬퍼 — TDD

프론트 타입과 API 호출을 추가하고, "내 순위를 목록에 합치는" 순수 헬퍼를 node:test로 검증한다.

**Files:**
- Modify: `apps/web/lib/types.ts`
- Modify: `apps/web/lib/api.ts`
- Create: `apps/web/lib/leaderboard.mjs`
- Create: `apps/web/lib/leaderboard.test.mjs`

**Interfaces:**
- Produces:
  - types: `LeaderboardItem`, `MyRank`, `LeaderboardResponse`, `MyProgress`
  - `api.leaderboard.get()`, `api.me.progress()`, `api.me.setPreferences(hidden: boolean)`
  - `mergeMyRank(hallOfFame, me)` — me 가 Top N 안에 있으면 그대로, 밖이면 끝에 `{ ...me, isMe: true }` 추가하고 안에 있으면 해당 행에 `isMe: true` 표시.

- [ ] **Step 1: 실패하는 헬퍼 테스트 작성**

Create `apps/web/lib/leaderboard.test.mjs`:

```javascript
import assert from 'node:assert/strict';
import { describe, it } from 'node:test';
import { mergeMyRank } from './leaderboard.mjs';

describe('mergeMyRank', () => {
  it('marks the current user inside the top N', () => {
    const hof = [
      { rank: 1, name: 'A', score: 20, labs_completed: 2 },
      { rank: 2, name: 'Me', score: 10, labs_completed: 1 },
    ];
    const me = { rank: 2, score: 10, labs_completed: 1 };
    const rows = mergeMyRank(hof, me);
    assert.equal(rows.length, 2);
    assert.equal(rows[1].isMe, true);
    assert.equal(rows[0].isMe ?? false, false);
  });

  it('appends the current user when outside the top N', () => {
    const hof = [{ rank: 1, name: 'A', score: 20, labs_completed: 2 }];
    const me = { rank: 17, score: 10, labs_completed: 1 };
    const rows = mergeMyRank(hof, me);
    assert.equal(rows.length, 2);
    assert.equal(rows[1].isMe, true);
    assert.equal(rows[1].rank, 17);
  });

  it('does not append when the user has no public rank', () => {
    const hof = [{ rank: 1, name: 'A', score: 20, labs_completed: 2 }];
    const me = { rank: 0, score: 10, labs_completed: 1 };
    const rows = mergeMyRank(hof, me);
    assert.equal(rows.length, 1);
  });
});
```

- [ ] **Step 2: 테스트 실패 확인**

Run: `cd apps/web && node --test lib/leaderboard.test.mjs`
Expected: FAIL — `Cannot find module './leaderboard.mjs'`.

- [ ] **Step 3: 헬퍼 구현**

Create `apps/web/lib/leaderboard.mjs`:

```javascript
// 명예의 전당 목록에 본인 행을 합친다.
// - me.rank 가 0 이면(미공개/완료 없음) 추가하지 않는다.
// - me 가 Top N 안에 있으면 해당 행에 isMe=true 표시.
// - 밖에 있으면 끝에 본인 행을 isMe=true 로 덧붙인다.
export function mergeMyRank(hallOfFame, me) {
  const rows = hallOfFame.map((r) => ({ ...r, isMe: me.rank !== 0 && r.rank === me.rank }));
  if (me.rank === 0) return rows;
  if (rows.some((r) => r.isMe)) return rows;
  return [...rows, { rank: me.rank, name: '나', score: me.score, labs_completed: me.labs_completed, isMe: true }];
}
```

- [ ] **Step 4: 테스트 통과 확인**

Run: `cd apps/web && node --test lib/leaderboard.test.mjs`
Expected: 3개 테스트 PASS.

- [ ] **Step 5: 타입 추가**

`apps/web/lib/types.ts` 끝에 추가:

```typescript
/** 리더보드 한 행(명예의 전당/급상승) — GET /api/v1/leaderboard */
export interface LeaderboardItem {
  rank: number;
  name: string;
  score: number;
  labs_completed: number;
}

/** 본인 순위 — rank=0 은 미공개(옵트아웃)/완료 없음 */
export interface MyRank {
  rank: number;
  score: number;
  labs_completed: number;
}

/** GET /api/v1/leaderboard 응답 */
export interface LeaderboardResponse {
  hall_of_fame: LeaderboardItem[];
  recent_risers: LeaderboardItem[];
  me: MyRank;
}

/** GET /api/v1/me/progress 응답 */
export interface MyProgress {
  score: number;
  rank: number;
  labs_completed: number;
  by_difficulty: Record<Difficulty, number>;
  recent_completions: { lab_id: string; session_id: string; completed_at: string }[];
}
```

- [ ] **Step 6: API 클라이언트 추가**

`apps/web/lib/api.ts` 상단 import에 신규 타입 추가:

```typescript
import type {
  HintResponse,
  Lab,
  LeaderboardResponse,
  MyProgress,
  Session,
  StepProgress,
  User,
} from './types';
```

`export const api = {` 객체 내부, `sessions: { ... }` 블록 다음에 두 네임스페이스 추가(기존 콤마 규칙 유지):

```typescript
  leaderboard: {
    get: () => request<LeaderboardResponse>('/api/v1/leaderboard'),
  },

  me: {
    progress: () => request<MyProgress>('/api/v1/me/progress'),
    setPreferences: (leaderboardHidden: boolean) =>
      request<{ leaderboard_hidden: boolean }>('/api/v1/me/preferences', {
        method: 'PATCH',
        body: JSON.stringify({ leaderboard_hidden: leaderboardHidden }),
      }),
  },
```

만약 `api` 객체에 이미 `me` 키가 있으면 새로 만들지 말고 그 안에 메서드만 추가한다(중복 키 금지).

- [ ] **Step 7: 타입체크 + 린트 + 헬퍼 테스트**

Run: `cd apps/web && pnpm typecheck && pnpm lint && node --test lib/leaderboard.test.mjs`
Expected: typecheck/lint 에러 없음, 테스트 PASS.

- [ ] **Step 8: 커밋**

```bash
cd /Users/kylekim1223/request700k/cledyu
git add apps/web/lib/types.ts apps/web/lib/api.ts apps/web/lib/leaderboard.mjs apps/web/lib/leaderboard.test.mjs
git commit -m "feat(web): add leaderboard types, api client, and rank-merge helper" \
  -m "Leaderboard and progress response types, api.leaderboard/api.me client methods,
and a tested mergeMyRank helper that keeps the current user visible."
```

---

### Task 5: 웹 `/leaderboard` 페이지

명예의 전당 + 최근 7일 급상승 + 내 현황 카드 + 옵트아웃 토글을 렌더하는 페이지를 만든다.

**Files:**
- Create: `apps/web/app/(platform)/leaderboard/page.tsx`

**Interfaces:**
- Consumes: `api.leaderboard.get()`, `api.me.progress()`, `api.me.setPreferences()` (Task 4); `mergeMyRank` (Task 4).

- [ ] **Step 1: 페이지 작성**

Create `apps/web/app/(platform)/leaderboard/page.tsx`:

```tsx
'use client';

import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { mergeMyRank } from '@/lib/leaderboard.mjs';
import type { LeaderboardItem } from '@/lib/types';

function RankTable({ rows }: { rows: (LeaderboardItem & { isMe?: boolean })[] }) {
  if (rows.length === 0) {
    return <p className="text-slate-500 text-sm">아직 기록이 없습니다.</p>;
  }
  return (
    <table className="w-full text-sm">
      <thead>
        <tr className="text-slate-400 text-left">
          <th className="py-2 w-12">#</th>
          <th className="py-2">이름</th>
          <th className="py-2 text-right">점수</th>
          <th className="py-2 text-right">완료</th>
        </tr>
      </thead>
      <tbody>
        {rows.map((r) => (
          <tr
            key={`${r.rank}-${r.name}`}
            className={r.isMe ? 'bg-brand-500/10 text-white' : 'text-slate-300'}
          >
            <td className="py-2">{r.rank}</td>
            <td className="py-2">{r.name}</td>
            <td className="py-2 text-right">{r.score}</td>
            <td className="py-2 text-right">{r.labs_completed}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

export default function LeaderboardPage() {
  const queryClient = useQueryClient();
  const { data, isLoading, isError } = useQuery({
    queryKey: ['leaderboard'],
    queryFn: () => api.leaderboard.get(),
  });
  const { data: progress } = useQuery({
    queryKey: ['my-progress'],
    queryFn: () => api.me.progress(),
  });

  const toggleHidden = useMutation({
    mutationFn: (hidden: boolean) => api.me.setPreferences(hidden),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['leaderboard'] });
      queryClient.invalidateQueries({ queryKey: ['my-progress'] });
    },
  });

  if (isLoading) return <p className="text-slate-400">불러오는 중...</p>;
  if (isError || !data) return <p className="text-red-400">리더보드를 불러오지 못했습니다.</p>;

  const isHidden = data.me.rank === 0;
  const hofRows = mergeMyRank(data.hall_of_fame, data.me);

  return (
    <div className="space-y-8">
      <div>
        <h1 className="text-2xl font-bold text-white">리더보드</h1>
        <p className="text-slate-400 mt-1 text-sm">랩을 완료하고 명예의 전당에 이름을 올리세요.</p>
      </div>

      <section className="rounded-lg border border-slate-800 bg-slate-900/50 p-5">
        <h2 className="text-white font-semibold mb-3">내 학습 현황</h2>
        <div className="flex gap-6 text-sm">
          <div>
            <div className="text-slate-400">점수</div>
            <div className="text-white text-xl font-bold">{progress?.score ?? data.me.score}</div>
          </div>
          <div>
            <div className="text-slate-400">순위</div>
            <div className="text-white text-xl font-bold">
              {data.me.rank === 0 ? '비공개' : `#${data.me.rank}`}
            </div>
          </div>
          <div>
            <div className="text-slate-400">완료한 랩</div>
            <div className="text-white text-xl font-bold">{data.me.labs_completed}</div>
          </div>
        </div>
        <label className="mt-4 flex items-center gap-2 text-sm text-slate-300">
          <input
            type="checkbox"
            checked={!isHidden}
            onChange={(e) => toggleHidden.mutate(!e.target.checked)}
            disabled={toggleHidden.isPending}
          />
          리더보드에 내 이름 표시
        </label>
      </section>

      <section className="rounded-lg border border-slate-800 bg-slate-900/50 p-5">
        <h2 className="text-white font-semibold mb-3">명예의 전당</h2>
        <RankTable rows={hofRows} />
      </section>

      <section className="rounded-lg border border-slate-800 bg-slate-900/50 p-5">
        <h2 className="text-white font-semibold mb-3">최근 7일 급상승</h2>
        <RankTable rows={data.recent_risers} />
      </section>
    </div>
  );
}
```

참고: `isHidden`은 `me.rank === 0`으로 근사한다(옵트아웃 또는 완료 없음). 완료가 있는데도 rank=0이면 옵트아웃 상태이므로 체크 해제로 표시된다 — v1 허용 범위.

- [ ] **Step 2: 타입체크 + 린트**

Run: `cd apps/web && pnpm typecheck && pnpm lint`
Expected: 에러 없음.

- [ ] **Step 3: 프로덕션 빌드 확인 (dev 서버 미실행 상태에서)**

주의: dev 서버가 같은 디렉터리에서 돌고 있으면 `.next` 충돌로 빌드가 깨진다. dev 서버를 끄고 실행할 것.

Run: `cd apps/web && pnpm build`
Expected: 빌드 성공, `/leaderboard` 라우트가 출력에 포함.

- [ ] **Step 4: 커밋**

```bash
cd /Users/kylekim1223/request700k/cledyu
git add "apps/web/app/(platform)/leaderboard/page.tsx"
git commit -m "feat(web): add leaderboard page" \
  -m "Hall of fame, recent-7-day risers, personal status card, and a leaderboard
visibility opt-out toggle on the existing /leaderboard route."
```

---

### Task 6: 전체 게이트 검증 + PR

머지 후 CI가 깨지지 않도록 전 게이트를 일괄 실행하고 PR을 연다.

**Files:** 없음(검증/PR 전용).

- [ ] **Step 1: Go 전체 게이트**

Run: `cd apps/api && go test ./... -race && go vet ./... && gofmt -l internal && golangci-lint run --timeout=5m`
Expected: 모든 테스트 PASS, vet/gofmt 무출력, golangci-lint 무에러. (revive var-naming 등 지적 시 즉시 수정 — 예: 약어 대문자 IDP.)

- [ ] **Step 2: 웹 전체 게이트**

Run: `cd apps/web && pnpm typecheck && pnpm lint && node --test lib/leaderboard.test.mjs lib/terminal-tail.test.mjs`
Expected: 에러 없음, 테스트 PASS.

- [ ] **Step 3: pre-commit (변경 파일)**

Run: `cd /Users/kylekim1223/request700k/cledyu && pre-commit run --files apps/api/internal/store/migrations/0002_leaderboard.sql apps/api/internal/store/store.go apps/api/internal/api/handlers/leaderboard.go apps/api/internal/api/handlers/leaderboard_test.go apps/api/internal/api/handlers/user.go apps/api/internal/api/handlers/progress.go apps/api/internal/api/handlers/progress_test.go apps/api/internal/api/router.go apps/web/lib/types.ts apps/web/lib/api.ts apps/web/lib/leaderboard.mjs apps/web/lib/leaderboard.test.mjs "apps/web/app/(platform)/leaderboard/page.tsx"`
Expected: 모든 훅 통과(gitleaks 포함).

- [ ] **Step 4: 푸시 + PR 생성**

```bash
cd /Users/kylekim1223/request700k/cledyu
git push -u origin feat/user-leaderboard
gh pr create --base main --head feat/user-leaderboard \
  --title "feat(api): add user leaderboard with hall of fame and personal status" \
  --body "설계: docs/superpowers/specs/2026-06-26-user-leaderboard-design.md

난이도 가중 완료 점수 기반 명예의 전당(Top 10) + 최근 7일 급상승 + 본인 순위(항상 포함)
+ 개인 학습 현황 + 리더보드 노출 옵트아웃. 온프렘 Postgres 컬럼 1개 + 읽기 쿼리만 추가,
신규 클라우드 리소스 없음(+\$0). 배지는 v2.

테스트: go test ./... -race, golangci-lint, pnpm typecheck/lint, node --test 통과."
```

Expected: PR 생성, CI 시작. (리뷰어 지정은 Lab+서비스 레이어 담당에 맞춰 수동 지정.)

---

## Self-Review (작성자 점검 결과)

- **Spec coverage:** 점수 모델(Task 2), 스키마/옵트아웃(Task 1), API 3종+/me(Task 3), 프론트 페이지/현황/토글(Task 4-5), 테스트/게이트(전 태스크+Task 6), SLO/비용(코드 변경 없음 — 검증으로 충족). 누락 없음.
- **Placeholder scan:** 모든 코드 단계에 실제 코드 포함. "적절히 처리" 류 없음.
- **Type consistency:** `LeaderboardRow`(store) → `leaderboardEntry`/`rankEntries`(handlers) → `leaderboardItem`/`myRank`(JSON) → `LeaderboardItem`/`MyRank`/`LeaderboardResponse`(web) → `mergeMyRank` 전 구간 필드명(`score`, `labs_completed`, `rank`) 일치.
- **알려진 근사:** 웹 `isHidden`을 `me.rank===0`으로 근사(완료 없음과 옵트아웃을 구분하지 않음). v1 허용. 필요 시 v2에서 `/me/progress`에 명시적 `leaderboard_hidden` 추가.
