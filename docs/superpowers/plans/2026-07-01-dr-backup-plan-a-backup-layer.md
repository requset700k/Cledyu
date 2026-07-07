# DR/백업 Plan A — 백업 계층 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 온프렘 durable 데이터(cledyu Postgres, Vault, 범용 PVC)를 S3로 오프사이트 백업하고, Postgres는 CloudNativePG로 이관해 WAL 연속 아카이빙·PITR을 확보한다.

**Architecture:** S3 백업 버킷 + 전용 IAM 키(Terraform) → Vault→ESO로 클러스터에 자격증명 주입 → (1) cledyu Postgres를 CNPG Cluster로 이관해 barman S3 백업, (2) Vault raft 스냅샷 CronJob. 마지막에 PITR 복원 드릴로 RPO 실측.

**Tech Stack:** Terraform(AWS S3/IAM), External Secrets Operator, CloudNativePG operator, HashiCorp Vault(raft), Longhorn, ArgoCD(App-of-Apps).

## Global Constraints

- 리전: `ap-northeast-2` (기존 EC2 오버플로우와 동일, `docs/RUNBOOK/ec2-overflow.md`)
- 백업 버킷(단일): `cledyu-lab-dr-backups`, 프리픽스 `postgres/`, `vault/`, `velero/`
- S3 자격증명은 정적 IAM 키 → Vault kv → ESO(온프렘이라 IRSA 불가). 기존 ESO 패턴: ClusterSecretStore `vault-backend`, `remoteRef.key` 상대경로
- ArgoCD 앱 등록 패턴: `gitops/argocd/apps/<name>.yaml`(Application) + `gitops/apps/<name>/`(내용). repoURL `https://github.com/requset700k/Cledyu.git`, targetRevision `main`
- 커밋만 실행자가 하고, **사용자 확인 전 커밋 금지 규칙은 실행 단계에서 사용자 지시에 따른다**
- Postgres RPO 목표 5~15분, Vault RPO 1~24h
- 매니페스트 검증: `kubeconform`(CRD-aware, 기존 pre-commit 사용) / `helm template` / `terraform validate`

---

### Task 1: S3 백업 버킷 + 전용 IAM 사용자 (Terraform)

**Files:**
- Create: `infra/terraform/aws/backup.tf`
- Modify: `infra/terraform/aws/outputs.tf` (버킷명·IAM 사용자명 출력 추가)

**Interfaces:**
- Produces: S3 버킷 `${var.name_prefix}-dr-backups`(= `cledyu-lab-dr-backups`) — 버전ing·퍼블릭차단·SSE-KMS·Object Lock·수명주기 포함
- Produces: 프리픽스별 IAM 사용자 2개 `cledyu-lab-backup-writer-postgres`(`postgres/*`만), `cledyu-lab-backup-writer-vault`(`vault/*`만) + 각 프리픽스 한정 정책(최소권한, 교차 프리픽스 GetObject 차단). **액세스 키는 Terraform이 만들지 않는다**(api/engine 관례 — 장기 키를 S3 state에 안 남김). apply 후 콘솔/CLI로 수동 발급 → Vault.

- [ ] **Step 1: 백업 리소스 정의 작성**

`infra/terraform/aws/backup.tf` — 컨벤션 준수: `var.name_prefix` 사용, 정책은 `data.aws_iam_policy_document`
(기존 baker/api/engine 스타일), 퍼블릭차단·SSE-KMS(CMK)·Object Lock(GOVERNANCE 30일)·프리픽스별 수명주기 포함,
IAM 사용자는 `for_each`로 postgres/vault 프리픽스별 분리, `aws_iam_access_key`는 두지 않음.
(실제 파일 내용이 소스 오브 트루스 — 이 저장소의 `infra/terraform/aws/backup.tf` 참조)

핵심 리소스:
```hcl
resource "aws_s3_bucket"                            "dr_backups" { bucket = "${var.name_prefix}-dr-backups", object_lock_enabled = true }
resource "aws_s3_bucket_versioning"                 "dr_backups" { ... status = "Enabled" }
resource "aws_s3_bucket_object_lock_configuration"  "dr_backups" { GOVERNANCE 30일 }
resource "aws_s3_bucket_public_access_block"        "dr_backups" { ... 4개 true }
resource "aws_kms_key" / "aws_kms_alias"            "dr_backups" { 고객 관리 CMK(root 위임, 로테이션) }
resource "aws_s3_bucket_server_side_encryption_configuration" "dr_backups" { ... aws:kms + bucket key }
resource "aws_s3_bucket_lifecycle_configuration"    "dr_backups" { postgres/ backstop + vault/ 90d 만료 (velero/ 는 Task 8) }
resource "aws_iam_user"                             "backup" { for_each postgres/vault → "${var.name_prefix}-backup-writer-<프리픽스>" }
data     "aws_iam_policy_document"                  "backup" { 프리픽스 한정 PutObject/GetObject/Abort/ListMultipartParts + List(s3:prefix 조건) + KMS. **DeleteObject 없음**(정리는 lifecycle) }
resource "aws_iam_user_policy"                      "backup" { for_each, policy = data....json }
# aws_iam_access_key 리소스 없음 — 수동 발급
```

- [ ] **Step 2: outputs 추가**

`infra/terraform/aws/outputs.tf` 에 append (시크릿 output 없음 — api_iam_user 스타일):
```hcl
output "backup_bucket" {
  description = "DR 백업 S3 버킷명."
  value       = aws_s3_bucket.dr_backups.bucket
}

output "backup_iam_users" {
  description = "프리픽스별 백업 IAM 사용자명 맵(postgres/vault) — 각 사용자의 액세스 키를 발급해 Vault(cledyu/aws/backup-postgres, cledyu/aws/backup-vault)에 보관한다."
  value       = { for k, u in aws_iam_user.backup : k => u.name }
}
```

