# D3 강사 분석 대시보드 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** D2 BigQuery 뷰를 강사 전용 `/instructor` 페이지 + `GET /api/v1/instructor/analytics` 로 노출하는 분석 대시보드를 구현한다.

**Architecture:** apps/api 에 `internal/bq` 패키지(읽기전용 BigQuery 클라이언트, NULL은 클라이언트에서 흡수해 클린 DTO만 노출)를 추가하고, handlers 의 `bqAnalytics` 인터페이스로 핸들러가 의존한다(store/persistence 패턴). `GET /api/v1/instructor/analytics` 는 `RequireMinRole("instructor")` 게이트. apps/web 의 `/instructor` 페이지가 결과를 렌더한다. 핸들러는 fake 로 TDD, 실제 api↔BQ 쿼리는 수동 스모크.

**Tech Stack:** Go + Gin + cloud.google.com/go/bigquery, Next.js App Router + TanStack Query, Terraform(google), External Secrets, Go testing / node:test.

## Global Constraints

- 문서/주석 한국어, 코드 식별자·CLI·키 영어. 이모지 금지.
- 커밋 subject 소문자 시작, Conventional Commits. scope: `api`(Go) / `web` / `infra`. body 줄당 ≤100자.
- BQ 데이터셋 `cledyu_analytics`, 뷰 `v_lab_completion`(lab_id, started, completed, completion_rate), `v_step_funnel`(lab_id, step_id, validation_failures), `v_hint_usage`(lab_id, step_id, hint_source, hint_count). 컬럼명 그대로 사용.
- 엔드포인트 `GET /api/v1/instructor/analytics`, `middleware.RequireMinRole("instructor")`(admin 도 통과).
- BQ 미설정(로컬/CI) → 핸들러 503 (기존 `h.db == nil` 패턴). 쿼리 실패 → 500 로깅.
- 자격증명: 읽기전용 SA 키 → Vault `cledyu/gcp/api-analytics-reader` → ESO → api ns Secret → `GOOGLE_APPLICATION_CREDENTIALS`. 하드코딩 금지.
- 읽기전용 SA IAM: `roles/bigquery.dataViewer`(데이터셋 스코프) + `roles/bigquery.jobUser`(프로젝트). airflow SA(dataEditor)와 분리.
- 차트는 경량(테이블/진행바). 무거운 차트 라이브러리 미도입(YAGNI).
- 종료프로젝트([[project_deadline_terminating]]) — 자동화는 작성+정적검증(go test/vet/gofmt/golangci-lint, terraform fmt/validate, ruff/yamllint, typecheck/lint, node:test). 라이브 api↔BQ·terraform apply·SA키·vault put 은 김용균님 수동 단계(런북).
- 검증 게이트: `go test ./... -race`, `golangci-lint run`, `gofmt -l`, `terraform fmt -check`+`validate`, `pnpm typecheck`+`lint`, `pre-commit`(yamllint).

**작업 디렉터리:** `/Users/kylekim1223/request700k/cledyu-d3` (worktree, 브랜치 feat/instructor-analytics-d3). 모든 명령·커밋은 이 경로에서.

---

### Task 1: internal/bq 패키지 — 읽기전용 BigQuery 클라이언트 + DTO

D2 뷰를 조회해 클린 DTO 로 반환하는 BigQuery 클라이언트. NULL(예: completion_rate)은 클라이언트에서 zero-value 로 흡수한다. 산출물: `go build`/`go vet` 통과(라이브 쿼리 테스트 없음 — store.go 와 동일 정책).

**Files:**
- Create: `apps/api/internal/bq/bq.go`
- Modify: `apps/api/go.mod`, `apps/api/go.sum` (cloud.google.com/go/bigquery 추가)

**Interfaces:**
- Produces:
  - `bq.LabCompletionRow{ LabID string; Started int64; Completed int64; CompletionRate float64 }` (json: lab_id/started/completed/completion_rate)
  - `bq.StepFunnelRow{ LabID string; StepID int64; ValidationFailures int64 }`
  - `bq.HintUsageRow{ LabID string; StepID int64; HintSource string; HintCount int64 }`
  - `(*bq.Client).LabCompletion(ctx) ([]LabCompletionRow, error)`, `.StepFunnel(ctx) ([]StepFunnelRow, error)`, `.HintUsage(ctx) ([]HintUsageRow, error)`, `.Close()`
  - `bq.NewClient(ctx context.Context, projectID, dataset string) (*Client, error)`

