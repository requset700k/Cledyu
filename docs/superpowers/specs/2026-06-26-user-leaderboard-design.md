# 유저별 리더보드 v1 설계

- 작성일: 2026-06-26
- 상태: 설계 승인 완료 (구현 플랜 대기)
- 영향 레이어: 서비스(Go/Gin Session API, Next.js), 일부 보안(프라이버시/옵트아웃)
- 관련 사전 작업: PR #180 / #183 (랩 검증 false-pass 차단 → "완료 = 실제 통과" 신뢰성 확보)

## 1. 배경과 목표

학습자가 자신의 교육 현황을 확인하고, 상위 학습자가 "명예의 전당"으로 공시되는
gamification 기능을 만든다. 목표는 학습 동기 부여이되, 리서치에서 확인된 함정
(하위권 탈동기화, 빈 그라인딩, 힌트/시도 페널티로 인한 학습 위축, 요소 과다)을 피한다.

리서치 핵심 교훈(타 교육앱):
- Duolingo: 소그룹 리그 + 주간 리셋으로 "전체 단일 순위"의 하위권 좌절을 회피.
- Khan Academy: 순위 경쟁보다 개인 성취(포인트/배지/명확한 성장 경로) 중심.
- KodeKloud(실습형): 실습 "완료" 자체를 점수의 근거로 사용.
- 공통 함정: 단일 전체 순위만 노출, 힌트 감점, 요소 과다, 게이밍 가능한 점수(치팅).

## 2. 확정된 설계 결정

| 항목 | 결정 | 근거 |
|---|---|---|
| 형태 | 명예의 전당(Top N) + 모두가 보는 개인 현황 | 상위 공시 욕구 충족 + 하위권 좌절 완화 |
| 점수 | 난이도 가중 완료 점수 (힌트 감점 없음) | 깊이 보상, 그라인딩 방지, AI 튜터와 비충돌 |
| 공시/프라이버시 | 이름 표시 + 옵트아웃 (기본 노출, 숨김 가능) | 공개 노출 플랫폼 + 비경쟁 성향 배려 |
| 기간 | 누적 명예의 전당 + 최근 7일 급상승 | 시즌 리셋 없이 신규/복귀 학습자 노출 기회 |
| 범위 | 리더보드 + 개인 현황만, 배지는 v2 | "작게 시작" — 요소 과다 회피(YAGNI) |

### 의도적 비포함 (YAGNI, v2 이후)
배지/업적, 시즌 리셋, 리그/티어, org별 분리 리더보드, BigQuery/Airflow 분석 집계.

## 3. 기존 토대 (실측 확인)

리더보드의 데이터 원천이 이미 온프렘 Postgres에 durable하게 존재한다.

- `lab_completions` 테이블: `(user_id, lab_id)` PK = (유저, 랩) 최초 완료 1건.
  `completed_at` 보유. (`apps/api/internal/store/migrations/0001_init.sql`)
- `users` 테이블: Keycloak `sub`(id), email, **name**, role.
- `session_steps`: step별 attempts, hint_level, checks(JSONB) — 개인 현황 상세에 활용 가능.
- store 함수: `RecordCompletion`(멱등), `ListCompletionsByUser`, `ListUsers`.
  (`apps/api/internal/store/store.go`)
- 랩 콘텐츠: YAML 메타에 `difficulty` 보유. 현재 5개 랩 전부 `beginner`.
  (`apps/api/internal/content/labs/*.yaml`)
- API: `/api/v1` JWT 그룹(`middleware.JWT`), `GET /me`는 `points:0, badges:[]` STUB.
  (`apps/api/internal/api/router.go`, `handlers/user.go`)
- Web: `/leaderboard` 네비 링크만 존재, 페이지/API 미구현.
  (`apps/web/components/ui/Navbar.tsx:10`)

### 저장 위치 / 이관 안전성
- Postgres는 온프렘 K8s StatefulSet + Longhorn(replica 3). DSN은 Vault→ESO 주입
  (`CLEDYU_DB_DSN`). 리더보드는 컬럼 1개 + 읽기 쿼리만 추가하므로 스토리지 부담 거의 0.
- 추후 클라우드 이관 시: 스키마는 마이그레이션 파일로 동승, DSN은 시크릿 repoint(코드 변경 0),
  점수는 표준 SQL + Go라 포터블. **온프렘 전용 가정(Longhorn 경로/노드 로컬 등)을 코드에 넣지 않는다.**

## 4. 점수 모델

```
score(user) = Σ over user's lab_completions of weight(difficulty(lab_id))
```

- 가중치 맵(설정 상수, Go): `beginner: 10, intermediate: 25, advanced: 50`.
- 난이도는 in-memory 랩 콘텐츠(`h.labs`)에서 조회 → 새 랩 추가/난이도 상향 시 자동 반영.
- 현재 전부 beginner이므로 당장은 `score = 완료 수 × 10`과 동치. 중급/고급 랩 추가 시
  자연히 차등화된다(스키마/코드 변경 불필요).
- 동점 처리: `last_completed_at`(유저의 가장 최근 완료 시각) 빠른 순. 먼저 도달한 학습자 우선.
- 알 수 없는 difficulty 값(콘텐츠 오타 등)은 가중치 0이 아니라 beginner(10)로 폴백하고 로그 경고.

## 5. 데이터 / 영속

신규 마이그레이션 `apps/api/internal/store/migrations/0002_leaderboard.sql`:

```sql
-- 리더보드 노출 옵트아웃 — 기본 노출(false). 학습자가 명시적으로 숨길 수 있다.
ALTER TABLE users ADD COLUMN IF NOT EXISTS leaderboard_hidden boolean NOT NULL DEFAULT false;
```