- [ ] **Step 3: 검증 (validate)**

Run: `cd infra/terraform/aws && terraform init -backend=false && terraform validate && terraform fmt -check backup.tf outputs.tf`
Expected: `Success! The configuration is valid.` + fmt 통과

- [ ] **Step 4: apply (AWS 자격증명 단독 — `AWS_PROFILE=cledyu`)**

Run: `cd infra/terraform/aws && terraform init && terraform plan`
Expected: `Plan: N to add, 0 to change, 0 to destroy` — **기존 리소스 change/destroy가 0인지 확인 후** `terraform apply`.
`terraform output backup_bucket` → `cledyu-lab-dr-backups`, `terraform output backup_iam_users` → `{postgres = "cledyu-lab-backup-writer-postgres", vault = "cledyu-lab-backup-writer-vault"}`

- [ ] **Step 5: Commit**

```bash
git add infra/terraform/aws/backup.tf infra/terraform/aws/outputs.tf
git commit -m "feat(dr): S3 백업 버킷·백업 전용 IAM 사용자 추가"
```

---

### Task 2: S3 자격증명 Vault 등록 + ESO 동기화

**Files:**
- Create: `gitops/apps/backup-secrets/Chart.yaml`
- Create: `gitops/apps/backup-secrets/templates/externalsecret.yaml`
- Create: `gitops/apps/backup-secrets/values.yaml`
- Create: `gitops/argocd/apps/data-backup-secrets.yaml`

**Interfaces:**
- Consumes: Task 1의 프리픽스별 IAM 사용자 2개(`cledyu-lab-backup-writer-postgres`/`-vault`, 콘솔/CLI로 수동 발급한 액세스 키)
- Produces: 네임스페이스 `postgres`/`vault` 각각에 Secret `cledyu-backup-s3`
  (키: `ACCESS_KEY_ID`, `ACCESS_SECRET_KEY`) — 각 ns는 자기 프리픽스 키만 참조. Task 4·5가 사용.

- [ ] **Step 1: 액세스 키 수동 발급 + Vault kv 등록 (수동 사전작업)**

apply 후 콘솔/CLI로 **두 사용자 각각** 액세스 키를 발급해 프리픽스별 Vault 경로에 등록한다:
```bash
# postgres 백업용
aws iam create-access-key --user-name cledyu-lab-backup-writer-postgres
vault kv put cledyu/aws/backup-postgres \
  access_key_id="<AccessKeyId>" \
  secret_access_key="<SecretAccessKey>"

# vault 백업용
aws iam create-access-key --user-name cledyu-lab-backup-writer-vault
vault kv put cledyu/aws/backup-vault \
  access_key_id="<AccessKeyId>" \
  secret_access_key="<SecretAccessKey>"
```
Expected: 각각 `Success! Data written to: cledyu/aws/backup-postgres` / `...-vault`

- [ ] **Step 2: Helm 차트 스캐폴드 작성**

`gitops/apps/backup-secrets/Chart.yaml`:
```yaml
apiVersion: v2
name: backup-secrets
version: 0.1.0
```

`gitops/apps/backup-secrets/values.yaml` (네임스페이스별로 다른 프리픽스 키 참조):
```yaml
secrets:
  - namespace: postgres
    vaultKey: aws/backup-postgres
  - namespace: vault
    vaultKey: aws/backup-vault
```

`gitops/apps/backup-secrets/templates/externalsecret.yaml`:
```yaml
{{- range .Values.secrets }}
---
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: cledyu-backup-s3
  namespace: {{ .namespace }}
  annotations:
    argocd.argoproj.io/sync-options: SkipDryRunOnMissingResource=true
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: vault-backend
    kind: ClusterSecretStore
  target:
    name: cledyu-backup-s3
    creationPolicy: Owner
    deletionPolicy: Retain
  data:
    - secretKey: ACCESS_KEY_ID
      remoteRef:
        key: {{ .vaultKey }}
        property: access_key_id
    - secretKey: ACCESS_SECRET_KEY
      remoteRef:
        key: {{ .vaultKey }}
        property: secret_access_key
{{- end }}
```

- [ ] **Step 3: ArgoCD Application 작성**

`gitops/argocd/apps/data-backup-secrets.yaml`:
```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: data-backup-secrets
  namespace: argocd
  finalizers:
    - resources-finalizer.argocd.argoproj.io
spec:
  project: default
  source:
    repoURL: https://github.com/requset700k/Cledyu.git
    targetRevision: main
    path: gitops/apps/backup-secrets
    helm:
      releaseName: backup-secrets
  destination:
    server: https://kubernetes.default.svc
    namespace: postgres
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
      - ServerSideApply=true
```

- [ ] **Step 4: 검증 (템플릿 렌더 + 스키마)**

Run: `helm template gitops/apps/backup-secrets | kubeconform -strict -ignore-missing-schemas`
Expected: 에러 없음. 2개 ExternalSecret 렌더됨(`namespace: postgres`→`aws/backup-postgres`, `vault`→`aws/backup-vault`).

- [ ] **Step 5: Sync 후 Secret 생성 확인**

Run:
```bash
argocd app sync data-backup-secrets
kubectl -n postgres get secret cledyu-backup-s3 -o jsonpath='{.data.ACCESS_KEY_ID}' | base64 -d
```
Expected: IAM access key id 출력(비어있지 않음).

- [ ] **Step 6: Commit**

```bash
git add gitops/apps/backup-secrets gitops/argocd/apps/data-backup-secrets.yaml
git commit -m "feat(dr): S3 백업 자격증명 ESO 동기화(backup-secrets)"
```

---