- [ ] **Step 1: BQ 의존성 추가**

Run: `cd apps/api && go get cloud.google.com/go/bigquery@latest && go get google.golang.org/api/iterator@latest`
Expected: go.mod/go.sum 갱신, 에러 없음.

- [ ] **Step 2: bq.go 작성**

Create `apps/api/internal/bq/bq.go`:

```go
// Package bq는 D3 강사 분석용 읽기전용 BigQuery 클라이언트다.
// D2 가 만든 cledyu_analytics 뷰를 조회해 클린 DTO 로 반환한다.
// NULL(예: SAFE_DIVIDE 의 completion_rate)은 여기서 zero-value 로 흡수해
// 핸들러/JSON 은 nullable 타입을 다루지 않는다.
package bq

import (
	"context"
	"fmt"

	"cloud.google.com/go/bigquery"
	"google.golang.org/api/iterator"
)

// LabCompletionRow는 v_lab_completion 한 행이다.
type LabCompletionRow struct {
	LabID          string  `json:"lab_id"`
	Started        int64   `json:"started"`
	Completed      int64   `json:"completed"`
	CompletionRate float64 `json:"completion_rate"`
}

// StepFunnelRow는 v_step_funnel 한 행이다(스텝별 검증 실패).
type StepFunnelRow struct {
	LabID              string `json:"lab_id"`
	StepID             int64  `json:"step_id"`
	ValidationFailures int64  `json:"validation_failures"`
}

// HintUsageRow는 v_hint_usage 한 행이다.
type HintUsageRow struct {
	LabID      string `json:"lab_id"`
	StepID     int64  `json:"step_id"`
	HintSource string `json:"hint_source"`
	HintCount  int64  `json:"hint_count"`
}

// Client는 cledyu_analytics 데이터셋을 조회하는 읽기전용 클라이언트다.
type Client struct {
	bq      *bigquery.Client
	dataset string
}

// NewClient는 ADC(GOOGLE_APPLICATION_CREDENTIALS)로 BigQuery 클라이언트를 만든다.
func NewClient(ctx context.Context, projectID, dataset string) (*Client, error) {
	c, err := bigquery.NewClient(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("bigquery client: %w", err)
	}
	return &Client{bq: c, dataset: dataset}, nil
}

func (c *Client) Close() { _ = c.bq.Close() }

// LabCompletion은 v_lab_completion 을 조회한다. NULL completion_rate 는 0 으로 흡수.
func (c *Client) LabCompletion(ctx context.Context) ([]LabCompletionRow, error) {
	q := c.bq.Query(fmt.Sprintf(
		"SELECT lab_id, started, completed, completion_rate FROM `%s.v_lab_completion`", c.dataset))
	it, err := q.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("query v_lab_completion: %w", err)
	}
	out := make([]LabCompletionRow, 0)
	for {
		var raw struct {
			LabID          bigquery.NullString
			Started        bigquery.NullInt64
			Completed      bigquery.NullInt64
			CompletionRate bigquery.NullFloat64
		}
		err := it.Next(&raw)
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("scan v_lab_completion: %w", err)
		}
		out = append(out, LabCompletionRow{
			LabID:          raw.LabID.StringVal,
			Started:        raw.Started.Int64,
			Completed:      raw.Completed.Int64,
			CompletionRate: raw.CompletionRate.Float64,
		})
	}
	return out, nil
}

// StepFunnel은 v_step_funnel 을 조회한다.
func (c *Client) StepFunnel(ctx context.Context) ([]StepFunnelRow, error) {
	q := c.bq.Query(fmt.Sprintf(
		"SELECT lab_id, step_id, validation_failures FROM `%s.v_step_funnel`", c.dataset))
	it, err := q.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("query v_step_funnel: %w", err)
	}
	out := make([]StepFunnelRow, 0)
	for {
		var raw struct {
			LabID              bigquery.NullString
			StepID             bigquery.NullInt64
			ValidationFailures bigquery.NullInt64
		}
		err := it.Next(&raw)
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("scan v_step_funnel: %w", err)
		}
		out = append(out, StepFunnelRow{
			LabID:              raw.LabID.StringVal,
			StepID:             raw.StepID.Int64,
			ValidationFailures: raw.ValidationFailures.Int64,
		})
	}
	return out, nil
}

// HintUsage는 v_hint_usage 를 조회한다.
func (c *Client) HintUsage(ctx context.Context) ([]HintUsageRow, error) {
	q := c.bq.Query(fmt.Sprintf(
		"SELECT lab_id, step_id, hint_source, hint_count FROM `%s.v_hint_usage`", c.dataset))
	it, err := q.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("query v_hint_usage: %w", err)
	}
	out := make([]HintUsageRow, 0)
	for {
		var raw struct {
			LabID      bigquery.NullString
			StepID     bigquery.NullInt64
			HintSource bigquery.NullString
			HintCount  bigquery.NullInt64
		}
		err := it.Next(&raw)
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("scan v_hint_usage: %w", err)
		}
		out = append(out, HintUsageRow{
			LabID:      raw.LabID.StringVal,
			StepID:     raw.StepID.Int64,
			HintSource: raw.HintSource.StringVal,
			HintCount:  raw.HintCount.Int64,
		})
	}
	return out, nil
}
```

