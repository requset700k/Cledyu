# DR Plan A-2 — Keycloak DB → CNPG 이관 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ansible 소유 Bitnami Keycloak DB를 CNPG Cluster `keycloak-pg`로 무손실 이관하고, barman으로 S3 `keycloak/` 프리픽스에 WAL 연속 아카이빙 + 일 base backup을 확보해 RPO 5~15분·PITR을 달성한다.

**Architecture:** cledyu-pg(Plan A Task 4)와 동일 패턴 — 신 CNPG 클러스터를 구 Bitnami와 공존 생성 → write-freeze(Keycloak `instances:0`) 하에 논리 import → row 검증 → Keycloak CR `db.host`를 `keycloak-pg-rw`로 cutover → 유예기간 후 Bitnami 폐기. cledyu-pg와의 결정적 차이: freeze/폐기 대상이 GitOps가 아닌 **Ansible 소유**라 root-apps 정비 창은 불필요하나 "정비 창 중 keycloak 플레이북 재실행 금지" 제약이 생기고, cutover는 CR `db.host` live patch 후 Ansible 기본값 커밋으로 정합화한다.

**Tech Stack:** Terraform(AWS S3/IAM/KMS), External Secrets Operator, CloudNativePG 1.25.0(in-tree barman), HashiCorp Vault, Ansible(Keycloak Operator v26.6.1 · Bitnami PostgreSQL), ArgoCD.

## Global Constraints

- 리전: `ap-northeast-2` / 백업 버킷: `cledyu-lab-dr-backups`, 신규 프리픽스 `keycloak/`
- CNPG 오퍼레이터는 chart **0.23.0(=1.25.0)** 핀 고정 — in-tree `barmanObjectStore` 정상 동작(≥1.26 상향 시 barman-cloud 플러그인 이관)
- `retentionPolicy`는 **설정하지 않는다** — writer IAM에 `s3:DeleteObject` 없음 + Object Lock GOVERNANCE 30일. retention은 S3 lifecycle이 전담
- 수량(cpu/memory/size)은 **반드시 `| quote`** — 미quote 시 ArgoCD 영구 OutOfSync
- ArgoCD 앱 등록: `gitops/argocd/apps/<name>.yaml`(Application) + `gitops/apps/<name>/`(내용). repoURL `https://github.com/requset700k/Cledyu.git`, targetRevision `main`
- 커밋 메시지에 `Co-Authored-By` 줄 금지. `git commit -m` 방식(heredoc 금지)
- 정비 창(임포트~cutover) 동안 **keycloak 플레이북(`70-keycloak-foundation.yml`) 재실행 금지**
- 매니페스트 검증: `helm template | kubeconform -strict -ignore-missing-schemas` / `terraform validate` + `terraform fmt`
- 상위 설계: `docs/superpowers/specs/2026-07-09-keycloak-db-cnpg-migration-design.md`

---

### Task 1: S3 `keycloak/` 프리픽스 + 전용 IAM writer (Terraform)

**Files:**
- Modify: `infra/terraform/aws/backup.tf` (`local.backup_writers`에 `keycloak` 추가, lifecycle에 `keycloak/` 규칙 추가)

**Interfaces:**
- Produces: IAM 사용자 `cledyu-lab-backup-writer-keycloak`(`keycloak/*` 한정, DeleteObject 없음) + `keycloak/` 프리픽스 lifecycle(current 35일 만료). Task 3의 barman이 사용.
- Produces: (수동) Vault 경로 `cledyu/aws/backup-keycloak`(access_key_id/secret_access_key). Task 2가 사용.

- [ ] **Step 1: backup_writers에 keycloak 추가**

`infra/terraform/aws/backup.tf`의 `locals` 블록(현재 `toset(["postgres", "vault", "velero"])`)을 수정:
```hcl
locals {
  # 프리픽스명 = IAM 사용자 suffix. 각 writer 는 자기 프리픽스만 read/write(교차 프리픽스 차단).
  backup_writers = toset(["postgres", "vault", "velero", "keycloak"])
}
```

- [ ] **Step 2: keycloak/ lifecycle 규칙 추가**

`aws_s3_bucket_lifecycle_configuration.dr_backups`의 `abort-incomplete-multipart` rule **앞에** 규칙을 추가한다(postgres/ 규칙과 동일 성격 — CNPG PITR 창 30d + Object Lock 경합 방지 5일 = 35일):
```hcl
  rule {
    id     = "expire-keycloak-backups"
    status = "Enabled"
    filter {
      prefix = "keycloak/"
    }
    expiration {
      days = 35
    }
    noncurrent_version_expiration {
      noncurrent_days = 30
    }
  }
```