### Task 3: CloudNativePG 오퍼레이터 설치

> **구현 방식(실제)**: wrapper Chart 대신 **ArgoCD 멀티소스**(공식 차트 + `$values`)로 구현했다.
> wrapper Chart는 `helm dependency build`로 공식 차트를 레포에 내려받아야 하는 부담이 있어, 멀티소스로
> "레지스트리 차트 직접 + 우리 values.yaml"을 배포 시점에 합치는 방식이 더 깔끔하다. 따라서 로컬
> `Chart.yaml`은 만들지 않고 `values.yaml`만 둔다(값은 upstream 차트에 직접 주입되므로 nesting 없음).

**Files:**
- Create: `gitops/apps/cnpg-operator/values.yaml` (Chart.yaml 없음 — 멀티소스)
- Create: `gitops/argocd/apps/data-cnpg-operator.yaml`

**Interfaces:**
- Produces: `postgresql.cnpg.io` CRD군(`Cluster`, `ScheduledBackup`, `Backup`) — Task 4가 사용

- [ ] **Step 1: values.yaml 작성 (nesting 없이 upstream 직접 주입)**

`gitops/apps/cnpg-operator/values.yaml`:
```yaml
# 멀티소스라 cloudnative-pg: 래핑 없이 upstream 차트에 직접 주입된다.
crds:
  create: true
monitoring:
  podMonitorEnabled: true  # 기존 kube-prometheus-stack이 수집
```

- [ ] **Step 2: ArgoCD 멀티소스 Application 작성**

`gitops/argocd/apps/data-cnpg-operator.yaml` — 소스 2개: (1) 공식 차트, (2) values 제공 레포(`ref: values`).
`sync-wave: 0`으로 Cluster CR(data-postgres-cnpg)보다 먼저 CRD·오퍼레이터가 서게 한다.
```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: data-cnpg-operator
  namespace: argocd
  annotations:
    argocd.argoproj.io/sync-wave: "0"
  finalizers:
    - resources-finalizer.argocd.argoproj.io
spec:
  project: default
  sources:
    - repoURL: https://cloudnative-pg.github.io/charts
      chart: cloudnative-pg
      targetRevision: 0.23.0        # apply 전 최신 stable 확인해 핀
      helm:
        releaseName: cnpg
        valueFiles:
          - $values/gitops/apps/cnpg-operator/values.yaml
    - repoURL: https://github.com/requset700k/Cledyu.git
      targetRevision: main
      ref: values
  destination:
    server: https://kubernetes.default.svc
    namespace: cnpg-system
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
      - ServerSideApply=true        # 대형 CRD annotation 초과 방지
    retry:
      limit: 5
      backoff:
        duration: 10s
        factor: 2
        maxDuration: 3m
```

> 검증: 멀티소스는 로컬 `helm template` 단독으로 못 합친다. 렌더 확인이 필요하면 upstream 차트를
> 임시로 받아 `helm template cloudnative-pg/cloudnative-pg -f values.yaml`로 확인하거나, ArgoCD
> diff(`argocd app diff`)로 sync 전 검증한다.

- [ ] **Step 3: Sync 후 CRD·오퍼레이터 확인**

Run:
```bash
argocd app sync data-cnpg-operator
kubectl get crd clusters.postgresql.cnpg.io
kubectl -n cnpg-system rollout status deploy/cnpg-cloudnative-pg
```
Expected: CRD 존재, 오퍼레이터 Deployment `Available`.

- [ ] **Step 4: Commit**

```bash
git add gitops/apps/cnpg-operator gitops/argocd/apps/data-cnpg-operator.yaml
git commit -m "feat(dr): CloudNativePG 오퍼레이터 설치"
```

---

### Task 4: cledyu Postgres를 CNPG Cluster로 이관 + S3 백업

> 가장 위험한 태스크. **write-freeze**(api 정지)로 무손실을 보장한다: cnpg 앱을 수동 sync로 만들고 →
> **정비 창(root-apps 정지)** → api 정지 → 논리 import → row 검증(G1) → api DSN cutover+unfreeze →
> cnpg automated 전환(git) + **정비 창 종료** → **유예기간** 후 구 StatefulSet 폐기.
> root-apps(selfHeal)가 자식 앱 spec을 git과 강제 일치시키므로 런타임 sync-policy 조작은 정비 창 안에서만 유효하다.
> 상세 설계·결정 근거: `docs/superpowers/specs/2026-07-06-t4-postgres-cnpg-migration-design.md`.

**Files:**
- Create: `gitops/apps/postgres-cnpg/Chart.yaml`
- Create: `gitops/apps/postgres-cnpg/values.yaml`
- Create: `gitops/apps/postgres-cnpg/templates/cluster.yaml`
- Create: `gitops/apps/postgres-cnpg/templates/scheduledbackup.yaml`
- Create: `gitops/argocd/apps/data-postgres-cnpg.yaml`
- Modify(폐기): `gitops/argocd/apps/data-postgres.yaml` (구 StatefulSet 앱 — Step 11에서 제거)

**Interfaces:**
- Consumes: Secret `cledyu-backup-s3`(Task 2), Secret `postgres-credentials`(기존), 구 서비스 `postgres.postgres.svc`(임포트 소스)
- Produces: CNPG Cluster `cledyu-pg` → 서비스 `cledyu-pg-rw.postgres.svc:5432`, S3 프리픽스 `postgres/`에 WAL+baseBackup

- [ ] **Step 1: CNPG Cluster 매니페스트 작성 (barman S3 + 구 DB 임포트)**