- [ ] **Step 3: build + vet + fmt**

Run: `cd apps/api && go build ./... && go vet ./internal/bq/ && gofmt -l internal/bq/bq.go`
Expected: 에러 없음, gofmt 무출력. (go get 으로 받은 BQ SDK 가 빌드에 포함됨.)

- [ ] **Step 4: 커밋**

```bash
cd /Users/kylekim1223/request700k/cledyu-d3
git add apps/api/internal/bq/bq.go apps/api/go.mod apps/api/go.sum
git commit -m "feat(api): add read-only bigquery client for instructor analytics" \
  -m "internal/bq queries the D2 cledyu_analytics views and returns clean DTOs,
absorbing NULLs (e.g. completion_rate) to zero values."
```

---

### Task 2: 강사 분석 핸들러 + 인터페이스 + fake + 라우터 — TDD

`bqAnalytics` 인터페이스로 핸들러가 BQ 에 의존하고, fake 로 핸들러를 TDD 한다. instructor RBAC 그룹에 라우트 등록.

**Files:**
- Create: `apps/api/internal/api/handlers/instructor.go`
- Create: `apps/api/internal/api/handlers/instructor_test.go`
- Modify: `apps/api/internal/api/handlers/handlers.go` (Handler 필드 `bq` + setter)
- Modify: `apps/api/internal/api/router.go` (instructor 그룹)

**Interfaces:**
- Consumes: `bq.LabCompletionRow/StepFunnelRow/HintUsageRow` (Task 1), `middleware.RequireMinRole`, `h.err`.
- Produces:
  - `bqAnalytics` 인터페이스: `LabCompletion(ctx)([]bq.LabCompletionRow,error)`, `StepFunnel(ctx)([]bq.StepFunnelRow,error)`, `HintUsage(ctx)([]bq.HintUsageRow,error)`
  - `(*Handler).SetBQAnalytics(b bqAnalytics)` setter, `h.bq` 필드
  - `(*Handler).GetInstructorAnalytics(c *gin.Context)` — `GET /api/v1/instructor/analytics`

- [ ] **Step 1: Handler 에 bq 필드 + setter 추가**

`apps/api/internal/api/handlers/handlers.go` 의 Handler struct 에 필드 추가(`ec2Dial` 근처):

```go
	bq        bqAnalytics                   // D3 강사 분석 BigQuery 조회. nil 허용 — 미설정 시 503.
```

`SetEC2Dial` 메서드 근처에 setter 추가:

```go
// SetBQAnalytics는 강사 분석용 BigQuery 조회기를 주입한다(main 이 설정 시에만).
func (h *Handler) SetBQAnalytics(b bqAnalytics) { h.bq = b }
```

handlers.go import 에 `"github.com/requset700k/cledyu/api/internal/bq"` 추가.

- [ ] **Step 2: 실패하는 핸들러 테스트 작성**

Create `apps/api/internal/api/handlers/instructor_test.go`:

```go
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

func (f fakeBQ) LabCompletion(context.Context) ([]bq.LabCompletionRow, error) { return f.completion, nil }
func (f fakeBQ) StepFunnel(context.Context) ([]bq.StepFunnelRow, error)       { return f.funnel, nil }
func (f fakeBQ) HintUsage(context.Context) ([]bq.HintUsageRow, error)         { return f.hints, nil }

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
```

- [ ] **Step 2b: 테스트 실패 확인**

Run: `cd apps/api && go test ./internal/api/handlers/ -run TestGetInstructorAnalytics -v`
Expected: 컴파일 실패 — `undefined: ... GetInstructorAnalytics` / `bqAnalytics`.