- [ ] **Step 3: 검증 (validate + fmt)**

Run: `cd infra/terraform/aws && terraform init -backend=false && terraform validate && terraform fmt -check backup.tf`
Expected: `Success! The configuration is valid.` + fmt 통과(출력 없음).

- [ ] **Step 4: apply (AWS 자격증명 단독 — `AWS_PROFILE=cledyu`)**

Run: `cd infra/terraform/aws && terraform init && terraform plan`
Expected: `Plan: 3 to add, 0 to change, 0 to destroy` (keycloak writer user + user_policy + lifecycle 규칙 갱신). **기존 리소스 change/destroy가 0인지 확인 후** `terraform apply`.
확인: `terraform output backup_iam_users` → 맵에 `keycloak = "cledyu-lab-backup-writer-keycloak"` 포함.

- [ ] **Step 5: 액세스 키 수동 발급 + Vault 등록 (수동)**

Run:
```bash
aws iam create-access-key --user-name cledyu-lab-backup-writer-keycloak
vault kv put cledyu/aws/backup-keycloak \
  access_key_id="<AccessKeyId>" \
  secret_access_key="<SecretAccessKey>"
```
Expected: `Success! Data written to: cledyu/aws/backup-keycloak`

- [ ] **Step 6: Commit**

```bash
git add infra/terraform/aws/backup.tf
git commit -m "feat(dr): S3 keycloak/ 프리픽스 백업 writer·수명주기 추가"
```

---

### Task 2: backup-secrets에 keycloak ns 자격증명 추가 (ESO)

**Files:**
- Modify: `gitops/apps/backup-secrets/values.yaml` (keycloak ns 엔트리 추가)

**Interfaces:**
- Consumes: Task 1의 Vault `cledyu/aws/backup-keycloak`
- Produces: keycloak ns에 Secret `cledyu-backup-s3`(키 `ACCESS_KEY_ID`/`ACCESS_SECRET_KEY`). Task 3 barman이 사용.

- [ ] **Step 1: values.yaml에 keycloak 엔트리 추가**

`gitops/apps/backup-secrets/values.yaml`의 `secrets:` 리스트에 추가(기존 postgres/vault 아래):
```yaml
secrets:
  - namespace: postgres
    vaultKey: aws/backup-postgres
  - namespace: vault
    vaultKey: aws/backup-vault
  - namespace: keycloak
    vaultKey: aws/backup-keycloak
```

- [ ] **Step 2: 검증 (렌더 + 스키마)**

Run: `helm template gitops/apps/backup-secrets | kubeconform -strict -ignore-missing-schemas`
Expected: 에러 없음. 3개 ExternalSecret 렌더(`namespace: keycloak`→`aws/backup-keycloak` 포함).

- [ ] **Step 3: Commit + push**

```bash
git add gitops/apps/backup-secrets/values.yaml
git commit -m "feat(dr): keycloak ns S3 백업 자격증명 ESO 추가"
git push
```

- [ ] **Step 4: Sync 후 Secret 생성 확인**

Run:
```bash
argocd app sync data-backup-secrets
kubectl -n keycloak get secret cledyu-backup-s3 -o jsonpath='{.data.ACCESS_KEY_ID}' | base64 -d
```
Expected: IAM access key id 출력(비어있지 않음).

---

### Task 3: keycloak-pg CNPG 매니페스트 작성 (수동 sync, import 대기)

**Files:**
- Create: `gitops/apps/keycloak-pg/Chart.yaml`
- Create: `gitops/apps/keycloak-pg/values.yaml`
- Create: `gitops/apps/keycloak-pg/templates/cluster.yaml`
- Create: `gitops/apps/keycloak-pg/templates/scheduledbackup.yaml`
- Create: `gitops/argocd/apps/data-keycloak-pg.yaml`

**Interfaces:**
- Consumes: Secret `cledyu-backup-s3`(Task 2, keycloak ns), 구 서비스 `keycloak-db-postgresql.keycloak.svc`(import 소스), Vault `cledyu/keycloak/postgres`(username/password)
- Produces: CNPG Cluster `keycloak-pg` → 서비스 `keycloak-pg-rw.keycloak.svc:5432`, S3 프리픽스 `keycloak/`에 WAL+baseBackup. Task 4가 cutover.

- [ ] **Step 1: G0 사전작업 — Vault 비밀번호 parity 확보 (수동)**