`gitops/apps/postgres-cnpg/templates/cluster.yaml`:
```yaml
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: cledyu-pg
  namespace: {{ .Release.Namespace }}
spec:
  instances: {{ .Values.instances }}
  imageName: "ghcr.io/cloudnative-pg/postgresql:16.4"
  storage:
    size: {{ .Values.storage.size }}
    storageClass: {{ .Values.storage.className }}
  bootstrap:
    initdb:
      database: cledyu
      owner: cledyu
      secret:
        name: postgres-credentials-cnpg
      import:
        type: microservice
        databases:
          - cledyu
        source:
          externalCluster: old-postgres
  externalClusters:
    - name: old-postgres
      connectionParameters:
        host: postgres.postgres.svc
        user: cledyu
        dbname: cledyu
      password:
        name: postgres-credentials
        key: password
  backup:
    barmanObjectStore:
      destinationPath: "s3://cledyu-lab-dr-backups/postgres"
      endpointURL: "https://s3.ap-northeast-2.amazonaws.com"
      s3Credentials:
        accessKeyId:
          name: cledyu-backup-s3
          key: ACCESS_KEY_ID
        secretAccessKey:
          name: cledyu-backup-s3
          key: ACCESS_SECRET_KEY
      wal:
        compression: gzip
    retentionPolicy: "30d"
```

> 주(2026-07-06 실측): 배포된 CNPG 오퍼레이터는 helm chart **0.23.0 = 오퍼레이터 1.25.0**이다.
> in-tree `barmanObjectStore`는 **1.26부터 deprecated**(barman-cloud 플러그인이 후속 경로)이나
> **1.25.0에서는 기본·정상 동작**하므로 위 설정이 그대로 유효하다. 오퍼레이터를 chart 0.23.0(=1.25.0)에
> **핀 고정**하고, ≥1.26 상향 시 barman-cloud 플러그인으로 이관한다.

- [ ] **Step 2: CNPG용 credentials Secret + ScheduledBackup 작성**

CNPG bootstrap 은 `username`/`password` 키의 basic-auth Secret 을 요구한다. 기존
`postgres-credentials`(password만)와 별개로 ESO 매핑을 추가한다.

`gitops/apps/postgres-cnpg/templates/scheduledbackup.yaml`:
```yaml
apiVersion: postgresql.cnpg.io/v1
kind: ScheduledBackup
metadata:
  name: cledyu-pg-daily
  namespace: {{ .Release.Namespace }}
spec:
  schedule: "0 0 2 * * *"   # 매일 02:00 base backup (WAL은 연속 아카이빙)
  backupOwnerReference: self
  cluster:
    name: cledyu-pg
---
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: postgres-credentials-cnpg
  namespace: {{ .Release.Namespace }}
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: vault-backend
    kind: ClusterSecretStore
  target:
    name: postgres-credentials-cnpg
    template:
      type: kubernetes.io/basic-auth
    creationPolicy: Owner
  data:
    - secretKey: username
      remoteRef:
        key: db/postgres
        property: username
    - secretKey: password
      remoteRef:
        key: db/postgres
        property: password
```

> 사전작업: `vault kv patch cledyu/db/postgres username=cledyu` (기존 kv에 username 키 추가).

`gitops/apps/postgres-cnpg/values.yaml`:
```yaml
instances: 1
storage:
  className: longhorn
  size: 10Gi
```
`gitops/apps/postgres-cnpg/Chart.yaml`:
```yaml
apiVersion: v2
name: postgres-cnpg
version: 0.1.0
```

- [ ] **Step 3: 검증 (렌더 + 스키마)**

Run: `helm template gitops/apps/postgres-cnpg --namespace postgres | kubeconform -strict -ignore-missing-schemas -schema-location default -schema-location 'https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/main/docs/src/samples/{{.ResourceKind}}.json'`
Expected: 렌더 성공(Cluster/ScheduledBackup/ExternalSecret). CRD 스키마 없으면 `-ignore-missing-schemas`로 통과.

- [ ] **Step 4: ArgoCD Application 작성 (수동 sync) + 매니페스트 커밋**

`gitops/argocd/apps/data-postgres-cnpg.yaml` (골격 동일, path `gitops/apps/postgres-cnpg`, releaseName `postgres-cnpg`, namespace `postgres`).
**중요**: 이관 동안은 `syncPolicy.automated`를 두지 않는다(수동 sync). 자동 sync면 커밋 즉시
ArgoCD가 Cluster를 만들어 import를 실행 → **api가 아직 안 멈춘 구 DB를 스냅샷**하게 되어 write-freeze가
무의미해진다. cutover 검증 후 Step 10에서 **git으로**(런타임 토글 아님) automated로 전환한다.
Run:
```bash
git add gitops/apps/postgres-cnpg gitops/argocd/apps/data-postgres-cnpg.yaml
git commit -m "feat(dr): postgres-cnpg 매니페스트 추가 (수동 sync, import 대기)"
git push
kubectl -n argocd get application data-postgres-cnpg
```
Expected: `data-postgres-cnpg` Application 존재, 상태 OutOfSync(아직 sync 안 함).

- [ ] **Step 5: 정비 창 진입 + write-freeze (api 정지)**

