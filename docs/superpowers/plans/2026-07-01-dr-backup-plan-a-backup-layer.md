# DR/백업 Plan A — 백업 계층 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 온프렘 durable 데이터(cledyu Postgres, Vault, 범용 PVC)를 S3로 오프사이트 백업하고, Postgres는 CloudNativePG로 이관해 WAL 연속 아카이빙·PITR을 확보한다.

**Architecture:** S3 백업 버킷 + 전용 IAM 키(Terraform) → Vault→ESO로 클러스터에 자격증명 주입 → (1) cledyu Postgres를 CNPG Cluster로 이관해 barman S3 백업, (2) Vault raft 스냅샷 CronJob, (3) Longhorn backup target+RecurringJob. 마지막에 PITR 복원 드릴로 RPO 실측.

**Tech Stack:** Terraform(AWS S3/IAM), External Secrets Operator, CloudNativePG operator, HashiCorp Vault(raft), Longhorn, ArgoCD(App-of-Apps).

## Global Constraints

- 리전: `ap-northeast-2` (기존 EC2 오버플로우와 동일, `docs/RUNBOOK/ec2-overflow.md`)
- 백업 버킷(단일): `cledyu-dr-backups`, 프리픽스 `postgres/`, `vault/`, `longhorn/`
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
- Produces: S3 버킷 `${var.name_prefix}-dr-backups`(= `cledyu-lab-dr-backups`) — 버전ing·퍼블릭차단·SSE·수명주기 포함
- Produces: IAM 사용자 `cledyu-lab-backup-writer` + 해당 버킷 한정 정책. **액세스 키는 Terraform이 만들지 않는다**(api/engine 관례 — 장기 키를 GCS state에 안 남김). apply 후 콘솔/CLI로 수동 발급 → Vault.

- [ ] **Step 1: 백업 리소스 정의 작성**

`infra/terraform/aws/backup.tf` — 컨벤션 준수: `var.name_prefix` 사용, 정책은 `data.aws_iam_policy_document`
(기존 baker/api/engine 스타일), 퍼블릭차단·SSE(AES256)·프리픽스별 수명주기 포함, `aws_iam_access_key`는
두지 않음. (실제 파일 내용이 소스 오브 트루스 — 이 저장소의 `infra/terraform/aws/backup.tf` 참조)

핵심 리소스:
```hcl
resource "aws_s3_bucket"                            "dr_backups" { bucket = "${var.name_prefix}-dr-backups" }
resource "aws_s3_bucket_versioning"                 "dr_backups" { ... status = "Enabled" }
resource "aws_s3_bucket_public_access_block"        "dr_backups" { ... 4개 true }
resource "aws_s3_bucket_server_side_encryption_configuration" "dr_backups" { ... AES256 }
resource "aws_s3_bucket_lifecycle_configuration"    "dr_backups" { postgres/ backstop + vault/ 90d 만료 }
resource "aws_iam_user"                             "backup" { name = "${var.name_prefix}-backup-writer" }
data     "aws_iam_policy_document"                  "backup" { PutObject/GetObject/DeleteObject/ListBucket/GetBucketLocation, 버킷 한정 }
resource "aws_iam_user_policy"                      "backup" { policy = data....json }
# aws_iam_access_key 리소스 없음 — 수동 발급
```

- [ ] **Step 2: outputs 추가**

`infra/terraform/aws/outputs.tf` 에 append (시크릿 output 없음 — api_iam_user 스타일):
```hcl
output "backup_bucket" {
  description = "DR 백업 S3 버킷명."
  value       = aws_s3_bucket.dr_backups.bucket
}

output "backup_iam_user" {
  description = "백업용 IAM 사용자명 — 이 사용자의 액세스 키를 발급해 Vault(cledyu/aws/backup)에 보관한다."
  value       = aws_iam_user.backup.name
}
```

- [ ] **Step 3: 검증 (validate)**

Run: `cd infra/terraform/aws && terraform init -backend=false && terraform validate && terraform fmt -check backup.tf outputs.tf`
Expected: `Success! The configuration is valid.` + fmt 통과

- [ ] **Step 4: apply (AWS+GCP 자격증명 필요)**

Run: `cd infra/terraform/aws && terraform init && terraform plan`
Expected: `Plan: N to add, 0 to change, 0 to destroy` — **기존 리소스 change/destroy가 0인지 확인 후** `terraform apply`.
`terraform output backup_bucket` → `cledyu-lab-dr-backups`, `terraform output backup_iam_user` → `cledyu-lab-backup-writer`

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
- Consumes: Task 1의 IAM 사용자 `cledyu-lab-backup-writer`(콘솔/CLI로 수동 발급한 액세스 키)
- Produces: 네임스페이스 `postgres`/`vault` 각각에 Secret `cledyu-backup-s3`
  (키: `ACCESS_KEY_ID`, `ACCESS_SECRET_KEY`) — Task 4·5가 참조. (Longhorn은 키명 규격이 달라 Task 6 전용 ES)

