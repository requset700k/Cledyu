# DR Failback · 아카이브 계보 reconciliation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 온프렘 복구 후 EKS(재해 중 최신 primary)의 데이터를 온프렘으로 되돌리는 failback을, 반복 재해에도 아카이브 충돌 없이(drEpoch 계보) 무손실로 수행하는 매니페스트·IAM·런북을 구현한다.

**Architecture:** S3 barman 루프백 역복제 — EKS DR primary가 `postgres-dr/`·`keycloak-dr/`에 아카이빙(backupEnabled+ScheduledBackup), 온프렘이 신설 failback 차트로 거기서 recovery. serverName에 단조 정수 `drEpoch` 접미를 넣어 매 사이트-전환이 빈 새 아카이브에 쓰게 함. cutover 시 EKS 쓰기 quiesce로 무손실, DNS(terraform)로 단일 권한 전환. 수동 런북 + GitOps path-swap.

**Tech Stack:** Terraform(IAM/S3/Route53, AWS provider), CloudNativePG 1.27.1(in-tree barmanObjectStore, bootstrap.recovery, managed.roles, ScheduledBackup), Helm(gitops 차트), ArgoCD(path-swap), ESO(Vault→Secret), S3(Object Lock GOVERNANCE 30d).

## Global Constraints

- **기준 baseline:** `origin/main`(730d82d). 작업 브랜치 `feat/dr-failback-reconciliation`(origin/main에서 분기, 생성됨).
- **리전:** `ap-northeast-2`. 버킷 `cledyu-lab-dr-backups`, KMS `alias/cledyu-lab-dr-backups`.
- **CNPG 이미지 digest 고정(변경 금지):** postgres `ghcr.io/cloudnative-pg/postgresql:16.4@sha256:99be063781d171d3971089b49c992706bdab9ccbd2b57cdf126c7542773aedfe` · keycloak `ghcr.io/cloudnative-pg/postgresql:18.2-system-trixie@sha256:3f44daf4c2ddea3481b018b3b004f91a439b93fc995a387f9aff69058bef19ac`.
- **CNPG 오퍼레이터:** 온프렘 chart 0.26.1=1.27.1(in-tree barman 지원). EKS도 동일 계열.
- **quote 규율:** CNPG Cluster의 storage.size·resources 수량은 반드시 `| quote` — 미quote 시 bare int로 렌더돼 ArgoCD 영구 OutOfSync.
- **terraform 규율:** tfvars 부재 → 전체 plan/apply 금지, `-target`만. 리소스/변수/출력 변경 커밋엔 재생성 `README.md` 동반(pre-commit `terraform_docs` 훅). 정책은 `data.aws_iam_policy_document`, `var.name_prefix` 접두.
- **검증:** `terraform fmt -check`·`terraform validate` / `helm template <chart> | kubeconform -strict -ignore-missing-schemas` / grep 어서션.
- **커밋:** 사용자 확인 전 커밋 금지·커밋은 사용자가 실행(실행 단계에서 명령어만 제공). 커밋 메시지에 Co-Authored-By 금지, heredoc 금지(`git commit -m`).
- **drEpoch 정합 계약:** 온프렘 운영·DR·failback 6개 values의 `drEpoch`는 한 커밋으로 lockstep. failover는 bump 안 함, failback 완료 시만 N→N+1.
- **드릴 불변식:** 모든 변경은 `drEpoch=0`·`backupEnabled=false`에서 현행과 동일 렌더(1차 드릴 경로 불변). 실증은 각 태스크의 grep 어서션.

---

### Task 1: Terraform failback 준비 — 온프렘 writer 키 `-dr/` read 확장 (backup.tf) + DNS 주석

failback recovery가 `postgres-dr/`·`keycloak-dr/`를 읽으려면 온프렘 정적 키에 GetObject/ListBucket을 확장한다. KMS는 이미 `UseBackupKmsKey`에 Decrypt 보유 → 무변경. `postgres`·`keycloak` writer만 확장, `vault`·`velero`는 불변. 아울러 DNS 원복 절차를 route53 record 옆 주석으로 남긴다(신규 리소스 없음).

**Files:**
- Modify: `infra/terraform/aws/backup.tf` (data.aws_iam_policy_document.backup)
- Modify: `infra/terraform/aws/public-ingress.tf` (aws_route53_record.public 위 failback 주석)
- Modify: `infra/terraform/aws/README.md` (terraform-docs 재생성)

**Interfaces:**
- Produces: `postgres`/`keycloak` writer 키가 `{prefix}-dr/*` GetObject + ListBucket 가능 (Task 4 failback 차트가 이 키로 recovery read).

- [ ] **Step 1: `dr_readers` local 추가**

`infra/terraform/aws/backup.tf`의 `locals { backup_writers = ... }` 블록 근처에 추가:

```hcl
  # -dr/ 프리픽스 read 확장 대상(failback recovery 소스). vault/velero 는 -dr 개념 없어 제외.
  dr_readers = toset(["postgres", "keycloak"])
```

- [ ] **Step 2: `ReadDrPrefix` dynamic statement 추가**

`data "aws_iam_policy_document" "backup"` 안, `PrefixObjects` statement 바로 뒤에 추가:

```hcl
  # failback: 온프렘 recovery 가 자기 DR 형제 프리픽스(-dr/)에서 base backup+WAL 을 읽는다(read 전용).
  # PutObject/AbortMultipart 부여 안 함 — 온프렘은 -dr 에 쓰지 않는다(최소권한). postgres/keycloak 만.
  dynamic "statement" {
    for_each = contains(local.dr_readers, each.key) ? [1] : []
    content {
      sid       = "ReadDrPrefix"
      actions   = ["s3:GetObject"]
      resources = ["${aws_s3_bucket.dr_backups.arn}/${each.key}-dr/*"]
    }
  }
```

- [ ] **Step 3: `ListOwnPrefix` 조건에 `-dr` 프리픽스 추가**

