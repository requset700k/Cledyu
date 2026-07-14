# DR 재해 감지 + 알림 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 온프렘 상실을 오탐 없이(pull+push AND) 감지해 온프렘-독립 AWS 경로로 Discord에 알린다. 복구는 기존 수동 런북 유지(자동 오케스트레이션은 비목표).

**Architecture:** 두 독립 신호 — pull(Route53 health check가 `auth.cledyu.com` 딥체크) + push(온프렘 heartbeat CronJob이 us-east-1 CloudWatch에 dead man's switch metric 기록) — 를 CloudWatch 복합알람(AND)으로 묶어, SNS → Lambda → Discord 웹훅으로 알린다. Route53 health check 메트릭이 us-east-1 전용이므로 **감지 알람 스택 전체를 us-east-1**에 둔다(멤버 알람·복합알람 동일 리전 요건).

**Tech Stack:** Terraform(Route53/CloudWatch/SNS/Lambda/IAM/SecretsManager, aws provider `~> 5.0`, `us-east-1` alias), Helm(CronJob + ExternalSecret), ArgoCD, External Secrets Operator(`vault-backend` ClusterSecretStore), Python 3.12 Lambda.

## Global Constraints

- **리전:** 감지 스택(Route53 health check·pull/push 알람·복합알람·SNS·Lambda·Secrets Manager)은 **us-east-1**. heartbeat도 us-east-1로 발행. 나머지 Cledyu AWS 스택(ap-northeast-2)과 갈리는 유일 예외 — 스펙 § 결정 5.
- **terraform plan은 `-target` 부분 plan만.** tfvars 부재라 전체 plan 시 게이트 리소스 오-destroy 위험(메모리 `feedback_terraform_target_plan`).
- **terraform 변경 커밋엔 재생성된 `infra/terraform/aws/README.md`를 항상 함께 add** — 안 하면 pre-commit `terraform_docs` 훅이 커밋 중단(메모리 `feedback_terraform_docs`).
- **네이밍:** `var.name_prefix`(기본 `cledyu-lab`) 접두. 정책은 `data.aws_iam_policy_document`. 시크릿 output 없음.
- **apply·argocd sync·commit은 운영자(사용자)가 직접 실행.** 이 계획의 `terraform apply`/`argocd sync`/`git commit` 스텝은 운영자 스텝이다(메모리 `feedback_user_runs_commits`). 커밋 메시지는 `git commit -m` 방식, `Co-Authored-By` 금지.
- 검증: `terraform validate`/`fmt -check`, `helm template | kubeconform -strict -ignore-missing-schemas`.
- **실측 확인됨(2026-07-14):** ap-northeast-2 CloudWatch egress 열림(HTTP 404), `auth.cledyu.com` 라이브(HTTP 200, `"realm":"cledyu-learn"`). us-east-1 egress는 Task 1 Step 0에서 재확인(리전 변경분).

---

### Task 1: heartbeat 전용 IAM 사용자 + 정책 (dr-detection.tf 신설)

> `backup.tf`의 IAM writer 패턴을 따른다. PutMetricData는 리소스레벨 ARN 미지원이라 `resources=["*"]` + `cloudwatch:namespace` 조건으로 최소권한. 액세스 키는 TF가 만들지 않고(평문 state 회피) apply 후 CLI로 발급→Vault 저장.

**Files:**
- Create: `infra/terraform/aws/dr-detection.tf`
- Modify: `infra/terraform/aws/README.md` (terraform_docs 재생성)

**Interfaces:**
- Produces: IAM 사용자 `${var.name_prefix}-dr-heartbeat`(액세스 키를 Vault `cledyu/aws/dr-heartbeat`에 `access_key_id`/`secret_access_key`로 저장) — Task 2가 ESO로 소비.
- Produces: `provider "aws" { alias = "use1" }`(us-east-1) — Task 3·4가 사용.

- [ ] **Step 0: us-east-1 egress 재확인 (게이트, 운영자)**

heartbeat가 us-east-1로 발행하므로, 오늘 확인한 ap-northeast-2가 아니라 us-east-1 CloudWatch egress를 확인한다. `default` ns에서(egress NetworkPolicy 없음):

```bash
sudo kubectl run egress-use1 --rm -i --restart=Never --image=curlimages/curl -n default \
  --overrides='{"spec":{"securityContext":{"runAsNonRoot":true,"runAsUser":100,"seccompProfile":{"type":"RuntimeDefault"}},"containers":[{"name":"egress-use1","image":"curlimages/curl","securityContext":{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]},"runAsNonRoot":true,"seccompProfile":{"type":"RuntimeDefault"}},"args":["curl","-sS","-m","10","-o","/dev/null","-w","HTTP %{http_code}\n","https://monitoring.us-east-1.amazonaws.com/"]}]}}'
```
Expected: 아무 HTTP 코드(예 `HTTP 404`) = egress 열림 → 계속. 타임아웃 = 막힘 → **폴백 발동**(스펙 § 결정 5: pull을 ap-northeast-2 Synthetics canary로 바꾸고 전 스택 ap-northeast-2로 회귀). 이 계획은 egress 열림을 전제로 진행한다.

- [ ] **Step 1: dr-detection.tf 작성 (provider alias + heartbeat IAM)**

```hcl
# ─────────────────────────────────────────────────────────────────────────
# DR 재해 감지 (Plan C 감지 계층) — us-east-1 앵커.
# Route53 health check 메트릭이 us-east-1 전용이라, 복합알람 멤버 동일 리전
# 요건을 맞추려 감지 알람 스택 전체를 us-east-1 에 둔다(스펙 § 결정 5).
# ─────────────────────────────────────────────────────────────────────────
provider "aws" {
  alias  = "use1"
  region = "us-east-1"
}

# 온프렘 push 하트비트(dead man's switch)용 최소권한 사용자.
# PutMetricData 는 리소스레벨 ARN 을 지원하지 않으므로 resources=["*"] 로 두고
# cloudwatch:namespace 조건으로 Cledyu/DR 에만 한정한다. backup writer 키와 분리.
resource "aws_iam_user" "dr_heartbeat" {
  name = "${var.name_prefix}-dr-heartbeat"
}

data "aws_iam_policy_document" "dr_heartbeat" {
  statement {
    sid       = "PutHeartbeatMetric"
    actions   = ["cloudwatch:PutMetricData"]
    resources = ["*"]
    condition {
      test     = "StringEquals"
      variable = "cloudwatch:namespace"
      values   = ["Cledyu/DR"]
    }
  }
}

resource "aws_iam_user_policy" "dr_heartbeat" {
  name   = "${var.name_prefix}-dr-heartbeat"
  user   = aws_iam_user.dr_heartbeat.name
  policy = data.aws_iam_policy_document.dr_heartbeat.json
}
```

- [ ] **Step 2: 정적 검증**

Run (fmt로 정규화 후 validate — 손으로 쓴 HCL은 정렬이 안 맞아 `-check`가 실패하므로 먼저 포맷):
```bash
cd infra/terraform/aws && terraform fmt dr-detection.tf && terraform init -backend=false && terraform validate
```
Expected: `fmt`가 정규화(변경 시 `dr-detection.tf` 출력, 재실행 시 무출력) + `Success! The configuration is valid.`

- [ ] **Step 3: 부분 plan (운영자, -target)**

Run:
```bash
cd infra/terraform/aws && terraform plan \
  -target=aws_iam_user.dr_heartbeat -target=aws_iam_user_policy.dr_heartbeat
```
Expected: `Plan: 2 to add, 0 to change, 0 to destroy` (사용자 1 + user_policy 1). 다른 리소스 destroy가 뜨면 중단.

- [ ] **Step 4: apply + 액세스 키 발급 → Vault (운영자)**

```bash
cd infra/terraform/aws && terraform apply \
  -target=aws_iam_user.dr_heartbeat -target=aws_iam_user_policy.dr_heartbeat
aws iam create-access-key --user-name cledyu-lab-dr-heartbeat   # 출력값을 아래에 사용
vault kv put cledyu/aws/dr-heartbeat access_key_id=<AccessKeyId> secret_access_key=<SecretAccessKey>
```
Expected: Vault 경로 `cledyu/aws/dr-heartbeat`에 두 키 저장.

- [ ] **Step 5: README 재생성 + Commit (운영자)**

```bash
pre-commit run terraform_docs --files infra/terraform/aws/dr-detection.tf
git add infra/terraform/aws/dr-detection.tf infra/terraform/aws/README.md
git commit -m "feat(dr): 온프렘 heartbeat 전용 IAM(PutMetricData·namespace 한정) + us-east-1 provider"
```

---

### Task 2: dr-heartbeat Helm 차트 + ArgoCD 앱

> `backup-secrets` 차트의 ESO 패턴 + `data-postgres-cnpg` ArgoCD 앱 패턴을 따른다. 30초 상주 루프 대신 **1분 CronJob**(push 임계값 3분이라 충분, k8s 관용적). Kyverno `baseline-workload-security`가 `runAsNonRoot`를 강제하므로 securityContext 필수(2026-07-14 egress 테스트에서 실측 확인된 정책).

**Files:**
- Create: `gitops/apps/dr-heartbeat/Chart.yaml`
- Create: `gitops/apps/dr-heartbeat/values.yaml`
- Create: `gitops/apps/dr-heartbeat/templates/cronjob.yaml`
- Create: `gitops/apps/dr-heartbeat/templates/externalsecret.yaml`
- Create: `gitops/argocd/apps/platform-dr-heartbeat.yaml`

**Interfaces:**
- Consumes: Vault `cledyu/aws/dr-heartbeat`(Task 1), ClusterSecretStore `vault-backend`.
- Produces: CloudWatch custom metric `Cledyu/DR`·`OnPremHeartbeat=1`(us-east-1, ~1/분) — Task 3 push 알람이 소비.

- [ ] **Step 1: Chart.yaml**

```yaml
apiVersion: v2
name: dr-heartbeat
description: 온프렘 생존 신호를 us-east-1 CloudWatch에 기록하는 dead man's switch(DR 감지)
type: application
version: 0.1.0
```

- [ ] **Step 2: values.yaml**

```yaml
# 온프렘 → us-east-1 CloudWatch 하트비트. 키는 Vault aws/dr-heartbeat 에서 ESO로 주입.
# 키 발급: infra/terraform/aws Task 1 Step 4 참고.
region: us-east-1
namespace: Cledyu/DR
metricName: OnPremHeartbeat
schedule: "* * * * *"   # 매 1분
image: amazon/aws-cli:2
vaultKey: aws/dr-heartbeat
```

- [ ] **Step 3: templates/externalsecret.yaml**

```yaml
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: dr-heartbeat-creds
  namespace: {{ .Release.Namespace }}
  annotations:
    argocd.argoproj.io/sync-options: SkipDryRunOnMissingResource=true
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: vault-backend
    kind: ClusterSecretStore
  target:
    name: dr-heartbeat-creds
    creationPolicy: Owner
  data:
    - secretKey: ACCESS_KEY_ID
      remoteRef:
        key: {{ .Values.vaultKey }}
        property: access_key_id
    - secretKey: ACCESS_SECRET_KEY
      remoteRef:
        key: {{ .Values.vaultKey }}
        property: secret_access_key
```

- [ ] **Step 4: templates/cronjob.yaml**

```yaml
apiVersion: batch/v1
kind: CronJob
metadata:
  name: dr-heartbeat
  namespace: {{ .Release.Namespace }}
spec:
  schedule: {{ .Values.schedule | quote }}
  concurrencyPolicy: Forbid
  successfulJobsHistoryLimit: 1
  failedJobsHistoryLimit: 3
  jobTemplate:
    spec:
      backoffLimit: 1
      activeDeadlineSeconds: 50
      template:
        spec:
          restartPolicy: Never
          securityContext:
            runAsNonRoot: true
            runAsUser: 1000
            seccompProfile:
              type: RuntimeDefault
          containers:
            - name: heartbeat
              image: {{ .Values.image }}
              securityContext:
                allowPrivilegeEscalation: false
                capabilities:
                  drop: ["ALL"]
              env:
                - name: HOME
                  value: /tmp
                - name: AWS_ACCESS_KEY_ID
                  valueFrom:
                    secretKeyRef: { name: dr-heartbeat-creds, key: ACCESS_KEY_ID }
                - name: AWS_SECRET_ACCESS_KEY
                  valueFrom:
                    secretKeyRef: { name: dr-heartbeat-creds, key: ACCESS_SECRET_KEY }
                - name: AWS_DEFAULT_REGION
                  value: {{ .Values.region | quote }}
              command: ["aws"]
              args:
                - cloudwatch
                - put-metric-data
                - --namespace
                - {{ .Values.namespace | quote }}
                - --metric-name
                - {{ .Values.metricName | quote }}
                - --value
                - "1"
```

- [ ] **Step 5: gitops/argocd/apps/platform-dr-heartbeat.yaml**

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: platform-dr-heartbeat
  namespace: argocd
  finalizers:
    - resources-finalizer.argocd.argoproj.io
spec:
  project: default
  source:
    repoURL: https://github.com/requset700k/Cledyu.git
    targetRevision: main
    path: gitops/apps/dr-heartbeat
    helm:
      releaseName: dr-heartbeat
  destination:
    server: https://kubernetes.default.svc
    namespace: dr-system
  syncPolicy:
    automated: { prune: true, selfHeal: true }
    syncOptions:
      - CreateNamespace=true
      - ServerSideApply=true
    retry:
      limit: 5
      backoff: { duration: 10s, factor: 2, maxDuration: 3m }
```

- [ ] **Step 6: 정적 검증**

Run:
```bash
helm template dr-heartbeat gitops/apps/dr-heartbeat -n dr-system | kubeconform -strict -ignore-missing-schemas
```
Expected: CronJob·ExternalSecret 렌더 성공, 에러 0.

- [ ] **Step 7: Commit (운영자)**

```bash
git add gitops/apps/dr-heartbeat gitops/argocd/apps/platform-dr-heartbeat.yaml
git commit -m "feat(dr): 온프렘 heartbeat CronJob(1분)·ESO — us-east-1 dead man's switch"
```

- [ ] **Step 8: sync + dr-system egress 재확인 + 지표 도달 (운영자)**

```bash
argocd app sync platform-dr-heartbeat
# dr-system ns 에 egress NetworkPolicy 없음 재확인(대표성): Task 1 Step 0 명령의 -n 을 dr-system 으로
# 2~3분 후 us-east-1 지표 도달 확인:
aws cloudwatch get-metric-statistics --region us-east-1 --namespace Cledyu/DR \
  --metric-name OnPremHeartbeat --start-time $(date -u -d '5 min ago' +%FT%TZ) \
  --end-time $(date -u +%FT%TZ) --period 60 --statistics Sum
```
Expected: 최근 데이터포인트 존재(Sum≈1/분). CronJob 파드가 Kyverno에 막히면 securityContext(Step 4) 확인.

---

### Task 3: pull 프로브 + pull/push 알람 + 복합알람 + SNS (dr-detection.tf 확장)

> pull=Route53 health check(딥 STR_MATCH), push=Task 2 지표. `treat_missing_data=breaching`로 데이터 부재를 위반으로 본다. 복합알람은 둘 다 ALARM일 때만(AND). SNS·알람은 `aws.use1`.

**Files:**
- Modify: `infra/terraform/aws/dr-detection.tf`
- Modify: `infra/terraform/aws/README.md`

**Interfaces:**
- Consumes: `var.public_keycloak_host`(=auth.cledyu.com), Task 2 지표, Task 1 provider `aws.use1`.
- Produces: SNS 토픽 `aws_sns_topic.dr_alert`(us-east-1) — Task 4가 구독. 복합알람 `${var.name_prefix}-dr-disaster`.

- [ ] **Step 1: Route53 health check + SNS + 3개 알람 추가**

dr-detection.tf 하단에 append:
```hcl
# pull 프로브: 공개 엔드포인트를 딥 HTTP 로 감시. 온프렘이 죽으면 ALB→tailnet 프록시
# 업스트림이 끊겨 5xx → search_string 불일치로 health check 실패.
resource "aws_route53_health_check" "onprem_pull" {
  fqdn              = var.public_keycloak_host   # auth.cledyu.com
  type              = "HTTPS_STR_MATCH"
  resource_path     = "/realms/cledyu-learn"
  search_string     = "cledyu-learn"
  port              = 443
  request_interval  = 30
  failure_threshold = 5   # 제안값(드릴 튜닝)
  tags = { Name = "${var.name_prefix}-dr-pull" }
}

# 알림 허브. 복합알람 → SNS → (Task 4) Lambda → Discord.
resource "aws_sns_topic" "dr_alert" {
  provider = aws.use1
  name     = "${var.name_prefix}-dr-alert"
}

# pull 알람: Route53 HealthCheckStatus(<1=비정상). 이 메트릭은 us-east-1 전용.
resource "aws_cloudwatch_metric_alarm" "pull" {
  provider            = aws.use1
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

# push 알람: heartbeat 지표가 M분(=evaluation_periods) 없으면 breaching.
resource "aws_cloudwatch_metric_alarm" "push" {
  provider            = aws.use1
  alarm_name          = "${var.name_prefix}-dr-push"
  namespace           = "Cledyu/DR"
  metric_name         = "OnPremHeartbeat"
  comparison_operator = "LessThanThreshold"
  threshold           = 1
  evaluation_periods  = 3    # 3분(제안값)
  period              = 60
  statistic           = "Sum"
  treat_missing_data  = "breaching"
}

# 복합알람: 둘 다 ALARM 일 때만(AND) → 단일 신호 오탐 차단.
resource "aws_cloudwatch_composite_alarm" "disaster" {
  provider      = aws.use1
  alarm_name    = "${var.name_prefix}-dr-disaster"
  alarm_rule    = "ALARM(${aws_cloudwatch_metric_alarm.pull.alarm_name}) AND ALARM(${aws_cloudwatch_metric_alarm.push.alarm_name})"
  alarm_actions = [aws_sns_topic.dr_alert.arn]
}
```

- [ ] **Step 2: 정적 검증**

Run:
```bash
cd infra/terraform/aws && terraform fmt dr-detection.tf && terraform validate
```
Expected: fmt 정규화 + valid.

- [ ] **Step 3: 부분 apply (운영자, -target)**

```bash
cd infra/terraform/aws && terraform apply \
  -target=aws_route53_health_check.onprem_pull -target=aws_sns_topic.dr_alert \
  -target=aws_cloudwatch_metric_alarm.pull -target=aws_cloudwatch_metric_alarm.push \
  -target=aws_cloudwatch_composite_alarm.disaster
```
Expected: 5 to add. apply 후 pull 알람은 곧 OK(auth.cledyu.com 200), push 알람은 heartbeat 도착 시 OK.

- [ ] **Step 4: 4분면 오탐 방지 실증 (운영자)**

heartbeat만 끊었을 때 push=ALARM 이지만 **복합=OK**(pull 정상)인지 확인 = AND 오탐 방지 실증.
```bash
kubectl -n dr-system patch cronjob dr-heartbeat -p '{"spec":{"suspend":true}}'
# 4분 후
aws cloudwatch describe-alarms --region us-east-1 \
  --alarm-names cledyu-lab-dr-push --query 'MetricAlarms[].StateValue'
aws cloudwatch describe-alarms --region us-east-1 \
  --alarm-names cledyu-lab-dr-disaster --query 'CompositeAlarms[].StateValue'
kubectl -n dr-system patch cronjob dr-heartbeat -p '{"spec":{"suspend":false}}'   # 원복
```
Expected: push=`ALARM`, disaster=`OK`. (둘 다 죽어야 disaster=ALARM — Task 5 전체 드릴에서 실증)

- [ ] **Step 5: README 재생성 + Commit (운영자)**

```bash
pre-commit run terraform_docs --files infra/terraform/aws/dr-detection.tf
git add infra/terraform/aws/dr-detection.tf infra/terraform/aws/README.md
git commit -m "feat(dr): pull(Route53)·push 알람 복합(AND) 재해 감지 + SNS(us-east-1)"
```

---

### Task 4: SNS → Lambda → Discord 알림 (dr-alert-lambda)

> 레포 최초 Lambda. Discord 웹훅은 **AWS Secrets Manager**에 둔다(온프렘 Vault 비의존 — 재해 시 Vault가 죽어도 알림이 가야 함, 스펙 § 결정 1). 웹훅 값은 TF가 관리 안 함(평문 state 회피), apply 후 CLI로 주입.

**Files:**
- Create: `infra/terraform/aws/dr-alert-lambda.tf`
- Create: `infra/terraform/aws/dr-alert-lambda/index.py`
- Modify: `infra/terraform/aws/versions.tf` (archive provider 추가)
- Modify: `infra/terraform/aws/README.md`

**Interfaces:**
- Consumes: `aws_sns_topic.dr_alert`(Task 3), provider `aws.use1`.
- Produces: 복합알람 ALARM → Discord 채널 메시지.

- [ ] **Step 0: archive provider 선언 (versions.tf)**

`data.archive_file`은 `hashicorp/archive` provider를 요구한다. `versions.tf`의 `required_providers`에 추가(현재 `aws`만 선언):
```hcl
    archive = {
      source  = "hashicorp/archive"
      version = "~> 2.4"
    }
```
그 뒤 `cd infra/terraform/aws && terraform init` 으로 provider 설치(운영자, 실 backend). 오프라인 검증만 할 땐 `terraform init -backend=false`.

- [ ] **Step 1: Lambda 소스 `dr-alert-lambda/index.py`**

```python
import json
import os
import urllib.request

import boto3

_sm = boto3.client("secretsmanager")
_webhook = None


def _webhook_url():
    global _webhook
    if _webhook is None:
        resp = _sm.get_secret_value(SecretId=os.environ["WEBHOOK_SECRET_ARN"])
        _webhook = json.loads(resp["SecretString"])["url"]
    return _webhook


def handler(event, context):
    for record in event.get("Records", []):
        raw = record["Sns"]["Message"]
        try:
            alarm = json.loads(raw)
            text = (
                ":rotating_light: **DR 재해 감지** — "
                f"{alarm.get('AlarmName', 'unknown')}\n"
                f"상태: {alarm.get('NewStateValue', '?')}\n"
                f"사유: {alarm.get('NewStateReason', '')}"
            )
        except (ValueError, KeyError):
            text = f":rotating_light: DR alert: {raw}"
        payload = json.dumps({"content": text}).encode("utf-8")
        req = urllib.request.Request(
            _webhook_url(),
            data=payload,
            headers={"Content-Type": "application/json"},
            method="POST",
        )
        urllib.request.urlopen(req, timeout=10)
    return {"ok": True}
```

- [ ] **Step 2: `dr-alert-lambda.tf`**

```hcl
# ── DR 재해 알림: SNS → Lambda → Discord (us-east-1) ──
data "archive_file" "dr_alert" {
  type        = "zip"
  source_file = "${path.module}/dr-alert-lambda/index.py"
  output_path = "${path.module}/dr-alert-lambda/dr-alert.zip"
}

# Discord 웹훅 URL. 값은 TF 밖에서 넣는다(평문 state 회피). 토큰이 history·ps·argv 에
# 남지 않도록 0600 임시파일 + file:// 로 주입 — 절차는 런북 §7.1(웹훅 로테이션) 참고.
resource "aws_secretsmanager_secret" "discord_webhook" {
  provider = aws.use1
  name     = "${var.name_prefix}-dr-discord-webhook"
}

data "aws_iam_policy_document" "dr_alert_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "dr_alert" {
  name               = "${var.name_prefix}-dr-alert-lambda"
  assume_role_policy = data.aws_iam_policy_document.dr_alert_assume.json
}

data "aws_iam_policy_document" "dr_alert" {
  statement {
    sid       = "ReadWebhook"
    actions   = ["secretsmanager:GetSecretValue"]
    resources = [aws_secretsmanager_secret.discord_webhook.arn]
  }
  statement {
    sid       = "Logs"
    actions   = ["logs:CreateLogGroup", "logs:CreateLogStream", "logs:PutLogEvents"]
    resources = ["arn:aws:logs:us-east-1:*:*"]
  }
}

resource "aws_iam_role_policy" "dr_alert" {
  name   = "${var.name_prefix}-dr-alert-lambda"
  role   = aws_iam_role.dr_alert.id
  policy = data.aws_iam_policy_document.dr_alert.json
}

resource "aws_lambda_function" "dr_alert" {
  provider         = aws.use1
  function_name    = "${var.name_prefix}-dr-alert"
  filename         = data.archive_file.dr_alert.output_path
  source_code_hash = data.archive_file.dr_alert.output_base64sha256
  handler          = "index.handler"
  runtime          = "python3.12"
  role             = aws_iam_role.dr_alert.arn
  timeout          = 15
  environment {
    variables = {
      WEBHOOK_SECRET_ARN = aws_secretsmanager_secret.discord_webhook.arn
    }
  }
}

resource "aws_lambda_permission" "sns" {
  provider      = aws.use1
  statement_id  = "AllowSNSInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.dr_alert.function_name
  principal     = "sns.amazonaws.com"
  source_arn    = aws_sns_topic.dr_alert.arn
}

resource "aws_sns_topic_subscription" "dr_alert" {
  provider  = aws.use1
  topic_arn = aws_sns_topic.dr_alert.arn
  protocol  = "lambda"
  endpoint  = aws_lambda_function.dr_alert.arn
}
```

- [ ] **Step 3: 정적 검증**

Run:
```bash
cd infra/terraform/aws && terraform fmt dr-alert-lambda.tf versions.tf && terraform validate
python3 -m py_compile dr-alert-lambda/index.py
```
Expected: fmt 정규화 + valid + py_compile 무출력.

- [ ] **Step 4: 부분 apply + 웹훅 주입 (운영자)**

Discord 서버에서 **신규 웹훅**(on-prem discord-proxy와 별개 채널 권장) 발급 후:
```bash
cd infra/terraform/aws && terraform apply \
  -target=aws_secretsmanager_secret.discord_webhook -target=aws_iam_role.dr_alert \
  -target=aws_iam_role_policy.dr_alert -target=aws_lambda_function.dr_alert \
  -target=aws_lambda_permission.sns -target=aws_sns_topic_subscription.dr_alert
# 웹훅 토큰을 shell history·ps·argv 에 남기지 않고 주입(0600 임시파일 + file://):
umask 077; tmp=$(mktemp)
read -rs -p "Discord webhook URL: " WEBHOOK_URL; echo
printf '{"url":"%s"}' "$WEBHOOK_URL" > "$tmp"
aws secretsmanager put-secret-value --region us-east-1 \
  --secret-id cledyu-lab-dr-discord-webhook \
  --secret-string file://"$tmp"
shred -u "$tmp" 2>/dev/null || rm -f "$tmp"; unset WEBHOOK_URL
```

- [ ] **Step 5: SNS 테스트 발행 → Discord 도착 확인 (운영자)**

```bash
TOPIC_ARN=$(aws sns list-topics --region us-east-1 \
  --query "Topics[?ends_with(TopicArn, ':cledyu-lab-dr-alert')].TopicArn" --output text)
aws sns publish --region us-east-1 --topic-arn "$TOPIC_ARN" \
  --message '{"AlarmName":"cledyu-lab-dr-disaster","NewStateValue":"ALARM","NewStateReason":"테스트 발행"}'
```
Expected: Discord 채널에 :rotating_light: 메시지 도착. 안 오면 Lambda 로그 확인:
`aws logs tail /aws/lambda/cledyu-lab-dr-alert --region us-east-1 --since 5m`

- [ ] **Step 6: README 재생성 + Commit (운영자)**

```bash
pre-commit run terraform_docs --files infra/terraform/aws/dr-alert-lambda.tf
git add infra/terraform/aws/dr-alert-lambda.tf infra/terraform/aws/dr-alert-lambda/index.py infra/terraform/aws/versions.tf infra/terraform/aws/README.md
git commit -m "feat(dr): SNS→Lambda→Discord 재해 알림(웹훅 Secrets Manager, us-east-1)"
```

---

### Task 5: 감지 드릴(end-to-end) + 런북

> 감지→알림 전체가 실제로 도는지 증명하고 체감 감지 지연을 실측한다. 4분면 오탐 방지 + 진짜 재해(둘 다 죽음)일 때만 Discord 발동을 확인한다.

**Files:**
- Create: `docs/RUNBOOK/dr-detection.md`

**Interfaces:**
- Consumes: Task 1~4 전체(배포·apply 완료 상태).

- [ ] **Step 1: 감지 드릴 실행 (운영자, 타임스탬프 기록)**

1. **정상 확인:** `disaster=OK`, `pull=OK`, `push=OK`.
2. **오탐 방지(push만):** heartbeat suspend → 3~4분 후 `push=ALARM`·`disaster=OK` 확인(Task 3 Step 4).
3. **진짜 재해(둘 다):** heartbeat suspend 유지 + pull 실패 유도(격리·짧게 — 예: health check `resource_path`를 일시적으로 존재하지 않는 경로로 바꾸거나, 검증 창에서만 프록시 차단) → `pull=ALARM`·`push=ALARM`·`disaster=ALARM` → **Discord에 실제 알림 도착** 확인.
4. **원복:** heartbeat suspend 해제 + health check 원복 → 수 분 후 `disaster=OK` 복귀.

각 전이의 벽시계 시각을 기록(감지 지연 실측). 예상 체감 지연 ~2.5~3분(pull failure_threshold=5@30s, push 3@60s의 AND).

- [ ] **Step 2: 런북 작성 `docs/RUNBOOK/dr-detection.md`**

포함: (a) 아키텍처 1장 요약(pull/push/AND/SNS→Lambda→Discord, us-east-1 앵커 이유), (b) Step 1의 드릴 절차와 **실측 감지 지연·4분면 표**, (c) 알림 수신 시 대응 = `dr-eks-bootstrap.md` 수동 복구로 링크, (d) 임계값(pull N=5·push M=3분)과 튜닝 근거, (e) 웹훅 로테이션(Secrets Manager put-secret-value)·heartbeat 키 로테이션 절차.

- [ ] **Step 3: Commit (운영자)**

```bash
git add docs/RUNBOOK/dr-detection.md
git commit -m "docs(dr): 재해 감지 런북·드릴 실측(4분면 오탐방지·감지지연·대응링크)"
```

---

## Self-Review

**Spec coverage (스펙 § 대비):**
- 신호 A pull(Route53 STR_MATCH) → Task 3 ✓
- 신호 B push(heartbeat dead man's switch) → Task 1(IAM)+Task 2(CronJob) ✓
- 결합 복합알람 AND → Task 3 ✓
- 알림 SNS→Lambda→Discord, 웹훅 Secrets Manager(Vault 비의존) → Task 4 ✓
- 결정 5 us-east-1 앵커 + provider alias → Task 1(alias)·Task 3·4(provider) ✓
- 결정 2 egress(리전 변경분 재확인) → Task 1 Step 0 ✓
- 결정 3 1분 CronJob → Task 2 ✓
- 감지 드릴·4분면 실증·실측 지연 → Task 3 Step 4 + Task 5 ✓
- 엣지: dr-system egress 재확인 → Task 2 Step 8 ✓

**의존성 순서:** Task 1(IAM·provider) → 2(지표 발행) → 3(알람, 지표·provider 소비) → 4(SNS 구독) → 5(드릴). push 4분면 드릴(Task 3 Step 4)은 Task 2 배포 후 성립.

**타입/이름 일관성:** SNS `aws_sns_topic.dr_alert`(Task 3 생성 → Task 4 구독), 시크릿 이름 `cledyu-lab-dr-discord-webhook`(TF·put-secret-value·env 일치), Vault 경로 `cledyu/aws/dr-heartbeat`(Task 1 저장 → Task 2 ESO `vaultKey: aws/dr-heartbeat`), ESO Secret 키 `ACCESS_KEY_ID`/`ACCESS_SECRET_KEY`(externalsecret ↔ cronjob env 일치) — 모두 정합.

**제안값(드릴 튜닝 대기):** pull `failure_threshold=5`, push `evaluation_periods=3`.

**폴백:** Task 1 Step 0에서 us-east-1 egress가 막히면 → pull을 ap-northeast-2 Synthetics canary로 전환하고 전 스택 ap-northeast-2 회귀(스펙 § 결정 5). 이 경우 Task 3의 pull 리소스와 provider가 canary로 교체됨.
