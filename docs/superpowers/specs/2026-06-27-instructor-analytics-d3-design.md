# D3 강사 분석 대시보드 설계 (BigQuery 뷰 → Go API → /instructor)

- 작성일: 2026-06-27
- 상태: 설계 승인 — 구현 플랜 대기
- 영향 레이어: 서비스(Go/Gin API, Next.js), 보안(instructor RBAC, 읽기전용 GCP SA)
- 제약: [[project_deadline_terminating]] (2026-07-22 종료) — 속도·데모 우선
- 상위: [학습 분석 3트랙](2026-06-26-learning-analytics-design.md) 의 D3. D1(#191)·D2(#195) 머지 완료.

## 1. 목적 / 배경

D2가 BigQuery에 만든 집계 뷰를 **강사/운영자용 분석 화면**으로 노출한다. 목적: 코칭(막히는 학습자·랩 식별), 콘텐츠 개선(실패율 높은 스텝·힌트 과다 지점), 교육 효과 측정. 학습자용이 아닌 강사용 내부 뷰. KodeKloud for Business·Khan Academy Teacher Dashboard 등 교육 플랫폼의 관습적 기능.

### 현재 상태 (실측)
- D2 BigQuery 뷰 존재(main): `v_lab_completion`(랩별 시작/완료/완료율), `v_step_funnel`(랩·스텝별 validation_failed), `v_hint_usage`(랩·스텝별 hint_requested ai/static). (`apps/airflow/dags/sql/d3_views.sql`)
- instructor RBAC 스캐폴딩 완료: `middleware.RequireMinRole(minRole)`(rbac.go), 역할 우선순위 admin>instructor>student(oidc.go). admin 그룹이 `RequireMinRole("admin")` 사용. 코드 주석에 "강사 모드 도입 시 같은 패턴으로 추가" 명시.
- `/instructor` 네비 링크만 존재, 페이지·API 없음.
- apps/api 에 GCP 의존성 없음(D2는 SA를 Airflow ns 에만 주입). D3는 api 에 읽기전용 BQ 접근을 새로 배선.

## 2. 확정된 설계 결정

| 항목 | 결정 |
|---|---|
| 표면 | **자체 웹 페이지(/instructor) + Go→BigQuery** (D1과 일관, Looker Studio 아님) |
| API | `GET /api/v1/instructor/analytics`, `RequireMinRole("instructor")` |
| BQ 접근 | apps/api 에 `bqAnalytics` 인터페이스 + BigQuery 구현, **읽기전용 SA** |
| 차트 | 경량(테이블 + 진행바) — 무거운 차트 라이브러리 미도입 |
| 비포함 | 강사 "관전 모드"(라이브 세션), 드릴다운/필터/CSV, 개인 상세(기존 admin activity로 충분) |

## 3. 아키텍처 / 데이터 흐름

```
BigQuery cledyu_analytics 뷰 3종 (D2)
  ← apps/api: bqAnalytics 인터페이스(BigQuery 구현, 읽기전용 SA)
      GET /api/v1/instructor/analytics  [JWT → RequireMinRole("instructor")]
      → { lab_completion: [...], step_funnel: [...], hint_usage: [...] }
  ← apps/web: /instructor 페이지(instructor/admin 역할만), 테이블 렌더
```
뷰가 이미 소량 집계라 쿼리 결과가 작아 저지연·저비용. 강사 저트래픽.

## 4. 컴포넌트

### apps/api
- **`bqAnalytics` 인터페이스** (store 의 persistence 패턴): 메서드
  - `LabCompletion(ctx) ([]LabCompletionRow, error)` — v_lab_completion
  - `StepFunnel(ctx) ([]StepFunnelRow, error)` — v_step_funnel
  - `HintUsage(ctx) ([]HintUsageRow, error)` — v_hint_usage
- **BigQuery 구현**: `cloud.google.com/go/bigquery`, 프로젝트/데이터셋 설정, SELECT * FROM 각 뷰. nil 허용(미설정 시 핸들러 503).
- **핸들러** `GetInstructorAnalytics`: 3개 메서드 호출 → JSON `{lab_completion, step_funnel, hint_usage}`. bq 미설정 → 503, 쿼리 실패 → 500 로깅.
- **라우터**: `instructor := v1.Group("/instructor"); instructor.Use(middleware.RequireMinRole("instructor"))` (admin 그룹과 동일 패턴, admin 도 통과). `GET /instructor/analytics`.
- **자격증명**: 읽기전용 SA 키 → Vault `cledyu/gcp/api-analytics-reader` → ESO → api ns Secret → api 파드 `GOOGLE_APPLICATION_CREDENTIALS`. (Strimzi 와 무관, BQ 전용.)

### apps/web
- `/instructor` 페이지(`app/(platform)/instructor/page.tsx`): `api.instructor.analytics()` → 3 섹션(랩 완료율·이탈 지점·힌트 사용) 테이블/진행바. 데이터 없으면 "데이터 없음".
- 역할 게이팅: `/me` 의 role 이 instructor/admin 이 아니면 접근 안내(네비 링크는 이미 존재 — 역할로 노출 제어).
- `lib/api.ts`: `api.instructor.analytics()`. `lib/types.ts`: 응답 타입.

### infra/terraform/gcp (기존 모듈 확장)
- 신규 **읽기전용 SA** `api-analytics-reader` + IAM: `roles/bigquery.dataViewer` (데이터셋 스코프 `cledyu_analytics`) + `roles/bigquery.jobUser`(프로젝트). airflow SA(dataEditor)와 분리 — 최소권한.

## 5. 에러 처리 / 멱등성

- bq 클라이언트 미설정(로컬/CI) → 503 (기존 `h.db == nil` 패턴).
- 쿼리 실패 → 500 + 로깅.
- 빈 뷰(공개 전 데이터 희소) → 빈 배열, 페이지는 안내 문구. 읽기전용이라 멱등성 이슈 없음.

## 6. 테스트 / 검증

- **자동 검증**: `bqAnalytics` 인터페이스 + fake 더블로 핸들러 TDD(JSON shape, 503 분기, RBAC 통과). 웹 순수 헬퍼 node:test. golangci-lint/gofmt/go test, pnpm typecheck/lint.
- **라이브 불가(별도 스모크)**: 실제 api↔BigQuery 쿼리는 GCP SA + 적재된 BQ 필요 → D2 처럼 수동 스모크(런북). SA 생성/키는 김용균님 단계.

## 7. SLO / 비용 / DR

- 강사 저트래픽 읽기전용 → Lab/Validation/VM/WS SLO 무관. BQ 쿼리 크레딧 소량(뷰 결과 작음).
- DR 무관 — BQ 는 재구성 가능 싱크.

## 8. 의도적 비포함 (YAGNI / 후속)

- 강사 "관전 모드"(학습자 라이브 세션 들여다보기) — 별개 무거운 기능, 별도 spec.
- 드릴다운·기간 필터·CSV export, 개인별 상세(기존 `admin/users/:uid/activity`로 충분).
- Looker Studio 표면(자체 웹으로 결정).