기존 `ListOwnPrefix` statement의 `condition.values`를 조건식으로 교체:

```hcl
  statement {
    sid       = "ListOwnPrefix"
    actions   = ["s3:ListBucket", "s3:ListBucketMultipartUploads"]
    resources = [aws_s3_bucket.dr_backups.arn]
    condition {
      test     = "StringLike"
      variable = "s3:prefix"
      # failback: dr_readers 는 자기 -dr/ 목록도 허용(barman backup catalog 조회).
      values = contains(local.dr_readers, each.key) ? ["${each.key}/*", each.key, "${each.key}-dr/*", "${each.key}-dr"] : ["${each.key}/*", each.key]
    }
  }
```

- [ ] **Step 4: `DenyForeignPrefixListing` 예외에 `-dr` 프리픽스 추가**

기존 `DenyForeignPrefixListing` statement의 두 번째 `condition`(StringNotLike)의 `values`를 동일 조건식으로 교체 — **누락 시 `postgres-dr/*`가 `postgres/*`와 불일치해 이 Deny에 걸려 ListBucket 차단됨**:

```hcl
    condition {
      test     = "StringNotLike"
      variable = "s3:prefix"
      values   = contains(local.dr_readers, each.key) ? ["${each.key}/*", each.key, "${each.key}-dr/*", "${each.key}-dr"] : ["${each.key}/*", each.key]
    }
```

- [ ] **Step 5: public-ingress.tf 에 failback 주석 추가**

`infra/terraform/aws/public-ingress.tf`의 `resource "aws_route53_record" "public"` 바로 위에 추가:

```hcl
# failback: 재해 중 이 레코드는 CLI(route53 change-resource-record-sets)로 EKS ALB 를 가리키게
# 바뀐다(state 밖 드리프트). 온프렘 복구 후 원복 =
#   terraform apply -var enable_public_ingress=true -target=aws_route53_record.public
# ⚠️ -var enable_public_ingress=true 필수 — 생략 시 count/for_each=0 으로 평가돼 레코드가 destroy 된다.
# 데이터 정합 확인 후 최후 단계(split-brain 단일 권한 스위치). 상세: docs/RUNBOOK/dr-failback.md.
```

- [ ] **Step 6: fmt·validate**

Run: `cd infra/terraform/aws && terraform fmt -check && terraform validate`
Expected: `Success! The configuration is valid.` (fmt 차이 없음)

- [ ] **Step 7: `-target` plan 으로 postgres/keycloak 정책만 변화 확인 (destroy 없음)**

Run: `terraform plan -target='aws_iam_user_policy.backup["postgres"]' -target='aws_iam_user_policy.backup["keycloak"]'`
Expected: `~ update in-place` 2건(policy json 변경), `destroy` 0건. vault/velero 정책 미포함. (public-ingress 주석은 no-op plan.)

- [ ] **Step 8: README 재생성**

Run: `cd infra/terraform/aws && terraform-docs markdown table --output-file README.md .`
Expected: README.md 갱신(변수/출력 표). git diff 로 확인.

- [ ] **Step 9: Commit**

```bash
git add infra/terraform/aws/backup.tf infra/terraform/aws/public-ingress.tf infra/terraform/aws/README.md
git commit -m "feat(dr): failback 위해 온프렘 postgres·keycloak writer -dr/ read 확장 + DNS 원복 주석"
```

---

### Task 2: 운영 차트 drEpoch 파라미터화 (postgres-cnpg · keycloak-pg)

failback 후 전진 아카이빙이 새 epoch 경로로 가도록 backup serverName을 명시·파라미터화한다. `drEpoch=0`은 현행과 동일 렌더(무-마이그레이션). bootstrap import fail-safe·externalClusters·주석은 **그대로 둔다**(Edit만).

> ⚠️ **라이브 프로덕션 변경**: postgres-cnpg/keycloak-pg는 온프렘 현역 DB다. drEpoch=0에서 명시 serverName은 barman default(=cluster명)와 **동일값**이라 아카이브 경로 무변경·`"Expected empty archive"` 재검증 없음(실측 확인). 스펙 필드 1개 추가라 ArgoCD가 1회 ServerSideApply sync만 하고 **파드 롤아웃 없음**(backup config 변경은 pod-spec 아님). 즉 archive-neutral.

**Files:**
- Modify: `gitops/apps/postgres-cnpg/values.yaml` · `gitops/apps/postgres-cnpg/templates/cluster.yaml`
- Modify: `gitops/apps/keycloak-pg/values.yaml` · `gitops/apps/keycloak-pg/templates/cluster.yaml`

**Interfaces:**
- Produces: 운영 backup serverName = `cledyu-pg{-e{N}}`·`keycloak-pg{-e{N}}` (Task 3 DR 차트 recovery 소스가 이 이름을 f(N)으로 읽음).

- [ ] **Step 1: (관찰) 현재 렌더에 serverName 부재 확인**

Run: `helm template gitops/apps/postgres-cnpg | grep -A12 barmanObjectStore | grep serverName || echo "NO serverName (defaults to cledyu-pg)"`
Expected: `NO serverName ...` (현재 암묵 default).

- [ ] **Step 2: values 에 `drEpoch` 추가**

`gitops/apps/postgres-cnpg/values.yaml` 끝에 추가:

```yaml
# failback 아카이브 계보 epoch(완료된 failback 횟수). 0=현행(접미 없음). failback 런북이 lockstep bump.
# backup serverName = cledyu-pg{-e{drEpoch}} → 매 failback 후 빈 새 아카이브 경로(Object Lock 충돌 회피).
drEpoch: 0
```

`gitops/apps/keycloak-pg/values.yaml` 끝에 동일하게 추가(주석의 `cledyu-pg`→`keycloak-pg`).

- [ ] **Step 3: cluster.yaml backup 블록에 명시 serverName 추가**