- [ ] **Step 3: 인터페이스 + 핸들러 구현**

Create `apps/api/internal/api/handlers/instructor.go`:

```go
package handlers

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/requset700k/cledyu/api/internal/bq"
	"go.uber.org/zap"
)

// bqAnalytics는 D3 강사 분석 BigQuery 조회 의존성이다(*bq.Client 가 구현).
type bqAnalytics interface {
	LabCompletion(ctx context.Context) ([]bq.LabCompletionRow, error)
	StepFunnel(ctx context.Context) ([]bq.StepFunnelRow, error)
	HintUsage(ctx context.Context) ([]bq.HintUsageRow, error)
}

// GetInstructorAnalytics는 D2 BigQuery 뷰(완료율·이탈지점·힌트사용)를 강사에게 반환한다.
// GET /api/v1/instructor/analytics — RequireMinRole("instructor") 게이트(라우터).
func (h *Handler) GetInstructorAnalytics(c *gin.Context) {
	if h.bq == nil {
		h.err(c, http.StatusServiceUnavailable, "analytics store not configured")
		return
	}
	ctx := c.Request.Context()

	completion, err := h.bq.LabCompletion(ctx)
	if err != nil {
		h.log.Error("bq lab_completion", zap.Error(err))
		h.err(c, http.StatusInternalServerError, "load analytics failed")
		return
	}
	funnel, err := h.bq.StepFunnel(ctx)
	if err != nil {
		h.log.Error("bq step_funnel", zap.Error(err))
		h.err(c, http.StatusInternalServerError, "load analytics failed")
		return
	}
	hints, err := h.bq.HintUsage(ctx)
	if err != nil {
		h.log.Error("bq hint_usage", zap.Error(err))
		h.err(c, http.StatusInternalServerError, "load analytics failed")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"lab_completion": completion,
		"step_funnel":    funnel,
		"hint_usage":     hints,
	})
}
```

- [ ] **Step 4: 테스트 통과 확인**

Run: `cd apps/api && go test ./internal/api/handlers/ -run TestGetInstructorAnalytics -v`
Expected: 두 테스트 PASS.

- [ ] **Step 5: 라우터에 instructor 그룹 등록**

`apps/api/internal/api/router.go` 의 admin 그룹 블록 다음(또는 앞)에 추가:

```go
	// 강사 전용 — instructor 이상(admin 포함) 접근. 강사 분석 대시보드 백엔드.
	instructor := v1.Group("/instructor")
	instructor.Use(middleware.RequireMinRole("instructor"))
	{
		instructor.GET("/analytics", h.GetInstructorAnalytics)
	}
```

- [ ] **Step 6: 패키지 테스트 + vet + fmt**

Run: `cd apps/api && go test ./internal/api/handlers/ && go vet ./... && gofmt -l internal/api`
Expected: 전부 PASS, vet/gofmt 무출력.

- [ ] **Step 7: 커밋**

```bash
cd /Users/kylekim1223/request700k/cledyu-d3
git add apps/api/internal/api/handlers/instructor.go apps/api/internal/api/handlers/instructor_test.go apps/api/internal/api/handlers/handlers.go apps/api/internal/api/router.go
git commit -m "feat(api): add instructor analytics endpoint" \
  -m "GET /api/v1/instructor/analytics returns the D2 views (completion, step funnel,
hint usage) via the bqAnalytics interface, gated by RequireMinRole(instructor)."
```

---

### Task 3: main 배선 + config — BigQuery 클라이언트 주입

설정이 있으면 bq.Client 를 만들어 핸들러에 주입한다(D2 가 Airflow 에만 넣은 것과 별개로 api 에 읽기전용 BQ). 산출물: `go build`/`go vet`(라이브는 SA 필요 — 런북).

**Files:**
- Modify: `apps/api/internal/config/config.go` (BQ 설정 필드)
- Modify: `apps/api/cmd/server/main.go` (bq.Client 생성 + SetBQAnalytics)

**Interfaces:**
- Consumes: `bq.NewClient` (Task 1), `(*Handler).SetBQAnalytics` (Task 2).

- [ ] **Step 1: config 에 BQ 필드 추가**