- [ ] **Step 1: 액세스 키 수동 발급 + Vault kv 등록 (수동 사전작업)**

apply 후 콘솔/CLI로 `cledyu-lab-backup-writer` 사용자의 액세스 키를 발급한다:
```bash
aws iam create-access-key --user-name cledyu-lab-backup-writer
# 출력의 AccessKeyId / SecretAccessKey 를 Vault 에 등록
vault kv put cledyu/aws/backup \
  access_key_id="<AccessKeyId>" \
  secret_access_key="<SecretAccessKey>"
```
Expected: `Success! Data written to: cledyu/aws/backup`

- [ ] **Step 2: Helm 차트 스캐폴드 작성**

`gitops/apps/backup-secrets/Chart.yaml`:
```yaml
apiVersion: v2
name: backup-secrets
version: 0.1.0
```

`gitops/apps/backup-secrets/values.yaml`:
```yaml
# ESO가 S3 백업 자격증명을 뿌릴 네임스페이스 목록
namespaces:
  - postgres
  - vault
  - longhorn-system
```

`gitops/apps/backup-secrets/templates/externalsecret.yaml`:
```yaml
{{- range .Values.namespaces }}
---
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: cledyu-backup-s3
  namespace: {{ . }}
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
        key: aws/backup
        property: access_key_id
    - secretKey: ACCESS_SECRET_KEY
      remoteRef:
        key: aws/backup
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
Expected: 에러 없음. 3개 ExternalSecret 렌더됨(`namespace: postgres/vault/longhorn-system`).

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

**Files:**
- Create: `gitops/apps/cnpg-operator/Chart.yaml`
- Create: `gitops/apps/cnpg-operator/values.yaml`
- Create: `gitops/argocd/apps/data-cnpg-operator.yaml`

**Interfaces:**
- Produces: `postgresql.cnpg.io` CRD군(`Cluster`, `ScheduledBackup`, `Backup`) — Task 4가 사용

- [ ] **Step 1: 오퍼레이터 차트 래핑 작성**

`gitops/apps/cnpg-operator/Chart.yaml`:
```yaml
apiVersion: v2
name: cnpg-operator
version: 0.1.0
dependencies:
  - name: cloudnative-pg
    version: "0.22.1"
    repository: https://cloudnative-pg.github.io/charts
```

`gitops/apps/cnpg-operator/values.yaml`:
```yaml
cloudnative-pg:
  crds:
    create: true
  monitoring:
    podMonitorEnabled: true  # 기존 kube-prometheus-stack이 수집
```

- [ ] **Step 2: 의존성 빌드 + 검증**

Run: `cd gitops/apps/cnpg-operator && helm dependency build && helm template . | kubeconform -strict -ignore-missing-schemas`
Expected: 렌더 성공(Deployment `cnpg-cloudnative-pg` 등).

- [ ] **Step 3: ArgoCD Application 작성**

`gitops/argocd/apps/data-cnpg-operator.yaml` (Task 2 Application과 동일 골격, 값만 교체):
```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: data-cnpg-operator
  namespace: argocd
  finalizers:
    - resources-finalizer.argocd.argoproj.io
spec:
  project: default
  source:
    repoURL: https://github.com/requset700k/Cledyu.git
    targetRevision: main
    path: gitops/apps/cnpg-operator
    helm:
      releaseName: cnpg
  destination:
    server: https://kubernetes.default.svc
    namespace: cnpg-system
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
      - ServerSideApply=true