`gitops/apps/postgres-cnpg/templates/cluster.yaml`의 `backup.barmanObjectStore.destinationPath` 아래에 추가:

```yaml
      # failback 계보: drEpoch=0 은 cledyu-pg(현행 동일), ≥1 은 cledyu-pg-e{N}(빈 새 경로).
      serverName: "cledyu-pg{{- if gt (int .Values.drEpoch) 0 }}-e{{ .Values.drEpoch }}{{- end }}"
```

`gitops/apps/keycloak-pg/templates/cluster.yaml`의 backup 블록에 동일(`cledyu-pg`→`keycloak-pg`).

- [ ] **Step 4: (검증) drEpoch=0 현행 불변 + drEpoch=2 접미 확인**

Run:
```
helm template gitops/apps/postgres-cnpg | grep 'serverName:'
helm template gitops/apps/postgres-cnpg --set drEpoch=2 | grep 'serverName:'
```
Expected: 첫 줄 `serverName: cledyu-pg`, 둘째 줄 `serverName: cledyu-pg-e2`.

- [ ] **Step 5: kubeconform**

Run: `helm template gitops/apps/postgres-cnpg | kubeconform -strict -ignore-missing-schemas` (keycloak-pg 동일)
Expected: 스키마 에러 없음(CNPG CRD는 ignore-missing-schemas).

- [ ] **Step 6: Commit**

```bash
git add gitops/apps/postgres-cnpg/values.yaml gitops/apps/postgres-cnpg/templates/cluster.yaml gitops/apps/keycloak-pg/values.yaml gitops/apps/keycloak-pg/templates/cluster.yaml
git commit -m "feat(dr): 운영 CNPG 차트 backup serverName 을 drEpoch 로 파라미터화(failback 계보)"
```

---

### Task 3: DR 차트 drEpoch + postgres-cnpg-dr ScheduledBackup

EKS DR 차트가 recovery 소스(f(N))·DR 아카이브(f_dr(N+1)) serverName을 drEpoch에서 파생하고, base backup anchor를 위해 `postgres-cnpg-dr`에 ScheduledBackup을 추가(keycloak-pg-dr엔 이미 있음). `drEpoch=0`·`backupEnabled=false`에서 현행 동일 렌더.

**Files:**
- Modify: `gitops/apps/postgres-cnpg-dr/values.yaml` · `templates/cluster.yaml` · **Create** `templates/scheduledbackup.yaml`
- Modify: `gitops/apps/keycloak-pg-dr/values.yaml` · `templates/cluster.yaml`

**Interfaces:**
- Consumes: Task 2 운영 serverName f(N) (recovery 소스).
- Produces: DR 아카이브 serverName `cledyu-pg-dr-e{N+1}`·`keycloak-pg-dr-e{N+1}` (Task 4 failback 차트 recovery 소스).

- [ ] **Step 1: DR values 에 drEpoch 추가**

`gitops/apps/postgres-cnpg-dr/values.yaml`·`keycloak-pg-dr/values.yaml` 끝에:

```yaml
# 운영 차트와 lockstep 되는 failback epoch. recovery 소스=f(N), 자체 -dr 아카이브=f_dr(N+1).
drEpoch: 0
```

- [ ] **Step 2: externalClusters recovery 소스 serverName 파라미터화**

`postgres-cnpg-dr/templates/cluster.yaml`의 `externalClusters[0].barmanObjectStore.serverName: cledyu-pg`를 교체:

```yaml
        # 운영이 f(N) 으로 쓴 아카이브를 읽는다(drEpoch=0 → cledyu-pg, 현행 동일).
        serverName: "cledyu-pg{{- if gt (int .Values.drEpoch) 0 }}-e{{ .Values.drEpoch }}{{- end }}"
```

`keycloak-pg-dr` 동일(`cledyu-pg`→`keycloak-pg`).

- [ ] **Step 3: 자체 -dr 아카이브 serverName 을 f_dr(N+1) 로 파라미터화**

`postgres-cnpg-dr/templates/cluster.yaml`의 `{{- if .Values.backupEnabled }}` 블록 내 `backup.barmanObjectStore.serverName: cledyu-pg-dr`를 교체:

```yaml
      # 진입 epoch 의 -dr 아카이브(빈 새 경로). drEpoch=0 → cledyu-pg-dr-e1.
      serverName: "cledyu-pg-dr-e{{ add (int .Values.drEpoch) 1 }}"
```

`keycloak-pg-dr` 동일(`cledyu-pg-dr`→`keycloak-pg-dr`).

- [ ] **Step 4: postgres-cnpg-dr 에 ScheduledBackup 신설 (keycloak-pg-dr 미러)**

Create `gitops/apps/postgres-cnpg-dr/templates/scheduledbackup.yaml`:

```yaml
## 복원된 DR 클러스터의 base backup(신 -dr 프리픽스). Cluster backup stanza(backupEnabled)에 종속되므로
## backupEnabled=false(default)면 렌더 안 함 — barmanObjectStore 없이 ScheduledBackup 만 있으면 실패한다.
## real-DR post-failover 에서 backupEnabled=true 로 켜면 활성. immediate:true = 복원 직후 1회 base backup
## → failback recovery 가 항상 anchor 를 갖는다(WAL 만 있고 base 없으면 recovery 불가). keycloak-pg-dr 와 대칭.
## CNPG cron 6필드(초 분 시 일 월 요일). "0 0 2 * * *" = 매일 02:00:00.
{{- if .Values.backupEnabled }}
apiVersion: postgresql.cnpg.io/v1
kind: ScheduledBackup
metadata:
  name: cledyu-pg-daily
  namespace: {{ .Release.Namespace }}
spec:
  schedule: "0 0 2 * * *"
  immediate: true
  backupOwnerReference: self
  cluster:
    name: cledyu-pg
{{- end }}
```

- [ ] **Step 5: (검증) drEpoch=0 현행 불변 + real-DR 렌더 확인**

