# D1 학습자 대시보드 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 학습자가 자신의 풍부한 학습 현황과 랩별 수료 상태를 보는 per-user 대시보드를 온프렘 Postgres 데이터로 구현한다(파이프라인 무의존).

**Architecture:** 점수·순위·완료는 기존 `lab_completions`/`session_steps`와 PR #188의 `userScore`/`rankEntries`를 재사용하고, 랩별 상태(completed/in_progress/not_started)는 랩 카탈로그(`h.labs`)를 완료 집합·진행중 집합과 대조해 계산한다. 신규 `GET /api/v1/me/dashboard` 엔드포인트(Postgres only)와 `/dashboard` Next.js 페이지를 추가한다.

**Tech Stack:** Go 1.x + Gin + pgx/v5(Postgres), Next.js App Router + TanStack Query, node:test(웹 lib), Go testing.

## Global Constraints

- 문서/주석 한국어, 코드 식별자·CLI·키 영어.
- 커밋 subject 소문자 시작, Conventional Commits. scope: `api`(Go) / `web`(프론트). body 줄당 ≤100자. 이모지 금지.
- PR 제목: `<type>(<scope>): <소문자 subject>`.
- 대시보드 데이터는 전부 Postgres. `h.db == nil` → 503. 타인 데이터 노출 금지(본인 user_id만).
- BigQuery/파이프라인 의존 없음(D1은 독립).
- 랩 카탈로그 소스는 `h.labs`(content.LabContent, 스코어링과 동일). 맵 순회는 lab_id 정렬로 결정적 출력.
- 난이도 가중/랭킹은 기존 로직 재사용(중복 구현 금지): `h.weightForLab`, `rankEntries`, `store.Completion`.
- 저장소에 live-DB store 단위테스트 없음(`go test -race`는 DB 없이 실행). 영속 로직은 `fakePersistence` 핸들러 테스트로 검증. 새 live-DB 하니스 만들지 않음.
- 검증 게이트: `go test ./... -race`, `golangci-lint run`, `gofmt -l`, `pnpm typecheck`, `pnpm lint`, **`pre-commit run prettier --files <web파일>`**(prettier 함정 — 로컬 eslint는 prettier 미검사), `node --test`.

**작업 디렉터리:** repo 루트 `/Users/kylekim1223/request700k/cledyu`. Go는 `apps/api`, 웹은 `apps/web`. 브랜치: `feat/learning-analytics`(생성됨).

---

### Task 1: in_progress 판정용 store 메서드 + 인터페이스/테스트더블

랩별 "진행중" 상태 판정에 필요한, 유저의 진행기록이 있는 lab_id 목록을 반환하는 store 메서드를 추가한다. 산출물은 `go build`/`go vet`/`gofmt` 통과(live-DB 테스트 없음).

**Files:**
- Modify: `apps/api/internal/store/store.go` (lab completions 섹션 근처에 추가)
- Modify: `apps/api/internal/api/handlers/progress.go` (persistence 인터페이스)
- Modify: `apps/api/internal/api/handlers/progress_test.go` (fakePersistence)

**Interfaces:**
- Produces: `(*store.Store).ListInProgressLabIDsByUser(ctx context.Context, userID string) ([]string, error)` — `session_progress`에 진행기록이 있는 lab_id 목록(완료 포함 가능, 호출부가 완료를 우선 판정). persistence 인터페이스에도 추가.

- [ ] **Step 1: store.go에 메서드 추가**

`apps/api/internal/store/store.go`의 `ListCompletionsByUser` 함수 닫는 `}` 다음에 추가:

```go
// ListInProgressLabIDsByUser는 유저가 진행기록(session_progress)을 가진 lab_id 목록을 반환한다.
// 완료된 랩도 진행기록이 남아 있을 수 있으므로, 호출부는 완료 여부를 먼저 판정한 뒤
// 이 목록을 'in_progress' 후보로 사용한다.
func (s *Store) ListInProgressLabIDsByUser(ctx context.Context, userID string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT DISTINCT lab_id FROM session_progress WHERE user_id = $1`, userID)
	if err != nil {
		return nil, fmt.Errorf("list in-progress labs: %w", err)
	}
	defer rows.Close()

	out := make([]string, 0)
	for rows.Next() {
		var labID string
		if err := rows.Scan(&labID); err != nil {
			return nil, fmt.Errorf("scan in-progress lab: %w", err)
		}
		out = append(out, labID)
	}
	return out, rows.Err()
}
```

- [ ] **Step 2: persistence 인터페이스에 추가**

`apps/api/internal/api/handlers/progress.go`의 `persistence` 인터페이스, `SetLeaderboardHidden` 줄 다음에 추가:

```go
	ListInProgressLabIDsByUser(ctx context.Context, userID string) ([]string, error)
```

- [ ] **Step 3: fakePersistence에 필드+메서드 추가**

`apps/api/internal/api/handlers/progress_test.go`의 `fakePersistence` struct에 필드 추가(`hidden map[string]bool` 근처):

```go
	inProgress map[string][]string // user_id → lab_ids (진행기록 있는 랩)
```

`newFakePersistence` 반환 리터럴에 `inProgress: map[string][]string{},` 추가. 파일 끝에 메서드 추가:

```go
func (f *fakePersistence) ListInProgressLabIDsByUser(_ context.Context, userID string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inProgress[userID], nil
}
```

- [ ] **Step 4: 컴파일·vet·fmt 확인**

Run: `cd apps/api && go build ./... && go vet ./... && gofmt -l internal/store/store.go internal/api/handlers/progress.go internal/api/handlers/progress_test.go`
Expected: 에러 없음, gofmt 무출력.

- [ ] **Step 5: 커밋**

```bash
cd /Users/kylekim1223/request700k/cledyu
git add apps/api/internal/store/store.go apps/api/internal/api/handlers/progress.go apps/api/internal/api/handlers/progress_test.go
git commit -m "feat(api): add store query for a user's in-progress lab ids" \
  -m "Lists lab_ids with a session_progress row, used to derive in_progress status on
the learner dashboard."
```

---

### Task 2: 대시보드 페이로드 빌더 + 핸들러 + 라우터 — TDD

랩 카탈로그·완료·진행중을 받아 요약 + 랩별 상태를 만드는 순수 함수를 TDD로 만들고, 이를 쓰는 `GET /api/v1/me/dashboard` 핸들러와 라우트를 추가한다.

**Files:**
- Create: `apps/api/internal/api/handlers/dashboard.go`
- Create: `apps/api/internal/api/handlers/dashboard_test.go`
- Modify: `apps/api/internal/api/router.go` (라우트 등록)

**Interfaces:**
- Consumes: `store.Completion`(LabID, CompletedAt), `content.LabContent`(ID, Title, Difficulty), `h.weightForLab`, `rankEntries`, `h.db.LeaderboardRows`/`ListCompletionsByUser`/`ListInProgressLabIDsByUser`.
- Produces: `(*Handler).GetMyDashboard(c *gin.Context)`; pure `buildDashboard(labs map[string]content.LabContent, completions []store.Completion, inProgress []string) (dashboardSummary, []dashboardLab)`.

- [ ] **Step 1: 실패하는 빌더 테스트 작성**

Create `apps/api/internal/api/handlers/dashboard_test.go`:

```go
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
```

- [ ] **Step 2: 테스트 실패 확인**

Run: `cd apps/api && go test ./internal/api/handlers/ -run TestBuildDashboard -v`
Expected: 컴파일 실패 — `undefined: buildDashboard`.

- [ ] **Step 3: 빌더 구현**

Create `apps/api/internal/api/handlers/dashboard.go`:

```go
package handlers

import (
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
	CompletedAt *time.Time `json:"completed_at"`
}

