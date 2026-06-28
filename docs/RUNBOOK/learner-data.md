# RUNBOOK: 학습자 데이터 영속화 (PostgreSQL)

유저 미러·실습 진행 상태·랩 완료 이력을 보관하는 앱 DB 운영 절차.
신원의 원본은 Keycloak(`cledyu-learn`)이고, 이 DB 는 앱 데이터(진행/이력)의 원천이다.

## 1. 구조 한눈에

```
api (Session API)
 ├─ in-memory stepStore = 캐시
 │    ├─ 변경 시 write-through ──►  PostgreSQL (ns: postgres, data-postgres 앱)
 │    └─ 캐시 미스 시 load-on-miss ◄─   ├─ users               (로그인 시 upsert)
 │                                      ├─ session_progress/steps (진행 상태)
 └─ 랩 완료 시 RecordCompletion ───►    └─ lab_completions     (수료증·배지 원천)
```

- DSN 미설정/연결 실패 시 api 는 **in-memory 전용**으로 동작한다(시작 로그 경고).
  기능은 유지되지만 재시작 시 진행 상태가 사라진다 — 종전(영속화 이전)과 동일.
- 스키마 마이그레이션은 api 시작 시 자동 적용된다(`internal/store/migrations/`,
  `schema_migrations` 로 이력 관리).

## 2. 최초 활성화 절차

> 선행 1회: `cledyu-eso-reader` 정책에 `cledyu/data/db/*`·`cledyu/metadata/db/*` read 가
> 있어야 ESO 가 아래 KV 를 읽는다(`infra/vault/policies/cledyu-eso-reader.hcl`). 없으면 ESO 가
> `403 permission denied` 로 동기화 실패하고 Secret 이 생성되지 않는다 — 정책 갱신 후
> `vault policy write cledyu-eso-reader <파일>` 적용.

```bash
# 1) DB 비밀번호 생성 → Vault (postgres StatefulSet 이 ESO 로 주입받음)
vault kv put cledyu/db/postgres password=$(openssl rand -hex 24)

# 2) api 접속 DSN 등록 (비밀번호 포함 — 값 전체가 시크릿)
PW=$(vault kv get -field=password cledyu/db/postgres)
vault kv put cledyu/db/api dsn="postgres://cledyu:${PW}@postgres.postgres.svc:5432/cledyu"

# 3) ESO 동기화 확인
kubectl -n postgres get externalsecret postgres-credentials
kubectl -n api get externalsecret cledyu-api-db

# 4) api 재시작(또는 롤아웃 대기) 후 로그 확인
kubectl -n api logs deploy/api | grep "db 연결"
#   "db 연결 — 유저/진행 상태 영속화 활성" 이 보여야 한다
```

`infra/kubernetes/external-secrets/cledyu-api-db-externalsecret.yaml` 은 다른
ESO 매니페스트와 같은 방식으로 적용한다(kubectl apply).

## 3. 스키마 개요

| 테이블 | 내용 | 쓰는 시점 |
|---|---|---|
| `users` | Keycloak 신원 미러(id=sub, email, name, role, last_login_at) | 로그인 콜백·silent refresh |
| `session_progress` | 세션 헤더(lab/user/current_step) | 세션 생성·스텝 변경 |
| `session_steps` | 스텝별 status/attempts/hint_level/checks(JSONB) | 검증·힌트 사용 |
| `lab_completions` | (user, lab) 최초 완료 + session id | 마지막 스텝 통과 시 |

## 4. 운영 작업

- **백업**: PVC(Longhorn) 스냅샷 + Velero 일정에 `postgres` 네임스페이스 포함(RPO 1h).
  복구 시 StatefulSet 보다 PVC 를 먼저 복원한다.
- **비밀번호 회전**: §2 의 1→2 재실행 후 postgres 파드 재시작 → api 롤아웃.
  (postgres 이미지는 기존 데이터의 비밀번호를 바꾸지 않으므로 `ALTER USER cledyu WITH
  PASSWORD '<새 값>'` 을 먼저 실행한 뒤 Vault 를 갱신하는 순서가 무중단이다)
- **수동 조회**:
  ```bash
  kubectl -n postgres exec -it postgres-0 -- psql -U cledyu -d cledyu \
    -c "SELECT lab_id, count(*) FROM lab_completions GROUP BY lab_id;"
  ```

## 5. 트러블슈팅

| 증상 | 원인 / 조치 |
|---|---|
| api 로그 `db 미설정(CLEDYU_DB_DSN)` | Secret cledyu-api-db 미생성 — §2 의 Vault 등록·ESO 확인 |
| api 로그 `db 연결 실패` | postgres 파드/Service 상태, DSN 의 비밀번호 불일치 확인 |
| 재시작 후 진행 상태 소실 | 영속화 비활성 상태였는지 위 두 항목 확인. DB 활성 상태였다면 `session_progress` 에 행이 있는지 조회 |
| `진행 상태 DB 저장 실패` 경고 반복 | DB 다운/디스크 풀. 캐시로 동작은 계속되지만 재시작 시 유실 — postgres 우선 복구 |

## 5.1 Redis 공유 카운터 (세션 락 + AI rate limit)

Redis(ns: `redis`, data-redis 앱)는 두 가지 공유 카운터를 제공해 다중 레플리카에서도
정확히 동작하게 한다. **카운터/락 전용**이라 영속 디스크가 없고(유실 허용, TTL 자가정리),
재시작 시 락은 TTL 만료로 풀리고 rate limit 카운터는 리셋된다.

| 용도 | 사용처 | env | DB |
|---|---|---|---|
| 세션 생성 분산 락(유저당 1세션) | api | `CLEDYU_REDIS_ADDR=redis.redis.svc:6379` | 0 |
| AI 힌트 rate limit(분당 6/세션 15) | ai-tutor | `AI_TUTOR_REDIS_URL=redis://redis.redis.svc:6379/1` | 1 |

- 두 서비스 모두 **Redis 미연결 시 in-memory 폴백**한다(api 시작 로그 `redis 미연결`,
  ai-tutor `rate_limiter backend`). 단일 레플리카면 기능은 동일하다.
- api 락 경합/Redis 오류는 `409 {code: session_locked}` — 프론트는 잠시 후 재시도(fail-closed,
  단일 세션 불변식 보호). ai-tutor 는 Redis 장애 시 rate limit 을 **fail-open**(허용) — 가용성
  우선(가격 보호는 일시 약화).
- 비밀번호는 두지 않았다(클러스터 내부 전용). redis 접근 제한 NetworkPolicy 는 후속.

## 6. 제약 / 후속

- 단일 인스턴스(HA 없음) — 내구성은 Longhorn replica + Velero 에 의존. 규모 확장 시
  CloudNativePG 오퍼레이터 이관 검토
- api 다중 레플리카: 세션 생성 락은 Redis 로 해결됐으나 **stepStore 캐시가 인스턴스
  로컬**이고 validation consumer 가 한 인스턴스에만 있어 여전히 미지원. 레플리카 확장 전에
  캐시 무효화(pub/sub) 또는 DB-직결 읽기로 전환 필요
- redis 접근 제한 NetworkPolicy(api·ai-tutor ns 만 허용) 후속
- Gamification(포인트/배지)·수료증은 lab_completions 를 원천으로 후속 구현