CNPG owner `keycloak`의 비밀번호는 라이브 Bitnami와 동일해야 cutover 후 Keycloak이 접속한다. 라이브 Secret과 Vault 값을 대조하고, Vault에 username/password를 라이브 기준으로 맞춘다:
```bash
# 라이브 Bitnami 자격증명(Keycloak이 현재 쓰는 값)
kubectl -n keycloak get secret keycloak-db-credentials -o jsonpath='{.data.username}' | base64 -d; echo
kubectl -n keycloak get secret keycloak-db-credentials -o jsonpath='{.data.password}' | base64 -d; echo
# Vault 현재 값 확인
vault kv get cledyu/keycloak/postgres
# 불일치·부재 시 라이브 값으로 맞춘다(username 키가 없으면 함께 넣는다)
vault kv patch cledyu/keycloak/postgres \
  username="<위 라이브 username=keycloak>" \
  password="<위 라이브 password>"
```
Expected: `vault kv get cledyu/keycloak/postgres`의 username=`keycloak`, password가 라이브 Secret과 동일.

- [ ] **Step 2: Chart.yaml + values.yaml 작성**

`gitops/apps/keycloak-pg/Chart.yaml`:
```yaml
apiVersion: v2
name: keycloak-pg
version: 0.1.0
```

`gitops/apps/keycloak-pg/values.yaml`:
```yaml
# Keycloak DB → CNPG 이관 차트 values.
# 백업 대상 = 학습자 신원 원본(계정·크레덴셜·소셜 연동). RPO 5~15분(WAL 연속 + 일 base backup).
# 소비자는 Keycloak 서버 하나(Ansible 소유 CR의 db.host → keycloak-pg-rw).

# 단일 인스턴스 — DR 목적은 백업이지 HA가 아니다(Bitnami도 standalone이었음).
instances: 1

storage:
  # 구 Bitnami와 동일 규격(Longhorn 20Gi). 데이터 durability는 barman S3가 담당.
  className: longhorn
  size: 20Gi

# QoS 보장(BestEffort 방지). Bitnami primary.resources와 정렬.
resources:
  requests:
    cpu: 250m
    memory: 512Mi
  limits:
    cpu: "1"
    memory: 2Gi
```

- [ ] **Step 3: PG 메이저 실측 + 이미지 digest 확정**

CNPG 타깃 major는 구 Bitnami의 major와 같거나 그 이상이어야 안전하다(논리 import는 target ≥ source). 라이브에서 major를 실측하고, 그 major의 CNPG 이미지 digest를 구한다:
```bash
# 구 Bitnami major 실측
kubectl -n keycloak exec sts/keycloak-db-postgresql -- \
  psql -U keycloak -d keycloak -tAc "show server_version_num"   # 예: 170004 → major 17
# 대상 CNPG 이미지 digest 확정(major는 위 실측값에 맞춤; 예시는 17)
docker buildx imagetools inspect ghcr.io/cloudnative-pg/postgresql:17.4 --format '{{.Manifest.Digest}}'
```
Expected: major 정수(예 17), `sha256:...` digest. 이 digest를 다음 Step의 `imageName`에 박는다.

- [ ] **Step 4: cluster.yaml 작성 (barman S3 + 구 DB import)**

