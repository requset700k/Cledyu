# Plan A-2 — Keycloak DB → CNPG 이관 설계

- 작성일: 2026-07-09
- 담당: 김찬영
- 상태: 설계 승인, 구현 계획 착수 전
- 상위 문서: `docs/superpowers/specs/2026-07-01-aws-dr-backup-design.md` (DR/백업 전체 설계)
- 관련 플랜: `docs/superpowers/plans/2026-07-01-dr-backup-plan-a-backup-layer.md` (Plan A 본체, Keycloak DB는 §Self-Review에서 "본 플랜 범위 밖(Plan A-2)"로 분리)
- 선행 참조: `docs/superpowers/specs/2026-07-06-t4-postgres-cnpg-migration-design.md` (cledyu Postgres 이관 — 본 설계가 그대로 재사용하는 패턴)
- 브랜치: `feat/dr-keycloak-cnpg`

## 배경

Plan A 본체(Task 1~8)는 origin/main에 병합 완료됐다: cledyu Postgres는 CNPG(`cledyu-pg`)로 이관돼 S3 WAL·PITR을 확보했고, Vault raft 스냅샷·Velero 오브젝트 백업도 가동 중이다. 백업 계층에서 **유일하게 남은 대상이 Keycloak DB**다.

Keycloak DB는 학습자 **신원의 원본**이다 — 앱 `users` 테이블은 Keycloak `sub`의 미러일 뿐이라, Keycloak DB가 유실되면 학습자가 로그인 자체를 못 한다(소셜 로그인 federated identity 포함). Plan A 본체가 GitOps in-cluster 백업을 다룬 반면 Keycloak DB는 **Ansible 소유의 별도 스택**이라 라이프사이클이 갈려 Plan A-2로 분리했다.

**DR 성공 기준과의 연결**: DR 성공 기준은 "과금 기능(수료증·진도)이 재해 후 정상 동작하는가"다. 그 기능은 로그인을 전제하므로, Keycloak DB 복원 가능성이 확보돼야 DR이 실제로 성립한다.

## 현재 상태 (실측, origin/main 기준)

- Keycloak DB: **Bitnami PostgreSQL** Helm 차트(`oci://registry-1.docker.io/bitnamicharts/postgresql`, 차트 `18.6.1` → **PostgreSQL 17.x** 번들), `architecture: standalone`(단일 인스턴스)
- 배포 주체: **Ansible** `postgres_single` role (`ansible/roles/postgres_single/`), 플레이북 `ansible/playbooks/70-keycloak-foundation.yml`. **GitOps/ArgoCD 관리 아님**
- 위치: ns `keycloak`, DB명 `keycloak`, owner `keycloak`, 서비스 `keycloak-db-postgresql.keycloak.svc:5432`, Longhorn 20Gi
- 자격증명: Secret `keycloak-db-credentials`(키 `password`) — `postgres_single` role이 Ansible Vault 변수에서 생성
- 소비자: **Keycloak 서버 하나뿐**. Keycloak CR(`ansible/roles/keycloak_foundation/templates/keycloak.yaml.j2`)의 `spec.db.host`가 `keycloak_foundation_db_service_name`(기본값 `keycloak-db-postgresql`)을 가리킴. Keycloak Operator v26.6.1가 CR을 reconcile
- Vault: 정책 `infra/vault/policies/cledyu-keycloak-db.hcl`이 `cledyu/keycloak/postgres` 경로 read 권한을 이미 부여(자격증명 배선용). **이 경로를 CNPG bootstrap 자격증명 소스로 재사용한다**
- 배포된 CNPG 오퍼레이터: cnpg helm chart **0.23.0 = 오퍼레이터 1.25.0** (Plan A Task 3, in-tree barman 정상 동작)

## 목표 / 비목표

### 목표
- Keycloak DB를 CNPG Cluster `keycloak-pg`로 **무손실 이관**(임포트~cutover 간 쓰기 유실 0)
- S3(`keycloak/` 프리픽스)에 WAL 연속 아카이빙 + 일 base backup, **RPO 5~15분**(cledyu Postgres와 동일 수준 — 사용자 결정)
- Plan C DR 복원에서 keycloak-pg를 cledyu-pg와 **동일한 CNPG `bootstrap.recovery`**로 복구 → 복원 경로 통일
- 실패 시 즉시 복귀 가능한 롤백 경로 확보