Run:
```
# 드릴 경로(현행 불변): backupEnabled=false, drEpoch=0
helm template gitops/apps/postgres-cnpg-dr | grep -E 'serverName:|kind: ScheduledBackup'
# real-DR: backupEnabled=true, drEpoch=0
helm template gitops/apps/postgres-cnpg-dr --set backupEnabled=true | grep -E 'serverName:|kind: ScheduledBackup'
# failback 후 재-failover: drEpoch=1
helm template gitops/apps/postgres-cnpg-dr --set backupEnabled=true --set drEpoch=1 | grep 'serverName:'
```
Expected:
- 1행(드릴): `serverName: cledyu-pg`(externalClusters만), ScheduledBackup **없음**.
- 2행(real-DR): externalClusters `serverName: cledyu-pg` + backup `serverName: cledyu-pg-dr-e1` + `kind: ScheduledBackup`.
- 3행(drEpoch=1): externalClusters `serverName: cledyu-pg-e1` + backup `serverName: cledyu-pg-dr-e2`.

- [ ] **Step 6: kubeconform** — `helm template gitops/apps/postgres-cnpg-dr --set backupEnabled=true | kubeconform -strict -ignore-missing-schemas` (keycloak-pg-dr 동일). Expected: 에러 없음.

- [ ] **Step 7: Commit**

```bash
git add gitops/apps/postgres-cnpg-dr/ gitops/apps/keycloak-pg-dr/
git commit -m "feat(dr): DR CNPG 차트 serverName drEpoch 파생 + postgres-cnpg-dr ScheduledBackup(anchor)"
```

---

### Task 4: 온프렘 failback 차트 신설 (postgres-cnpg-failback · keycloak-pg-failback)

DR 차트의 온프렘 대칭판. `-dr/`(f_dr(N+1))에서 recovery, longhorn SC, 정적 키(IRSA 아님), 운영 resources·ExternalSecret 미러, backup·ScheduledBackup 없음.

**Files:**
- Create: `gitops/apps/postgres-cnpg-failback/{Chart.yaml,values.yaml,templates/cluster.yaml,templates/externalsecret.yaml}`
- Create: `gitops/apps/keycloak-pg-failback/{Chart.yaml,values.yaml,templates/cluster.yaml,templates/externalsecret.yaml}`

**Interfaces:**
- Consumes: Task 3 DR 아카이브 `cledyu-pg-dr-e{N+1}`·`keycloak-pg-dr-e{N+1}` (recovery 소스); Task 1 IAM(온프렘 키 -dr read).
- Produces: 온프렘 `cledyu-pg`/`keycloak-pg` Cluster(recovery) → adopt 후 운영 차트(Task 2)가 전진 아카이빙.

- [ ] **Step 1: postgres-cnpg-failback Chart.yaml**

Create `gitops/apps/postgres-cnpg-failback/Chart.yaml`:

```yaml
apiVersion: v2
name: postgres-cnpg-failback
description: 온프렘 failback 전용 — EKS DR 아카이브(postgres-dr/)에서 cledyu-pg 를 bootstrap.recovery 로 복원. 운영 postgres-cnpg(import fail-safe)와 별개, 온프렘 정적 키·longhorn. path-swap 으로 transient 적용 후 adopt.
type: application
version: 0.1.0
```

- [ ] **Step 2: postgres-cnpg-failback values.yaml**

```yaml
# 온프렘 failback recovery 차트 values. recovery 소스 = EKS 가 쓴 -dr 아카이브(f_dr(N+1)).
# drEpoch = failback 진입 시점의 epoch N(운영/DR과 lockstep). 소스 serverName = cledyu-pg-dr-e{N+1}.
drEpoch: 0
storage:
  # 운영 미러(durability 는 S3 barman, PV 는 워킹셋).
  className: longhorn
  size: 10Gi
resources:
  requests:
    cpu: 100m
    memory: 256Mi
  limits:
    cpu: "1"
    memory: 1Gi
```

- [ ] **Step 3: postgres-cnpg-failback templates/cluster.yaml**

```yaml
## 온프렘 failback — EKS DR 아카이브(postgres-dr/, serverName=cledyu-pg-dr-e{N+1})에서 recovery.
## 운영 postgres-cnpg(import fail-safe)와 완전 별개. path-swap 으로 data-postgres-cnpg 앱에 transient
## 적용 → stale cledyu-pg 삭제 후 ArgoCD 가 이 recovery 로 fresh 생성 → 정합 확인·drEpoch bump 후 운영 adopt.
## 정적 키(cledyu-backup-s3, postgres ns) 사용 — 온프렘은 IRSA 불가. longhorn SC·운영 resources(온프렘 QoS).
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: cledyu-pg
  namespace: {{ .Release.Namespace }}
spec:
  instances: 1
  imageName: "ghcr.io/cloudnative-pg/postgresql:16.4@sha256:99be063781d171d3971089b49c992706bdab9ccbd2b57cdf126c7542773aedfe"
  storage:
    size: {{ .Values.storage.size | quote }}
    storageClass: {{ .Values.storage.className }}
  resources:
    requests:
      cpu: {{ .Values.resources.requests.cpu | quote }}
      memory: {{ .Values.resources.requests.memory | quote }}
    limits:
      cpu: {{ .Values.resources.limits.cpu | quote }}
      memory: {{ .Values.resources.limits.memory | quote }}
  bootstrap:
    # 최신 WAL 끝까지 재생(recoveryTarget 미지정) = quiesce 시점까지 복원.
    recovery:
      source: cledyu-pg-dr-origin
  externalClusters:
    - name: cledyu-pg-dr-origin
      barmanObjectStore:
        destinationPath: "s3://cledyu-lab-dr-backups/postgres-dr"
        # EKS 가 진입 epoch 에 쓴 -dr 아카이브. drEpoch=0(첫 failback) → cledyu-pg-dr-e1.
        serverName: "cledyu-pg-dr-e{{ add (int .Values.drEpoch) 1 }}"
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
  managed:
    # 복원 데이터의 cledyu role 비번을 온프렘 Vault(postgres-credentials-cnpg)에 맞춰 ALTER
    # → S3 백업(EKS 비번) vs 온프렘 Vault 스큐 흡수. api DSN 인증 불변. DR 차트와 동일 패턴.
    roles:
      - name: cledyu
        ensure: present
        login: true
        superuser: false
        passwordSecret:
          name: postgres-credentials-cnpg
  # backup·ScheduledBackup 없음 — recovery 전용. 전진 아카이빙은 adopt 후 운영 차트가 담당.
  monitoring:
    enablePodMonitor: true
```