`gitops/apps/keycloak-pg/templates/cluster.yaml` — `<major>`/`<sha256>`는 Step 3 실측값으로 치환:
```yaml
## CNPG Cluster — 구 Bitnami keycloak-db(keycloak ns)를 논리 import(microservice)로 이관하고,
## barman으로 S3(keycloak/ 프리픽스)에 WAL 연속 아카이빙 + base backup을 남긴다.
##
## 버전 주의: 배포 오퍼레이터 = cnpg chart 0.23.0 = 1.25.0. in-tree barmanObjectStore는 1.26부터
## deprecated(barman-cloud 플러그인이 후속)이나 1.25.0에서는 정상. ≥1.26 상향 시 이관 필요.
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: keycloak-pg
  namespace: {{ .Release.Namespace }}
spec:
  instances: {{ .Values.instances }}
  # 구 Bitnami major와 동일(Step 3 실측). 태그+digest 고정(재풀 시 revision 드리프트 방지).
  # 오퍼레이터 상향 등으로 CNPG 호환 Postgres 버전이 바뀌면 이 digest도 갱신.
  imageName: "ghcr.io/cloudnative-pg/postgresql:<major>.4@<sha256>"
  storage:
    size: {{ .Values.storage.size | quote }}
    storageClass: {{ .Values.storage.className }}
  # 수량은 반드시 quote — 미quote 시 ArgoCD 영구 OutOfSync.
  resources:
    requests:
      cpu: {{ .Values.resources.requests.cpu | quote }}
      memory: {{ .Values.resources.requests.memory | quote }}
    limits:
      cpu: {{ .Values.resources.limits.cpu | quote }}
      memory: {{ .Values.resources.limits.memory | quote }}
  bootstrap:
    # 최초 이관: 구 Bitnami(old-keycloak-db)에서 논리 import(microservice). 생성 시 1회만 실행.
    # ⚠ 재생성 시 fail-safe(의도됨): 구 DB 폐기 후 이 매니페스트로 CR을 다시 만들면 import 소스가
    #    없어 bootstrap이 실패한다. 안전장치다(빈 상태 자동 재생성 방지). 실제 DR 복구는 이 운영
    #    매니페스트가 아니라 별도 recovery 매니페스트로 수행(docs/RUNBOOK/dr-restore-drill.md).
    initdb:
      # 계약: owner는 아래 secret(keycloak-pg-credentials)의 username과 일치해야 한다(CNPG 검증).
      database: keycloak
      owner: keycloak
      secret:
        name: keycloak-pg-credentials
      import:
        type: microservice
        databases:
          - keycloak
        source:
          externalCluster: old-keycloak-db
  externalClusters:
    # 구 Bitnami import 소스. 재생성 시 여기 연결 실패로 멈춘다(위 fail-safe).
    - name: old-keycloak-db
      connectionParameters:
        host: keycloak-db-postgresql.keycloak.svc
        user: keycloak
        dbname: keycloak
      password:
        name: keycloak-pg-credentials
        key: password
  backup:
    barmanObjectStore:
      destinationPath: "s3://cledyu-lab-dr-backups/keycloak"
      endpointURL: "https://s3.ap-northeast-2.amazonaws.com"
      # Task 2가 keycloak ns에 뿌린 Secret(프리픽스 keycloak/ 한정 IAM 키).
      s3Credentials:
        accessKeyId:
          name: cledyu-backup-s3
          key: ACCESS_KEY_ID
        secretAccessKey:
          name: cledyu-backup-s3
          key: ACCESS_SECRET_KEY
      wal:
        compression: gzip
    # retentionPolicy 의도적으로 미설정 — writer에 s3:DeleteObject 없음 + Object Lock 30일.
    # 삭제는 전적으로 S3 lifecycle(backup.tf의 keycloak/ 규칙)이 담당.
  monitoring:
    # CNPG 1.25.0에서 정상. 오퍼레이터 상향 시 수동 PodMonitor로 교체 필요.
    enablePodMonitor: true
```

- [ ] **Step 5: scheduledbackup.yaml 작성 (+ credentials ExternalSecret)**

`gitops/apps/keycloak-pg/templates/scheduledbackup.yaml`:
```yaml
## 일 base backup 스케줄(WAL은 Cluster barman이 연속 아카이빙 → RPO 5~15분).
## CNPG cron은 6필드(초 분 시 일 월 요일). "0 0 3 * * *" = 매일 03:00:00(cledyu-pg 02:00과 분리).
apiVersion: postgresql.cnpg.io/v1
kind: ScheduledBackup
metadata:
  name: keycloak-pg-daily
  namespace: {{ .Release.Namespace }}
spec:
  schedule: "0 0 3 * * *"
  # immediate: 생성 즉시 1회 base backup(cutover 직후 완전 복원점 즉시 확보).
  immediate: true
  backupOwnerReference: self
  cluster:
    name: keycloak-pg
---
## CNPG bootstrap이 요구하는 basic-auth(username/password) Secret.
## Vault cledyu/keycloak/postgres에서 두 키를 가져온다(Task 3 Step 1 G0에서 라이브와 일치 확인).
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: keycloak-pg-credentials
  namespace: {{ .Release.Namespace }}
  annotations:
    argocd.argoproj.io/sync-options: SkipDryRunOnMissingResource=true
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: vault-backend
    kind: ClusterSecretStore
  target:
    name: keycloak-pg-credentials
    creationPolicy: Owner
    deletionPolicy: Retain
    template:
      type: kubernetes.io/basic-auth
  data:
    - secretKey: username
      remoteRef:
        key: keycloak/postgres
        property: username
    - secretKey: password
      remoteRef:
        key: keycloak/postgres
        property: password
```

- [ ] **Step 6: ArgoCD Application 작성 (수동 sync — automated 없음)**