### 비목표 (YAGNI)
- HA(다중 인스턴스) — DR 목적은 백업이지 HA가 아님. `instances: 1` 유지(Bitnami도 standalone이었음). Ansible `postgres_ha` role은 본 설계 범위 밖
- Keycloak realm 설정 백업 — `infra/terraform/keycloak`로 재생성(상위 설계 §68). 대상은 학습자 계정이 쌓이는 **DB뿐**
- barman-cloud 플러그인 — 1.25.0 in-tree barman으로 충분(cledyu-pg와 동일 조건)
- 무중단 이관 — 짧은 계획 정지(write-freeze)로 충분

## 설계

### 1. 이관 메커니즘 — 논리 임포트 + write-freeze

cledyu-pg(Task 4 §1)와 동일하게 CNPG `bootstrap.initdb.import`(type: microservice)로 구 Bitnami DB를 신 클러스터로 논리 복제한다. 논리 임포트는 "임포트 시작 시점"의 일회성 스냅샷이라, 임포트~cutover 사이 구 DB로 들어온 쓰기(신규 가입·비번 변경·소셜 계정 연동)는 신 DB에 반영되지 않는다. 이 유실 창을 **write-freeze**로 제거한다:

1. 임포트 직전 **Keycloak CR `spec.instances: 0`** 으로 정지 → 구 DB로의 쓰기를 물리적으로 차단
2. 임포트 실행 → row count 검증(G1)
3. `db.host` cutover → Keycloak `instances: 1` 재기동

다운타임 동안 실제 영향: 신규 **로그인·토큰 갱신 불가**. 단 이미 발급된 access 토큰(JWT)은 만료 전까지 유효하므로 진행 중 세션이 즉시 끊기진 않는다. Keycloak DB는 신원 데이터라 용량이 작아 임포트는 수 분이면 끝난다 — 트래픽 적은 시간대에 잡는 짧은 계획 정지.

### 2. 토폴로지 — 공존 후 db.host 스왑

신 클러스터 `keycloak-pg`(ns `keycloak`, `instances: 1`, **PG 17 이미지**로 Bitnami parity)를 구 Bitnami와 **공존**시켜 생성한다 → CNPG가 read-write 서비스 `keycloak-pg-rw.keycloak.svc:5432` 제공.

cutover는 Keycloak CR의 `spec.db.host`를 `keycloak-db-postgresql` → `keycloak-pg-rw`로 교체한다. cledyu-pg는 Vault DSN 문자열 한 줄 교체였지만, Keycloak은 **CR의 host 필드** 교체다.

**cutover 실행 방식(사용자 결정 = live patch 후 커밋)**:
1. 정비 창 중 **live `kubectl patch`** 로 Keycloak CR `spec.db.host`를 `keycloak-pg-rw`로 즉시 변경(Operator가 Keycloak 파드를 신 DB로 재기동)
2. 로그인 검증 통과 후, **Ansible role 기본값** `keycloak_foundation_db_service_name`을 `keycloak-pg-rw`로 바꿔 **커밋**해 정합화(다음 플레이북 실행이 live 상태와 일치)

**공존시키는 이유**: 검증·롤백을 위해 두 DB가 동시에 살아있어야 한다. **롤백 = `db.host`를 `keycloak-db-postgresql`로 원복 + Keycloak 재기동**하면 즉시 구 DB로 복귀(구 DB는 유예기간까지 살려둔다).

### 3. 자격증명 — 비밀번호 parity가 핵심 (G0)

cutover 후 Keycloak은 **기존 `keycloak-db-credentials` Secret**(user `keycloak`, password X)로 신 DB에 접속한다. 따라서 CNPG `keycloak-pg`의 owner `keycloak` 비밀번호가 **X와 동일해야** cutover가 성립한다.

CNPG bootstrap은 `username`/`password` 키의 basic-auth Secret을 요구한다. 이를 ESO로 Vault `cledyu/keycloak/postgres`(정책 이미 존재)에서 매핑해 Secret `keycloak-pg-credentials`(type `kubernetes.io/basic-auth`)를 만든다. CNPG는 이 Secret의 password로 owner를 생성한다.

**G0 (임포트 전 필수)**: Vault `cledyu/keycloak/postgres`의 `username=keycloak` / `password`가 **현재 라이브 Bitnami의 값과 일치**해야 한다. 착수 시 라이브 Secret `keycloak-db-credentials`의 password와 Vault 경로 값을 대조하고, 불일치면 Vault를 라이브 기준으로 맞춘 뒤 임포트한다(불일치 상태로 진행하면 cutover 후 Keycloak이 인증 실패).

### 4. 백업 설정 — in-tree barman (1.25.0), retentionPolicy 미설정

cledyu-pg와 동일한 `spec.backup.barmanObjectStore`:

- destinationPath: `s3://cledyu-lab-dr-backups/keycloak`
- endpointURL: `https://s3.ap-northeast-2.amazonaws.com`
- s3Credentials: Secret `cledyu-backup-s3`(keycloak ns, §5에서 ESO로 생성; 키 `ACCESS_KEY_ID`/`ACCESS_SECRET_KEY`)
- WAL 압축: gzip
- ScheduledBackup `keycloak-pg-daily`: 매일 base backup(WAL 연속 아카이빙), `immediate: true`로 sync 즉시 첫 백업

**retentionPolicy는 설정하지 않는다**(Task 4 코드리뷰 교훈 그대로): `backup-writer-keycloak` IAM은 `s3:DeleteObject`가 없고(무-delete 정책), 버킷에 Object Lock GOVERNANCE 30일 + writer에 `BypassGovernanceRetention` 없음. 이 상태에서 CNPG `retentionPolicy`를 켜면 barman이 만료분을 직접 지우려다 매번 AccessDenied. retention은 **S3 lifecycle의 `keycloak/` 규칙에만** 맡긴다(§5).

**버전 조건**: in-tree barman은 1.26부터 deprecated이나 배포된 1.25.0에서 정상. 오퍼레이터는 chart 0.23.0(=1.25.0)에 이미 핀 고정돼 있음. 매니페스트 주석에 "≥1.26 상향 시 barman-cloud 플러그인 이관"을 명시(cledyu-pg와 동일).

### 5. S3 / IAM / 자격증명 (Velero 프리픽스 추가와 동형)

`infra/terraform/aws/backup.tf`:
- `local.backup_writers`에 `"keycloak"` 추가 → `for_each`로 `cledyu-lab-backup-writer-keycloak`(`keycloak/*` 한정 정책, DeleteObject 없음) 자동 생성
- lifecycle에 `keycloak/` 규칙 추가 — current 만료 35일(PITR 창 30d + Object Lock 경합 방지 5일) + noncurrent 정리. `postgres/` 규칙과 동일 성격

자격증명 배선:
- 액세스 키 **수동 발급** → Vault `cledyu/aws/backup-keycloak`(kv put)
- `gitops/apps/backup-secrets/values.yaml`에 `{namespace: keycloak, vaultKey: aws/backup-keycloak}` 추가 → 기존 backup-secrets 차트가 keycloak ns에 Secret `cledyu-backup-s3` 생성(코드 변경 없이 값만 추가)

### 6. 정비 창 — root-apps 대응이 (freeze 대상엔) 불필요

Task 4는 freeze 대상 `service-api`가 GitOps 앱이라 root-apps selfHeal이 sync-policy 토글을 되돌려 **정비 창(root-apps 정지)** 이 필수였다. **Keycloak은 다르다** — Keycloak CR·Bitnami는 Ansible 소유로 `gitops/argocd/apps/` 밖이라 root-apps가 reconcile하지 않는다. 따라서:

- Keycloak CR `instances: 0` / `db.host` **live patch는 되돌려지지 않는다**(상시 재조정 주체 없음; Keycloak Operator는 CR을 따를 뿐 CR을 바꾸지 않음). → **root-apps 정지 불필요**
- 신규 CNPG 앱 `data-keycloak-pg`는 GitOps(root-apps 관리) 앱이지만, **automated 블록 없이 커밋**하면 root-apps가 manual 상태를 그대로 유지한다 → 조기 자동 import 방지는 git으로 제어(런타임 토글 아님). cutover 검증 후 automated 블록을 **git 커밋**으로 추가

**대신 새 제약**: 정비 창(임포트~cutover) 동안 **keycloak 플레이북(`70-keycloak-foundation.yml`)을 재실행하지 않는다.** 재실행하면 CR이 git/Ansible 기준값(`instances:1`, 구 `db.host`)으로 재적용돼 freeze·cutover가 깨진다. cutover 직후 Ansible role 기본값 커밋(§2)으로 이 위험을 제거한다.

### 7. 롤백 & 폐기 — 유예기간

cutover 성공 후 구 Bitnami를 **즉시 삭제하지 않고 살려둔다**(롤백 안전망). 유예기간 검증 항목:

- 첫 S3 base backup + WAL 실물 도달(G3)
- Keycloak 안정 운영(로그인·토큰 갱신·소셜 로그인 정상)
- (권장) PITR 복원 드릴로 백업 복원 가능성 실증

세 항목 통과 후 Bitnami 폐기. **Task 4와 폐기 방식이 다르다** — Bitnami는 ArgoCD가 아니라 Ansible 소유라 git-rm/prune이 안 통한다:
1. 플레이북 `70-keycloak-foundation.yml`에서 `postgres_single`(keycloak) role 호출을 제거/가드해 재배포를 멈춘다(커밋)
2. 수동으로 `helm uninstall keycloak-db -n keycloak` + PVC 삭제(Longhorn `Retain`이면 별도 정리)

