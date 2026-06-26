# 학습 분석 — 3트랙 설계 (학습자 대시보드 / 데이터 파이프라인 / 강사 분석)

- 작성일: 2026-06-26
- 상태: 설계 승인 — D1 상세 spec 확정, D2/D3는 후속 sub-project
- 영향 레이어: 서비스(Go/Gin API, Next.js)=D1, 데이터(Kafka/Airflow/BigQuery/dbt)=D2·D3
- 제약: 프로젝트는 [[project_deadline_terminating]] (2026-07-22 종료) — 속도·데모 우선, 내구성 과투자 금지

## 1. 배경 / 현재 상태 (실측)

- **소스 살아있음**: `lab-events` Kafka 토픽 + producer(mTLS, 6종: lab_started/step_completed/validation_failed/hint_requested/lab_completed/lab_abandoned)가 main에서 발행 중. (`apps/api/internal/events/`)
- **Airflow**: 설치만 됨(`gitops/apps/airflow`: namespace/values/secret), **DAG 0개** — 구현 전무.
- **웨어하우스**: `infra/`에 BigQuery 데이터셋·GCS 버킷·Terraform 전무. 완전 그린필드.
- **분석 API·웹 대시보드**: 없음. ("대시보드 미로딩"의 원인은 파이프라인 부재가 아니라 **대시보드 자체 미구현**.)
- **이미 있는 자산(D1 토대)**: PR #188로 머지된 `GET /api/v1/me/progress`(score, rank, labs_completed, by_difficulty, recent_completions)와 `lab_completions`·`session_steps` 테이블, 랩 카탈로그(`content.LabContent`: id/title/difficulty).

## 2. 아키텍처 결정 — 3트랙 분리

| 트랙 | 정체 | 데이터 원천 | 소유 | 파이프라인 의존 |
|---|---|---|---|---|
| **D1 학습자 대시보드** | 학습자 대면 per-user 분석 화면 | **Postgres**(저지연) | 서비스(API/web) | 없음 |
| **D2 데이터 파이프라인** | Kafka→Airflow→BigQuery+GCS, 영구 아카이브 + 데이터엔지니어링 역량 증명 | Kafka→BQ | 데이터(김찬영) | 본체 |
| **D3 강사 분석** | 랩 실패율·힌트 패턴·코호트 집계 | **BigQuery** | 데이터 + 서비스 | D2 산출물 소비 |

**핵심 원칙:** BigQuery는 유저 서빙 DB가 아니다(웨어하우스 — 지연·스캔과금). per-user 저지연 = Postgres, 대규모 집계/이력 = BQ. → 학습자 대시보드(D1)를 BQ로 서빙하지 않는다.

**파이프라인(D2) 정당화:** (1) "데이터 파이프라인 시연"이 확정 산출물 = 역량 증명, (2) 강사/코호트 분석(D3)의 데이터 원천. 학습자 대시보드 때문이 아님(Postgres로 충분).

**구현 순서:** D1 먼저(웹 대시보드 UI 렌더 검증이 급함, 파이프라인 무의존) → D2 → D3.

## 3. D1 — 학습자 대시보드 (이번 구현 대상, 상세 spec)

### 3.1 보여줄 내용 (확정)
- **풍부한 개인 현황**: 점수·순위·총 완료수·전체 랩 대비 완료율·난이도별(완료/전체).
- **랩별 성취/수료 현황**: 카탈로그의 각 랩에 대해 상태(완료/진행중/미시작) + 완료 시각.
- (옵션, 낮은 우선순위) **시계열 활동 추이** — D2 롤업 연동 시 추가. v1 미포함.
- (비포함) 코호트 비교(D3), 배지.

### 3.2 데이터 원천 — 전부 Postgres (신규 저장소 없음)
- `lab_completions(user_id, lab_id, completed_at)` — 완료 판정.
- `session_progress` / `session_steps(status, attempts, hint_level)` — 진행중 판정.
- 랩 카탈로그(in-memory `h.labs`: id/title/difficulty) — 전체 랩 목록·난이도.