`gitops/argocd/apps/data-keycloak-pg.yaml`. **중요**: 이관 동안은 `syncPolicy.automated`를 넣지 않는다(수동 sync). 자동 sync면 커밋 즉시 ArgoCD가 Cluster를 만들어 **아직 안 멈춘 구 DB를 import** → write-freeze 무의미. root-apps는 이 파일의 git 상태(automated 없음)를 그대로 유지하므로 런타임 토글 불필요. Task 4에서 automated 블록을 git으로 추가한다.
```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: data-keycloak-pg
  namespace: argocd
  annotations:
    # 오퍼레이터(data-cnpg-operator, wave 0) 이후 Cluster CR이 서도록 wave 1.
    argocd.argoproj.io/sync-wave: "1"
    # CNPG mutating webhook 기본값을 diff에서 정규화(영구 OutOfSync 방지). ServerSideApply와 짝.
    argocd.argoproj.io/compare-options: ServerSideDiff=true,IncludeMutationWebhook=true
  finalizers:
    - resources-finalizer.argocd.argoproj.io
spec:
  project: default
  source:
    repoURL: https://github.com/requset700k/Cledyu.git
    targetRevision: main
    path: gitops/apps/keycloak-pg
    helm:
      releaseName: keycloak-pg
  destination:
    server: https://kubernetes.default.svc
    namespace: keycloak
  # 이관 중: automated 없음(수동 sync). cutover 검증 후 Task 4에서 git으로 automated 추가.
  syncPolicy:
    syncOptions:
      - CreateNamespace=true
      - ServerSideApply=true
    retry:
      limit: 5
      backoff:
        duration: 10s
        factor: 2
        maxDuration: 3m
```

- [ ] **Step 7: 검증 (렌더 + 스키마)**

Run: `helm template gitops/apps/keycloak-pg --namespace keycloak | kubeconform -strict -ignore-missing-schemas`
Expected: 렌더 성공(Cluster/ScheduledBackup/ExternalSecret). CRD 스키마 없으면 `-ignore-missing-schemas`로 통과. `imageName`에 `<major>`/`<sha256>` 플레이스홀더가 남아있지 않은지 육안 확인.

- [ ] **Step 8: Commit + push**

```bash
git add gitops/apps/keycloak-pg gitops/argocd/apps/data-keycloak-pg.yaml
git commit -m "feat(dr): keycloak-pg CNPG 매니페스트 추가 (수동 sync, import 대기)"
git push
kubectl -n argocd get application data-keycloak-pg
```
Expected: `data-keycloak-pg` Application 존재, 상태 OutOfSync(아직 sync 안 함).

---

### Task 4: 이관 실행 — freeze → import → 검증 → cutover → unfreeze

> 가장 위험한 태스크. Keycloak을 정지(write-freeze)하고 논리 import → row 검증(G1) → 첫 백업 도달(G3) → `db.host` cutover(live patch) → 로그인 검증(G2) → Ansible 기본값·CNPG automated 커밋. Keycloak은 Ansible 소유라 root-apps 정비 창은 불필요하나, **이 태스크 동안 keycloak 플레이북을 재실행하지 않는다**(재실행 시 CR이 기본값으로 되돌아가 freeze/cutover 붕괴).

**Files:**
- Modify: `ansible/roles/keycloak_foundation/defaults/main.yml` (Step 8: `db_service_name` cutover)
- Modify: `gitops/argocd/apps/data-keycloak-pg.yaml` (Step 9: automated 블록 추가)

**Interfaces:**
- Consumes: Task 3의 매니페스트(수동 sync 대기), Keycloak CR `cledyu-keycloak`(ns keycloak)
- Produces: Keycloak이 `keycloak-pg-rw`에 접속, `keycloak-pg` automated sync 전환

- [ ] **Step 1: G0 재확인 (import 직전)**

Run: `vault kv get -field=password cledyu/keycloak/postgres` 와 `kubectl -n keycloak get secret keycloak-db-credentials -o jsonpath='{.data.password}' | base64 -d`
Expected: 두 값 **동일**(Task 3 Step 1에서 맞춤). 다르면 중단하고 Vault 재조정.

- [ ] **Step 2: write-freeze (Keycloak 정지)**