`apps/api/internal/config/config.go` 에 분석 설정을 추가한다. 기존 구조를 따라(예: `Analytics` 섹션 또는 평면 필드) 다음 두 값을 읽도록 한다 — 환경변수 `CLEDYU_ANALYTICS_PROJECT`, `CLEDYU_ANALYTICS_DATASET`:

```go
// Analytics는 D3 강사 분석용 BigQuery 설정이다. ProjectID 가 비면 비활성(핸들러 503).
type Analytics struct {
	ProjectID string `mapstructure:"project_id"`
	Dataset   string `mapstructure:"dataset"`
}
```

Config struct 에 `Analytics Analytics` 필드 추가하고, viper 기본값/바인딩을 기존 패턴대로 등록(예: `v.SetDefault("analytics.dataset", "cledyu_analytics")`, env 바인딩 `CLEDYU_ANALYTICS_PROJECT`→`analytics.project_id`). 정확한 viper 등록은 기존 db.dsn 패턴(config.go 의 SetDefault/BindEnv)을 그대로 따른다.

- [ ] **Step 2: main 에서 bq.Client 생성 + 주입**

`apps/api/cmd/server/main.go` 에서 Handler 생성 후, db 주입과 같은 위치에 추가(기존 `New(...)` 호출 다음, 라우터 구성 전):

```go
	// D3 강사 분석 — ProjectID 설정 시에만 BigQuery 조회기 주입(미설정 시 핸들러 503).
	if cfg.Analytics.ProjectID != "" {
		bqClient, err := bq.NewClient(ctx, cfg.Analytics.ProjectID, cfg.Analytics.Dataset)
		if err != nil {
			logger.Warn("BigQuery 분석 클라이언트 생성 실패 — 강사 분석 비활성", zap.Error(err))
		} else {
			h.SetBQAnalytics(bqClient)
			defer bqClient.Close()
		}
	}
```

main.go import 에 `"github.com/requset700k/cledyu/api/internal/bq"` 추가. `ctx`/`logger`/`h`/`cfg` 는 기존 main 의 변수명에 맞춘다(다르면 그에 맞게 조정).

- [ ] **Step 3: build + vet + fmt**

Run: `cd apps/api && go build ./... && go vet ./... && gofmt -l internal/config cmd/server`
Expected: 에러 없음, gofmt 무출력.

- [ ] **Step 4: 커밋**

```bash
cd /Users/kylekim1223/request700k/cledyu-d3
git add apps/api/internal/config/config.go apps/api/cmd/server/main.go
git commit -m "feat(api): wire bigquery analytics client from config" \
  -m "Construct the read-only bq.Client when CLEDYU_ANALYTICS_PROJECT is set and inject
it into the handler; absent config leaves instructor analytics at 503."
```

---

### Task 4: 웹 /instructor 페이지 + API 클라이언트 + 타입

**Files:**
- Modify: `apps/web/lib/types.ts`
- Modify: `apps/web/lib/api.ts`
- Create: `apps/web/app/(platform)/instructor/page.tsx`

**Interfaces:**
- Produces: 타입 `InstructorAnalytics`; `api.instructor.analytics()`.

- [ ] **Step 1: 타입 추가**

`apps/web/lib/types.ts` 끝에 추가:

```typescript
/** GET /api/v1/instructor/analytics 응답 (강사 분석) */
export interface InstructorAnalytics {
  lab_completion: { lab_id: string; started: number; completed: number; completion_rate: number }[];
  step_funnel: { lab_id: string; step_id: number; validation_failures: number }[];
  hint_usage: { lab_id: string; step_id: number; hint_source: string; hint_count: number }[];
}
```

- [ ] **Step 2: API 클라이언트 추가**

`apps/web/lib/api.ts` 상단 import 에 `InstructorAnalytics` 추가. `export const api = {` 내부에 네임스페이스 추가(기존 콤마 규칙):

```typescript
  instructor: {
    analytics: () => request<InstructorAnalytics>('/api/v1/instructor/analytics'),
  },
```

- [ ] **Step 3: /instructor 페이지 작성**

Create `apps/web/app/(platform)/instructor/page.tsx`:

```tsx
'use client';

import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="rounded-lg border border-slate-800 bg-slate-900/50 p-5">
      <h2 className="text-white font-semibold mb-3">{title}</h2>
      {children}
    </section>
  );
}

export default function InstructorPage() {
  const { data, isLoading, isError } = useQuery({
    queryKey: ['instructor-analytics'],
    queryFn: () => api.instructor.analytics(),
  });

  if (isLoading) return <p className="text-slate-400">불러오는 중...</p>;
  if (isError || !data) {
    return <p className="text-red-400">분석을 불러오지 못했습니다. (강사 권한 또는 분석 미설정)</p>;
  }

  return (
    <div className="space-y-8">
      <div>
        <h1 className="text-2xl font-bold text-white">강사 분석</h1>
        <p className="text-slate-400 mt-1 text-sm">코호트 완료율·이탈 지점·힌트 사용 패턴.</p>
      </div>

      <Section title="랩별 완료율">
        {data.lab_completion.length === 0 ? (
          <p className="text-slate-500 text-sm">데이터 없음.</p>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="text-slate-400 text-left">
                <th className="py-2">랩</th>
                <th className="py-2 text-right">시작</th>
                <th className="py-2 text-right">완료</th>
                <th className="py-2 text-right">완료율</th>
              </tr>
            </thead>
            <tbody>
              {data.lab_completion.map((r) => (
                <tr key={r.lab_id} className="text-slate-300">
                  <td className="py-2">{r.lab_id}</td>
                  <td className="py-2 text-right">{r.started}</td>
                  <td className="py-2 text-right">{r.completed}</td>
                  <td className="py-2 text-right">{Math.round(r.completion_rate * 100)}%</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Section>

      <Section title="이탈 지점 (스텝별 검증 실패)">
        {data.step_funnel.length === 0 ? (
          <p className="text-slate-500 text-sm">데이터 없음.</p>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="text-slate-400 text-left">
                <th className="py-2">랩</th>
                <th className="py-2 text-right">스텝</th>
                <th className="py-2 text-right">검증 실패</th>
              </tr>
            </thead>
            <tbody>
              {data.step_funnel.map((r) => (
                <tr key={`${r.lab_id}-${r.step_id}`} className="text-slate-300">
                  <td className="py-2">{r.lab_id}</td>
                  <td className="py-2 text-right">{r.step_id}</td>
                  <td className="py-2 text-right">{r.validation_failures}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Section>

      <Section title="힌트 사용 패턴">
        {data.hint_usage.length === 0 ? (
          <p className="text-slate-500 text-sm">데이터 없음.</p>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="text-slate-400 text-left">
                <th className="py-2">랩</th>
                <th className="py-2 text-right">스텝</th>
                <th className="py-2">소스</th>
                <th className="py-2 text-right">횟수</th>
              </tr>
            </thead>
            <tbody>
              {data.hint_usage.map((r) => (
                <tr key={`${r.lab_id}-${r.step_id}-${r.hint_source}`} className="text-slate-300">
                  <td className="py-2">{r.lab_id}</td>
                  <td className="py-2 text-right">{r.step_id}</td>
                  <td className="py-2">{r.hint_source}</td>
                  <td className="py-2 text-right">{r.hint_count}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Section>
    </div>
  );
}
```

(역할 게이팅: instructor 권한이 없으면 API 가 403 → isError 분기로 안내. 네비 링크는 기존대로 두되, v1 범위에선 API 403 으로 충분.)

- [ ] **Step 4: 타입체크 + 린트 + prettier**

Run: `cd apps/web && pnpm typecheck && pnpm lint`
그다음 prettier(함정 방지): `cd /Users/kylekim1223/request700k/cledyu-d3 && pre-commit run prettier --files "apps/web/app/(platform)/instructor/page.tsx" apps/web/lib/api.ts apps/web/lib/types.ts`
Expected: typecheck/lint 에러 없음. prettier 가 수정하면 결과를 커밋에 포함하고 재실행해 Passed.

- [ ] **Step 5: 프로덕션 빌드(dev 서버 미실행 시)**

Run: `cd apps/web && pnpm build`
Expected: 빌드 성공, `/instructor` 라우트 출력에 포함.

- [ ] **Step 6: 커밋**

```bash
cd /Users/kylekim1223/request700k/cledyu-d3
git add apps/web/lib/types.ts apps/web/lib/api.ts "apps/web/app/(platform)/instructor/page.tsx"
git commit -m "feat(web): add instructor analytics page" \
  -m "/instructor renders cohort completion, step funnel, and hint usage from the
analytics endpoint; api.instructor.analytics client and types."
```

---

### Task 5: 읽기전용 SA (terraform) + ESO 자격증명

**Files:**
- Modify: `infra/terraform/gcp/main.tf` (읽기전용 SA + IAM)
- Modify: `infra/terraform/gcp/outputs.tf` (reader sa_email)
- Create: `gitops/apps/api/externalsecret-bq-reader.yaml`