```

- [ ] **Step 4: Sync 후 CRD·오퍼레이터 확인**

Run:
```bash
argocd app sync data-cnpg-operator
kubectl get crd clusters.postgresql.cnpg.io
kubectl -n cnpg-system rollout status deploy/cnpg-cloudnative-pg
```
Expected: CRD 존재, 오퍼레이터 Deployment `Available`.

- [ ] **Step 5: Commit**

```bash
git add gitops/apps/cnpg-operator gitops/argocd/apps/data-cnpg-operator.yaml
git commit -m "feat(dr): CloudNativePG 오퍼레이터 설치"
```

---

### Task 4: cledyu Postgres를 CNPG Cluster로 이관 + S3 백업

> 가장 위험한 태스크. 기존 데이터를 CNPG로 논리 임포트 → 검증 → api DSN cutover → 구 StatefulSet 폐기 순서로 진행한다.

**Files:**
- Create: `gitops/apps/postgres-cnpg/Chart.yaml`
- Create: `gitops/apps/postgres-cnpg/values.yaml`
- Create: `gitops/apps/postgres-cnpg/templates/cluster.yaml`
- Create: `gitops/apps/postgres-cnpg/templates/scheduledbackup.yaml`
- Create: `gitops/argocd/apps/data-postgres-cnpg.yaml`
- Modify(폐기): `gitops/argocd/apps/data-postgres.yaml` (구 StatefulSet 앱 — Step 8에서 제거)

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
      destinationPath: "s3://cledyu-dr-backups/postgres"
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

> 주: CNPG ≥1.26에서 in-tree `barmanObjectStore`는 deprecated이고 barman-cloud 플러그인이 후속 경로다.
> 위 chart 의존 오퍼레이터 버전(0.22.1)에서는 in-tree가 동작한다. 오퍼레이터 상향 시 플러그인으로 이관.

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

- [ ] **Step 4: ArgoCD Application 작성 + Sync (임포트 실행)**

`gitops/argocd/apps/data-postgres-cnpg.yaml` (골격 동일, path `gitops/apps/postgres-cnpg`, releaseName `postgres-cnpg`, namespace `postgres`).
Run:
```bash
argocd app sync data-postgres-cnpg
kubectl -n postgres wait --for=condition=Ready cluster/cledyu-pg --timeout=600s
```
Expected: Cluster `Ready`. 임포트 Job 이 구 DB를 논리 복제.

- [ ] **Step 5: 데이터 일치 검증 (cutover 전 필수)**

Run:
```bash
# 구 DB row 수
kubectl -n postgres exec postgres-0 -- psql -U cledyu -d cledyu -tAc \
  "select count(*) from session_progress"
# 신 CNPG row 수
kubectl -n postgres exec cledyu-pg-1 -- psql -U cledyu -d cledyu -tAc \
  "select count(*) from session_progress"
```
Expected: 두 값 **동일**. 다르면 cutover 중단하고 임포트 재점검.

- [ ] **Step 6: 첫 backup S3 도달 확인**

Run:
```bash
kubectl -n postgres exec cledyu-pg-1 -- \
  cnpg backup cledyu-pg   # 즉시 base backup 트리거(또는 ScheduledBackup 대기)
aws s3 ls s3://cledyu-dr-backups/postgres/ --recursive | head
```
Expected: `postgres/` 하위에 base backup + WAL 객체 존재.

- [ ] **Step 7: api DSN cutover**

Run:
```bash
# CNPG rw 서비스로 DSN 교체 (기존 cledyu 사용자/비밀번호 유지)
vault kv patch cledyu/db/api \
  dsn="postgresql://cledyu:$(vault kv get -field=password cledyu/db/postgres)@cledyu-pg-rw.postgres.svc:5432/cledyu?sslmode=require"
# ESO 강제 리프레시 후 api 롤아웃
kubectl -n api annotate externalsecret cledyu-api-db force-sync=$(date +%s) --overwrite
kubectl -n api rollout restart deploy/api
kubectl -n api rollout status deploy/api
```
Expected: api 파드 Ready, `/health` 200. 세션 진도 조회 정상.

- [ ] **Step 8: 구 Postgres StatefulSet 폐기**

Run:
```bash
git rm -r gitops/apps/postgres gitops/argocd/apps/data-postgres.yaml
```
(ArgoCD가 prune으로 구 StatefulSet/PVC 제거. PVC는 `deletionPolicy: Retain` 아님에 유의 —
삭제 전 Step 6 백업 존재를 반드시 확인했어야 함.)

- [ ] **Step 9: Commit**

```bash
git add gitops/apps/postgres-cnpg gitops/argocd/apps/data-postgres-cnpg.yaml
git commit -m "feat(dr): cledyu Postgres를 CNPG로 이관·S3 WAL 백업, 구 StatefulSet 폐기"
```

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
                    s3://cledyu-dr-backups/vault/vault-raft-$TS.snap
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
aws s3 ls s3://cledyu-dr-backups/vault/
```
Expected: job 완료, `vault/vault-raft-*.snap` 객체 존재.

- [ ] **Step 5: Commit**

```bash
git add gitops/apps/vault-backup gitops/argocd/apps/platform-vault-backup.yaml
git commit -m "feat(dr): Vault raft 스냅샷 CronJob S3 백업"
```

---

### Task 6: Longhorn backup target + RecurringJob (범용 PVC)

**Files:**
- Create: `gitops/apps/kubevirt/longhorn-backuptarget.yaml`
- Create: `gitops/apps/kubevirt/longhorn-recurringjob.yaml`

> Longhorn 리소스는 기존 `gitops/apps/kubevirt/`(storageclass-longhorn-r2.yaml 등)와 동거.