논리 import는 시작 시점의 일회성 스냅샷이라, import~cutover 사이 구 DB 쓰기는 신 DB에 반영되지 않는다. Keycloak을 정지해 구 DB 쓰기를 물리적으로 차단한다(live patch — Ansible 소유라 되돌려지지 않음).
Run:
```bash
kubectl -n keycloak patch keycloak cledyu-keycloak --type merge -p '{"spec":{"instances":0}}'
kubectl -n keycloak rollout status sts/cledyu-keycloak --timeout=120s
# 구 DB 활성 커넥션 없음 확인(자기 세션 제외)
kubectl -n keycloak exec sts/keycloak-db-postgresql -- psql -U keycloak -d keycloak -tAc \
  "select count(*) from pg_stat_activity where datname='keycloak' and state='active' and pid<>pg_backend_pid()"
```
Expected: Keycloak STS replicas 0, 구 DB 활성 커넥션 0.

- [ ] **Step 3: import 실행 (수동 sync)**

Run:
```bash
argocd app sync data-keycloak-pg
kubectl -n keycloak wait --for=condition=Ready cluster/keycloak-pg --timeout=600s
```
Expected: Cluster `keycloak-pg` `Ready`. import Job이 정지된 구 DB를 논리 복제.

- [ ] **Step 4: G1 — 데이터 일치 검증 (cutover 전 필수)**

Run:
```bash
for t in user_entity credential user_role_mapping federated_identity user_attribute; do
  echo -n "$t old="
  kubectl -n keycloak exec sts/keycloak-db-postgresql -- psql -U keycloak -d keycloak -tAc "select count(*) from $t"
  echo -n "$t new="
  kubectl -n keycloak exec keycloak-pg-1 -- psql -U keycloak -d keycloak -tAc "select count(*) from $t"
done
```
Expected: 각 테이블의 old/new row 수 **동일**. 다르면 cutover 중단, import 재점검(구 DB는 정지 상태라 안전).

- [ ] **Step 5: G3 — 첫 backup S3 도달 확인**

ScheduledBackup `immediate: true`라 sync 순간 첫 base backup이 시작된다. 완료만 확인.
Run:
```bash
kubectl -n keycloak get backup -l cnpg.io/cluster=keycloak-pg   # phase=completed 대기(수 분)
aws s3 ls s3://cledyu-lab-dr-backups/keycloak/ --recursive | head
```
Expected: `keycloak/` 하위에 base backup + WAL 객체 존재.
> completed가 안 뜨면 `kubectl cnpg backup keycloak-pg -n keycloak`로 수동 재시도.

- [ ] **Step 6: cutover — db.host live patch**

Keycloak CR의 `db.host`를 신 CNPG rw 서비스로 교체(사용자/비밀번호는 기존 Secret `keycloak-db-credentials` 유지 — G0로 신 DB와 일치).
Run:
```bash
kubectl -n keycloak patch keycloak cledyu-keycloak --type merge \
  -p '{"spec":{"db":{"host":"keycloak-pg-rw"},"instances":1}}'
kubectl -n keycloak rollout status sts/cledyu-keycloak --timeout=180s
```
Expected: Keycloak 파드 Ready(신 DB `keycloak-pg-rw`에 접속).

- [ ] **Step 7: G2 — 로그인 검증 (cutover 후)**

Run:
```bash
# admin realm 토큰 발급 확인(신 DB 상대 인증 동작)
kubectl -n keycloak get keycloak cledyu-keycloak -o jsonpath='{.status.conditions}'; echo
# 실제 로그인: auth.cledyu.com 로그인 + 소셜 로그인(네이버/구글) 1건씩 수동 확인
```
Expected: Keycloak status `Ready` 조건 true, 웹 로그인·소셜 로그인·토큰 발급 정상.
> 롤백: `kubectl patch ... '{"spec":{"db":{"host":"keycloak-db-postgresql"}}}'` → 구 DB로 즉시 복귀(구 DB는 Task 6 전까지 살아있음).

- [ ] **Step 8: Ansible 기본값 커밋 (live patch 정합화)**

live patch를 Ansible 소스와 일치시켜 다음 플레이북 실행이 cutover 상태를 유지하게 한다.
`ansible/roles/keycloak_foundation/defaults/main.yml`의 `keycloak_foundation_db_service_name`을 수정:
```yaml
keycloak_foundation_db_service_name: keycloak-pg-rw
```
Run:
```bash
git add ansible/roles/keycloak_foundation/defaults/main.yml
git commit -m "chore(dr): keycloak db.host를 CNPG keycloak-pg-rw로 cutover"
```

- [ ] **Step 9: CNPG automated 전환 (git) + push**