논리 import는 시작 시점의 일회성 스냅샷이라, import~cutover 사이 구 DB 쓰기는 신 DB에 반영되지 않는다.
import 직전 api를 정지해 구 DB 쓰기를 물리적으로 차단한다.
**root-apps 제약**: App-of-Apps 루트 `root-apps`(`ansible/roles/argocd/templates/root-app.yaml.j2`)가
`selfHeal: true`로 자식 앱 spec을 git과 강제 일치시킨다(재조정 ~3분). `service-api`를 런타임에
`sync-policy none`으로 바꿔도 root-apps가 되돌려(→ api 재기동) freeze가 깨진다. **먼저 root-apps를 정지**
(정비 창)해야 그 아래 토글이 유지된다. root-apps는 Ansible이 심을 뿐 상시 재조정 주체가 없어 정지는
Step 10 복원 전까지 유지된다(정비 창 동안 다른 앱 재조정도 멈춤 — 통제된 유지보수 시간).
Run:
```bash
# 1) 정비 창 진입 — root-apps 정지(자식 토글이 되돌려지지 않게)
argocd app set root-apps --sync-policy none
# 2) write-freeze — service-api 정지 + api scale 0
argocd app set service-api --sync-policy none
kubectl -n api scale deploy/api --replicas=0
kubectl -n api rollout status deploy/api --timeout=120s
# 3) 구 DB 활성 커넥션 없음 확인(자기 세션 제외)
kubectl -n postgres exec postgres-0 -- psql -U cledyu -d cledyu -tAc \
  "select count(*) from pg_stat_activity where datname='cledyu' and state='active' and pid<>pg_backend_pid()"
```
Expected: root-apps·service-api 모두 sync-policy none, api replicas 0, 구 DB 활성 커넥션 0.

- [ ] **Step 6: import 실행 (수동 sync)**

Run:
```bash
argocd app sync data-postgres-cnpg
kubectl -n postgres wait --for=condition=Ready cluster/cledyu-pg --timeout=600s
```
Expected: Cluster `Ready`. import Job이 정지된 구 DB를 논리 복제.

- [ ] **Step 7: 데이터 일치 검증 (G1 — cutover 전 필수)**

Run:
```bash
# 구 DB row 수
kubectl -n postgres exec postgres-0 -- psql -U cledyu -d cledyu -tAc \
  "select count(*) from session_progress"
# 신 CNPG row 수
kubectl -n postgres exec cledyu-pg-1 -- psql -U cledyu -d cledyu -tAc \
  "select count(*) from session_progress"
```
Expected: 두 값 **동일**. 다르면 cutover 중단, import 재점검(구 DB는 정지 상태라 데이터 안전).
> 착수 시 실제 스키마를 보고 검증 대상 테이블(session_progress + 수료증 관련)을 확정한다.

- [ ] **Step 8: 첫 backup S3 도달 확인 (G3)**

ScheduledBackup에 `immediate: true`가 있어 Step 6에서 Cluster와 함께 sync되는
순간 첫 base backup이 자동으로 시작된다 — 수동 트리거 불필요, 완료만 확인
Run:
```bash
kubectl -n postgres get backup -l cnpg.io/cluster=cledyu-pg   # phase=completed 대기(수 분 소요 가능)
aws s3 ls s3://cledyu-lab-dr-backups/postgres/ --recursive | head
```
Expected: `postgres/` 하위에 base backup + WAL 객체 존재. (구 DB 폐기 전 필수 확인 항목)
> completed가 안 뜨면 `kubectl cnpg backup cledyu-pg -n postgres`로 수동 재시도.

- [ ] **Step 9: api DSN cutover + unfreeze**

> `cledyu-api-db` ExternalSecret은 `infra/kubernetes/external-secrets/`에 존재(out-of-band 적용)하므로 아래
> annotate는 유효하다. Vault `cledyu/db/api:dsn` 미설정이면 api는 in-memory 폴백이라 DSN/ESO 단계는 생략 가능.
> 정비 창 중이라 service-api 재개 토글은 유지된다.
Run:
```bash
# 1) DSN을 CNPG rw 서비스로 교체(사용자/비밀번호 유지)
vault kv patch cledyu/db/api \
  dsn="postgresql://cledyu:$(vault kv get -field=password cledyu/db/postgres)@cledyu-pg-rw.postgres.svc:5432/cledyu?sslmode=require"
# 2) ESO 강제 리프레시(Secret cledyu-api-db 갱신)
kubectl -n api annotate externalsecret cledyu-api-db force-sync=$(date +%s) --overwrite
# 3) api 재기동(=unfreeze). 정비 창 중이라 root-apps가 되돌리지 않는다.
argocd app set service-api --sync-policy automated --self-heal --auto-prune
kubectl -n api rollout status deploy/api --timeout=180s
```
Expected: api 파드 Ready, `/health` 200, 세션 진도 조회 정상.
> 롤백: DSN 원복 → api 롤아웃. 구 DB는 계속 살아있어(Step 11 전) 즉시 복귀 가능.

- [ ] **Step 10: cnpg automated 전환(git) + 정비 창 종료**

cnpg 앱의 automated 전환은 **런타임이 아니라 git으로** 한다 — root-apps가 git을 강제하므로 런타임 토글은
복원 시 되돌려진다. `data-postgres-cnpg.yaml`에 automated 블록을 추가·커밋한 뒤 root-apps를 복원한다.
**구 DB는 이 시점에 정지·삭제하지 않는다** — 계속 살려둬 롤백 안전망으로 쓴다(정지하면 root-apps 복원 시
git이 여전히 구 DB를 automated로 선언해 다시 기동돼 충돌한다). 폐기는 유예기간 후 git-rm(Step 11)으로만.
Run:
```bash
# 1) 앱 파일에 automated 블록 추가(수동 sync 주석 → 실제 automated: {prune:true, selfHeal:true})
git add gitops/argocd/apps/data-postgres-cnpg.yaml
git commit -m "chore(dr): postgres-cnpg 앱 automated sync 전환 (이관 완료)"
git push
# 2) 정비 창 종료 — root-apps 복원. 자식 spec을 git 기준 재정렬:
#    service-api(automated 유지)·data-postgres-cnpg(automated 반영)·data-postgres(그대로 유지, 폐기 전).
argocd app set root-apps --sync-policy automated --self-heal --auto-prune
```
Expected: root-apps Synced, data-postgres-cnpg automated, api 정상, 구 postgres는 여전히 기동(롤백용).