- [ ] **Step 4: postgres-cnpg-failback templates/externalsecret.yaml (운영/DR 미러 — path-swap prune 대비)**

```yaml
## path-swap 시 운영 차트가 렌더 안 되므로, prune 으로 postgres-credentials-cnpg 가 orphan 되지 않게 미러.
## deletionPolicy:Retain + cnpg.io/reload 라벨(managed.roles 재적용 트리거) 동일.
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: postgres-credentials-cnpg
  namespace: {{ .Release.Namespace }}
  annotations:
    argocd.argoproj.io/sync-options: SkipDryRunOnMissingResource=true
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: vault-backend
    kind: ClusterSecretStore
  target:
    name: postgres-credentials-cnpg
    creationPolicy: Owner
    deletionPolicy: Retain
    template:
      type: kubernetes.io/basic-auth
      metadata:
        labels:
          cnpg.io/reload: "true"
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

- [ ] **Step 5: keycloak-pg-failback 4파일 (postgres 대칭)**

`gitops/apps/keycloak-pg-failback/`에 위 4파일을 아래 치환으로 생성(코드 반복 — 실행자가 순서 무관 참조):
- Chart.yaml: `name: keycloak-pg-failback`, description의 `postgres`→`keycloak`.
- values.yaml: `storage.size: 20Gi`, `resources`= keycloak 값(requests cpu250m/mem512Mi, limits cpu"1"/mem2Gi). `drEpoch: 0`. `storage.className: longhorn`.
- cluster.yaml: `metadata.name: keycloak-pg`, `imageName:` = keycloak digest(`18.2-system-trixie@sha256:3f44daf4c2ddea3481b018b3b004f91a439b93fc995a387f9aff69058bef19ac`), `destinationPath: "s3://cledyu-lab-dr-backups/keycloak-dr"`, `serverName: "keycloak-pg-dr-e{{ add (int .Values.drEpoch) 1 }}"`, `externalClusters[0].name: keycloak-pg-dr-origin`, `bootstrap.recovery.source: keycloak-pg-dr-origin`, `managed.roles[0].name: keycloak` + `passwordSecret.name: keycloak-pg-credentials`. `monitoring.enablePodMonitor: true`. s3Credentials 는 `cledyu-backup-s3`(keycloak ns) 동일.
- externalsecret.yaml: `metadata.name`·`target.name: keycloak-pg-credentials`, `data[*].remoteRef.key: keycloak/postgres`.

- [ ] **Step 6: (검증) recovery 소스·정적키·longhorn·resources·ES 렌더 확인**

Run:
```
helm template gitops/apps/postgres-cnpg-failback | grep -E 'serverName:|storageClass:|accessKeyId:|inheritFromIAMRole|kind: ExternalSecret|cpu:'
helm template gitops/apps/postgres-cnpg-failback --set drEpoch=1 | grep 'serverName:'
```
Expected: `serverName: cledyu-pg-dr-e1`(drEpoch=0) / `cledyu-pg-dr-e2`(drEpoch=1), `storageClass: longhorn`, `accessKeyId` 존재·`inheritFromIAMRole` **없음**, `kind: ExternalSecret` 존재, resources cpu 렌더.

- [ ] **Step 7: kubeconform (양 차트)**

Run: `for c in postgres-cnpg-failback keycloak-pg-failback; do helm template gitops/apps/$c | kubeconform -strict -ignore-missing-schemas; done`
Expected: 에러 없음.

- [ ] **Step 8: Commit**

```bash
git add gitops/apps/postgres-cnpg-failback/ gitops/apps/keycloak-pg-failback/
git commit -m "feat(dr): 온프렘 failback recovery 차트 신설(postgres·keycloak, -dr 소스·정적키·longhorn)"
```

---

### Task 5: failover 런북 real-DR 확장 (dr-eks-bootstrap.md)

drill(backupEnabled=false·drEpoch=0)은 불변으로 두고, **real-DR**에서 DR-창 쓰기를 캡처하도록 backupEnabled=true·epoch 취급을 명시하는 섹션을 추가한다.

**Files:**
- Modify: `docs/RUNBOOK/dr-eks-bootstrap.md` (CNPG 재-failover 가드 섹션 뒤에 "real-DR: DR-창 쓰기 캡처" 하위섹션 추가)

**Interfaces:**
- Consumes: Task 3 backupEnabled·drEpoch.
- Produces: real-DR 상태에서 `postgres-dr/cledyu-pg-dr-e{N+1}`·`keycloak-dr/keycloak-pg-dr-e{N+1}`에 base backup+WAL 축적(Task 6 failback recovery 전제).

- [ ] **Step 1: 섹션 추가**

`docs/RUNBOOK/dr-eks-bootstrap.md`의 "CNPG 재-failover 가드" 섹션 뒤에 아래 내용을 추가:

```markdown
### real-DR: DR-창 쓰기 캡처 (backupEnabled=true — 드릴과 다름)

> 드릴은 `backupEnabled=false`(반복 드릴 -dr 충돌 회피)로 두지만, **실재해**에선 온프렘 복구 후
> failback 하려면 EKS 가 DR-창 쓰기를 S3(`-dr/`)에 남겨야 한다. 아래는 real-DR 에서만 수행.

