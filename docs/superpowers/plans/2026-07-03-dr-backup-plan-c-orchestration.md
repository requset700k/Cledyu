# DR/백업 Plan C — DR 오케스트레이션 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 온프렘 상실을 오탐 없이 감지하고(pull+push AND + 수동 승인), 감지 후 EKS로 컨트롤플레인·durable 데이터를 자동 복구하며(Lambda/Step Functions), 온프렘 복귀 시 스플릿 브레인을 방지한다(DNS 단일 권한 + 수동 failback).

**Architecture:** 온프렘 push 하트비트(CloudWatch) + AWS pull 프로브(Route 53) → CloudWatch 복합 알람(AND) → EventBridge → Step Functions(수동 승인 게이트 → EKS 기동 → ArgoCD 부트스트랩 → S3 복원 → DNS 전환). 복원 자격증명은 정적 키가 아니라 실행 롤(S3 read + GCP KMS decrypt). failback은 자동 금지, 역복제 후 수동 전환.

**Tech Stack:** Terraform(CloudWatch/EventBridge/Step Functions/Lambda/IAM/Route 53), 온프렘 CronJob(awscli PutMetricData), Vault(GCP KMS auto-unseal), CloudNativePG(PITR 복원), Velero(오브젝트 복원).

## Dependencies (중요)

- **Plan A(백업 계층) 완료 전제**: S3에 Postgres WAL/베이스백업(`postgres/`), Vault raft 스냅샷(`vault/`), Velero(`velero/`)이 실제로 쌓여 있어야 복원할 대상이 있다. Task 5·6은 이 백업들을 읽는다.
- **Plan B(EKS 오버레이) 완료 전제**: Task 4의 "EKS 기동 → ArgoCD 동기화"는 Plan B가 만든 EKS용 오버레이(Longhorn→EBS, MetalLB→ALB Controller 등)가 있어야 성립한다. **Task 4는 Plan B 없이는 완결 불가** — Task 1~3(감지)·Task 7(런북)은 Plan B와 독립으로 선행 가능.
- **이중 클라우드 인증**: `infra/terraform/aws`는 GCS backend라 apply에 AWS+GCP 자격증명 동시 필요(`project_aws_tf_dual_cloud_auth`). 정적 검증까지만 하고 실제 apply·검증은 양쪽 접근자에게 위임하는 것이 기본 패턴.

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

- [ ] **Step 4: apply 후 4분면 동작 확인 (AWS+GCP 자격증명 필요)**

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
복원 내부 순서(스펙 § 백업 우선순위 기술 순서): **Vault 복원→unseal(GCP KMS)→ESO 정상화 → Postgres PITR/Keycloak**. Vault가 먼저 열려야 나머지가 시크릿을 받는다.

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

### Task 5: 복원 자격증명 — IAM 롤 + GCP KMS 접근

> 복원 경로에 정적 키를 두지 않는다(S3 키는 Vault 안에 있어 순환). 복원 컴퓨트에 롤로 S3 read를 주고, 복원된 Vault가 스스로 unseal하도록 GCP KMS decrypt 자격을 **Vault 바깥에서** 공급한다.

**Files:**
- Modify: `infra/terraform/aws/dr-orchestration.tf` (IAM 롤/정책)
- Create: `gitops/apps/dr-restore-gcpkms/*` (복원된 Vault용 GCP SA Secret 주입 — EKS 오버레이에 포함)

**Interfaces:**
- Produces: 복원 실행 롤(S3 `cledyu-lab-dr-backups` read-only), 복원된 Vault에 GCP KMS decrypt 자격 공급 경로

- [ ] **Step 1: 복원 실행 롤 (S3 read-only)**

```hcl
data "aws_iam_policy_document" "restore" {
  statement {
    actions   = ["s3:GetObject", "s3:ListBucket", "s3:GetBucketLocation"]
    resources = [aws_s3_bucket.dr_backups.arn, "${aws_s3_bucket.dr_backups.arn}/*"]
  }
  # 버킷이 SSE-KMS(dr_backups CMK)라, 백업을 읽으려면 그 키의 복호화 권한도 필요하다(Plan A backup.tf).
  statement {
    actions   = ["kms:Decrypt", "kms:DescribeKey"]
    resources = [aws_kms_key.dr_backups.arn]
  }
}
# Step Functions/Lambda 실행 롤에 attach. 정적 키 없음.
```

- [ ] **Step 2: GCP KMS 자격 공급 (닭-달걀 회피)**

복원된 Vault는 sealed로 뜨고 GCP KMS로 unseal한다(스펙 § Vault 부트스트랩 체인). GCP KMS 호출 자격을 Vault 안에 둘 수 없으므로(잠겨서 못 꺼냄), **복원 프로세스가 GCP SA 키(kms decrypt 권한)를 EKS Secret으로 미리 주입**해 Vault config가 참조하게 한다. 이 GCP SA 키만은 Vault 밖의 안전한 곳(예: AWS Secrets Manager, 복원 롤로 접근)에 둔다.

> 이중 클라우드 지점: AWS 복원 롤(S3) + GCP SA(KMS)가 **둘 다** 있어야 복원이 완결된다. terraform apply와 동일한 dual-cloud 제약이 DR 런타임에도 존재.

- [ ] **Step 3: 검증 + Commit**

Run: `terraform validate && terraform fmt -check`
```bash
git add infra/terraform/aws/dr-orchestration.tf gitops/apps/dr-restore-gcpkms
git commit -m "feat(dr): 복원 실행 롤(S3 read)·GCP KMS unseal 자격 공급"
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
- 복원 자격증명 IAM 롤 + GCP KMS → Task 5 ✓
- 스플릿 브레인/failback → Task 6 ✓
- RTO 실측 드릴 → Task 7 ✓

**의존성 경고:**
- Task 4는 Plan B(EKS 오버레이) 없이는 완결 불가 — Task 1~3(감지)·6·7 런북은 선행 가능하나, 실제 복구 실행은 Plan B 완료 후.
- Task 1 push 하트비트는 온프렘 CloudWatch egress가 열려 있어야 함(Task 1 Step 0 실측이 게이트).

**제안값(팀장 논의·실측 대기):**
- pull N=5회/push M=3분 임계값, RTO 각 구간 배분(EKS 기동 30분 등)은 드릴 실측으로 교정
- 승인 게이트 야간 무응답 대응(다채널 알림), 역복제 방식(논리 vs 스냅샷)
- EKS 프로비저닝: 사전생성 빈 클러스터 vs 완전 IaC(비용/RTO 트레이드오프, 스펙 § 미결)

**이중 클라우드:** 복원은 AWS 롤(S3) + GCP SA(KMS) 둘 다 필요. apply·드릴은 양쪽 자격증명 보유자가 수행.