- [ ] **Step 11: 유예기간 후 구 Postgres 폐기 + Commit**

유예기간(구 DB는 살아있는 상태로 대기) 동안 (1) 첫 S3 base backup+WAL 도달(Step 8/G3),
(2) api 안정 운영, (3) 권장: PITR 복원 드릴(Task 7) 통과 를 **모두 확인한 뒤에만** 실행한다.
Run:
```bash
git rm -r gitops/apps/postgres gitops/argocd/apps/data-postgres.yaml
git commit -m "feat(dr): cledyu Postgres를 CNPG로 이관·S3 WAL 백업, 구 StatefulSet 폐기"
git push
```
(root-apps가 data-postgres 앱을 prune → 구 StatefulSet 제거. PVC는 `Retain`이라 잔존 — 수동 정리 별도.)

---

### Task 5: Vault raft 스냅샷 CronJob → S3

**Files:**
- Create: `gitops/apps/vault-backup/Chart.yaml`
- Create: `gitops/apps/vault-backup/values.yaml`
- Create: `gitops/apps/vault-backup/templates/cronjob.yaml`
- Create: `gitops/apps/vault-backup/templates/rbac.yaml`
- Create: `gitops/argocd/apps/platform-vault-backup.yaml`

**Interfaces:**
- Consumes: Secret `cledyu-backup-s3`(Task 2, `vault` ns), Vault 토큰(전용 정책)
- Produces: S3 프리픽스 `vault/` 에 `vault-raft-<ts>.snap`

- [ ] **Step 1: 스냅샷 정책·토큰 사전작업 (수동)**

Run:
```bash
vault policy write snapshot - <<'EOF'
path "sys/storage/raft/snapshot" { capabilities = ["read"] }
EOF
# k8s auth role 로 CronJob SA에 snapshot 정책 부여(기존 vault k8s auth 사용)
vault write auth/kubernetes/role/vault-backup \
  bound_service_account_names=vault-backup \
  bound_service_account_namespaces=vault \
  policies=snapshot ttl=15m
```
Expected: 정책·role 생성.

- [ ] **Step 2: CronJob + RBAC 작성**

`gitops/apps/vault-backup/templates/cronjob.yaml`:
```yaml
apiVersion: batch/v1
kind: CronJob
metadata:
  name: vault-raft-snapshot
  namespace: {{ .Release.Namespace }}
spec:
  schedule: "0 */6 * * *"   # 6시간마다 (RPO 1~24h 목표 충족)
  concurrencyPolicy: Forbid
  jobTemplate:
    spec:
      template:
        spec:
          serviceAccountName: vault-backup
          restartPolicy: OnFailure
          containers:
            - name: snapshot
              image: hashicorp/vault:1.21.2
              env:
                - name: VAULT_ADDR
                  value: "https://vault-active.vault.svc:8200"
                - name: VAULT_SKIP_VERIFY
                  value: "true"
                - name: AWS_ACCESS_KEY_ID
                  valueFrom: { secretKeyRef: { name: cledyu-backup-s3, key: ACCESS_KEY_ID } }
                - name: AWS_SECRET_ACCESS_KEY
                  valueFrom: { secretKeyRef: { name: cledyu-backup-s3, key: ACCESS_SECRET_KEY } }
              command: ["/bin/sh", "-c"]
              args:
                - |
                  set -e
                  VAULT_TOKEN=$(vault write -field=token auth/kubernetes/login \
                    role=vault-backup jwt=@/var/run/secrets/kubernetes.io/serviceaccount/token)
                  export VAULT_TOKEN
                  TS=$(date -u +%Y%m%dT%H%M%SZ)
                  vault operator raft snapshot save /tmp/vault-raft-$TS.snap
                  aws s3 cp /tmp/vault-raft-$TS.snap \
                    s3://cledyu-lab-dr-backups/vault/vault-raft-$TS.snap
```

`gitops/apps/vault-backup/templates/rbac.yaml`:
```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: vault-backup
  namespace: {{ .Release.Namespace }}
```

`gitops/apps/vault-backup/values.yaml`: `{}`
`gitops/apps/vault-backup/Chart.yaml`:
```yaml
apiVersion: v2
name: vault-backup
version: 0.1.0
```

> 주: 위 이미지에 `aws` CLI가 없으면 initContainer로 awscli를 추가하거나 s3 업로드를 포함한
> 이미지를 쓴다. 실행 단계에서 `hashicorp/vault` 이미지에 aws CLI 부재 확인 시 → sidecar 방식으로.

- [ ] **Step 3: 검증**

Run: `helm template gitops/apps/vault-backup --namespace vault | kubeconform -strict -ignore-missing-schemas`
Expected: CronJob/SA 렌더 성공.

- [ ] **Step 4: ArgoCD Application 작성 + Sync + 수동 트리거**

`gitops/argocd/apps/platform-vault-backup.yaml` (골격 동일, path `gitops/apps/vault-backup`, namespace `vault`).
Run:
```bash
argocd app sync platform-vault-backup
kubectl -n vault create job --from=cronjob/vault-raft-snapshot vault-snap-test
kubectl -n vault wait --for=condition=complete job/vault-snap-test --timeout=180s
aws s3 ls s3://cledyu-lab-dr-backups/vault/
```
Expected: job 완료, `vault/vault-raft-*.snap` 객체 존재.

- [ ] **Step 5: Commit**

```bash
git add gitops/apps/vault-backup gitops/argocd/apps/platform-vault-backup.yaml
git commit -m "feat(dr): Vault raft 스냅샷 CronJob S3 백업"
```

---

### Task 7: PITR 복원 드릴 (RPO 실측·검증)

> 백업이 실제로 복원 가능한지 증명한다. 임시 네임스페이스에서 CNPG PITR 복원 후 폐기.

