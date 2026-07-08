# DR/백업 Plan C — DR 오케스트레이션 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 온프렘 상실을 오탐 없이 감지하고(pull+push AND + 수동 승인), 감지 후 EKS로 컨트롤플레인·durable 데이터를 자동 복구하며(Lambda/Step Functions), 온프렘 복귀 시 스플릿 브레인을 방지한다(DNS 단일 권한 + 수동 failback).

**Architecture:** 온프렘 push 하트비트(CloudWatch) + AWS pull 프로브(Route 53) → CloudWatch 복합 알람(AND) → EventBridge → Step Functions(수동 승인 게이트 → EKS 기동 → ArgoCD 부트스트랩 → S3 복원 → DNS 전환). 복원 자격증명은 정적 키가 아니라 실행 롤(S3 read + Vault unseal용 AWS KMS decrypt). failback은 자동 금지, 역복제 후 수동 전환.

**Tech Stack:** Terraform(CloudWatch/EventBridge/Step Functions/Lambda/IAM/Route 53), 온프렘 CronJob(awscli PutMetricData), Vault(AWS KMS auto-unseal, alias/cledyu-vault-unseal), CloudNativePG(PITR 복원), Velero(오브젝트 복원).

## Dependencies (중요)

- **Plan A(백업 계층) 완료 전제**: S3에 Postgres WAL/베이스백업(`postgres/`), Vault raft 스냅샷(`vault/`), Velero(`velero/`)이 실제로 쌓여 있어야 복원할 대상이 있다. Task 5·6은 이 백업들을 읽는다.
- **Plan B(EKS 오버레이) 완료 전제**: Task 4의 "EKS 기동 → ArgoCD 동기화"는 Plan B가 만든 EKS용 오버레이(Longhorn→EBS, MetalLB→ALB Controller 등)가 있어야 성립한다. **Task 4는 Plan B 없이는 완결 불가** — Task 1~3(감지)·Task 7(런북)은 Plan B와 독립으로 선행 가능.
- **단일 클라우드 인증 (2026-07-04 갱신)**: `infra/terraform/aws`의 state는 GCS→S3로 이전 완료(PR #245),
  `keycloak` state도 S3로 이전(PR #249). Vault unseal도 GCP KMS→AWS KMS로 이전 완료(PR #246/#247).
  이 Plan C의 apply·복원 런타임 모두 **AWS 자격증명만으로 완결**된다 — 더 이상 GCP 자격증명이 필요
  없다(과거엔 이중 클라우드 인증이 블로커였음, `project_aws_tf_dual_cloud_auth` 메모리는 이제 stale).

## Global Constraints

- 리전: `ap-northeast-2` (Plan A/EC2 오버플로우와 동일)
- 산출물 위치: `infra/terraform/aws/dr-*.tf` (감지·오케스트레이션), 온프렘 하트비트는 `gitops/apps/dr-heartbeat/`
- 감지 임계값은 **제안값(팀장 논의·실측 튜닝 대기)**: pull 1분 주기 N=5회 연속 실패, push 30초 주기 M=3분 공백. RTO 목표 ~2.5시간(장애 발생~PITR 완료, 스펙 § RTO 설계)
- Terraform 컨벤션: `var.name_prefix`, 정책은 `data.aws_iam_policy_document`, 시크릿 output 없음
- 검증: `terraform validate` / `terraform fmt -check` / `helm template | kubeconform`
- 커밋만 실행자가 하고, 사용자 확인 전 커밋 금지 규칙은 실행 단계에서 사용자 지시에 따른다

---

### Task 1: push 하트비트 (온프렘 → CloudWatch, dead man's switch)

> Plan B와 독립. 온프렘이 tailnet을 거치지 않는 아웃바운드(HTTPS 직접)로 "살아있음"을 30초마다 CloudWatch에 기록한다. S3 백업·BigQuery 적재와 동일 경로라 egress가 이미 열려 있을 가능성이 높으나 실측 확인 필요.

**Files:**
- Create: `gitops/apps/dr-heartbeat/Chart.yaml`
- Create: `gitops/apps/dr-heartbeat/values.yaml`
- Create: `gitops/apps/dr-heartbeat/templates/cronjob.yaml`
- Create: `gitops/apps/dr-heartbeat/templates/externalsecret.yaml`
- Create: `gitops/argocd/apps/platform-dr-heartbeat.yaml`

**Interfaces:**
- Consumes: `aws/backup` Vault kv(또는 하트비트 전용 IAM 키) → CloudWatch `PutMetricData` 권한
- Produces: CloudWatch custom metric `Cledyu/DR OnPremHeartbeat=1` (30초 간격)

- [ ] **Step 0: egress 실측 (사전 확인)**

Run (온프렘 클러스터에서):
```bash
kubectl run egress-test --rm -it --image=amazon/aws-cli --restart=Never -- \
  cloudwatch put-metric-data --namespace Cledyu/DR \
  --metric-name OnPremHeartbeat --value 1 --region ap-northeast-2
```
Expected: 에러 없이 반환. 실패(timeout)면 온프렘 방화벽에서 `monitoring.ap-northeast-2.amazonaws.com` egress 허용 필요 — push 하트비트 전제가 여기서 확정된다.

- [ ] **Step 1: 하트비트 전용 IAM 권한**

`infra/terraform/aws/dr-detection.tf` 에 하트비트용 최소 권한 추가(`cloudwatch:PutMetricData`, 네임스페이스 조건). Task 1에서는 기존 `backup-writer` 키에 PutMetricData를 얹지 않고 **전용 사용자**를 두는 것을 권장(권한 분리). 액세스 키는 수동 발급 → Vault `aws/dr-heartbeat`.

- [ ] **Step 2: CronJob + ESO 작성**

`gitops/apps/dr-heartbeat/templates/cronjob.yaml` (30초 간격은 CronJob 최소 단위 1분보다 짧으므로, 1분 CronJob 안에서 `sleep` 루프로 30초 2회 전송하거나 Deployment+루프로 구현):
```yaml
apiVersion: apps/v1
kind: Deployment          # 30초 주기라 CronJob(분 단위) 대신 상주 루프
metadata:
  name: dr-heartbeat
  namespace: dr-system
spec:
  replicas: 1
  selector: { matchLabels: { app: dr-heartbeat } }
  template:
    metadata: { labels: { app: dr-heartbeat } }
    spec:
      containers:
        - name: heartbeat
          image: amazon/aws-cli:2
          env:
            - name: AWS_ACCESS_KEY_ID
              valueFrom: { secretKeyRef: { name: dr-heartbeat-creds, key: ACCESS_KEY_ID } }
            - name: AWS_SECRET_ACCESS_KEY
              valueFrom: { secretKeyRef: { name: dr-heartbeat-creds, key: ACCESS_SECRET_KEY } }
            - name: AWS_DEFAULT_REGION
              value: ap-northeast-2
          command: ["/bin/sh", "-c"]
          args:
            - |
              while true; do
                aws cloudwatch put-metric-data --namespace Cledyu/DR \
                  --metric-name OnPremHeartbeat --value 1 || true
                sleep 30
              done
```
`externalsecret.yaml`은 Task 2(backup-secrets) 패턴을 `aws/dr-heartbeat` 경로로 복제.

- [ ] **Step 3: 검증**

Run: `helm template gitops/apps/dr-heartbeat --namespace dr-system | kubeconform -strict -ignore-missing-schemas`
Expected: Deployment/ExternalSecret 렌더 성공.

- [ ] **Step 4: Sync 후 지표 도달 확인**

Run:
```bash
argocd app sync platform-dr-heartbeat
aws cloudwatch get-metric-statistics --namespace Cledyu/DR --metric-name OnPremHeartbeat \
  --start-time $(date -u -d '5 min ago' +%FT%TZ) --end-time $(date -u +%FT%TZ) \
  --period 60 --statistics Sum --region ap-northeast-2
```
Expected: 최근 5분 데이터포인트 존재(Sum ≈ 2/분).

- [ ] **Step 5: Commit**

```bash
git add gitops/apps/dr-heartbeat gitops/argocd/apps/platform-dr-heartbeat.yaml infra/terraform/aws/dr-detection.tf
git commit -m "feat(dr): 온프렘 push 하트비트(CloudWatch dead man's switch)"
```

---

### Task 2: pull 프로브 + CloudWatch 복합 알람 (AND)

> Plan B와 독립. Route 53 헬스체크로 `auth.cledyu.com`(ALB→프록시→tailnet→Keycloak)을 딥 HTTP+문자열 매칭으로 감시하고, push 하트비트 알람과 AND로 묶는다.

**Files:**
- Modify: `infra/terraform/aws/dr-detection.tf`

**Interfaces:**
- Consumes: Task 1의 `Cledyu/DR OnPremHeartbeat` 지표, 공개 엔드포인트 `auth.cledyu.com`(`public-ingress.tf`)
- Produces: CloudWatch 복합 알람 `cledyu-dr-disaster` (pull ALARM AND push ALARM)

- [ ] **Step 1: Route 53 헬스체크(pull) 정의**

```hcl
resource "aws_route53_health_check" "onprem_pull" {
  fqdn              = var.public_keycloak_host          # auth.cledyu.com
  type              = "HTTPS_STR_MATCH"                 # 딥: 본문 문자열 매칭
  resource_path     = "/realms/cledyu-learn"
  search_string     = "cledyu-learn"                   # 앱이 실제 응답하는지
  port              = 443
  request_interval  = 30                                # 30초 주기
  failure_threshold = 5                                 # N=5 연속 실패(제안값)
  tags = { Name = "${var.name_prefix}-dr-pull" }
}
```

- [ ] **Step 2: pull·push 알람 + 복합 알람(AND)**

```hcl
# pull 알람: Route53 헬스체크 상태(HealthCheckStatus < 1 = 비정상)
resource "aws_cloudwatch_metric_alarm" "pull" {
  alarm_name          = "${var.name_prefix}-dr-pull"
  namespace           = "AWS/Route53"
  metric_name         = "HealthCheckStatus"
  dimensions          = { HealthCheckId = aws_route53_health_check.onprem_pull.id }
  comparison_operator = "LessThanThreshold"
  threshold           = 1
  evaluation_periods  = 1
  period              = 60
  statistic           = "Minimum"
  treat_missing_data  = "breaching"
}

# push 알람: 하트비트 지표가 M초간 없으면 breaching (dead man's switch 핵심)
resource "aws_cloudwatch_metric_alarm" "push" {
  alarm_name          = "${var.name_prefix}-dr-push"
  namespace           = "Cledyu/DR"
  metric_name         = "OnPremHeartbeat"
  comparison_operator = "LessThanThreshold"
  threshold           = 1
  evaluation_periods  = 3                # M=3분(제안값)
  period              = 60
  statistic           = "Sum"
  treat_missing_data  = "breaching"      # 데이터 없음 = 위반
}

# 복합 알람: 둘 다 ALARM일 때만 (AND)
resource "aws_cloudwatch_composite_alarm" "disaster" {
  alarm_name        = "${var.name_prefix}-dr-disaster"
  alarm_rule        = "ALARM(${aws_cloudwatch_metric_alarm.pull.alarm_name}) AND ALARM(${aws_cloudwatch_metric_alarm.push.alarm_name})"
  alarm_actions     = [aws_sns_topic.dr_alert.arn]     # Task 3
}
```

- [ ] **Step 3: 검증**

Run: `cd infra/terraform/aws && terraform init -backend=false && terraform validate && terraform fmt -check dr-detection.tf`
Expected: valid + fmt 통과.

- [ ] **Step 4: apply 후 4분면 동작 확인 (AWS 자격증명만 필요)**

apply 후, 하트비트를 일시 중단(Deployment scale 0)했을 때 push 알람만 ALARM이 되고 **복합 알람은 OK 유지**(pull 정상이므로)를 확인 → 오탐 방지 동작 실증. 스펙 § 재해 감지 4분면 표의 2행.
```bash
kubectl -n dr-system scale deploy/dr-heartbeat --replicas=0
# 4분 후
aws cloudwatch describe-alarms --alarm-names cledyu-lab-dr-push cledyu-lab-dr-disaster \
  --query 'MetricAlarms[].StateValue CompositeAlarms[].StateValue' --region ap-northeast-2
kubectl -n dr-system scale deploy/dr-heartbeat --replicas=1   # 원복
```
Expected: push=ALARM, disaster=OK. (둘 다 죽어야 disaster=ALARM)

- [ ] **Step 5: Commit**

```bash
git add infra/terraform/aws/dr-detection.tf
git commit -m "feat(dr): pull 프로브(Route53)·push 알람 복합(AND) 재해 감지"
```

---

### Task 3: EventBridge → 알림 → 수동 승인 게이트

> 복합 알람이 울려도 자동으로 복구를 시작하지 않는다. 사람이 "진짜 재해 vs 일시적"을 판단해 승인해야 Task 4가 진행된다(오판 시 데이터 영구 분기 방지).

**Files:**
- Modify: `infra/terraform/aws/dr-detection.tf` (SNS)
- Create: `infra/terraform/aws/dr-orchestration.tf` (EventBridge 규칙 → Step Functions 시작)

**Interfaces:**
- Consumes: 복합 알람 `cledyu-dr-disaster`(Task 2)
- Produces: SNS 토픽 `dr_alert`(Discord/이메일 구독), Step Functions 실행 시작(수동 승인 대기 상태로 진입)

- [ ] **Step 1: SNS 토픽 + 알림 구독**

```hcl
resource "aws_sns_topic" "dr_alert" { name = "${var.name_prefix}-dr-alert" }
# Discord/이메일 구독은 기존 알림 채널(alerting/discord-monitoring) 재사용
```

- [ ] **Step 2: EventBridge 규칙 (복합 알람 상태변화 → Step Functions)**

복합 알람 상태변화 이벤트를 매칭해 Step Functions 실행을 시작한다. Step Functions는 첫 상태로 **수동 승인(`.waitForTaskToken`)**에 진입해 사람이 승인할 때까지 멈춘다.
```hcl
resource "aws_cloudwatch_event_rule" "disaster" {
  name = "${var.name_prefix}-dr-disaster"
  event_pattern = jsonencode({
    source      = ["aws.cloudwatch"]
    detail-type = ["CloudWatch Alarm State Change"]
    detail = {
      alarmName = [aws_cloudwatch_composite_alarm.disaster.alarm_name]
      state     = { value = ["ALARM"] }
    }
  })
}
resource "aws_cloudwatch_event_target" "sfn" {
  rule     = aws_cloudwatch_event_rule.disaster.name
  arn      = aws_sfn_state_machine.dr.arn         # Task 4
  role_arn = aws_iam_role.eventbridge_sfn.arn
}
```

- [ ] **Step 3: 검증**

Run: `cd infra/terraform/aws && terraform validate && terraform fmt -check dr-detection.tf dr-orchestration.tf`
Expected: valid + fmt 통과.

- [ ] **Step 4: Commit**

```bash
git add infra/terraform/aws/dr-detection.tf infra/terraform/aws/dr-orchestration.tf
git commit -m "feat(dr): EventBridge 규칙·SNS 알림·Step Functions 트리거"
```

---

### Task 4: 복구 오케스트레이션 Step Functions (승인 → EKS → ArgoCD → 복원 → DNS)

> **Plan B(EKS 오버레이) 의존.** 승인 게이트 통과 후 순차 복구를 실행한다. 각 단계는 idempotent·재시도 가능해야 한다(RTO 버퍼 근거).

**Files:**
- Modify: `infra/terraform/aws/dr-orchestration.tf` (Step Functions 정의)
- Create: `infra/terraform/aws/dr-lambda/*` (단계별 Lambda 소스: eks-up, argocd-bootstrap, restore, dns-switch)

**Interfaces:**
- Consumes: 수동 승인 토큰(Task 3), S3 백업(Plan A), EKS 오버레이(Plan B), 복원 롤(Task 5)
- Produces: 복구된 EKS 서비스 + auth.cledyu.com/api DNS가 EKS를 가리킴

- [ ] **Step 1: 상태 머신 정의 (ASL)**

순차 단계 + 수동 승인 게이트. RTO 배분(스펙 § RTO): 승인 → EKS 기동 ~30분 → ArgoCD·앱 ~15분 → 복원·PITR ~30분 → DNS ~10분.
```
[Start]
  → ManualApproval (.waitForTaskToken; SNS로 승인 링크 발송, 사람 승인까지 대기)
  → EksUp        (Lambda: EKS 클러스터/노드 기동 또는 사전생성 클러스터 scale-up)
  → ArgoBootstrap(Lambda: ArgoCD 설치→App-of-Apps 동기화, Plan B 오버레이)
  → Restore      (Lambda: Postgres PITR + Vault raft + Velero 복원, 아래 순서)
  → DnsSwitch    (Lambda: Route53 auth/api → EKS 엔드포인트)
  → Notify       (SNS: RTO 타이머 종료)
```
복원 내부 순서(스펙 § 백업 우선순위 기술 순서): **Vault 복원→unseal(AWS KMS)→ESO 정상화 → Postgres PITR/Keycloak**. Vault가 먼저 열려야 나머지가 시크릿을 받는다.

> **복원 순서 의존성 — velero 오브젝트 복원은 CRD/오퍼레이터 뒤에 (필수).** Velero 백업은 CRD·StorageClass·PVC를
> 의도적으로 제외한다(`gitops/apps/velero/values.yaml`: CRD/StorageClass는 GitOps 오퍼레이터가 재설치, PVC는
> 온프렘 스토리지 종속). 따라서 `velero restore`로 namespaced CR(Certificate·ExternalSecret·Kafka `Kafka`·CNPG
> `Cluster` 등)을 되살리려면 **해당 CRD·오퍼레이터가 먼저 설치·Established 되어 있어야** 한다 — 아니면 CR 복원이
> `no matches for kind` 로 실패한다. DAG의 `ArgoBootstrap → Restore` 순서가 이를 담보하지만, **`ArgoBootstrap`은
> App-of-Apps sync를 트리거만 하고 반환하면 안 되고 CRD가 Established 될 때까지 대기(wait)** 해야 한다
> (예: `kubectl wait --for condition=Established crd/...` 게이트). 스토리지는 velero가 PVC를 복원하지 않으므로
> 각 오퍼레이터(CNPG/Strimzi 등)가 대상 클러스터 StorageClass로 PVC를 재생성한다 — velero는 오브젝트만 되살린다.
> (velero PR 리뷰 지적: 복원 순서 문서화 — cluster-scoped allowlist·CRD 제외 결정의 운영상 귀결)

- [ ] **Step 2: 단계별 Lambda 스켈레톤**

각 Lambda는 실패 시 Step Functions `Retry`/`Catch`로 재시도. `Restore`는 CNPG `Cluster`(bootstrap.recovery, targetTime=최신) + `velero restore` + Vault 스냅샷 복원을 호출. (구체 매니페스트는 Plan A Task 7 PITR 드릴 재사용)

- [ ] **Step 3: 검증**

Run: `cd infra/terraform/aws && terraform validate && terraform fmt -check dr-orchestration.tf` + Lambda 소스 lint.
Expected: valid. (실제 동작 검증은 Task 7 드릴에서)

- [ ] **Step 4: Commit**

```bash
git add infra/terraform/aws/dr-orchestration.tf infra/terraform/aws/dr-lambda
git commit -m "feat(dr): 복구 Step Functions(승인→EKS→ArgoCD→복원→DNS)"
```

---

### Task 5: 복원 자격증명 — IAM 롤 (S3 read + Vault unseal KMS)

> 복원 경로에 정적 키를 두지 않는다(S3 키는 Vault 안에 있어 순환). 복원 컴퓨트에 롤로 S3 read +
> Vault unseal용 KMS decrypt를 준다 — **둘 다 AWS 안에서 끝난다**.

**Files:**
- Modify: `infra/terraform/aws/dr-orchestration.tf` (IAM 롤/정책)

**Interfaces:**
- Produces: 복원 실행 롤(S3 `cledyu-lab-dr-backups` read-only + Vault unseal KMS decrypt)

- [ ] **Step 1: 복원 실행 롤 (S3 read-only + Vault unseal KMS)**

```hcl
data "aws_iam_policy_document" "restore" {
  statement {
    actions   = ["s3:GetObject", "s3:ListBucket", "s3:GetBucketLocation"]
    resources = [aws_s3_bucket.dr_backups.arn, "${aws_s3_bucket.dr_backups.arn}/*"]
  }
  # 버킷이 SSE-KMS(dr_backups CMK)라, 백업을 읽으려면 그 키의 복호화 권한도 필요하다(Plan A backup.tf).
  statement {
    sid       = "BackupBucketKms"
    actions   = ["kms:Decrypt", "kms:DescribeKey"]
    resources = [aws_kms_key.dr_backups.arn]
  }
  # 복원된 Vault가 auto-unseal 하려면 이 키의 Decrypt 권한이 필요하다(스펙 § Vault 부트스트랩 체인).
  # alias/cledyu-vault-unseal 은 Vault 보안 작업(PR #246)에서 별도 관리 — 여기선 참조만.
  statement {
    sid       = "VaultUnsealKms"
    actions   = ["kms:Decrypt", "kms:DescribeKey"]
    resources = ["arn:aws:kms:ap-northeast-2:504284203153:key/e29e3ec2-f5e0-4308-af6f-5b576cc99f52"]
  }
}
# Step Functions/Lambda 실행 롤에 attach. 정적 키 없음. GCP 자격증명 불필요(단일 클라우드로 완결).
```

> **복원된 Vault 파드에 위 KMS decrypt 권한을 공급하는 방식은 둘 중 하나** (온프렘 운영과 동일):
> - **IRSA**(권장): Vault ServiceAccount ↔ IAM 롤 연동 → 파드가 static 키 없이 KMS 호출. EKS 네이티브.
> - **`vault-aws-kms-creds`**: awskms seal이 표준 AWS 자격증명 체인(env)에서 읽는 k8s Secret
>   (`AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`). 온프렘에서 쓰던 방식 그대로. 마이그레이션
>   런북(`docs/RUNBOOK/vault-seal-migration-awskms.md`) 참조.

- [ ] **Step 2: recovery key break-glass 경로 확인 (문서)**

auto-unseal(AWS KMS)이 어떤 이유로 실패하면 Vault는 recovery key로 수동 unseal해야 한다. seal
마이그레이션(#246/#247) 후 recovery key 백업은 **AWS Secrets Manager `cledyu/vault/bootstrap`**에
있다(과거 GCP Secret Manager `cledyu-vault-bootstrap`에서 이관). DR 복원 시 이 경로가 최후 보루이며,
복원 컴퓨트/운영자가 접근 가능해야 한다 — 이 역시 AWS 안에서 완결(GCP 불필요).

- [ ] **Step 3: 검증 + Commit**

Run: `terraform validate && terraform fmt -check`
```bash
git add infra/terraform/aws/dr-orchestration.tf
git commit -m "feat(dr): 복원 실행 롤(S3 read · Vault unseal AWS KMS decrypt)"
```

---

### Task 6: 스플릿 브레인 방지 / Failback 런북

> 코드보다 절차가 핵심. 온프렘 복구 시 자동 전환을 막고, 역복제 후 수동으로만 되돌린다.

**Files:**
- Create: `docs/RUNBOOK/dr-failback.md`
- Modify: `infra/terraform/aws/dr-orchestration.tf` (failback은 자동화하지 않음을 주석으로 명시, DNS 권한 단일화 확인)

**Interfaces:**
- Consumes: Task 1 하트비트(온프렘 복구=하트비트 재개 신호), Route 53(DNS 단일 권한)

- [ ] **Step 1: DNS 단일 권한 확인**

"지금 누가 서비스하나"를 Route 53 한 곳이 결정하도록, 온프렘/EKS 어느 쪽도 DNS 밖에서 트래픽을 자기 쪽으로 끌지 않음을 확인(온프렘 앱은 DR 중 scale-to-zero/read-only 상태 유지).

- [ ] **Step 2: failback 런북 작성**

`docs/RUNBOOK/dr-failback.md` — 순서:
1. 온프렘 하트비트 재개 확인(복구 신호)
2. **자동 failback 없음** — EKS가 read-write 유지
3. EKS 최신 데이터 → 온프렘 역복제(재해 중 최신은 EKS에 있으므로 필수)
4. 데이터 정합성 확인
5. 수동 승인 → DNS 온프렘 전환
6. EKS Cold 축소
> 역복제 방식(논리 복제 vs 스냅샷)은 실행 단계에서 확정. failback을 failover보다 엄격히 확인.

- [ ] **Step 3: Commit**

```bash
git add docs/RUNBOOK/dr-failback.md infra/terraform/aws/dr-orchestration.tf
git commit -m "docs(dr): 스플릿 브레인 방지·failback 런북"
```

---

### Task 7: DR 드릴 (end-to-end, 실제 RTO 측정)

> 전체가 실제로 도는지 증명한다. 감지→승인→복구→검증을 임시로 돌려 RTO를 실측하고, 스펙의 ~2.5시간 목표와 비교한다.

**Files:**
- Modify: `docs/RUNBOOK/dr-restore-drill.md` (Plan A Task 7 런북에 오케스트레이션 드릴 섹션 추가)

**Interfaces:**
- Consumes: Task 1~6 전체

- [ ] **Step 1: 감지 드릴 (오탐 방지 실증)**

하트비트만 끊었을 때 복합 알람이 발동하지 않음(4분면 2행) + pull까지 끊었을 때만 발동함을 확인. Task 2 Step 4 확장.

- [ ] **Step 2: 복구 드릴 (승인~PITR, 각 단계 타임스탬프 기록)**

승인 → EKS 기동 → ArgoCD → 복원 → DNS 전환을 격리 환경에서 실행하고 **단계별 소요 시간을 타임스탬프로 기록**. 종료선은 "PITR 재생 완료"(데이터 최신화). 실측 RTO를 런북에 남긴다(포폴 재료 — 목표 대비 실측).

- [ ] **Step 3: failback 드릴**

역복제 → 정합성 → 수동 DNS 전환 → EKS 축소를 리허설. 스플릿 브레인이 발생하지 않음을 확인.

- [ ] **Step 4: Commit**

```bash
git add docs/RUNBOOK/dr-restore-drill.md
git commit -m "docs(dr): DR 오케스트레이션 드릴·실측 RTO 기록"
```

---

## Self-Review

**Spec coverage (스펙 § DR 오케스트레이션 / 재해 감지 / RTO / Failback 대비):**
- 재해 감지 pull+push AND + 수동 게이트 → Task 1·2·3 ✓
- 복구 순차 오케스트레이션(EKS→ArgoCD→복원→DNS) → Task 4 (Plan B 의존) ✓
- 복원 자격증명 IAM 롤 + Vault unseal AWS KMS → Task 5 ✓
- 스플릿 브레인/failback → Task 6 ✓
- RTO 실측 드릴 → Task 7 ✓

**의존성 경고:**
- Task 4는 Plan B(EKS 오버레이) 없이는 완결 불가 — Task 1~3(감지)·6·7 런북은 선행 가능하나, 실제 복구 실행은 Plan B 완료 후.
- Task 1 push 하트비트는 온프렘 CloudWatch egress가 열려 있어야 함(Task 1 Step 0 실측이 게이트).

**제안값(팀장 논의·실측 대기):**
- pull N=5회/push M=3분 임계값, RTO 각 구간 배분(EKS 기동 30분 등)은 드릴 실측으로 교정
- 승인 게이트 야간 무응답 대응(다채널 알림), 역복제 방식(논리 vs 스냅샷)
- EKS 프로비저닝: 사전생성 빈 클러스터 vs 완전 IaC(비용/RTO 트레이드오프, 스펙 § 미결)

**단일 클라우드 (2026-07-04 갱신):** `aws` state의 S3 이전(#245) + Vault unseal의 AWS KMS 이전
(#246/#247) 완료로, 이 Plan C의 apply·복원 런타임은 **AWS 자격증명만으로 완결**된다. 과거엔
"복원은 AWS 롤(S3) + GCP SA(KMS) 둘 다 필요"였으나 이제 해당 없음 — GCP 관련 리소스·문서는
전부 이 갱신으로 제거됨.