**Interfaces:**
- Consumes: Secret `cledyu-backup-s3`(Task 2, `longhorn-system` ns)
- Produces: Longhorn backupTarget = `s3://cledyu-dr-backups/longhorn`, 백업 RecurringJob

- [ ] **Step 1: Longhorn S3 secret 키 이름 정합화**

Longhorn은 `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY` 키를 요구한다. Task 2의 Secret 키
(`ACCESS_KEY_ID`/`ACCESS_SECRET_KEY`)와 다르므로, longhorn-system 전용 ExternalSecret 키를
Longhorn 규격으로 매핑 추가한다.

`gitops/apps/kubevirt/longhorn-backuptarget.yaml`:
```yaml
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: cledyu-backup-s3-longhorn
  namespace: longhorn-system
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: vault-backend
    kind: ClusterSecretStore
  target:
    name: longhorn-s3-backup
    creationPolicy: Owner
  data:
    - secretKey: AWS_ACCESS_KEY_ID
      remoteRef: { key: aws/backup, property: access_key_id }
    - secretKey: AWS_SECRET_ACCESS_KEY
      remoteRef: { key: aws/backup, property: secret_access_key }
---
apiVersion: longhorn.io/v1beta2
kind: BackupTarget
metadata:
  name: default
  namespace: longhorn-system
spec:
  backupTargetURL: "s3://cledyu-dr-backups@ap-northeast-2/longhorn"
  credentialSecret: longhorn-s3-backup
  pollInterval: "5m"
```

- [ ] **Step 2: RecurringJob 작성**

`gitops/apps/kubevirt/longhorn-recurringjob.yaml`:
```yaml
apiVersion: longhorn.io/v1beta2
kind: RecurringJob
metadata:
  name: backup-hourly
  namespace: longhorn-system
spec:
  cron: "0 * * * *"     # 매시 backup
  task: "backup"
  groups: ["default"]   # default 그룹 볼륨 전체
  retain: 24
  concurrency: 2
```

- [ ] **Step 3: 검증**

Run: `kubeconform -strict -ignore-missing-schemas gitops/apps/kubevirt/longhorn-backuptarget.yaml gitops/apps/kubevirt/longhorn-recurringjob.yaml`
Expected: 통과(Longhorn CRD 스키마는 ignore-missing).

- [ ] **Step 4: Sync 후 backupTarget Available 확인**

Run:
```bash
argocd app sync platform-kubevirt   # 기존 앱이 kubevirt 경로 포함 시. 아니면 해당 앱 sync
kubectl -n longhorn-system get backuptarget default -o jsonpath='{.status.available}'
```
Expected: `true`. `false`면 자격증명/URL 점검.

- [ ] **Step 5: Commit**

```bash
git add gitops/apps/kubevirt/longhorn-backuptarget.yaml gitops/apps/kubevirt/longhorn-recurringjob.yaml
git commit -m "feat(dr): Longhorn S3 backup target·시간별 RecurringJob"
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
        destinationPath: "s3://cledyu-dr-backups/postgres"
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

## Self-Review

**Spec coverage (스펙 §백업 계층 대비):**
- Postgres wal-g/PITR → Task 3·4 (CNPG로 실현, 스펙의 wal-g를 CNPG barman으로 구체화 — 브레인스토밍서 사용자 승인) ✓
- Vault raft 스냅샷 → Task 5 ✓
- 범용 PVC Longhorn → Task 6 ✓
- S3 버킷/자격증명(버전ing, Vault→ESO) → Task 1·2 ✓
- Keycloak DB → **본 플랜 범위 밖(Plan A-2)**, 스펙·메모리에 분리 근거 명시 ✓
- 알림 체계 → Plan D(분리). 본 플랜은 백업 생성까지 ✓
- 복원 드릴/RPO 실측 → Task 7 ✓

**Placeholder scan:** Task 7 Step 1의 `targetTime`은 드릴 실행 시각 의존이라 런타임 값(플레이스홀더 아님, 실행자가 5분 전 UTC 삽입). 그 외 TODO/TBD 없음.

**Type consistency:** Secret `cledyu-backup-s3` 키명은 `ACCESS_KEY_ID`/`ACCESS_SECRET_KEY`로 Task 2·4·5 일관. Longhorn만 `AWS_ACCESS_KEY_ID` 규격이 달라 Task 6에서 별도 매핑 Secret(`longhorn-s3-backup`)으로 분리 — 의도적. CNPG 서비스명 `cledyu-pg-rw`는 Task 4 cutover와 일치.

**미해결/실행 중 확인 필요:**
- CNPG 오퍼레이터 차트 버전(0.22.1)·barman in-tree 지원 여부는 실행 시 최신 확인
- `hashicorp/vault` 이미지의 aws CLI 부재 시 sidecar 전환(Task 5 Step 2 주석)