**Files:**
- Create: `docs/RUNBOOK/dr-restore-drill.md`

**Interfaces:**
- Consumes: S3 `postgres/` 백업(Task 4)

- [ ] **Step 1: 복원용 Cluster 매니페스트 작성 (드릴)**

`/tmp/pitr-drill.yaml`:
```yaml
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: cledyu-pg-drill
  namespace: postgres
spec:
  instances: 1
  storage: { size: 10Gi, storageClass: longhorn }
  bootstrap:
    recovery:
      source: cledyu-pg
      recoveryTarget:
        targetTime: "{{ 5분 전 UTC 타임스탬프 }}"
  externalClusters:
    - name: cledyu-pg
      barmanObjectStore:
        destinationPath: "s3://cledyu-lab-dr-backups/postgres"
        endpointURL: "https://s3.ap-northeast-2.amazonaws.com"
        s3Credentials:
          accessKeyId: { name: cledyu-backup-s3, key: ACCESS_KEY_ID }
          secretAccessKey: { name: cledyu-backup-s3, key: ACCESS_SECRET_KEY }
```

- [ ] **Step 2: 복원 실행 + 데이터 검증**

Run:
```bash
kubectl apply -f /tmp/pitr-drill.yaml
kubectl -n postgres wait --for=condition=Ready cluster/cledyu-pg-drill --timeout=600s
kubectl -n postgres exec cledyu-pg-drill-1 -- psql -U cledyu -d cledyu -tAc \
  "select max(updated_at) from session_progress"
```
Expected: 복원된 DB의 최신 `updated_at`이 targetTime 근방(RPO 이내). 실측값을 런북에 기록.

- [ ] **Step 3: 드릴 정리 + 런북 작성**

Run: `kubectl -n postgres delete cluster cledyu-pg-drill`
`docs/RUNBOOK/dr-restore-drill.md`에 절차·실측 RPO/RTO·주의사항 기록(기존 런북 형식 준용).

- [ ] **Step 4: Commit**

```bash
git add docs/RUNBOOK/dr-restore-drill.md
git commit -m "docs(dr): PITR 복원 드릴 런북·RPO 실측 기록"
```

---

### Task 8: Velero — 클러스터 상태(오브젝트) 백업 → S3

> 2차 설계에서 추가. GitOps는 git에 선언된 리소스만 재현하므로, `lab-sessions`처럼 런타임 동적
> 생성 리소스는 복원되지 않는다. Velero로 "온프렘에 실제로 떠 있던 워크로드 구성"을 스냅샷 떠
> EKS 재현을 가능케 한다. 데이터(Postgres/Vault)는 Task 4·5가 담당하므로 Velero는 **오브젝트만**,
> **PV 스냅샷은 끈다**(Longhorn 스냅샷은 EKS/EBS에서 복원 불가 + PITR 우위). 상세: 스펙 § 클러스터 상태 백업.

**Files:**
- Create: `gitops/apps/velero/Chart.yaml`
- Create: `gitops/apps/velero/values.yaml`
- Create: `gitops/argocd/apps/platform-velero.yaml`
- Modify: `infra/terraform/aws/backup.tf` (`velero/` 수명주기 규칙 추가 — 아래 Step 0)

**Interfaces:**
- Consumes: `velero/` 전용 IAM 키(`aws/backup-velero`, Step 0에서 생성) — Velero는 `[default]` 프로파일
  형식의 credentials 파일을 요구하므로 velero ns 전용 ExternalSecret으로 매핑
- Produces: S3 프리픽스 `velero/`에 백업 tarball + 6시간 주기 스케줄

- [ ] **Step 0: 버킷 `velero/` 수명주기 규칙 + 전용 IAM 사용자 추가 (backup.tf)**

Velero 도입과 함께 (1) 버킷 정리 규칙과 (2) `velero/` 프리픽스 전용 IAM 사용자를 넣는다(주체가 생기는
시점에 규칙·권한도 추가 — 인과 정합, 프리픽스 분리 유지). `local.backup_writers` 에 `"velero"` 를 추가하면
`for_each` 로 `cledyu-lab-backup-writer-velero`(`velero/*` 한정)가 자동 생성된다. 액세스 키는 수동 발급 →
`vault kv put cledyu/aws/backup-velero ...`.
```hcl
locals {
  backup_writers = toset(["postgres", "vault", "velero"]) # velero 추가
}

# lifecycle 에 규칙 추가. writer 에 DeleteObject 가 없어(무-delete 정책) Velero 는 백업을 직접 못
# 지우므로, 정리는 전적으로 lifecycle 이 담당한다 — current 만료 + non-current 잔재 정리 둘 다 둔다.
# expiration 일수(velero 보존 기간)는 Task 8 에서 확정(예: 30일). Object Lock 30일보다 짧게 두면 무의미.
  rule {
    id     = "expire-velero"
    status = "Enabled"
    filter { prefix = "velero/" }
    expiration { days = 30 }                          # Task 8 에서 보존 기간 확정
    noncurrent_version_expiration { noncurrent_days = 30 }
  }
```
Run: `cd infra/terraform/aws && terraform validate && terraform fmt -check backup.tf`
Expected: valid + fmt 통과.

- [ ] **Step 1: Velero 자격증명 ExternalSecret (velero ns)**