`gitops/argocd/apps/data-keycloak-pg.yaml`의 `syncPolicy`를 automated로 교체(수동 sync 주석 제거):
```yaml
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
      - ServerSideApply=true
    retry:
      limit: 5
      backoff:
        duration: 10s
        factor: 2
        maxDuration: 3m
```
Run:
```bash
git add gitops/argocd/apps/data-keycloak-pg.yaml
git commit -m "chore(dr): keycloak-pg 앱 automated sync 전환 (이관 완료)"
git push
kubectl -n argocd get application data-keycloak-pg
```
Expected: `data-keycloak-pg` automated, Synced. Keycloak 정상, 구 Bitnami는 여전히 기동(롤백용).

---

### Task 5: PITR 복원 드릴 (keycloak-pg) — 폐기 게이트

> 백업이 실제 복원 가능한지 증명한다. 임시 클러스터로 keycloak-pg를 S3에서 복원 후 폐기. Bitnami 폐기(Task 6) 전 필수 권장.

**Files:**
- Modify: `docs/RUNBOOK/dr-restore-drill.md` (keycloak-pg 케이스 추가)

**Interfaces:**
- Consumes: S3 `keycloak/` 백업(Task 4)

- [ ] **Step 1: 복원용 Cluster 매니페스트 작성 (드릴)**

`/tmp/keycloak-pitr-drill.yaml` — `<major>.4@<sha256>`는 Task 3 Step 3 값, `<5분 전 UTC>`는 실행 시각:
```yaml
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: keycloak-pg-drill
  namespace: keycloak
spec:
  instances: 1
  imageName: "ghcr.io/cloudnative-pg/postgresql:<major>.4@<sha256>"
  storage: { size: 20Gi, storageClass: longhorn }
  bootstrap:
    recovery:
      source: keycloak-pg
      recoveryTarget:
        targetTime: "<5분 전 UTC 타임스탬프, 예: 2026-07-09 05:30:00+00>"
  externalClusters:
    - name: keycloak-pg
      barmanObjectStore:
        destinationPath: "s3://cledyu-lab-dr-backups/keycloak"
        endpointURL: "https://s3.ap-northeast-2.amazonaws.com"
        s3Credentials:
          accessKeyId: { name: cledyu-backup-s3, key: ACCESS_KEY_ID }
          secretAccessKey: { name: cledyu-backup-s3, key: ACCESS_SECRET_KEY }
```

- [ ] **Step 2: 복원 실행 + 데이터 검증**

Run:
```bash
kubectl apply -f /tmp/keycloak-pitr-drill.yaml
kubectl -n keycloak wait --for=condition=Ready cluster/keycloak-pg-drill --timeout=600s
kubectl -n keycloak exec keycloak-pg-drill-1 -- psql -U keycloak -d keycloak -tAc \
  "select count(*) from user_entity"
```
Expected: 복원된 DB의 `user_entity` row 수가 라이브 keycloak-pg와 근사(targetTime 이내 반영). 실측값을 런북에 기록.

- [ ] **Step 3: 드릴 정리 + 런북 작성 + Commit**

Run: `kubectl -n keycloak delete cluster keycloak-pg-drill`
`docs/RUNBOOK/dr-restore-drill.md`에 keycloak-pg 복원 절차·실측 RPO/RTO·주의사항을 기존 cledyu-pg 케이스와 나란히 추가(같은 형식).
```bash
git add docs/RUNBOOK/dr-restore-drill.md
git commit -m "docs(dr): keycloak-pg PITR 복원 드릴·실측 기록 추가"
git push
```

---

### Task 6: 유예기간 후 구 Bitnami Keycloak DB 폐기

> 유예기간 동안 (1) 첫 S3 백업 도달(G3), (2) Keycloak 안정 운영, (3) PITR 드릴(Task 5) 통과를 **모두 확인한 뒤에만** 실행한다. Bitnami는 Ansible 소유라 git-rm/prune이 아니라 Ansible 제거 + 수동 uninstall이다.

**Files:**
- Modify: `ansible/playbooks/70-keycloak-foundation.yml` (`postgres_single`(keycloak) role 호출 제거/가드)

**Interfaces:**
- Consumes: Task 4 cutover 완료 상태(Keycloak은 keycloak-pg 사용 중)

- [ ] **Step 1: 플레이북에서 postgres_single(keycloak) 제거**

`ansible/playbooks/70-keycloak-foundation.yml`에서 `postgres_single` role 호출(keycloak DB 배포)을 제거하거나 실행 조건으로 가드한다. 실제 파일의 role 나열/`roles:` 또는 `import_role`/`include_role` 위치를 확인해 해당 항목만 제거한다(다른 role: keycloak_operator, keycloak_foundation은 유지). 소스가 소스 오브 트루스.
```bash
git add ansible/playbooks/70-keycloak-foundation.yml
git commit -m "chore(dr): keycloak 플레이북에서 구 Bitnami postgres 배포 제거"
git push
```