### 3.3 API
신규 엔드포인트 (authed v1 그룹, 기존 패턴):
```
GET /api/v1/me/dashboard
→ {
  "summary": {
    "score": 30, "rank": 17, "labs_completed": 3,
    "total_labs": 5, "completion_pct": 60,
    "by_difficulty": { "beginner": {"done":3,"total":5}, "intermediate": {"done":0,"total":0}, "advanced": {"done":0,"total":0} }
  },
  "labs": [
    { "lab_id": "lab-docker-basics", "title": "Docker 기초", "difficulty": "beginner",
      "status": "completed", "completed_at": "2026-06-25T..." },
    { "lab_id": "lab-k8s-basics", "title": "Kubernetes 기초", "difficulty": "beginner",
      "status": "not_started", "completed_at": null }
  ],
  "recent_completions": [ { "lab_id": "...", "completed_at": "..." } ]
}
```
- 상태 판정: `lab_completions`에 행 있으면 `completed`; 없고 활성 세션 진행기록(`session_progress`/`steps`)이 있으면 `in_progress`; 둘 다 없으면 `not_started`.
- 점수/순위는 기존 `userScore`/랭킹 로직 재사용(중복 금지 — 헬퍼 공유).
- 전부 Postgres. `h.db == nil` → 503. 타인 데이터 노출 없음(본인 user_id만).
- (구현 판단) `/me/progress`를 확장할지 신규 `/me/dashboard`를 둘지는 플랜에서 결정 — per-lab status가 추가 데이터라 신규 엔드포인트가 깔끔. `/me/progress`는 유지(리더보드 페이지가 사용).

### 3.4 Web
- 신규 페이지 `app/(platform)/dashboard/page.tsx` + Navbar 링크 추가(`/dashboard`, 라벨 "내 학습").
- 섹션: ① 요약 카드(점수·순위·완료율·난이도별 진행바) ② 랩별 상태 그리드(완료/진행/미시작 배지) ③ 최근 활동.
- 기존 패턴 재사용: `useQuery` + `api.me.dashboard()`. 신규 타입 `DashboardResponse` in `lib/types.ts`.
- 차트는 가벼운 것만(진행바/그리드). 무거운 차트 라이브러리 신규 도입 지양(YAGNI).

### 3.5 테스트
- Go 핸들러(fakePersistence): per-lab 상태 판정(completed/in_progress/not_started), completion_pct, by_difficulty done/total, 빈 데이터.
- web: 순수 헬퍼(상태 배지 매핑 등)가 있으면 node:test. 페이지는 typecheck/lint/build.
- 게이트: `go test ./... -race`, `golangci-lint`, `gofmt`, `pnpm typecheck`/`lint`, **pre-commit prettier**([[feedback_plan_verify_includes_lint]]의 prettier 함정), `node --test`.

## 4. D2 — 데이터 파이프라인 (후속 sub-project, 별도 spec)

목표: `lab-events` Kafka → 영구 BigQuery 저장. **Airflow DAG로 오케스트레이션**(역량 증명). GCS는 raw 랜딩/스테이징.
- 후보 경로: Kafka → GCS(raw) → Airflow DAG(GCS→BQ load + dbt transform) → BQ. (대안: Airflow가 Kafka 배치 소비.)
- 인프라: GCS 버킷 + BQ 데이터셋 Terraform(`infra/`). **GCP $300 크레딧 사용** — 종료프로젝트라 만료/이관 무관, BQ·GCS는 크레딧 대상(Gemini는 제외, [[project_deadline_terminating]]).
- 상세(DAG 설계·스키마·dbt 모델·idempotency)는 D2 전용 spec에서.

## 5. D3 — 강사 분석 (후속 sub-project, 별도 spec)

BigQuery 위 집계로 강사/운영자 인사이트:
- 랩별 `validation_failed` 집계 → **이탈 지점**(어느 스텝에서 막히나).
- `hint_requested` 패턴 → 콘텐츠 난이도 진단.
- 코호트 완료 분포.
표면: `/instructor`(강사 모드) 차트 또는 내부용 Looker Studio(per-user 아니라 임베드 허용 가능). 상세는 D3 전용 spec에서.

## 6. SLO / 비용 / DR

- **D1**: 읽기전용 Postgres 집계, 신규 클라우드 0 → **+$0**. Lab/Validation/VM/WS SLO 무관.
- **D2/D3**: GCP 크레딧(만료 무관 — 종료프로젝트). DR 무관 — 웨어하우스는 재구성 가능한 싱크(system-of-record 아님).
- 학습자 대시보드 데이터의 system-of-record는 온프렘 Postgres 그대로.

## 7. 의도적 비포함 (YAGNI / 후속)
- D1 시계열 추이(D2 의존, 낮은 우선순위), 배지/업적, 코호트 비교(D3).
- 실시간 스트리밍(Dataflow 등) — Airflow 배치로 충분(데모).
- DataHub 리니지 — 여력 되면 D2에 부가.