신규 store 쿼리(읽기 전용):
- `LeaderboardRows(ctx, since *time.Time) ([]LeaderboardRow, error)`
  - 옵트아웃(`leaderboard_hidden = true`) 제외.
  - `since != nil`이면 `completed_at >= since` 필터(최근 7일 급상승용).
  - 반환 행: `{ UserID, Name, LabID, CompletedAt }`. 집계·가중은 Go에서 수행
    (난이도가 DB가 아닌 콘텐츠에 있으므로).
- 개인 현황은 기존 `ListCompletionsByUser` 재사용 + 콘텐츠 조인으로 난이도 분포 계산.

집계 전략: 소규모(클래스 규모)라 요청 시 계산으로 충분. 추후 부하 증가 시 짧은 TTL
인메모리 캐시 여지만 코드 주석으로 남긴다(v1엔 미구현).

## 6. API

기존 `/api/v1` JWT 그룹 패턴을 따른다(로그인 학습자만 접근).

### `GET /api/v1/leaderboard`
응답:
```json
{
  "hall_of_fame": [
    { "rank": 1, "name": "홍길동", "score": 120, "labs_completed": 12 }
  ],
  "recent_risers": [
    { "rank": 1, "name": "김철수", "score": 50, "labs_completed": 5 }
  ],
  "me": { "rank": 17, "score": 30, "labs_completed": 3 }
}
```
- `hall_of_fame`: 누적 점수 상위 N명(기본 N=10, 상수).
- `recent_risers`: 최근 7일(`completed_at` 윈도우) 내 획득 점수 기준 상위 N명.
- `me`: 본인 순위/점수. **Top N 밖이어도 항상 포함**(하위권 좌절 완화). 본인이 옵트아웃
  상태면 `me`는 본인에게만 보이며 명예의 전당 목록에선 제외.

### `GET /api/v1/me/progress`
개인 학습 현황:
```json
{
  "score": 30, "rank": 17, "labs_completed": 3,
  "by_difficulty": { "beginner": 3, "intermediate": 0, "advanced": 0 },
  "recent_completions": [ { "lab_id": "lab-docker-basics", "completed_at": "..." } ]
}
```

### `PATCH /api/v1/me/preferences`
```json
{ "leaderboard_hidden": true }
```
옵트아웃 토글. `users.leaderboard_hidden` 갱신(신규 store 함수 `SetLeaderboardHidden`).

### `GET /api/v1/me` 갱신
- `points` STUB(0) → 실제 score로 교체.
- `badges`는 `[]` 유지(v2).

### 보안
- 타인 정보는 **이름만** 노출. email·user_id·org는 응답에 포함하지 않는다.
- 옵트아웃 제외는 **서버측**에서 수행(클라이언트 필터 금지).
- 모든 엔드포인트 JWT 필수(기존 미들웨어). RBAC상 모든 학습자 읽기 가능.

## 7. 프론트엔드 (apps/web, App Router)

- `/leaderboard` 페이지 신설(네비 링크는 이미 존재):
  1. 명예의 전당 테이블(rank, name, score, labs_completed) — 본인 행 하이라이트.
  2. "최근 7일 급상승" 패널.
  3. 내 학습 현황 카드(내 순위·점수·완료 수·난이도 분포·최근 완료).
  4. "리더보드에 내 이름 표시" 토글(옵트아웃).
- `lib/api.ts`에 추가: `api.leaderboard.get()`, `api.me.progress()`, `api.me.setPreferences()`.
- 기존 컴포넌트/타입 패턴(`lib/types.ts`, react-query)을 따른다.

## 8. 테스트 / 검증

- Go:
  - 가중치 계산 단위테스트(beginner/intermediate/advanced, unknown 폴백).
  - 핸들러 테스트: Top N 정렬, 동점 tie-break, 옵트아웃 제외, 7일 윈도우,
    본인 순위 항상 포함, 타인 email/user_id 미노출.
  - store 쿼리 테스트(`LeaderboardRows` since 필터, 옵트아웃 제외).
- Web: `leaderboard` lib/컴포넌트 테스트(렌더, 본인 하이라이트, 토글).
- CI 게이트(머지 후 안 깨지게 플랜 검증에 포함):
  `go test ./... -race`, `golangci-lint`(revive var-naming, 예: IDP), `gofmt`,
  `eslint`, `pre-commit`.

## 9. SLO / 비용 / DR

- 읽기 전용 집계라 Lab 시작/Validation/VM 부팅/WebSocket SLO에 영향 없음.
- 추가 클라우드 리소스 없음 → 월 비용 +$0.
- 데이터는 기존 온프렘 Postgres(Longhorn replica 3) + Velero DR(RPO 1h/RTO 4h) 경로 그대로.
- (참고, 비스코프) Postgres는 자체 복제 없이 단일 Pod + 스토리지 복제 의존. 리더보드를
  고신뢰 공개 기능으로 키울 경우 Postgres HA(예: CloudNativePG)는 별도 과제로 검토.

## 10. 단계적 구현 순서(권장)

1. 마이그레이션 `0002_leaderboard.sql` + store 함수(`LeaderboardRows`, `SetLeaderboardHidden`).
2. 점수 계산 로직(가중치 맵 + 콘텐츠 조인 집계) + 단위테스트.
3. API 핸들러 3종(`/leaderboard`, `/me/progress`, `/me/preferences`) + `/me` score 연동 + 테스트.
4. Web `/leaderboard` 페이지 + `lib/api.ts` + 컴포넌트/테스트.
5. CI 게이트 일괄 검증 후 PR.