- [ ] **Step 2: Bitnami 릴리스·PVC 수동 정리**

Run:
```bash
# Keycloak이 keycloak-pg-rw를 쓰고 있는지 최종 확인
kubectl -n keycloak get keycloak cledyu-keycloak -o jsonpath='{.spec.db.host}'; echo   # keycloak-pg-rw
helm uninstall keycloak-db -n keycloak --kubeconfig /home/ubuntu/.kube/config
kubectl -n keycloak get pvc | grep keycloak-db-postgresql   # Retain이면 잔존 → 확인 후 삭제
kubectl -n keycloak delete pvc data-keycloak-db-postgresql-0
```
Expected: `db.host`=`keycloak-pg-rw` 확인 후 Bitnami 릴리스 삭제, PVC 정리. Keycloak 로그인 여전히 정상.

---

### Task 7: Plan C 복원 절 업데이트 (keycloak-pg 구체화)

**Files:**
- Modify: `docs/superpowers/plans/2026-07-03-dr-backup-plan-c-orchestration.md` (Restore 단계에 keycloak-pg recovery 명시)

**Interfaces:**
- Consumes: Task 4·5의 이관·복원 방식(CNPG bootstrap.recovery)

- [ ] **Step 1: Restore 절에 keycloak-pg 명시**

Plan C의 Restore 관련 서술(현재 "Postgres PITR/Keycloak"으로 뭉뚱그린 line 290·304 부근)을, keycloak-pg도 cledyu-pg와 동일한 CNPG `bootstrap.recovery`(source=`keycloak/` 프리픽스)로 복구됨을 명시하도록 수정한다. DR 흐름: Vault unseal → ESO 정상화 → CNPG가 `cledyu-pg`·`keycloak-pg` 둘 다 recovery → Velero가 keycloak ns 오브젝트(Keycloak CR, db.host=keycloak-pg-rw) 복원. (기존 주석·설명은 보존하고 추가/치환만.)

- [ ] **Step 2: Commit**

```bash
git add docs/superpowers/plans/2026-07-03-dr-backup-plan-c-orchestration.md
git commit -m "docs(dr): Plan C 복원 절에 keycloak-pg CNPG recovery 명시"
git push
```

---

## Self-Review

**Spec coverage (설계 §대비):**
- §1 논리 import + write-freeze → Task 4 (freeze=Keycloak instances:0) ✓
- §2 공존 후 db.host 스왑(live patch → Ansible 커밋) → Task 4 Step 6·8 ✓
- §3 자격증명 parity(G0) → Task 3 Step 1 + Task 4 Step 1 ✓
- §4 barman in-tree, retentionPolicy 미설정 → Task 3 Step 4 ✓
- §5 S3/IAM/ESO(keycloak/ 프리픽스) → Task 1·2 ✓
- §6 root-apps 불필요 + 플레이북 재실행 금지 → Task 3 Step 6, Task 4 헤더 ✓
- §7 롤백/폐기(Ansible 방식) → Task 4 Step 7 롤백, Task 6 폐기 ✓
- §8 Plan C 통합 → Task 7 ✓
- §9 게이트 G0~G3 → Task 3 Step 1(G0), Task 4 Step 4(G1)/7(G2)/5(G3) ✓
- PITR 드릴 → Task 5 ✓

**Placeholder scan:** `imageName`의 `<major>`/`<sha256>`와 드릴 `targetTime`은 런타임 실측값(Task 3 Step 3에서 명령으로 확정, Step 7 렌더 검증에서 잔존 여부 육안 확인). Task 6 Step 1은 실제 플레이북 role 나열 구조가 파일마다 달라 "소스 확인 후 제거"로 명시 — 그 외 TODO/TBD 없음.

**Type consistency:** Secret `cledyu-backup-s3` 키명 `ACCESS_KEY_ID`/`ACCESS_SECRET_KEY`(Task 2·3 일관). CNPG 서비스명 `keycloak-pg-rw`(Task 4 cutover·Task 6 확인·Task 7 일관). basic-auth Secret `keycloak-pg-credentials` owner=`keycloak`=Vault username(Task 3 Step 1·4·5 일관). Vault remoteRef key `keycloak/postgres`(상대경로, ESO ClusterSecretStore 관례 — Task 3 Step 5).