Velero는 AWS credentials를 `[default]\naws_access_key_id=...\naws_secret_access_key=...` 형식의
단일 파일 키로 요구한다. Task 2 Secret과 키 형식이 다르므로 ESO `template`으로 렌더한다:
```yaml
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: cledyu-backup-s3-velero
  namespace: velero
spec:
  refreshInterval: 1h
  secretStoreRef: { name: vault-backend, kind: ClusterSecretStore }
  target:
    name: velero-s3-credentials
    creationPolicy: Owner
    template:
      data:
        cloud: |
          [default]
          aws_access_key_id={{ .access_key_id }}
          aws_secret_access_key={{ .secret_access_key }}
  data:
    - secretKey: access_key_id
      remoteRef: { key: aws/backup-velero, property: access_key_id }
    - secretKey: secret_access_key
      remoteRef: { key: aws/backup-velero, property: secret_access_key }
```

- [ ] **Step 2: Velero 차트 래핑 (BSL = S3 직행, selective 범위)**

`gitops/apps/velero/Chart.yaml`:
```yaml
apiVersion: v2
name: velero
version: 0.1.0
dependencies:
  - name: velero
    version: "8.0.0"
    repository: https://vmware-tanzu.github.io/helm-charts
```

`gitops/apps/velero/values.yaml`:
```yaml
velero:
  initContainers:
    - name: velero-plugin-for-aws
      image: velero/velero-plugin-for-aws:v1.11.0
      volumeMounts:
        - { name: plugins, mountPath: /target }
  credentials:
    existingSecret: velero-s3-credentials   # Step 1 ESO 산출물
  configuration:
    backupStorageLocation:
      - name: default
        provider: aws
        bucket: cledyu-lab-dr-backups
        prefix: velero                       # 버킷 경로 분리(스펙 § 버킷 프리픽스)
        config:
          region: ap-northeast-2
          # s3Url 생략 = AWS S3 직행(MinIO 없음). 로컬 사본 필요 시 BSL 추가로 확장
    # PV 스냅샷 미사용 — 데이터는 wal-g/raft 담당. 오브젝트만 백업
    volumeSnapshotLocation: []
  snapshotsEnabled: false
  deployNodeAgent: false                     # 파일시스템 백업 미사용
  schedules:
    cluster-state:
      schedule: "0 */6 * * *"                # 6시간 주기(클러스터 상태 RPO)
      template:
        ttl: 168h
        # selective: 컨트롤플레인만, 세션/KubeVirt 제외
        includedNamespaces: ["api", "web", "ai-tutor", "keycloak", "postgres", "vault"]
        excludedNamespaces: ["lab-sessions", "kubevirt", "kubevirt-cdi"]
        snapshotVolumes: false
```

> 범위(includedNamespaces)는 실제 컨트롤플레인 네임스페이스에 맞춰 실행 단계에서 `kubectl get ns`로
> 확정한다. `lab-sessions`·KubeVirt 제외는 "세션은 버림" 결정과의 충돌 방지(스펙 §비목표).

- [ ] **Step 3: 검증 (의존성 빌드 + 렌더)**

Run: `cd gitops/apps/velero && helm dependency build && helm template . --namespace velero | kubeconform -strict -ignore-missing-schemas`
Expected: Velero Deployment/BackupStorageLocation/Schedule 렌더 성공, snapshotsEnabled=false 반영.

- [ ] **Step 4: ArgoCD Application 작성 + Sync**

`gitops/argocd/apps/platform-velero.yaml` (골격 동일, path `gitops/apps/velero`, releaseName `velero`, namespace `velero`).
Run:
```bash
argocd app sync platform-velero
kubectl -n velero rollout status deploy/velero
kubectl -n velero get backupstoragelocation default -o jsonpath='{.status.phase}'
```
Expected: Velero `Available`, BSL phase `Available`.

- [ ] **Step 5: 수동 백업 트리거 + S3 도달 확인**

Run:
```bash
kubectl -n velero exec deploy/velero -- /velero backup create cluster-state-test --wait
aws s3 ls s3://cledyu-lab-dr-backups/velero/ --recursive | head
```
Expected: 백업 Completed, `velero/` 하위에 tarball + 메타데이터 객체 존재.

- [ ] **Step 6: Commit**

```bash
git add gitops/apps/velero gitops/argocd/apps/platform-velero.yaml
git commit -m "feat(dr): Velero 클러스터 상태 백업(S3 직행, 컨트롤플레인 한정)"
```

---

## Self-Review

**Spec coverage (스펙 §백업 계층 대비):**
- Postgres wal-g/PITR → Task 3·4 (CNPG로 실현, 스펙의 wal-g를 CNPG barman으로 구체화 — 브레인스토밍서 사용자 승인) ✓
- Vault raft 스냅샷 → Task 5 ✓
- 클러스터 상태(Velero, 2차 추가) → Task 8 (오브젝트만·PV 스냅샷 끔, selective) ✓
- S3 버킷/자격증명(버전ing, Vault→ESO) → Task 1·2 ✓
- Keycloak DB → **본 플랜 범위 밖(Plan A-2)**, 스펙·메모리에 분리 근거 명시 ✓
- 알림 체계 → Plan D(분리). 본 플랜은 백업 생성까지 ✓
- 복원 드릴/RPO 실측 → Task 7 ✓

**Placeholder scan:** Task 7 Step 1의 `targetTime`은 드릴 실행 시각 의존이라 런타임 값(플레이스홀더 아님, 실행자가 5분 전 UTC 삽입). 그 외 TODO/TBD 없음.

**Type consistency:** Secret `cledyu-backup-s3` 키명은 `ACCESS_KEY_ID`/`ACCESS_SECRET_KEY`로 Task 2·4·5 일관. CNPG 서비스명 `cledyu-pg-rw`는 Task 4 cutover와 일치.

**미해결/실행 중 확인 필요:**
- CNPG 오퍼레이터 차트 버전(0.22.1)·barman in-tree 지원 여부는 실행 시 최신 확인
- `hashicorp/vault` 이미지의 aws CLI 부재 시 sidecar 전환(Task 5 Step 2 주석)