### 8. DR 복원(Plan C) 통합

Plan C(`docs/superpowers/plans/2026-07-03-dr-backup-plan-c-orchestration.md`)의 Restore 단계(현재 "Postgres PITR/Keycloak"으로 뭉뚱그림)에서, keycloak-pg는 이제 **cledyu-pg와 동일한 CNPG `bootstrap.recovery`**(targetTime=최신, source=`keycloak/` 프리픽스)로 복구된다. DR 시 흐름:

1. Vault 복원·unseal → ESO 정상화(시크릿 주입)
2. CNPG 오퍼레이터 기동(GitOps) → `cledyu-pg`·`keycloak-pg` 둘 다 recovery
3. Velero가 keycloak ns 오브젝트(Keycloak CR·Operator) 복원 → Keycloak CR의 `db.host`(=`keycloak-pg-rw`, cutover 반영값)로 신 DB 접속

**효과**: 두 durable DB의 복원 경로가 완전히 동일해져 Restore Lambda 로직이 단순해진다. **산출**: Plan C 문서 Restore 절에 keycloak-pg recovery를 명시하는 소규모 업데이트(별도 커밋).

### 9. 검증 게이트

- **G0 (임포트 전 필수)**: Vault `cledyu/keycloak/postgres` username/password가 라이브 Bitnami와 일치(§3). 불일치 시 임포트 중단
- **G1 (cutover 전 필수)**: 임포트 후 구/신 DB 핵심 신원 테이블 row count 동일. 대상: `user_entity`, `credential`, `user_role_mapping`, `federated_identity`(소셜 로그인 연동), `user_attribute`. 불일치 시 cutover 중단·임포트 재점검
- **G2 (cutover 후)**: 신 DB 상대로 **실제 로그인 성공**(신규 로그인 + 소셜 로그인) + 토큰 발급 정상
- **G3 (폐기 전 필수)**: `aws s3 ls s3://cledyu-lab-dr-backups/keycloak/ --recursive`로 base backup + WAL 객체 실존 확인. 미확인 시 Bitnami 폐기 금지

## 미해결 / 착수 시 실측 확인

- **G0 비밀번호 대조**: 착수 첫 단계로 라이브 `keycloak-db-credentials.password`와 Vault `cledyu/keycloak/postgres` 값을 실제 대조한다. Vault 경로에 username 키가 없을 수 있음(정책은 read만 확인됨) → 없으면 `vault kv patch`로 보강
- **PG 메이저 실측 후 이미지 핀**: Bitnami 차트 18.6.1의 실제 번들 PG 메이저를 착수 시 **실행 중인 파드에서 실측**한다(`select version()`). CNPG 타깃 이미지는 그 메이저와 **동일하거나 그 이상**으로 잡는다(논리 import는 target ≥ source major여야 안전). `ghcr.io/cloudnative-pg/postgresql:<major>.x@sha256:...` 형태로 태그·digest 핀(cledyu-pg가 digest 핀을 쓴 것과 동일 관례). 본문 §2의 "PG 17"은 잠정값 — 실측으로 확정
- **G1 대상 테이블 확정**: 실제 Keycloak 스키마(v26.6.1)를 보고 검증 테이블 목록을 최종 확정
- **Keycloak CR `instances` 필드 실측**: v2alpha1 Keycloak CR의 정지 방식(`spec.instances: 0`)이 Operator v26.6.1에서 STS scale 0으로 동작하는지 착수 시 확인

## 산출물 (예정)

- `infra/terraform/aws/backup.tf` 수정(`keycloak` writer + `keycloak/` lifecycle)
- `gitops/apps/backup-secrets/values.yaml` 수정(keycloak ns 엔트리)
- `gitops/apps/keycloak-pg/Chart.yaml`, `values.yaml`, `templates/cluster.yaml`, `templates/scheduledbackup.yaml`(+ `keycloak-pg-credentials` ExternalSecret)
- `gitops/argocd/apps/data-keycloak-pg.yaml`
- (cutover) `ansible/roles/keycloak_foundation/defaults/main.yml` `db.host` 기본값 변경
- (폐기 단계) `ansible/playbooks/70-keycloak-foundation.yml`에서 `postgres_single`(keycloak) 제거 + Bitnami 수동 정리
- (통합) Plan C 문서 Restore 절 업데이트
- (드릴) `docs/RUNBOOK/dr-restore-drill.md`에 keycloak-pg PITR 케이스 추가