**Interfaces:**
- Produces: SA `api-analytics-reader`, IAM dataViewer(dataset)+jobUser(project), ESO Secret `api-bq-reader`(key.json) in api ns.

- [ ] **Step 1: terraform 읽기전용 SA + IAM 추가**

`infra/terraform/gcp/main.tf` 끝에 추가:

```hcl
# ── API 읽기전용 분석 SA (D3) ─────────────────────────────────────────────────
resource "google_service_account" "api_analytics_reader" {
  account_id   = "api-analytics-reader"
  display_name = "Session API instructor analytics reader (read-only)"
}

resource "google_bigquery_dataset_iam_member" "api_reader_data_viewer" {
  dataset_id = google_bigquery_dataset.analytics.dataset_id
  role       = "roles/bigquery.dataViewer"
  member     = "serviceAccount:${google_service_account.api_analytics_reader.email}"
}

resource "google_project_iam_member" "api_reader_job_user" {
  project = var.project_id
  role    = "roles/bigquery.jobUser"
  member  = "serviceAccount:${google_service_account.api_analytics_reader.email}"
}
```

`infra/terraform/gcp/outputs.tf` 에 추가:

```hcl
output "api_reader_sa_email" {
  value = google_service_account.api_analytics_reader.email
}
```

- [ ] **Step 2: ESO 매니페스트 작성**

Create `gitops/apps/api/externalsecret-bq-reader.yaml`:

```yaml
---
# D3 강사 분석 — API 가 BigQuery 뷰를 읽는 읽기전용 SA 키.
# vault kv put cledyu/gcp/api-analytics-reader key.json=@reader-key.json
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: api-bq-reader
  namespace: api
  annotations:
    argocd.argoproj.io/sync-options: SkipDryRunOnMissingResource=true
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: vault-backend
    kind: ClusterSecretStore
  target:
    name: api-bq-reader
  data:
    - secretKey: key.json
      remoteRef:
        key: gcp/api-analytics-reader
        property: key.json
```

(참고: api Deployment 의 SA 키 마운트 + `GOOGLE_APPLICATION_CREDENTIALS`·`CLEDYU_ANALYTICS_PROJECT` env 설정은 런북의 라이브 적용 단계에서 `gitops/apps/api/values.yaml`/deployment 에 추가 — 정확한 차트 키는 apply 시 대조. v1 자동화 범위는 ESO 매니페스트까지.)

- [ ] **Step 3: terraform fmt/validate + yamllint**

Run:
```
cd /Users/kylekim1223/request700k/cledyu-d3/infra/terraform/gcp && terraform init -backend=false >/dev/null 2>&1 && terraform fmt -check && terraform validate
cd /Users/kylekim1223/request700k/cledyu-d3 && pre-commit run --files gitops/apps/api/externalsecret-bq-reader.yaml
```
Expected: fmt clean + "Success! The configuration is valid.", pre-commit 통과.

- [ ] **Step 4: 커밋**

```bash
cd /Users/kylekim1223/request700k/cledyu-d3
git add infra/terraform/gcp/main.tf infra/terraform/gcp/outputs.tf gitops/apps/api/externalsecret-bq-reader.yaml
git commit -m "feat(infra): add read-only bigquery sa and eso secret for api analytics" \
  -m "api-analytics-reader SA (dataset-scoped dataViewer + project jobUser) and an
ExternalSecret injecting its key into the api namespace."
```

---

### Task 6: 런북 + 전체 게이트 + PR

**Files:**
- Create: `docs/RUNBOOK/d3-instructor-analytics.md`

- [ ] **Step 1: 런북 작성**

Create `docs/RUNBOOK/d3-instructor-analytics.md`:

```markdown
# D3 강사 분석 — 적용 런북

자동화 범위는 코드 작성 + 정적검증까지. 라이브는 GCP/Vault 인증이 필요한 수동 단계(김용균/owner).

## 1. 읽기전용 SA apply + 키 → Vault
\`\`\`
cd infra/terraform/gcp && terraform apply   # api-analytics-reader 생성
gcloud iam service-accounts keys create reader-key.json \\
  --iam-account="$(terraform -chdir=infra/terraform/gcp output -raw api_reader_sa_email)"
vault kv put cledyu/gcp/api-analytics-reader key.json=@reader-key.json
rm reader-key.json
\`\`\`

## 2. api 에 SA 키 마운트 + env (gitops/apps/api/values.yaml 또는 deployment)
- Secret api-bq-reader 를 api 파드에 마운트(예: /etc/api-bq-reader/key.json).
- env: GOOGLE_APPLICATION_CREDENTIALS=/etc/api-bq-reader/key.json,
  CLEDYU_ANALYTICS_PROJECT=cledyu-project, CLEDYU_ANALYTICS_DATASET=cledyu_analytics.
- 정확한 차트 키는 helm show values 로 대조. ArgoCD sync 후 api 파드 재기동.

## 3. 검증
- 강사(instructor) 역할 계정으로 /instructor 접근 → 완료율·이탈지점·힌트 표가 채워지는지.
- 데이터가 없으면 D2 파이프라인을 먼저 돌려 cledyu_analytics 뷰에 데이터를 적재(D2 런북).
\`\`\`
bq query --use_legacy_sql=false 'SELECT * FROM cledyu_analytics.v_lab_completion'
\`\`\`
```

- [ ] **Step 2: 전체 정적 게이트**

Run:
```
cd /Users/kylekim1223/request700k/cledyu-d3/apps/api && go test ./... -race && go vet ./... && gofmt -l internal cmd && golangci-lint run --timeout=5m
cd /Users/kylekim1223/request700k/cledyu-d3/apps/web && pnpm typecheck && pnpm lint
cd /Users/kylekim1223/request700k/cledyu-d3/infra/terraform/gcp && terraform fmt -check
cd /Users/kylekim1223/request700k/cledyu-d3 && pre-commit run --files docs/RUNBOOK/d3-instructor-analytics.md gitops/apps/api/externalsecret-bq-reader.yaml
```
Expected: 모든 테스트 PASS, lint/vet/fmt 무출력/무에러, pre-commit 통과.

- [ ] **Step 3: 커밋 + 푸시 + PR**

```bash
cd /Users/kylekim1223/request700k/cledyu-d3
git add docs/RUNBOOK/d3-instructor-analytics.md
git commit -m "docs(api): add d3 instructor analytics runbook"
git push -u origin feat/instructor-analytics-d3
gh pr create --base main --head feat/instructor-analytics-d3 \
  --title "feat(api): add instructor analytics dashboard" \
  --body "설계: docs/superpowers/specs/2026-06-27-instructor-analytics-d3-design.md (D3)

강사 전용 /instructor 페이지 + GET /api/v1/instructor/analytics(RequireMinRole instructor)가
D2 BigQuery 뷰(완료율·이탈지점·힌트사용)를 읽기전용 SA로 조회. bqAnalytics 인터페이스+fake로
핸들러 TDD. 읽기전용 SA(dataset dataViewer)+ESO 추가.

자동화 = 작성+정적검증(go test/golangci-lint, terraform validate, typecheck/lint).
라이브 api↔BQ(SA 키·vault put·values 마운트)는 docs/RUNBOOK/d3-instructor-analytics.md 수동 단계.

테스트: go test ./... -race, golangci-lint, pnpm typecheck/lint, terraform fmt 통과."
gh pr edit --add-assignee ykgoesdumb || true
```

Expected: PR 생성, CI 시작.

---

## Self-Review (작성자 점검 결과)

- **Spec coverage:** 표면(자체 웹 Task 4), Go→BQ(Task 1·3), 엔드포인트+RBAC(Task 2), 읽기전용 SA+ESO(Task 5), 503/에러(Task 2), 런북/라이브(Task 6) 모두 커버.
- **Placeholder scan:** 모든 단계 실제 코드/HCL/YAML 포함. config viper 등록·api values 마운트는 "기존 패턴 대조"로 구체 위임(차트/viper 키가 환경 의존이라 — 정적검증으로 컴파일 보장).
- **Type consistency:** BQ 뷰 컬럼 → bq.*Row(json 태그) → bqAnalytics 인터페이스 → 핸들러 JSON 키(lab_completion/step_funnel/hint_usage) → web InstructorAnalytics 타입 전 구간 일치(lab_id/started/completed/completion_rate/step_id/validation_failures/hint_source/hint_count).
- **알려진 한계:** 실제 api↔BigQuery 쿼리·SA·values 마운트는 라이브(수동 런북). bq.Client 의 NullX→zero 흡수는 컴파일 검증, 실 NULL 동작은 스모크에서 확인.