// buildDashboard는 카탈로그·완료·진행중을 대조해 요약과 랩별 상태를 만든다(순수 함수).
// Score/Rank 는 채우지 않는다(DB 랭킹 필요 — 핸들러가 설정).
func buildDashboard(labs map[string]content.LabContent, completions []store.Completion, inProgress []string) (dashboardSummary, []dashboardLab) {
	completedAt := make(map[string]time.Time, len(completions))
	for _, c := range completions {
		completedAt[c.LabID] = c.CompletedAt
	}
	started := make(map[string]bool, len(inProgress))
	for _, id := range inProgress {
		started[id] = true
	}

	ids := make([]string, 0, len(labs))
	for id := range labs {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	byDiff := map[string]difficultyProgress{}
	rows := make([]dashboardLab, 0, len(ids))
	for _, id := range ids {
		lc := labs[id]
		row := dashboardLab{LabID: id, Title: lc.Title, Difficulty: lc.Difficulty, Status: "not_started"}
		if ts, ok := completedAt[id]; ok {
			row.Status = "completed"
			t := ts
			row.CompletedAt = &t
		} else if started[id] {
			row.Status = "in_progress"
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
```

- [ ] **Step 4: 빌더 테스트 통과 확인**

Run: `cd apps/api && go test ./internal/api/handlers/ -run TestBuildDashboard -v`
Expected: PASS.

- [ ] **Step 5: 실패하는 핸들러 테스트 작성**

`dashboard_test.go`에 추가:

```go
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
	fake.completionAt = map[string]time.Time{"u1|lab-docker-basics": at}
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
	// 실제 임베드 카탈로그는 5개 랩.
	if body.Summary.TotalLabs != 5 {
		t.Fatalf("total_labs want 5, got %d", body.Summary.TotalLabs)
	}
	statusByLab := map[string]string{}
	for _, l := range body.Labs {
		statusByLab[l.LabID] = l.Status
	}
	if statusByLab["lab-docker-basics"] != "completed" || statusByLab["lab-k8s-basics"] != "in_progress" {
		t.Fatalf("status mismatch: %+v", statusByLab)
	}
}
```

참고: 이 테스트는 `fakePersistence`에 `completionAt map[string]time.Time` 필드와, `ListCompletionsByUser`가 그 시각을 반환하도록 하는 보강이 필요하다(현재 fake 는 CompletedAt 을 안 채움). Step 6에서 fake 를 보강한다.

- [ ] **Step 6: fakePersistence의 완료시각 보강**

`apps/api/internal/api/handlers/progress_test.go`의 `fakePersistence` struct에 필드 추가:

```go
	completionAt map[string]string // "user|lab" → RFC3339, 비면 zero time
```

`ListCompletionsByUser` 메서드에서 `store.Completion` 생성 시 `CompletedAt`을 채우도록 수정. 기존:

```go
out = append(out, store.Completion{LabID: strings.TrimPrefix(key, userID+"|"), SessionID: sess})
```

를 다음으로 교체(상단 import에 `time` 이 이미 Task 1에서 추가됨):

```go
comp := store.Completion{LabID: strings.TrimPrefix(key, userID+"|"), SessionID: sess}
if f.completionAt != nil {
	if ts, ok := f.completionAt[key]; ok {
		if parsed, perr := time.Parse(time.RFC3339, ts); perr == nil {
			comp.CompletedAt = parsed
		}
	}
}
out = append(out, comp)
```

그리고 테스트(Step 5)의 `fake.completionAt = map[string]time.Time{...}` 를 RFC3339 문자열 맵으로 맞춘다:

```go
	fake.completionAt = map[string]string{"u1|lab-docker-basics": at.Format(time.RFC3339)}
```

(Step 5 테스트 코드의 해당 줄을 이 형태로 작성할 것.)

- [ ] **Step 7: 핸들러 구현**

`dashboard.go` 파일 끝에 추가:

```go
// userRank는 공개 랭킹에서 유저의 순위를 반환한다(없으면 0). GetMyProgress 와 공유.
func (h *Handler) userRank(ctx interface {
	Deadline() (time.Time, bool)
	Done() <-chan struct{}
	Err() error
	Value(any) any
}, userID string) int {
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
	inProgress, err := h.db.ListInProgressLabIDsByUser(ctx, uid)
	if err != nil {
		h.log.Warn("list in-progress labs", zap.Error(err), zap.String("user_id", uid))
		inProgress = nil
	}

	summary, rows := buildDashboard(h.labs, completions, inProgress)
	for _, comp := range completions {
		summary.Score += h.weightForLab(comp.LabID)
	}
	summary.Rank = h.userRank(ctx, uid)

	recent := completions
	if len(recent) > leaderboardTopN {
		recent = recent[:leaderboardTopN]
	}

	c.JSON(http.StatusOK, gin.H{
		"summary":            summary,
		"labs":               rows,
		"recent_completions": recent,
	})
}
```

참고: `userRank`의 ctx 타입이 장황하면 `context.Context`로 단순화하고 `"context"` import 를 추가할 것(권장). 즉:

```go
func (h *Handler) userRank(ctx context.Context, userID string) int {
```
이 경우 `dashboard.go` import 블록에 `"context"` 추가.

- [ ] **Step 8: 라우터 등록**

`apps/api/internal/api/router.go`의 `v1.GET("/me/progress", h.GetMyProgress)` 다음 줄에 추가:

```go
		v1.GET("/me/dashboard", h.GetMyDashboard)
```

- [ ] **Step 9: 전체 핸들러 테스트 + vet + fmt**

Run: `cd apps/api && go test ./internal/api/handlers/ -v && go vet ./... && gofmt -l internal/api`
Expected: 모든 테스트 PASS, vet/gofmt 무출력.

- [ ] **Step 10: 커밋**

```bash
cd /Users/kylekim1223/request700k/cledyu
git add apps/api/internal/api/handlers/dashboard.go apps/api/internal/api/handlers/dashboard_test.go apps/api/internal/api/handlers/progress_test.go apps/api/internal/api/router.go
git commit -m "feat(api): add learner dashboard endpoint" \
  -m "GET /me/dashboard returns per-user summary (score, rank, completion%, by-difficulty)
and per-lab status (completed/in_progress/not_started) from Postgres only."
```

---

### Task 3: 웹 타입 + API 클라이언트 + 상태 헬퍼 — TDD

대시보드 응답 타입과 API 호출을 추가하고, 랩 상태→표시 라벨 매핑 순수 헬퍼를 node:test로 검증한다.

**Files:**
- Modify: `apps/web/lib/types.ts`
- Modify: `apps/web/lib/api.ts`
- Create: `apps/web/lib/dashboard.mjs`
- Create: `apps/web/lib/dashboard.test.mjs`

**Interfaces:**
- Produces: 타입 `LabStatus`, `DashboardLab`, `DashboardSummary`, `DashboardResponse`; `api.me.dashboard()`; `labStatusLabel(status)` → 한국어 라벨.

- [ ] **Step 1: 실패하는 헬퍼 테스트 작성**

Create `apps/web/lib/dashboard.test.mjs`:

```javascript
import assert from 'node:assert/strict';
import { describe, it } from 'node:test';
import { labStatusLabel } from './dashboard.mjs';

describe('labStatusLabel', () => {
  it('maps known statuses to Korean labels', () => {
    assert.equal(labStatusLabel('completed'), '수료');
    assert.equal(labStatusLabel('in_progress'), '진행중');
    assert.equal(labStatusLabel('not_started'), '미시작');
  });

  it('falls back to the raw status for unknown values', () => {
    assert.equal(labStatusLabel('weird'), 'weird');
  });
});
```

- [ ] **Step 2: 테스트 실패 확인**

Run: `cd apps/web && node --test lib/dashboard.test.mjs`
Expected: FAIL — `Cannot find module './dashboard.mjs'`.

- [ ] **Step 3: 헬퍼 구현**

Create `apps/web/lib/dashboard.mjs`:

```javascript
// 랩 상태 코드를 학습자 화면용 한국어 라벨로 매핑한다. 알 수 없는 값은 원문 그대로.
const LABELS = { completed: '수료', in_progress: '진행중', not_started: '미시작' };

export function labStatusLabel(status) {
  return LABELS[status] ?? status;
}
```

- [ ] **Step 4: 헬퍼 테스트 통과 확인**

Run: `cd apps/web && node --test lib/dashboard.test.mjs`
Expected: 2개 테스트 PASS.

- [ ] **Step 5: 타입 추가**

`apps/web/lib/types.ts` 끝에 추가:

```typescript
/** 랩별 수료 상태 — GET /api/v1/me/dashboard */
export type LabStatus = 'completed' | 'in_progress' | 'not_started';

/** 난이도별 완료/전체 */
export interface DifficultyProgress {
  done: number;
  total: number;
}

/** 대시보드 상단 요약 */
export interface DashboardSummary {
  score: number;
  rank: number;
  labs_completed: number;
  total_labs: number;
  completion_pct: number;
  by_difficulty: Record<Difficulty, DifficultyProgress>;
}

/** 랩별 상태 1건 */
export interface DashboardLab {
  lab_id: string;
  title: string;
  difficulty: Difficulty;
  status: LabStatus;
  completed_at: string | null;
}

/** GET /api/v1/me/dashboard 응답 */
export interface DashboardResponse {
  summary: DashboardSummary;
  labs: DashboardLab[];
  recent_completions: { lab_id: string; session_id: string; completed_at: string }[];
}
```

- [ ] **Step 6: API 클라이언트 추가**

`apps/web/lib/api.ts` 상단 import 의 타입 목록에 `DashboardResponse` 추가(알파벳/기존 순서 유지). 그리고 `me:` 네임스페이스의 `progress:` 항목 다음에 추가:

```typescript
    dashboard: () => request<DashboardResponse>('/api/v1/me/dashboard'),
```

- [ ] **Step 7: 타입체크 + 린트 + 헬퍼 테스트**

Run: `cd apps/web && pnpm typecheck && pnpm lint && node --test lib/dashboard.test.mjs`
Expected: 에러 없음, 테스트 PASS.

- [ ] **Step 8: 커밋**

```bash
cd /Users/kylekim1223/request700k/cledyu
git add apps/web/lib/types.ts apps/web/lib/api.ts apps/web/lib/dashboard.mjs apps/web/lib/dashboard.test.mjs
git commit -m "feat(web): add dashboard types, api client, and status label helper" \
  -m "DashboardResponse types, api.me.dashboard client method, and a tested
labStatusLabel helper mapping lab status codes to Korean labels."
```

---

### Task 4: 웹 `/dashboard` 페이지 + 네비 링크

요약 카드 + 랩별 상태 그리드 + 최근 활동을 렌더하는 페이지를 만들고 네비에 링크를 추가한다.

**Files:**
- Create: `apps/web/app/(platform)/dashboard/page.tsx`
- Modify: `apps/web/components/ui/Navbar.tsx`

**Interfaces:**
- Consumes: `api.me.dashboard()` (Task 3), `labStatusLabel` (Task 3), 타입(Task 3).

- [ ] **Step 1: 네비 링크 추가**

`apps/web/components/ui/Navbar.tsx`의 `NAV_LINKS` 배열에서 `/leaderboard` 항목 앞에 추가:

```tsx
  { href: '/dashboard', label: '내 학습' },
```

- [ ] **Step 2: 페이지 작성**

Create `apps/web/app/(platform)/dashboard/page.tsx`:

```tsx
'use client';

import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { labStatusLabel } from '@/lib/dashboard.mjs';
import type { DashboardLab, Difficulty } from '@/lib/types';

const STATUS_CLASS: Record<string, string> = {
  completed: 'bg-emerald-500/15 text-emerald-300',
  in_progress: 'bg-amber-500/15 text-amber-300',
  not_started: 'bg-slate-700/40 text-slate-400',
};

const DIFFICULTY_LABEL: Record<Difficulty, string> = {
  beginner: '입문',
  intermediate: '중급',
  advanced: '고급',
};

function LabGrid({ labs }: { labs: DashboardLab[] }) {
  if (labs.length === 0) {
    return <p className="text-slate-500 text-sm">표시할 랩이 없습니다.</p>;
  }
  return (
    <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
      {labs.map((l) => (
        <div
          key={l.lab_id}
          className="rounded-lg border border-slate-800 bg-slate-900/50 p-4 flex items-center justify-between"
        >
          <div>
            <div className="text-white text-sm font-medium">{l.title}</div>
            <div className="text-slate-500 text-xs mt-0.5">{DIFFICULTY_LABEL[l.difficulty]}</div>
          </div>
          <span className={`px-2 py-1 rounded-md text-xs ${STATUS_CLASS[l.status] ?? ''}`}>
            {labStatusLabel(l.status)}
          </span>
        </div>
      ))}
    </div>
  );
}

export default function DashboardPage() {
  const { data, isLoading, isError } = useQuery({
    queryKey: ['my-dashboard'],
    queryFn: () => api.me.dashboard(),
  });

  if (isLoading) return <p className="text-slate-400">불러오는 중...</p>;
  if (isError || !data) return <p className="text-red-400">대시보드를 불러오지 못했습니다.</p>;

  const s = data.summary;

  return (
    <div className="space-y-8">
      <div>
        <h1 className="text-2xl font-bold text-white">내 학습</h1>
        <p className="text-slate-400 mt-1 text-sm">나의 학습 현황과 랩별 수료 상태를 확인하세요.</p>
      </div>

      <section className="rounded-lg border border-slate-800 bg-slate-900/50 p-5">
        <div className="flex flex-wrap gap-6 text-sm">
          <div>
            <div className="text-slate-400">점수</div>
            <div className="text-white text-xl font-bold">{s.score}</div>
          </div>
          <div>
            <div className="text-slate-400">순위</div>
            <div className="text-white text-xl font-bold">{s.rank === 0 ? '비공개' : `#${s.rank}`}</div>
          </div>
          <div>
            <div className="text-slate-400">완료율</div>
            <div className="text-white text-xl font-bold">
              {s.completion_pct}% ({s.labs_completed}/{s.total_labs})
            </div>
          </div>
        </div>
        <div className="mt-4 space-y-2">
          {(Object.keys(s.by_difficulty) as Difficulty[]).map((d) => {
            const dp = s.by_difficulty[d];
            if (dp.total === 0) return null;
            const pct = Math.round((dp.done / dp.total) * 100);
            return (
              <div key={d}>
                <div className="flex justify-between text-xs text-slate-400">
                  <span>{DIFFICULTY_LABEL[d]}</span>
                  <span>
                    {dp.done}/{dp.total}
                  </span>
                </div>
                <div className="h-2 rounded bg-slate-800 overflow-hidden">
                  <div className="h-full bg-brand-500" style={{ width: `${pct}%` }} />
                </div>
              </div>
            );
          })}
        </div>
      </section>

      <section>
        <h2 className="text-white font-semibold mb-3">랩별 현황</h2>
        <LabGrid labs={data.labs} />
      </section>
    </div>
  );
}
```

- [ ] **Step 3: 타입체크 + 린트**

Run: `cd apps/web && pnpm typecheck && pnpm lint`
Expected: 에러 없음.

- [ ] **Step 4: prettier 정렬(pre-commit 함정 방지)**

Run: `cd /Users/kylekim1223/request700k/cledyu && pre-commit run prettier --files "apps/web/app/(platform)/dashboard/page.tsx" apps/web/components/ui/Navbar.tsx`
Expected: 처음엔 파일 수정 후 Failed → 한 번 더 실행 시 Passed. (수정분을 그대로 커밋에 포함.)

- [ ] **Step 5: 프로덕션 빌드 확인 (dev 서버 미실행 상태)**

주의: dev 서버 실행 중이면 `.next` 충돌. 끄고 실행.

Run: `cd apps/web && pnpm build`
Expected: 빌드 성공, 출력에 `/dashboard` 라우트 포함.

- [ ] **Step 6: 커밋**

```bash
cd /Users/kylekim1223/request700k/cledyu
git add "apps/web/app/(platform)/dashboard/page.tsx" apps/web/components/ui/Navbar.tsx
git commit -m "feat(web): add learner dashboard page" \
  -m "Summary card (score, rank, completion% with per-difficulty bars) and a per-lab
status grid on a new /dashboard route, plus a nav link."
```

---

### Task 5: 전체 게이트 검증 + PR

머지 후 CI가 깨지지 않도록 전 게이트를 일괄 실행하고 PR을 연다.

**Files:** 없음(검증/PR 전용).

- [ ] **Step 1: Go 전체 게이트**

Run: `cd apps/api && go test ./... -race && go vet ./... && gofmt -l internal && golangci-lint run --timeout=5m`
Expected: 모든 테스트 PASS, vet/gofmt 무출력, golangci-lint 무에러.

- [ ] **Step 2: 웹 전체 게이트**

Run: `cd apps/web && pnpm typecheck && pnpm lint && node --test lib/dashboard.test.mjs lib/leaderboard.test.mjs lib/terminal-tail.test.mjs`
Expected: 에러 없음, 테스트 PASS.

- [ ] **Step 3: pre-commit (변경 파일)**

Run: `cd /Users/kylekim1223/request700k/cledyu && pre-commit run --files apps/api/internal/store/store.go apps/api/internal/api/handlers/progress.go apps/api/internal/api/handlers/progress_test.go apps/api/internal/api/handlers/dashboard.go apps/api/internal/api/handlers/dashboard_test.go apps/api/internal/api/router.go apps/web/lib/types.ts apps/web/lib/api.ts apps/web/lib/dashboard.mjs apps/web/lib/dashboard.test.mjs apps/web/components/ui/Navbar.tsx "apps/web/app/(platform)/dashboard/page.tsx"`
Expected: 모든 훅 통과(prettier·gitleaks 포함). prettier 가 수정하면 그 결과를 커밋하고 재실행.

- [ ] **Step 4: 푸시 + PR 생성**

```bash
cd /Users/kylekim1223/request700k/cledyu
git push -u origin feat/learning-analytics
gh pr create --base main --head feat/learning-analytics \
  --title "feat(api): add learner dashboard (per-user progress and lab status)" \
  --body "설계: docs/superpowers/specs/2026-06-26-learning-analytics-design.md (D1)

학습자 대면 대시보드 — 점수·순위·완료율·난이도별 진행 + 랩별 수료 상태
(completed/in_progress/not_started). 전부 온프렘 Postgres, 파이프라인 무의존, +\$0.
D2(Kafka-Airflow-BigQuery 파이프라인)/D3(강사 분석)는 후속 PR.

테스트: go test ./... -race, golangci-lint, pnpm typecheck/lint, node --test 통과."
```

Expected: PR 생성, CI 시작.

---

## Self-Review (작성자 점검 결과)

- **Spec coverage:** D1 spec의 보여줄 내용(풍부한 개인 현황=요약 카드, 랩별 수료=상태 그리드), 데이터 원천(Postgres: completions/in-progress/catalog), API(`/me/dashboard`), Web(`/dashboard` 페이지 + 네비), 테스트/게이트 모두 태스크로 커버. 시계열 추이는 spec에서 v1 비포함(D2 의존) — 미구현 정상.
- **Placeholder scan:** 모든 코드 단계에 실제 코드 포함. fakePersistence 보강(완료시각)도 구체 코드로 명시.
- **Type consistency:** `dashboardSummary`/`dashboardLab`/`difficultyProgress`(Go) ↔ `DashboardSummary`/`DashboardLab`/`DifficultyProgress`(TS) 필드명(score/rank/labs_completed/total_labs/completion_pct/by_difficulty/status/completed_at) 일치. 상태 코드 completed/in_progress/not_started 가 Go·TS·헬퍼 전 구간 동일.
- **알려진 결정:** 카탈로그 소스는 `h.labs`(스코어링과 동일). `mockLabs`(lab.go)와 동일 랩셋 전제 — 분기 시 reconcile 필요(현재 5개 동일). `userRank`는 GetMyProgress 의 랭킹 조회와 동일 로직 — 신규 핸들러에서 헬퍼로 추출(중복 최소화).