1. **backupEnabled=true 로 flip** — `gitops/apps/postgres-cnpg-dr/values.yaml`·`keycloak-pg-dr/values.yaml`
   의 `backupEnabled: false → true` 를 git 커밋(apps-eks 가 sync). → DR primary 가 `postgres-dr/cledyu-pg-dr-e{N+1}`·
   `keycloak-dr/keycloak-pg-dr-e{N+1}` 로 WAL 아카이빙 + ScheduledBackup(immediate) 이 base backup 1회.
   - `drEpoch` 는 **bump 하지 않는다**(여전히 N — 진입 epoch). serverName 의 -e{N+1} 는 템플릿이 자동 파생.
2. **anchor 도달 확인** — `kubectl -n postgres get backup` 에 `completed` base backup 1건 이상 + S3
   `s3://cledyu-lab-dr-backups/postgres-dr/cledyu-pg-dr-e{N+1}/` 아래 base·WAL 존재. keycloak 동일.
   (이게 없으면 failback recovery 가 anchor 없이 실패 — 반드시 게이트.)
```

- [ ] **Step 2: (검증) 섹션·앵커 존재 + 상호참조**

Run: `grep -nE "real-DR: DR-창 쓰기 캡처|backupEnabled: false → true|anchor 도달" docs/RUNBOOK/dr-eks-bootstrap.md`
Expected: 3개 매치.

- [ ] **Step 3: Commit**

```bash
git add docs/RUNBOOK/dr-eks-bootstrap.md
git commit -m "docs(dr): failover 런북에 real-DR DR-창 쓰기 캡처(backupEnabled·anchor) 추가"
```

---

### Task 6: failback 런북 (dr-failback.md) — 핵심 산출물

무손실 failback 9스텝 절차. 각 스텝 수동 승인 게이트. cutover 시 EKS 쓰기 quiesce로 무손실.

**Files:**
- Create: `docs/RUNBOOK/dr-failback.md`
- Modify: `docs/RUNBOOK/README.md` (런북 목록에 failback 추가)

**Interfaces:**
- Consumes: Task 1(IAM), 3(DR 아카이브), 4(failback 차트), 5(real-DR 캡처).

- [ ] **Step 1: dr-failback.md 작성**

Create `docs/RUNBOOK/dr-failback.md` (아래 전문):

```markdown
# DR Failback 런북 — 온프렘 복귀 (split-brain 방지·무손실)

> 온프렘 복구 후 서비스 권한을 EKS→온프렘으로 되돌린다. **자동 failback 없음** — 각 스텝 수동 승인.
> 데이터는 S3 barman 루프백으로 역복제(설계: docs/superpowers/specs/2026-07-13-dr-failback-reconciliation-design.md).
> 전제: failover 시 real-DR 캡처(dr-eks-bootstrap.md §real-DR)로 `-dr/` 에 base+WAL 축적됨. 진입 epoch=N.

## 값 표 (이번 사이클)
| 파일 | 이번 값 | failback 완료 후 |
|---|---|---|
| postgres-cnpg/values·keycloak-pg/values (운영) | drEpoch=N | drEpoch=N+1 |
| postgres-cnpg-dr/values·keycloak-pg-dr/values | drEpoch=N, backupEnabled=true | drEpoch=N+1, backupEnabled=false |
| postgres-cnpg-failback/values·keycloak-pg-failback/values | drEpoch=N | drEpoch=N+1 |

## 절차

### 0. 전제 확인
- 온프렘 인프라 정상(k3s·Vault unseal·ESO·cnpg-operator Running) + 하트비트 재개.
- EKS 여전히 서빙 중(api/app/auth → EKS ALB). 온프렘 앱은 미서빙(scale-0) 유지.

### 1. EKS 쓰기 quiesce (계획된 write-downtime 시작) — 【승인 게이트】
> 이후 새 쓰기를 막아 recovery 데이터셋을 고정한다. 없으면 flush~DNS전환 사이 EKS 쓰기가 소실.
```bash
kubectl --context eks-dr -n api scale deploy/api --replicas=0   # 쓰기 경로 정지(읽기도 멈춤 — 그 창만 다운)
```

### 2. EKS write frontier flush (EKS primary)
```bash
# postgres·keycloak 각각의 primary pod 에서:
kubectl --context eks-dr -n postgres exec -it cledyu-pg-1 -- psql -c "CHECKPOINT; SELECT pg_switch_wal();"
kubectl --context eks-dr -n keycloak exec -it keycloak-pg-1 -- psql -c "CHECKPOINT; SELECT pg_switch_wal();"
# (선택·최적화) 최신 base backup 으로 WAL 재생 단축. delete-first 로 반복 failback 멱등(AlreadyExists 방지).
kubectl --context eks-dr -n postgres delete backup failback-cutover --ignore-not-found
kubectl --context eks-dr -n postgres create -f - <<'YAML'
apiVersion: postgresql.cnpg.io/v1
kind: Backup
metadata: { name: failback-cutover, namespace: postgres }
spec: { cluster: { name: cledyu-pg } }
YAML
```

### 3. 온프렘 recovery — 【승인 게이트: 삭제 전 -dr 건전성 확인 필수】
```bash
N=<진입 epoch 정수>   # = 현재 운영/DR values 의 drEpoch(값 표 참조). 첫 failback 이면 0.
# (a) 선-확인: -dr 아카이브에 base+WAL 존재(불완전하면 stale 삭제 금지 — DB 소실).
aws s3 ls s3://cledyu-lab-dr-backups/postgres-dr/cledyu-pg-dr-e$((N+1))/ --recursive | grep -E 'base|wals' | head
# (b) path-swap: data-postgres-cnpg·data-keycloak-pg 앱 source.path 를 -failback 차트로 (git 커밋).
#     postgres-cnpg → postgres-cnpg-failback, keycloak-pg → keycloak-pg-failback. drEpoch=N 확인.
# (c) stale cluster 삭제(파괴적 — PVC 소멸) → ArgoCD 가 failback 차트로 fresh recovery.
kubectl -n postgres delete cluster cledyu-pg --ignore-not-found
kubectl -n keycloak delete cluster keycloak-pg --ignore-not-found
# (d) recovery 완료 대기.
kubectl -n postgres wait --for=condition=Ready cluster/cledyu-pg --timeout=900s
kubectl -n keycloak wait --for=condition=Ready cluster/keycloak-pg --timeout=900s
```

