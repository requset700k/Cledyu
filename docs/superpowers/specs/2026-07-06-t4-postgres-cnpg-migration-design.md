# T4 — cledyu Postgres → CNPG 이관 설계

- 작성일: 2026-07-06
- 담당: 김찬영
- 상태: 설계 승인, 구현 계획 착수 전
- 상위 문서: `docs/superpowers/specs/2026-07-01-aws-dr-backup-design.md` (DR/백업 전체 설계)
- 상위 플랜: `docs/superpowers/plans/2026-07-01-dr-backup-plan-a-backup-layer.md` Task 4
- 브랜치: `feat/dr-backup-jobs` (Plan A T4~T8 묶음)

## 배경

Plan A는 T1~T3(S3 버킷·자격증명·CNPG 오퍼레이터)까지 "백업을 받을 그릇"만 구축된
상태다(PR #244 등으로 커밋 완료). 실제로 데이터를 주기적으로 S3에 밀어넣는 **백업잡은
아직 하나도 없다** — 즉 지금 온프렘이 상실되면 복원할 백업이 S3에 없다.

T4는 그 첫 백업잡이자 가장 위험한 태스크다. 과금 기능의 원본 데이터(수료증·진도·리더보드)를
담은 cledyu Postgres를 CNPG Cluster로 이관하고, barman으로 S3에 WAL 연속 아카이빙 +
일 base backup을 확보해 **RPO 5~15분**을 달성한다.

**DR 성공 기준과의 연결**: DR 성공 기준은 "과금 기능이 재해 후에도 정상 동작하는가"다.
그 데이터가 여기 있으므로, T4가 완료되어야 DR이 실질적 의미를 갖는다.

## 현재 상태 (실측)

- 구 DB: `gitops/apps/postgres` — 단일 StatefulSet(`replicas: 1`), `postgres:16-alpine`,
  `PGDATA=/var/lib/postgresql/data/pgdata`, ns `postgres`, 서비스 `postgres.postgres.svc:5432`
- 스토리지: Longhorn 10Gi, `persistentVolumeClaimRetentionPolicy: Retain`(삭제·scale 시 PVC 보존)
- 자격증명: Vault `cledyu/db/postgres:password` → ESO → Secret `postgres-credentials`
- 소비자: **api 하나뿐**. Keycloak은 별도 in-cluster Postgres(keycloak ns)라 무관
- api 연결: Vault `cledyu/db/api:dsn`의 **전체 DSN 문자열** → ESO Secret `cledyu-api-db`
  → 환경변수 `CLEDYU_DB_DSN`. 호스트가 DSN 안에 박혀 있어 cutover = DSN 호스트만 교체
- 배포된 CNPG 오퍼레이터: cnpg helm chart **0.23.0 = 오퍼레이터 1.25.0**

## 목표 / 비목표

### 목표
- cledyu Postgres를 CNPG Cluster로 **무손실 이관**(임포트~cutover 간 쓰기 유실 0)
- S3(`postgres/` 프리픽스)에 WAL 연속 아카이빙 + 일 base backup, RPO 5~15분
- 실패 시 즉시 복귀 가능한 롤백 경로 확보

### 비목표 (YAGNI)
- HA(다중 인스턴스) — DR 목적은 백업이지 HA가 아니며 현 규모·예산에서 과잉. `instances: 1` 유지
- barman-cloud 플러그인 도입 — 배포된 1.25.0에서 in-tree barman이 정상 동작. 향후 과제
- 무중단 이관 — 짧은 계획 정지(write-freeze)로 충분(폭포수 랩 모델, 세션은 허용 손실)

## 설계

### 1. 이관 메커니즘 — 논리 임포트 + write-freeze

CNPG `bootstrap.initdb.import`(type: microservice)로 구 DB를 신 클러스터로 논리 복제한다.
논리 임포트는 "임포트 시작 시점"의 일회성 스냅샷이므로, 임포트~cutover 사이 구 DB로 들어온
쓰기는 신 DB에 반영되지 않는다. 이 유실 창을 **write-freeze**로 제거한다:

1. 임포트 직전 `api`를 `replicas: 0`으로 정지 → 구 DB로의 쓰기를 물리적으로 차단
2. 임포트 실행 → row count 검증(G1)
3. DSN cutover → api 재기동

> 플랜 초안에는 이 freeze 단계가 명시되지 않았다. 무손실 보장을 위해 **명시적으로 추가**한다.
> 다운타임은 짧은 계획 정지(임포트+검증 시간)로 허용한다.

### 2. 토폴로지 — 공존 후 DSN 스왑

신 클러스터 `cledyu-pg`(ns `postgres`, `instances: 1`)를 **구 StatefulSet과 공존**시켜
생성한다 → CNPG가 read-write 서비스 `cledyu-pg-rw.postgres.svc:5432` 제공.

cutover는 Vault `cledyu/db/api:dsn`의 호스트를 `postgres.postgres.svc` →
`cledyu-pg-rw.postgres.svc`로 교체하고, ESO force-sync 후 api를 롤아웃한다.
사용자/비밀번호는 기존 값을 유지한다.

**공존시키는 이유**: 검증과 롤백을 위해 두 DB가 동시에 살아있어야 한다.
**롤백 = DSN을 원복하고 api 롤아웃**하면 즉시 구 DB로 복귀한다.

### 3. 백업 설정 — in-tree barman (1.25.0 기준)

`spec.backup.barmanObjectStore`로 S3 백업을 구성한다:

- destinationPath: `s3://cledyu-lab-dr-backups/postgres`
- endpointURL: `https://s3.ap-northeast-2.amazonaws.com`
- s3Credentials: Task 2의 Secret `cledyu-backup-s3`(ACCESS_KEY_ID / ACCESS_SECRET_KEY)
- WAL 압축: gzip, retentionPolicy: `30d`
- ScheduledBackup `cledyu-pg-daily`: 매일 02:00 base backup(WAL은 연속 아카이빙)

**버전 조건**: in-tree `barmanObjectStore`는 CNPG 1.26부터 deprecated이며(barman-cloud
플러그인이 후속 경로), 배포된 **1.25.0에서는 기본·정상 동작**한다. 따라서 오퍼레이터를
**chart 0.23.0(=1.25.0)에 핀 고정**하고, 매니페스트 주석으로 "오퍼레이터 ≥1.26 상향 시
barman-cloud 플러그인으로 이관 필요"를 명시한다.

**retentionPolicy는 설정하지 않는다(코드 리뷰로 발견, 수정 반영됨)**: `backup-writer-postgres`
IAM 정책은 `s3:DeleteObject`를 의도적으로 제외하고(무-delete 정책), 버킷 전체에 Object Lock
GOVERNANCE 30일이 걸려 있으며 writer엔 `BypassGovernanceRetention`도 없다. 이 상태에서
CNPG `retentionPolicy`를 켜면 barman-cloud-backup-delete가 만료 backup/WAL을 직접 지우려
시도해 매번 AccessDenied로 실패한다. retention 관리는 `infra/terraform/aws/backup.tf`의
S3 lifecycle에만 맡긴다 — `postgres/` 프리픽스에 current object 만료 35일(PITR 창 30d +
Object Lock 해제일 경합 방지 여유 5일) 규칙을 추가했다.

### 4. 롤백 & 폐기 — 유예기간

cutover 성공 후 구 StatefulSet을 **즉시 삭제하지 않고 `replicas: 0`으로만 정지**한다.
유예기간 동안 다음을 검증한다:

- 첫 S3 base backup + WAL 실물 도달
- api 안정 운영
- (권장) PITR 복원 드릴(T7)로 백업 복원 가능성 실증

세 항목 통과 후 `gitops/apps/postgres` + `gitops/argocd/apps/data-postgres.yaml`를
`git rm` → ArgoCD prune으로 제거한다. PVC는 `Retain` 정책이라 prune 후에도 잔존한다
(수동 정리 별도).

### 5. 검증 게이트 (cutover/폐기 차단 조건)

- **G1 (cutover 전 필수)**: 임포트 후 구/신 DB의 핵심 테이블 row count 동일.
  대상 예: `session_progress` 및 수료증 관련 테이블. 불일치 시 cutover 중단·임포트 재점검.
- **G2 (cutover 후)**: api `/health` 200 + 세션 진도 조회 정상.
- **G3 (폐기 전 필수)**: `aws s3 ls s3://cledyu-lab-dr-backups/postgres/ --recursive`로
  base backup + WAL 객체 실존 확인. 미확인 시 구 DB 폐기 금지.

### 6. 정비 창 — root-apps selfHeal 대응

App-of-Apps 루트 `root-apps`(`ansible/roles/argocd/templates/root-app.yaml.j2`, Ansible 부트스트랩)는
`gitops/argocd/apps/` 아래 모든 Application spec을 `selfHeal: true`로 git과 강제 일치시킨다(재조정 ~3분).
그 결과 이관 중 필요한 런타임 sync-policy 조작이 되돌려진다:

- `service-api`를 `sync-policy none`으로 만들어도 root-apps가 automated로 원복 → api 재기동 → write-freeze 붕괴
- cnpg 앱을 런타임에 automated로 바꿔도 git(manual)로 원복

**해법 = 정비 창(maintenance window)**: freeze 직전 `root-apps`를 정지(`argocd app set root-apps
--sync-policy none`)한다. root-apps는 상시 재조정 주체가 없어(Ansible이 한 번 심음) 이 정지가 유지되며,
그 아래 자식 토글이 원복되지 않는다. cutover까지 이 창 안에서 진행하고, 끝에 root-apps를 복원한다.

- cnpg의 automated 전환은 **런타임이 아니라 git**으로 한다(앱 파일에 automated 블록 추가·커밋 → 복원 시 반영).
- **구 DB는 유예기간에 정지하지 않고 살려둔다.** 정지하면 root-apps 복원 시 git이 여전히 구 DB를 automated로
  선언해 다시 기동돼 충돌한다. 롤백 안전망은 "구 DB를 계속 켜둔 채 DSN만 되돌릴 수 있음"으로 확보하고,
  폐기는 유예기간 후 git-rm(→ root-apps prune)으로만 한다.
- 트레이드오프: 정비 창 동안 다른 앱들도 재조정이 멈춘다(수십 분의 통제된 유지보수 시간, DR 이관에선 수용 가능).

## 미해결 / 착수 시 실측 확인

- **api-db ExternalSecret 실태 (2026-07-06 확인)**: `cledyu-api-db` ExternalSecret은
  `infra/kubernetes/external-secrets/cledyu-api-db-externalsecret.yaml`에 존재하며, api Helm 차트가 아니라
  out-of-band로 적용된다(Vault `cledyu/db/api:dsn` → Secret `cledyu-api-db.dsn` → env `CLEDYU_DB_DSN`).
  → Step 9의 `annotate externalsecret cledyu-api-db`는 유효(새 파일 불필요). api는 DSN 미설정 시 in-memory
  폴백(`apps/api/internal/config/config.go:215`, `apps/api/cmd/server/main.go:193`)이라, Vault에 dsn이 실제
  설정돼 있는지가 '라이브 데이터 유무'를 결정한다. 어느 쪽이든 write-freeze 전략은 안전하다. 앱/Go 코드 수정 없음.
- **G1 대상 테이블 확정**: 실제 스키마를 보고 검증할 핵심 테이블 목록을 확정한다.

## 산출물 (예정)

- `gitops/apps/postgres-cnpg/Chart.yaml`, `values.yaml`, `templates/cluster.yaml`,
  `templates/scheduledbackup.yaml`(+ `postgres-credentials-cnpg` ExternalSecret)
- `gitops/argocd/apps/data-postgres-cnpg.yaml`
- (폐기 단계) `gitops/apps/postgres`, `gitops/argocd/apps/data-postgres.yaml` 제거