### 4. 데이터 정합 체크 — 【승인 게이트: 불일치 시 cutover 중단】
```bash
# EKS vs 온프렘 핵심 테이블 대조(무손실 실증). 불일치면 quiesce 유지·원인 규명.
# ⚠️ -d cledyu 필수 — api 테이블은 cledyu DB(기본 postgres DB 아님).
for ctx in eks-dr onprem; do
  echo "== $ctx =="
  kubectl --context $ctx -n postgres exec cledyu-pg-1 -- psql -d cledyu -tAc \
    "SELECT count(*) FROM lab_completions; SELECT count(*) FROM session_progress; SELECT max(updated_at) FROM session_progress;"
done
# keycloak user count 대조(-d keycloak).
for ctx in eks-dr onprem; do
  echo "== $ctx =="
  kubectl --context $ctx -n keycloak exec keycloak-pg-1 -- psql -d keycloak -tAc "SELECT count(*) FROM user_entity;"
done
```

### 5. drEpoch bump + adopt — 【승인 게이트】
```bash
# (a) lockstep bump: 운영·DR·failback 6개 values drEpoch N→N+1 + DR backupEnabled true→false 를 한 커밋.
#     사후 가드: grep -rn 'drEpoch:' gitops/apps/postgres-cnpg*/ gitops/apps/keycloak-pg*/ → 전부 N+1 동일 확인.
# (b) path-swap 원복: data-postgres-cnpg·data-keycloak-pg source.path 를 운영 차트로. (git 커밋)
# (c) adopt: 운영 차트 재-sync. cledyu-pg 는 이미 존재 → bootstrap 재실행 없음. backup serverName=cledyu-pg-e{N+1}
#     로 전진 아카이빙(WAL) 개시. ArgoCD OutOfSync(bootstrap diff) 나면 ignoreDifferences 검토(R1).
# (d) 새 epoch anchor 를 결정론적으로 확보 — 명시적 on-demand base backup.
#     ⚠️ 운영 ScheduledBackup(immediate:true)의 즉시백업은 CR '생성' 시에만 발화한다. adopt 시 path-swap
#        prune→recreate 로 발화하긴 하나 그 부수효과에 의존하지 않는다. 아래로 확실히 anchor 를 만든다.
#        (이게 없으면 새 epoch 에 WAL 만 있고 base 가 없어, 그 창에 재해 오면 f(N+1) recovery 가 anchor 없이 실패.)
# delete-first 로 멱등 — CR 이름 고정이라 반복 failback 시 AlreadyExists 방지.
# (Backup CR 삭제는 k8s 리소스만 지우고 S3 base backup 은 남긴다 — 이전 epoch anchor 보존.)
kubectl -n postgres delete backup failback-epoch-anchor --ignore-not-found
kubectl -n postgres create -f - <<'YAML'
apiVersion: postgresql.cnpg.io/v1
kind: Backup
metadata: { name: failback-epoch-anchor, namespace: postgres }
spec: { cluster: { name: cledyu-pg } }
YAML
kubectl -n keycloak delete backup failback-epoch-anchor --ignore-not-found
kubectl -n keycloak create -f - <<'YAML'
apiVersion: postgresql.cnpg.io/v1
kind: Backup
metadata: { name: failback-epoch-anchor, namespace: keycloak }
spec: { cluster: { name: keycloak-pg } }
YAML
# (e) anchor 도달 확인 — 새 epoch 경로에 completed base backup 존재.
kubectl -n postgres wait --for=jsonpath='{.status.phase}'=completed backup/failback-epoch-anchor --timeout=600s
aws s3 ls s3://cledyu-lab-dr-backups/postgres/cledyu-pg-e$((N+1))/base/ | head   # 비어 있으면 anchor 실패 → 다음 failover 불가
```

### 6. 수동 승인 → DNS 원복 — 【승인 게이트: split-brain 단일 권한 스위치】
> 여기서 서비스 권한이 온프렘으로 넘어감 = write-downtime 종료. **var 생략 시 레코드 destroy 주의**.
```bash
cd infra/terraform/aws && terraform apply -var enable_public_ingress=true -target=aws_route53_record.public
```

### 7. 온프렘 앱 재개
```bash
kubectl -n api rollout restart deploy/api && kubectl -n api rollout status deploy/api
kubectl -n web rollout restart deploy/web && kubectl -n web rollout status deploy/web
# Keycloak 은 operator CR(kind: Keycloak, cledyu-keycloak)이 StatefulSet 을 만든다 → deploy 아님.
# DB 재생성으로 커넥션 풀이 끊겼을 수 있어 재기동으로 keycloak-pg-rw 재연결 보장.
kubectl -n keycloak rollout restart statefulset/cledyu-keycloak && kubectl -n keycloak rollout status statefulset/cledyu-keycloak
kubectl -n api logs deploy/api | grep -E "db 연결|in-memory"   # in-memory 폴백 아님 확인
```

### 8. EKS 축소
```bash
cd infra/terraform/aws && terraform apply \
  -var enable_eks_dr=true -var eks_dr_active=false -var eks_dr_node_desired=0 \
  -target=module.eks_dr_vpc -target=module.eks_dr -target=module.eks_dr_ebs_csi_irsa \
  -target=module.eks_dr_alb_irsa -target=aws_security_group.eks_dr_endpoints \
  -target=aws_security_group.eks_dr_bastion -target=module.eks_dr_endpoints \
  -target=module.eks_dr_vault_unseal_irsa -target=aws_iam_policy.eks_dr_vault_unseal \
  -target=aws_iam_policy.eks_dr_cnpg_restore -target=module.eks_dr_cnpg_restore_irsa \
  -target=aws_iam_role.eks_dr_bastion -target=aws_iam_role_policy_attachment.eks_dr_bastion_ssm \
  -target=aws_iam_role_policy.eks_dr_bastion_describe -target=aws_iam_role_policy.eks_dr_bastion_vault_restore \
  -target=aws_iam_instance_profile.eks_dr_bastion -target=aws_instance.eks_dr_bastion
# ⚠️ enable_eks_dr=true 필수 — 생략 시 warm 컨트롤플레인까지 destroy. (목록은 dr-eks-bootstrap.md §Phase1 과 동일)
```

### 9. 사후 확인
- 온프렘 연속 아카이빙(새 epoch `postgres/cledyu-pg-e{N+1}`) 재개·RPO 정상.
- 로그인·진도 서빙 정상. `[P1b]` 용 warm etcd stale CNPG CR 은 다음 failover 가 처리.
```

- [ ] **Step 2: README 목록 추가**

`docs/RUNBOOK/README.md`의 런북 목록에 `- [DR Failback](dr-failback.md) — 온프렘 복귀·split-brain 방지·무손실 역복제` 추가.

- [ ] **Step 3: (검증) 9스텝·핵심 커맨드 존재**

Run: `grep -cE "^### [0-9]\." docs/RUNBOOK/dr-failback.md; grep -nE "enable_public_ingress=true|scale deploy/api --replicas=0|delete cluster cledyu-pg" docs/RUNBOOK/dr-failback.md`
Expected: `10`(0~9 스텝) + 3개 커맨드 매치.

- [ ] **Step 4: Commit**

```bash
git add docs/RUNBOOK/dr-failback.md docs/RUNBOOK/README.md
git commit -m "docs(dr): failback 런북(무손실 9스텝·quiesce·drEpoch·DNS 원복)"
```

---

### Task 7: 드릴 섹션 (dr-restore-drill.md 확장)

failback을 라이브로 증명하는 드릴 절차 추가 — 마커 무손실·quiesce 갭 부재·split-brain 부재·2사이클 반복·RTO.

**Files:**
- Modify: `docs/RUNBOOK/dr-restore-drill.md` (failback 드릴 섹션 추가)

**Interfaces:**
- Consumes: Task 1~6 전체.

- [ ] **Step 1: 섹션 추가**

`docs/RUNBOOK/dr-restore-drill.md` 끝에 추가:

```markdown
## Failback 드릴 (역복제·무손실·split-brain 실증)

전제: real-DR failover 상태(EKS primary, backupEnabled=true, -dr 아카이브 축적). 진입 epoch=N.

- [ ] **마커 주입** — quiesce **직전** EKS DR primary 에 고유 마커. `lab_completions` 는 PK(user_id,lab_id)
      + `session_id TEXT NOT NULL`(default 없음) 이므로 **session_id 필수**(누락 시 not-null 위반):
  ```bash
  kubectl --context eks-dr -n postgres exec cledyu-pg-1 -- psql -d cledyu -tAc \
    "INSERT INTO lab_completions(user_id,lab_id,session_id) VALUES('drill-marker','failback-N','drill-session') ON CONFLICT DO NOTHING;"
  ```
  (⚠️ `-d cledyu` 필수 — api 테이블은 cledyu DB 에 있음. 기본 postgres DB 로 붙으면 relation 없음. completed_at 은
  DEFAULT now() 라 생략. 반복 드릴 대비 ON CONFLICT DO NOTHING — PK(user_id,lab_id) 중복 무시.)
- [ ] **failback 수행** — dr-failback.md 0~9 전 절차.
- [ ] **무손실 실증** — 온프렘에 마커 존재:
  ```bash
  kubectl --context onprem -n postgres exec cledyu-pg-1 -- psql -d cledyu -tAc \
    "SELECT count(*) FROM lab_completions WHERE user_id='drill-marker';"   # → 1
  ```
- [ ] **quiesce 갭 실증** — quiesce 이후 EKS 쓰기 시도가 불가(api replicas=0)였고, recovery 데이터셋이
      quiesce 시점 고정 → 정합 체크(step4)에서 EKS==온프렘 count 일치 확인(recovery 창 write-loss 없음).
- [ ] **split-brain 부재 실증** — DNS 전환(step6) **전** 온프렘 미서빙 + quiesce 이후 양쪽 write 부재 확인.
- [ ] **반복 성립 실증** — 재-failover(EKS recover ← f(N+1)=cledyu-pg-e{N+1}) → 재-failback(→epoch N+2)
      1사이클 더. `-dr-e{N+2}` 새 경로라 Object Lock 충돌 없음 실측.
- [ ] **failback RTO 실측** — step1(quiesce)~step6(DNS) 창 = write-downtime, 각 스텝 타임스탬프 기록.
```

- [ ] **Step 2: (검증)** — Run: `grep -nE "Failback 드릴|마커 주입|quiesce 갭 실증|반복 성립" docs/RUNBOOK/dr-restore-drill.md`. Expected: 4개 매치.

- [ ] **Step 3: Commit**

```bash
git add docs/RUNBOOK/dr-restore-drill.md
git commit -m "docs(dr): failback 드릴 섹션(마커 무손실·quiesce 갭·2사이클 반복·RTO)"
```

---

## 실행 후: 라이브 드릴 (플랜 밖 — 실환경)

위 7태스크는 정적 산출물이다. R1~R6(adopt drift·epoch lockstep·재시도 충돌·anchor 도달·키 수명·downtime 창)은 **Task 7 드릴을 실환경에서 실행**해야 닫힌다. 드릴은 계획된 failover→failback→재-failover→재-failback 2사이클로 수행하고 RTO·무손실·충돌부재를 실측한다.
