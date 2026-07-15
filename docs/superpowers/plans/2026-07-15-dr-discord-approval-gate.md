# DR Discord 승인 게이트 Implementation Plan (Plan 1/2)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 복합알람이 울리면 Step Functions 가 시작돼 Discord 에 **승인 버튼 + Vault 스냅샷 드롭다운**을 띄우고,
허용된 사용자가 승인하면 태스크 토큰이 풀려 다음 단계로 넘어간다. **여기까지가 이 계획의 산출물**이며 과금은 ~0 이다.

**Architecture:** 복합알람(us-east-1) → EventBridge 규칙 → failover-trigger Lambda → `sfn.start_execution`(ap-northeast-2)
→ approval-request Lambda(`.waitForTaskToken`)가 S3 스냅샷 목록을 읽어 Discord 메시지 게시 → 사람이 클릭 →
Function URL → interaction Lambda(Ed25519 검증 → 허용목록 → DynamoDB) → `SendTaskSuccess({snapshot})`.

**Tech Stack:** Terraform(Lambda/DynamoDB/EventBridge/SFN/IAM/Secrets Manager), python3.12(boto3·표준 라이브러리만),
**nodejs20**(interaction Lambda — Ed25519 때문, §3.4), Discord Interactions API.

**설계 근거:** `docs/superpowers/specs/2026-07-15-dr-discord-approval-orchestration-design.md` (커밋 `02c14bd`).
이 계획의 모든 §참조는 그 스펙을 가리킨다.

**Plan 2 (별도):** CodeBuild·메인 SM 13단계·bastion 스크립트·dns-switch·런북·드릴.
이 계획이 끝나면 승인 후 SFN 은 `Succeed` 로 끝난다(Plan 2 가 그 자리에 [2]~[13] 을 붙인다).

## Global Constraints

- **리전:** 감지·알림·EventBridge·failover-trigger = **us-east-1**(`provider aws.use1`). 나머지 전부 **ap-northeast-2**(기본 provider). 근거 §3.2
- **작업 브랜치:** `docs/dr-discord-approval-orchestration` (스펙 `02c14bd` 이 이미 올라가 있음)
- **terraform 컨벤션:** `var.name_prefix` 접두, 정책은 `data.aws_iam_policy_document`, 시크릿 output 금지
- **⚠️ `-target` 없는 plan/apply 금지 (2026-07-15 실측 갱신)** — `terraform.tfvars` 는 존재하나
  `enable_public_ingress`·`dr_detection_armed`·`alert_email` **3개만** 설정한다. **`enable_eks_dr` 가 없어
  기본값 `false`** → `-target` 없이 apply 하면 state 의 **`eks_dr` 리소스 129개(warm pilot-light 스택,
  `module.eks_dr[0]`)가 전부 destroy** 된다. pilot-light 를 `-var enable_eks_dr=true` 로 apply 해서
  tfvars 에 안 남은 탓이다. `terraform.tfvars.example` 에도 DR 게이트가 하나도 없다.
  → **근본 해결은 tfvars 에 게이트 명시**(`enable_eks_dr=true` / `eks_dr_active=false` /
  `dr_orchestration_armed=false`) 후 `-target` 없이 plan 해서 `destroy 0` 확인. **미해결 — 후속 과제.**
- **⚠️ `-target` 에 `aws_iam_role_policy.*` 를 반드시 명시 (2026-07-15 실측)** — `-target` 은 **의존성만**
  따라가고 **의존하는 것(dependent)은 안 따라간다.** Lambda/SM 은 `aws_iam_role` 을 참조하지만
  `aws_iam_role_policy` 는 참조하지 않으므로(정책이 롤에 의존할 뿐), 롤만 생기고 **정책이 누락**된다 →
  권한 없는 롤로 SFN 생성 시도 → AccessDenied 재시도 루프(2분+ hang 후 실패). 4개 롤 전부
  `PolicyNames: []` 였다. `terraform validate` 는 디스크 전체를 보므로 이걸 못 잡는다.
- **⚠️ terraform docs** — `infra/terraform/aws` 의 리소스/변수/출력을 바꾼 커밋엔 재생성된 `README.md` 를 반드시 함께 `git add`(안 하면 pre-commit `terraform_docs` 훅이 커밋 중단)
- **pre-commit 훅:** `terraform_fmt`·`terraform_validate`·`terraform_tflint`·`terraform_docs`·`ruff`(--fix)·`ruff-format`
- **Lambda 패키징:** 기존 `dr-alert` 패턴 — `data.archive_file` + `source_file`, 외부 의존성 없음. 빌드 산출물 `.zip` 은 `.gitignore` 에 추가
- **커밋:** 사용자가 직접 실행한다. 각 Task 의 Commit 스텝은 **명령어만 제시**하고 실행하지 않는다
- **커밋 메시지:** `Co-Authored-By` 줄 금지. heredoc 금지(`git commit -m` 방식)

---

### Task 1: DynamoDB `approvals` 테이블 + 신규 변수

> 승인 토큰·스냅샷 선택을 담는 상태 저장소. Discord `custom_id` 가 100자 상한이라 태스크 토큰(수백 자)을
> 버튼에 실을 수 없어 필요하다(§3.5).

**Files:**
- Create: `infra/terraform/aws/dr-orchestration.tf`
- Modify: `infra/terraform/aws/variables.tf` (끝에 추가)
- Modify: `infra/terraform/aws/README.md` (terraform_docs 재생성)

**Interfaces:**
- Produces: `aws_dynamodb_table.dr_approvals`(이름 `${var.name_prefix}-dr-approvals`, PK `approvalId`, TTL 속성 `ttl`) — Task 2·3 이 참조
- Produces: `var.dr_orchestration_armed`, `var.dr_approver_ids` — Task 5 가 참조

- [ ] **Step 1: 변수 2개 추가**

`infra/terraform/aws/variables.tf` 끝에 추가:

```hcl
variable "dr_orchestration_armed" {
  description = <<-EOT
    DR 자동 페일오버 오케스트레이션(EventBridge → Step Functions) 무장 여부.
    false(기본): EventBridge 규칙이 생성되지 않아 복합알람이 ALARM 이 돼도 승인 요청이 발생하지 않는다.
    ⚠️ dr_detection_armed 와 "같은 패턴"이 아니다 — dr_detection_armed 는 actions_enabled 라 SNS 발행만
    억제하고, CloudWatch 는 알람 상태변화 이벤트를 EventBridge 기본 버스로 계속 쏜다. 그래서 EventBridge
    규칙은 local.pub && dr_detection_armed && dr_orchestration_armed 의 AND 로 게이트한다(설계 §7.4).
  EOT
  type        = bool
  default     = false
}

variable "dr_approver_ids" {
  description = <<-EOT
    DR 페일오버를 승인할 수 있는 Discord 사용자 ID 목록(snowflake 문자열).
    tfvars 가 아니라 여기 default 로 커밋한다 — .gitignore 가 *.tfvars 를 제외하므로 tfvars 에 두면
    승인자 변경이 코드 리뷰를 거치지 않는다(설계 §5.4). Discord user ID 는 서버 멤버에게 이미 보이는
    공개 식별자라 PUBLIC 레포 커밋을 감수한다 — 실질 방어선은 목록의 비밀성이 아니라 승인자 계정 2FA 다.
  EOT
  type        = list(string)
  default     = []
}
```

- [ ] **Step 2: DynamoDB 테이블 생성**

`infra/terraform/aws/dr-orchestration.tf` 신규:

```hcl
# ── DR 원클릭 페일오버 오케스트레이션 (설계: docs/superpowers/specs/2026-07-15-dr-discord-approval-orchestration-design.md) ──
# 감지·알림(dr-detection.tf, dr-alert-lambda.tf)은 그대로 두고 그 뒤를 잇는다.

# 승인 상태 저장소. Discord custom_id 100자 상한 때문에 태스크 토큰(수백 자)을 버튼에 실을 수 없어
# 짧은 approvalId 만 버튼에 싣고 토큰은 여기 둔다(설계 §3.5). select menu 선택도 여기 누적된다 —
# Discord 는 드롭다운 조작과 버튼 클릭을 별개 interaction 으로 보내고, 버튼 payload 에 드롭다운의
# 현재 선택값이 실려 오지 않기 때문이다.
resource "aws_dynamodb_table" "dr_approvals" {
  name         = "${var.name_prefix}-dr-approvals"
  billing_mode = "PAY_PER_REQUEST" # 재해·드릴 시에만 쓰는 저빈도 테이블
  hash_key     = "approvalId"

  attribute {
    name = "approvalId"
    type = "S"
  }

  # 승인 대기 24h(= approval-request 의 .waitForTaskToken 타임아웃)를 넘긴 항목 자동 청소.
  # DynamoDB TTL 삭제는 만료 시각 "이후"에 수행되므로(최대 48h 지연) 대기 중 조기 삭제는 없다.
  ttl {
    attribute_name = "ttl"
    enabled        = true
  }

  point_in_time_recovery {
    enabled = false # 재해 시 재생성 가능한 임시 상태 — 백업 불요
  }

  tags = { Project = "cledyu", Purpose = "dr", ManagedBy = "terraform" }
}
```

- [ ] **Step 3: 검증 — fmt/validate/부분 plan**

Run:
```bash
cd infra/terraform/aws
terraform fmt -check dr-orchestration.tf variables.tf
terraform validate
terraform plan -target=aws_dynamodb_table.dr_approvals
```
Expected: fmt 통과, `Success! The configuration is valid.`, plan 이 **`1 to add`** 만 표시(`destroy` 0건).
`destroy` 가 하나라도 있으면 **중단** — `-target` 이 빠졌거나 변수 기본값이 어긋난 것이다.

- [ ] **Step 4: terraform docs 재생성**

Run:
```bash
cd /home/user/Cledyu && pre-commit run terraform_docs --files infra/terraform/aws/variables.tf
```
Expected: `README.md` 가 수정됨(신규 변수 2개 반영). 훅이 파일을 고치므로 첫 실행은 `Failed` 로 표시될 수 있다 —
`git diff infra/terraform/aws/README.md` 로 변수 2개가 표에 들어갔는지 확인하고, 재실행하면 `Passed`.

- [ ] **Step 5: Commit** (사용자가 실행)

```bash
git add infra/terraform/aws/dr-orchestration.tf infra/terraform/aws/variables.tf infra/terraform/aws/README.md
git commit -m "feat(dr): 승인 상태 저장소(DynamoDB) + 오케스트레이션 무장/승인자 변수"
```

---

### Task 2: approval-request Lambda (스냅샷 목록 → Discord 승인 메시지)

> SFN 의 첫 상태. S3 에서 Vault 스냅샷을 최신순 25개 뽑아 드롭다운으로 만들고, 승인 버튼과 함께 Discord 에
> 게시한 뒤 `.waitForTaskToken` 으로 멈춘다.

**Files:**
- Create: `infra/terraform/aws/dr-orchestration-lambda/approval-request/index.py`
- Modify: `infra/terraform/aws/dr-orchestration.tf` (Lambda·IAM·로그그룹 추가)
- Modify: `.gitignore` (Lambda zip 규칙 확장)
- Modify: `infra/terraform/aws/README.md`

**Interfaces:**
- Consumes: `aws_dynamodb_table.dr_approvals`(Task 1)
- Produces: `aws_lambda_function.dr_approval_request` — Task 4(테스트 SM)·Plan 2(메인 SM) 가 참조
- Produces: DynamoDB item `{approvalId(PK), taskToken, latestSnapshot, ttl}` — Task 3 이 읽는다
- **Lambda 입력 계약(Task 4·5·Plan 2 가 지켜야 함):** `{"taskToken": "<토큰>", "input": {<SFN 실행 입력 전체>}}`.
  SFN 은 `"input.$" = "$"` 로 넘긴다. `input.mode` 가 **정확히 문자열 `"test"`** 일 때만 테스트 렌더이고
  그 외 전부 실재해다(§7.2 H3). `"mode.$" = "$.mode"` 로 직접 뽑으면 실재해 입력엔 그 키가 없어
  `States.Runtime` 으로 죽는다.
- Produces: 반환 `{"approvalId": "<16자hex>"}` (SFN 은 이 값을 쓰지 않는다 — 토큰 대기 중)

- [ ] **Step 1: .gitignore 규칙 확장**

현재 `.gitignore:13` 은 `infra/terraform/aws/dr-alert-lambda/dr-alert.zip` **단일 경로 하드코딩**이라
신규 Lambda 의 zip 이 커밋된다(설계 §10, 리뷰 지적 #15). 그 줄을 아래로 교체:

```gitignore
# Lambda 빌드 산출물(archive_file 이 apply 시 생성 — 커밋 금지)
infra/terraform/aws/**/*.zip
```

- [ ] **Step 2: Lambda 코드 작성**

`infra/terraform/aws/dr-orchestration-lambda/approval-request/index.py` 신규:

```python
"""DR 페일오버 승인 요청 — SFN .waitForTaskToken 의 첫 상태.

S3 의 Vault raft 스냅샷을 최신순 25개(Discord String Select 상한) 뽑아 드롭다운으로 만들고,
승인 버튼과 함께 Discord 채널에 게시한 뒤 taskToken 을 DynamoDB 에 저장하고 반환 없이 끝난다
(SFN 은 interaction Lambda 의 SendTaskSuccess 를 받을 때까지 대기).
"""

import json
import os
import time
import urllib.request
import uuid

import boto3

_s3 = boto3.client("s3")
_ddb = boto3.client("dynamodb")

# Discord String Select 옵션 상한(공식 문서). 6h 주기 스냅샷 기준 약 6일치.
MAX_OPTIONS = 25
# 승인 대기 24h = SFN .waitForTaskToken 타임아웃과 일치시킨다.
TTL_SECONDS = 24 * 60 * 60


def _webhook_url():
    # dr-alert 와 동일 — 캐싱하지 않아 웹훅 로테이션이 즉시 반영된다.
    #
    # ⚠️ 이 Lambda 는 ap-northeast-2 인데 시크릿은 dr-alert-lambda.tf 가 provider = aws.use1 로
    # 만든 us-east-1 리소스다. Secrets Manager 는 리전 서비스라 클라이언트가 자기 리전
    # 엔드포인트로만 요청하고, ARN 에 리전이 박혀 있어도 그 리전으로 자동 라우팅하지 않는다
    # (AWS 문서의 "ARN 을 쓰라"는 안내는 크로스계정 얘기지 크로스리전이 아니다) →
    # 기본 클라이언트로는 ResourceNotFoundException. ARN 에서 리전을 파싱해 클라이언트를 만든다.
    # 하드코딩하지 않는 이유: ARN 이 진실의 원천이라 시크릿이 옮겨져도 따라간다.
    arn = os.environ["WEBHOOK_SECRET_ARN"]
    sm = boto3.client("secretsmanager", region_name=arn.split(":")[3])
    resp = sm.get_secret_value(SecretId=arn)
    url = json.loads(resp["SecretString"])["url"]
    if not url.startswith("https://"):
        raise ValueError("webhook URL must be https")
    return url


def _list_snapshots():
    """s3://<bucket>/vault/ 의 스냅샷을 최신순으로 최대 25개."""
    bucket = os.environ["BACKUP_BUCKET"]
    paginator = _s3.get_paginator("list_objects_v2")
    keys = []
    for page in paginator.paginate(Bucket=bucket, Prefix="vault/"):
        # extend + 제너레이터 — for/if/append 루프는 ruff PERF401 에 걸린다.
        keys.extend(
            (obj["LastModified"], obj["Key"])
            for obj in page.get("Contents", [])
            if obj["Key"].endswith(".snap")
        )
    if not keys:
        raise RuntimeError(f"vault 스냅샷이 없다: s3://{bucket}/vault/")
    keys.sort(reverse=True)  # 최신순
    return [k for _, k in keys[:MAX_OPTIONS]]


def handler(event, context):
    task_token = event["taskToken"]
    # SFN 이 실행 입력 전체를 event["input"] 으로 넘긴다("input.$": "$").
    # ⚠️ ASL 에서 "mode.$": "$.mode" 로 직접 뽑으면 안 된다 — 실재해 경로(failover-trigger)는
    # 입력에 mode 를 넣지 않으므로 그 JSONPath 가 없어 States.Runtime 으로 즉시 죽는다.
    # 입력 전체를 받아 여기서 꺼내면 mode 유무와 무관하게 동작한다.
    payload = event.get("input") or {}
    # mode 는 메시지의 긴급도를 바꾸는 스위치라 fail-safe 로 판정한다 — 정확히 "test" 일 때만
    # 테스트 렌더, 그 외(필드 없음·null·오타·타입 불일치)는 전부 실재해다(설계 §7.2 H3).
    is_test = payload.get("mode") == "test" if isinstance(payload, dict) else False

    snapshots = _list_snapshots()
    latest = snapshots[0]
    approval_id = uuid.uuid4().hex[:16]  # custom_id 100자 상한 여유

    _ddb.put_item(
        TableName=os.environ["APPROVALS_TABLE"],
        Item={
            "approvalId": {"S": approval_id},
            "taskToken": {"S": task_token},
            "latestSnapshot": {"S": latest},
            "ttl": {"N": str(int(time.time()) + TTL_SECONDS)},
        },
    )

    prefix = "🧪 [테스트] " if is_test else "🚨 "
    title = f"{prefix}**DR 페일오버 승인 요청**"
    body = (
        f"{title}\n"
        "pull(Route53) + push(하트비트) 복합알람 ALARM — 온프렘 상실 감지\n\n"
        "⚠️ **승인 전 직접 확인**: 사이트 접속 · 온프렘 콘솔 · 일시적 네트워크 장애 여부\n"
        "승인하면 EKS 기동 → 복원 → **공개 DNS 전환**까지 자동 진행됩니다."
    )

    options = [
        {
            "label": s.split("/")[-1][:100],
            "value": s[:100],
            "default": s == latest,
        }
        for s in snapshots
    ]

    payload = {
        "content": body,
        "components": [
            {
                "type": 1,  # ActionRow
                "components": [
                    {
                        "type": 3,  # String Select
                        "custom_id": f"dr-snap:{approval_id}",
                        "placeholder": "Vault 스냅샷 시점",
                        "options": options,
                    }
                ],
            },
            {
                "type": 1,
                "components": [
                    {
                        "type": 2,  # Button
                        "style": 4 if not is_test else 2,  # Danger / Secondary
                        "label": "🧪 테스트 승인" if is_test else "🔴 DR 페일오버 승인",
                        "custom_id": f"dr-approve:{approval_id}",
                    }
                ],
            },
        ],
    }

    req = urllib.request.Request(  # noqa: S310
        _webhook_url(),
        data=json.dumps(payload).encode("utf-8"),
        headers={
            "Content-Type": "application/json",
            # Discord 는 Cloudflare 뒤라 기본 UA(Python-urllib/*)를 403 으로 막는다(#311).
            "User-Agent": "Cledyu-DR-Approval/1.0 (+https://cledyu.com)",
        },
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=10) as resp:  # noqa: S310
        resp.read()

    # 반환하지 않는다 — SFN 은 SendTaskSuccess 를 기다린다.
    return {"approvalId": approval_id}
```

- [ ] **Step 3: terraform — Lambda·IAM·로그그룹**

`infra/terraform/aws/dr-orchestration.tf` 에 추가:

```hcl
# ── approval-request Lambda (ap-northeast-2) ──
data "archive_file" "dr_approval_request" {
  type        = "zip"
  source_file = "${path.module}/dr-orchestration-lambda/approval-request/index.py"
  output_path = "${path.module}/dr-orchestration-lambda/approval-request/approval-request.zip"
}

data "aws_iam_policy_document" "dr_lambda_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "dr_approval_request" {
  name               = "${var.name_prefix}-dr-approval-request"
  assume_role_policy = data.aws_iam_policy_document.dr_lambda_assume.json
}

data "aws_iam_policy_document" "dr_approval_request" {
  statement {
    sid       = "ListVaultSnapshots"
    actions   = ["s3:ListBucket"]
    resources = [aws_s3_bucket.dr_backups.arn]
    condition {
      test     = "StringLike"
      variable = "s3:prefix"
      values   = ["vault/*"]
    }
  }
  statement {
    sid       = "PutApproval"
    actions   = ["dynamodb:PutItem"]
    resources = [aws_dynamodb_table.dr_approvals.arn]
  }
  statement {
    sid       = "ReadWebhook"
    actions   = ["secretsmanager:GetSecretValue"]
    resources = [aws_secretsmanager_secret.discord_webhook.arn]
  }
  statement {
    sid     = "Logs"
    actions = ["logs:CreateLogStream", "logs:PutLogEvents"]
    resources = [
      "arn:aws:logs:${var.region}:*:log-group:/aws/lambda/${var.name_prefix}-dr-approval-request",
      "arn:aws:logs:${var.region}:*:log-group:/aws/lambda/${var.name_prefix}-dr-approval-request:*",
    ]
  }
}

resource "aws_iam_role_policy" "dr_approval_request" {
  name   = "${var.name_prefix}-dr-approval-request"
  role   = aws_iam_role.dr_approval_request.id
  policy = data.aws_iam_policy_document.dr_approval_request.json
}

resource "aws_cloudwatch_log_group" "dr_approval_request" {
  name              = "/aws/lambda/${var.name_prefix}-dr-approval-request"
  retention_in_days = 30
}

resource "aws_lambda_function" "dr_approval_request" {
  function_name    = "${var.name_prefix}-dr-approval-request"
  depends_on       = [aws_cloudwatch_log_group.dr_approval_request]
  filename         = data.archive_file.dr_approval_request.output_path
  source_code_hash = data.archive_file.dr_approval_request.output_base64sha256
  handler          = "index.handler"
  runtime          = "python3.12"
  role             = aws_iam_role.dr_approval_request.arn
  timeout          = 30
  environment {
    variables = {
      APPROVALS_TABLE    = aws_dynamodb_table.dr_approvals.name
      BACKUP_BUCKET      = aws_s3_bucket.dr_backups.id
      WEBHOOK_SECRET_ARN = aws_secretsmanager_secret.discord_webhook.arn
    }
  }
}
```

> **🚨 이 Step 의 웹훅 코드는 폐기되었다 (실측 2026-07-15, 스펙 §3.6).**
>
> **일반 incoming 웹훅(dr-alert #310)으로는 버튼·드롭다운을 보낼 수 없다.** Discord 공식 문서:
> "Non-application-owned webhooks cannot send interactive components, and the `components` field will be
> **ignored**". 게다가 **에러가 아니라 2xx 를 주고 조용히 버린다** → Lambda 성공·DDB 저장 정상인데
> 메시지에 버튼만 없다(실측). 정적검증·리뷰로는 원리적으로 못 잡는다.
>
> → **승인 메시지는 Bot API 로 보낸다:**
> - `POST https://discord.com/api/v10/channels/{DISCORD_CHANNEL_ID}/messages`
> - 헤더 `Authorization: Bot <token>` (`"Bot "` 접두 필수 — 빠지면 401)
> - 시크릿 `aws_secretsmanager_secret.discord_bot_token` (**ap-northeast-2** — Lambda 와 같은 리전이라
>   아래 크로스리전 파싱이 **불요**하다)
> - env: `WEBHOOK_SECRET_ARN` → `BOT_TOKEN_SECRET_ARN` + `DISCORD_CHANNEL_ID`
> - IAM: `ReadWebhook` → `ReadBotToken`
> - 봇은 해당 채널에 **Send Messages** 권한 필요(없으면 `403 Missing Access`)
>
> **`dr-alert` 웹훅은 그대로 둔다** — 평문 알림은 컴포넌트가 없어 웹훅으로 충분(#310 무변경).
> 최종 코드는 `infra/terraform/aws/dr-orchestration-lambda/approval-request/index.py` 참조.
>
> **크로스리전 교훈은 여전히 유효하다(다른 시크릿에 적용):** 초안은 "ARN 에 리전이 박혀 있어 boto3 가
> 자동으로 붙는다"고 썼는데 **틀렸다.** Secrets Manager 는 리전 서비스라 클라이언트가 자기 리전
> 엔드포인트로만 요청하고 ARN 의 리전으로 라우팅하지 않는다(AWS 문서의 "ARN 을 쓰라"는 **크로스계정**
> 안내지 크로스리전이 아니다). us-east-1 시크릿을 ap-northeast-2 Lambda 에서 읽어야 할 때는
> **ARN 에서 리전을 파싱해 클라이언트를 만든다** — 이 방식은 양쪽 다 동작해 무조건 안전하다.

- [ ] **Step 4: 검증 — 린트/fmt/validate/부분 plan**

Run:
```bash
cd /home/user/Cledyu
ruff check infra/terraform/aws/dr-orchestration-lambda/approval-request/index.py
ruff format --check infra/terraform/aws/dr-orchestration-lambda/approval-request/index.py
cd infra/terraform/aws
terraform fmt -check dr-orchestration.tf
terraform validate
terraform plan -target=aws_lambda_function.dr_approval_request
```
Expected: ruff 통과, fmt 통과, validate 성공, plan 에 `destroy` **0건**.

- [ ] **Step 5: apply 후 스냅샷 목록 실측**

Run:
```bash
cd infra/terraform/aws
terraform apply -target=aws_dynamodb_table.dr_approvals -target=aws_lambda_function.dr_approval_request
# 스냅샷 목록·웹훅 크로스리전 조회가 실제로 되는지 — taskToken 은 더미(DynamoDB 저장만 확인)
# payload 모양은 SFN 이 실제로 넘기는 것과 동일해야 한다 — {taskToken, input:{...}}
aws lambda invoke --function-name cledyu-lab-dr-approval-request --region ap-northeast-2 \
  --payload '{"taskToken":"dummy-token-for-smoke-test","input":{"mode":"test"}}' \
  --cli-binary-format raw-in-base64-out /tmp/out.json
cat /tmp/out.json
```
Expected: `{"approvalId":"<16자hex>"}`. **Discord 채널에 `🧪 [테스트] DR 페일오버 승인 요청` 메시지 + 드롭다운
+ 버튼이 뜬다**(버튼은 아직 눌러도 아무 일 없음 — Task 3 미구현). 드롭다운 기본 선택이 **최신 스냅샷**인지 확인.
실패 시: `AccessDenied`(S3 prefix 조건)·`ResourceNotFoundException`(웹훅 크로스리전) 을 로그에서 확인.

- [ ] **Step 6: Commit** (사용자가 실행)

```bash
git add infra/terraform/aws/dr-orchestration.tf infra/terraform/aws/dr-orchestration-lambda/approval-request/index.py .gitignore infra/terraform/aws/README.md
git commit -m "feat(dr): 승인 요청 Lambda(스냅샷 드롭다운 + Discord 게시)"
```

---

### Task 3: interaction Lambda (Ed25519 검증 → 허용목록 → SendTaskSuccess)

> Discord 버튼 클릭을 받는 유일한 진입점. **공개 인터넷 엔드포인트**이므로 서명 검증이 유일한 관문이다(§5.4).
> python3.12 표준 라이브러리에 Ed25519 가 없어 **이 함수만 nodejs20** 이다(§3.4).

**Files:**
- Create: `infra/terraform/aws/dr-orchestration-lambda/interaction/index.mjs`
- Modify: `infra/terraform/aws/dr-orchestration.tf`
- Modify: `infra/terraform/aws/README.md`

**Interfaces:**
- Consumes: DynamoDB item `{approvalId, taskToken, latestSnapshot}`(Task 2), `var.dr_approver_ids`(Task 1)
- Consumes: Secrets Manager `${var.name_prefix}-dr-discord-pubkey`(신규 — Step 1)
- Produces: `aws_lambda_function_url.dr_interaction.function_url` — 사용자가 Discord Developer Portal 에 등록
- Produces: `SendTaskSuccess(taskToken, output={"snapshot": "<key>", "approvedBy": "<userId>"})`

- [ ] **Step 1: Public Key 시크릿 껍데기 생성 (값은 TF 밖에서)**

`infra/terraform/aws/dr-orchestration.tf` 에 추가:

```hcl
# Discord Application Public Key(hex). 값은 TF 밖에서 넣는다 — 평문 state 회피(웹훅과 동일 패턴).
# 이 키로 X-Signature-Ed25519 를 검증한다. 시크릿은 아니지만(공개키) 로테이션 편의를 위해 SM 에 둔다.
resource "aws_secretsmanager_secret" "discord_pubkey" {
  name = "${var.name_prefix}-dr-discord-pubkey"
}
```

값 주입(사용자가 Discord Developer Portal 에서 복사 후 실행):
```bash
umask 077 && cat > /tmp/pk.json <<'EOF'
{"public_key":"<Developer Portal 의 PUBLIC KEY hex>"}
EOF
aws secretsmanager put-secret-value --region ap-northeast-2 \
  --secret-id cledyu-lab-dr-discord-pubkey --secret-string file:///tmp/pk.json
rm -f /tmp/pk.json
```

- [ ] **Step 2: interaction Lambda 코드 작성**

`infra/terraform/aws/dr-orchestration-lambda/interaction/index.mjs` 신규:

```javascript
// Discord Interactions 엔드포인트 — 버튼/드롭다운 클릭 수신.
//
// ⚠️ Ed25519 서명 검증은 Discord 의 강제 요구다. Discord 는 무작위로 "가짜 서명" 요청을 보내
// 테스트하고, 401 을 뱉지 않으면 Interactions Endpoint URL 을 등록 해제한다(공식 문서).
// python3.12 표준 라이브러리엔 Ed25519 가 없어 이 함수만 nodejs20 이다(설계 §3.4).
import { createPublicKey, verify } from "node:crypto";
import { DynamoDBClient, GetItemCommand, UpdateItemCommand } from "@aws-sdk/client-dynamodb";
import { SFNClient, SendTaskSuccessCommand } from "@aws-sdk/client-sfn";
import { SecretsManagerClient, GetSecretValueCommand } from "@aws-sdk/client-secrets-manager";

const ddb = new DynamoDBClient({});
const sfn = new SFNClient({});
const sm = new SecretsManagerClient({});

const APPROVERS = JSON.parse(process.env.APPROVER_IDS || "[]");
const TABLE = process.env.APPROVALS_TABLE;

// Discord 공개키는 hex 문자열. Node 의 createPublicKey 는 raw 키를 직접 받지 않으므로
// Ed25519 SPKI DER 접두(12바이트) + 32바이트 raw 공개키로 DER 을 조립해 넘긴다.
const SPKI_PREFIX = Buffer.from("302a300506032b6570032100", "hex");
let cachedKey = null;

async function publicKey() {
  if (cachedKey) return cachedKey;
  const resp = await sm.send(new GetSecretValueCommand({ SecretId: process.env.PUBKEY_SECRET_ARN }));
  const hex = JSON.parse(resp.SecretString).public_key;
  const der = Buffer.concat([SPKI_PREFIX, Buffer.from(hex, "hex")]);
  cachedKey = createPublicKey({ key: der, format: "der", type: "spki" });
  return cachedKey;
}

function res(status, body) {
  return { statusCode: status, headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) };
}

// 원본 메시지의 버튼을 비활성화하고 승인 기록을 남긴다. 재클릭 방지 + 채널에 감사 흔적.
function disabledMessage(original, who) {
  const stamp = new Date().toISOString().replace("T", " ").slice(0, 19);
  return {
    type: 7, // UPDATE_MESSAGE
    data: {
      content: `${original}\n\n✅ <@${who}> 가 승인함 · ${stamp} UTC`,
      components: [],
    },
  };
}

export const handler = async (event) => {
  const sig = event.headers?.["x-signature-ed25519"];
  const ts = event.headers?.["x-signature-timestamp"];
  const raw = event.body || "";

  if (!sig || !ts) return res(401, { error: "missing signature headers" });

  let ok = false;
  try {
    ok = verify(null, Buffer.from(ts + raw), await publicKey(), Buffer.from(sig, "hex"));
  } catch {
    ok = false;
  }
  if (!ok) return res(401, { error: "invalid signature" });

  const body = JSON.parse(raw);

  // type 1 = PING. Discord 가 엔드포인트 등록 시 보낸다 — PONG 으로 답해야 저장이 성공한다.
  if (body.type === 1) return res(200, { type: 1 });

  // type 3 = MESSAGE_COMPONENT (버튼/드롭다운)
  if (body.type !== 3) return res(200, { type: 4, data: { content: "지원하지 않는 상호작용" } });

  const customId = body.data.custom_id || "";
  const [kind, approvalId] = customId.split(":");
  const userId = body.member?.user?.id ?? body.user?.id;

  // 허용목록을 모든 컴포넌트 분기보다 위에 둔다. 서명 검증만으로는 채널을 보는 누구나 누를 수 있고,
  // 드롭다운(복원 대상 선택)도 승인과 동등한 권한이다 — 비승인자가 스냅샷을 바꿔두면 승인자 화면엔
  // 변화가 안 보인 채(type 6) 그 값이 소비된다(설계 §5.4 3겹 방어).
  if (!APPROVERS.includes(userId)) {
    return res(200, { type: 4, data: { content: "⛔ 승인 권한이 없습니다.", flags: 64 } }); // ephemeral
  }

  // 드롭다운: 선택만 저장하고 조용히 확인. 버튼 클릭은 별개 interaction 으로 오고
  // 그 payload 엔 드롭다운 선택값이 실려 오지 않으므로 여기서 눌러 담아야 한다(설계 §3.5).
  if (kind === "dr-snap") {
    try {
      await ddb.send(new UpdateItemCommand({
        TableName: TABLE,
        Key: { approvalId: { S: approvalId } },
        // ⚠️ snapshot 은 DynamoDB 예약어라 표현식에 직접 못 쓴다(항상 ValidationException)
        // → ExpressionAttributeNames 로 우회. 쓰는 쪽(index.py)은 PutItem Item 맵이라 무관.
        UpdateExpression: "SET #snap = :s",
        ExpressionAttributeNames: { "#snap": "snapshot" },
        ExpressionAttributeValues: { ":s": { S: body.data.values[0] } },
        // UpdateItem 은 키가 없으면 항목을 만든다 → TTL 만료 후 드롭다운을 건드리면 ttl·taskToken
        // 없는 고아가 생기고, 이후 승인이 "만료" 분기를 건너뛰어 TypeError 로 죽는다.
        ConditionExpression: "attribute_exists(approvalId)",
      }));
    } catch (e) {
      if (e.name !== "ConditionalCheckFailedException") throw e;
      return res(200, { type: 4, data: { content: "⚠️ 만료된 승인 요청입니다(24h TTL).", flags: 64 } });
    }
    return res(200, { type: 6 }); // DEFERRED_UPDATE_MESSAGE — 메시지 변화 없음
  }

  if (kind !== "dr-approve") return res(200, { type: 4, data: { content: "알 수 없는 컴포넌트", flags: 64 } });

  const got = await ddb.send(new GetItemCommand({
    TableName: TABLE,
    Key: { approvalId: { S: approvalId } },
  }));
  if (!got.Item) {
    return res(200, { type: 4, data: { content: "⚠️ 만료된 승인 요청입니다(24h TTL).", flags: 64 } });
  }

  // 드롭다운을 건드리지 않고 바로 승인했으면 최신 스냅샷으로 폴백.
  const snapshot = got.Item.snapshot?.S ?? got.Item.latestSnapshot.S;

  await sfn.send(new SendTaskSuccessCommand({
    taskToken: got.Item.taskToken.S,
    output: JSON.stringify({ snapshot, approvedBy: userId, approvedAt: new Date().toISOString() }),
  }));

  return res(200, disabledMessage(body.message?.content ?? "", userId));
};
```

- [ ] **Step 3: terraform — Lambda·Function URL·IAM**

`infra/terraform/aws/dr-orchestration.tf` 에 추가:

```hcl
# ── interaction Lambda (nodejs20 — Ed25519, 설계 §3.4) ──
data "archive_file" "dr_interaction" {
  type        = "zip"
  source_file = "${path.module}/dr-orchestration-lambda/interaction/index.mjs"
  output_path = "${path.module}/dr-orchestration-lambda/interaction/interaction.zip"
}

resource "aws_iam_role" "dr_interaction" {
  name               = "${var.name_prefix}-dr-interaction"
  assume_role_policy = data.aws_iam_policy_document.dr_lambda_assume.json
}

data "aws_iam_policy_document" "dr_interaction" {
  statement {
    sid       = "ReadWriteApproval"
    actions   = ["dynamodb:GetItem", "dynamodb:UpdateItem"]
    resources = [aws_dynamodb_table.dr_approvals.arn]
  }
  statement {
    sid       = "ReadPubkey"
    actions   = ["secretsmanager:GetSecretValue"]
    resources = [aws_secretsmanager_secret.discord_pubkey.arn]
  }
  statement {
    sid = "ResumeStateMachine"
    # 태스크 토큰이 실행을 식별하므로 리소스 한정이 불가(AWS 설계) — 액션만 최소화한다.
    actions   = ["states:SendTaskSuccess", "states:SendTaskFailure"]
    resources = ["*"]
  }
  statement {
    sid     = "Logs"
    actions = ["logs:CreateLogStream", "logs:PutLogEvents"]
    resources = [
      "arn:aws:logs:${var.region}:*:log-group:/aws/lambda/${var.name_prefix}-dr-interaction",
      "arn:aws:logs:${var.region}:*:log-group:/aws/lambda/${var.name_prefix}-dr-interaction:*",
    ]
  }
}

resource "aws_iam_role_policy" "dr_interaction" {
  name   = "${var.name_prefix}-dr-interaction"
  role   = aws_iam_role.dr_interaction.id
  policy = data.aws_iam_policy_document.dr_interaction.json
}

resource "aws_cloudwatch_log_group" "dr_interaction" {
  name              = "/aws/lambda/${var.name_prefix}-dr-interaction"
  retention_in_days = 30
}

resource "aws_lambda_function" "dr_interaction" {
  function_name    = "${var.name_prefix}-dr-interaction"
  depends_on       = [aws_cloudwatch_log_group.dr_interaction]
  filename         = data.archive_file.dr_interaction.output_path
  source_code_hash = data.archive_file.dr_interaction.output_base64sha256
  handler          = "index.handler"
  runtime          = "nodejs20.x"
  role             = aws_iam_role.dr_interaction.arn
  timeout          = 10 # Discord 는 3초 내 응답 요구 — 여유만 두고 짧게

  # 공개 URL 이라 무제한이면 계정 Lambda 동시성을 소진해 같은 리전의 다른 함수까지
  # 굶길 수 있다. 승인은 초당 몇 건이면 충분하다(설계 §5.4 H5).
  reserved_concurrent_executions = 5

  environment {
    variables = {
      APPROVALS_TABLE   = aws_dynamodb_table.dr_approvals.name
      PUBKEY_SECRET_ARN = aws_secretsmanager_secret.discord_pubkey.arn
      APPROVER_IDS      = jsonencode(var.dr_approver_ids)
    }
  }
}

# Discord 가 IAM 서명 없이 POST 하므로 AuthType=NONE 이 강제된다 —
# Ed25519 서명 검증이 유일한 관문이다(설계 §5.4 H5).
resource "aws_lambda_function_url" "dr_interaction" {
  function_name      = aws_lambda_function.dr_interaction.function_name
  authorization_type = "NONE"
}

output "dr_interaction_url" {
  description = "Discord Developer Portal 의 Interactions Endpoint URL 에 등록할 값."
  value       = aws_lambda_function_url.dr_interaction.function_url
}
```

- [ ] **Step 4: 검증 — fmt/validate/부분 plan**

Run:
```bash
cd infra/terraform/aws
terraform fmt -check dr-orchestration.tf
terraform validate
terraform plan -target=aws_lambda_function.dr_interaction -target=aws_lambda_function_url.dr_interaction
```
Expected: 통과, plan 에 `destroy` 0건.

- [ ] **Step 5: apply + 잘못된 서명 → 401 실측 (§7.2(a))**

Run:
```bash
cd infra/terraform/aws
terraform apply -target=aws_secretsmanager_secret.discord_pubkey -target=aws_lambda_function.dr_interaction -target=aws_lambda_function_url.dr_interaction
URL=$(terraform output -raw dr_interaction_url)
# 쓰레기 서명 → 401 이어야 한다. Discord 가 상시 가짜 서명으로 테스트하며,
# 401 을 못 뱉으면 Interactions URL 을 등록 해제한다(= 등록 유지의 조건).
curl -s -o /dev/null -w "%{http_code}\n" -X POST "$URL" \
  -H "X-Signature-Ed25519: deadbeef" -H "X-Signature-Timestamp: 123" \
  -H "Content-Type: application/json" -d '{"type":1}'
# 헤더 자체가 없는 경우도 401
curl -s -o /dev/null -w "%{http_code}\n" -X POST "$URL" -d '{}'
```
Expected: **둘 다 `401`**. 200 이 나오면 서명 검증이 동작하지 않는 것 — 중단하고 원인 파악.

> 유효 서명은 **우리가 만들 수 없다**(Discord 가 개인키 보유, 앱엔 공개키만 — 설계 §7.2 H1/F3).
> 유효 경로는 Step 6 의 Discord PING 과 Task 4 의 실제 클릭으로만 검증된다.

- [ ] **Step 6: Interactions Endpoint URL 등록 → PING/PONG (§7.2(b))** (사용자가 실행)

1. `terraform output -raw dr_interaction_url` 값을 복사
2. Discord Developer Portal → 해당 Application → **Interactions Endpoint URL** 에 붙여넣고 저장

Expected: **저장 성공**. Discord 가 유효 서명으로 PING(type 1)을 보내고 우리가 PONG(type 1)으로 답해야만
저장된다 — **저장 성공 자체가 유효 서명 경로 통과의 증거**다. 저장이 거부되면 CloudWatch
`/aws/lambda/cledyu-lab-dr-interaction` 로그에서 서명 검증 실패 원인(공개키 hex·SPKI 조립)을 확인한다.

- [ ] **Step 7: Commit** (사용자가 실행)

```bash
git add infra/terraform/aws/dr-orchestration.tf infra/terraform/aws/dr-orchestration-lambda/interaction/index.mjs infra/terraform/aws/README.md
git commit -m "feat(dr): Discord interaction Lambda(Ed25519 검증 + 승인자 허용목록 + 버튼 비활성화)"
```

---

### Task 4: 테스트 상태 머신 + 승인 로직 실측 (§7.2(c))

> 허용목록·드롭다운·버튼은 **진짜 Discord 클릭**으로만 검증된다. 그런데 메인 SM 에서 클릭하면 [2] 가 시작돼
> 과금·인프라가 뜬다. `RequestApproval → Succeed` 두 상태짜리 테스트 SM 으로 승인 배선만 완결 검증한다.

**Files:**
- Modify: `infra/terraform/aws/dr-orchestration.tf`
- Modify: `infra/terraform/aws/README.md`

**Interfaces:**
- Consumes: `aws_lambda_function.dr_approval_request`(Task 2), `aws_lambda_function.dr_interaction`(Task 3)
- Produces: `aws_sfn_state_machine.dr_approval_test` — 이 계획 안에서만 쓰는 검증 하네스
- Produces: `aws_iam_role.dr_sfn`(SFN 실행 롤) — Plan 2 의 메인 SM 이 재사용

- [ ] **Step 1: SFN 실행 롤 + 테스트 상태 머신**

`infra/terraform/aws/dr-orchestration.tf` 에 추가:

```hcl
# ── Step Functions 실행 롤 (테스트 SM + Plan 2 의 메인 SM 공용) ──
data "aws_iam_policy_document" "dr_sfn_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["states.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "dr_sfn" {
  name               = "${var.name_prefix}-dr-sfn"
  assume_role_policy = data.aws_iam_policy_document.dr_sfn_assume.json
}

data "aws_iam_policy_document" "dr_sfn" {
  statement {
    sid       = "InvokeApprovalRequest"
    actions   = ["lambda:InvokeFunction"]
    resources = [aws_lambda_function.dr_approval_request.arn]
  }
  statement {
    sid = "Logs"
    # SFN 로깅은 리소스 한정을 지원하지 않는다(AWS 문서) — 액션만 최소화.
    actions = [
      "logs:CreateLogDelivery", "logs:GetLogDelivery", "logs:UpdateLogDelivery",
      "logs:DeleteLogDelivery", "logs:ListLogDeliveries", "logs:PutResourcePolicy",
      "logs:DescribeResourcePolicies", "logs:DescribeLogGroups",
    ]
    resources = ["*"]
  }
}

resource "aws_iam_role_policy" "dr_sfn" {
  name   = "${var.name_prefix}-dr-sfn"
  role   = aws_iam_role.dr_sfn.id
  policy = data.aws_iam_policy_document.dr_sfn.json
}

resource "aws_cloudwatch_log_group" "dr_sfn" {
  name              = "/aws/vendedlogs/states/${var.name_prefix}-dr"
  retention_in_days = 30
}

# ── 테스트 SM — 승인 배선만 검증(설계 §7.2(c)) ──
# 진짜 approval-request·interaction Lambda·DynamoDB·Discord 앱을 그대로 쓰고 하류만 없다.
# §7.3 의 "드릴 전용 코드 경로 금지"에 걸리지 않는다 — 프로덕션 SM 의 동작을 바꾸지 않는 별도 하네스다.
resource "aws_sfn_state_machine" "dr_approval_test" {
  name     = "${var.name_prefix}-dr-approval-test"
  role_arn = aws_iam_role.dr_sfn.arn

  logging_configuration {
    log_destination = "${aws_cloudwatch_log_group.dr_sfn.arn}:*"
    # ⚠️ false 필수 — true 면 LambdaFunctionScheduled 이벤트의 input 에 해석된 실제 taskToken 이
    # 평문으로 로그에 남는다. 토큰은 SendTaskSuccess 의 유일한 bearer 자격증명이라(그래서 IAM 이
    # resources=["*"]), 로그 읽기 권한 + states:SendTaskSuccess 만으로 서명·허용목록·arming
    # 3겹을 전부 우회해 페일오버를 승인할 수 있다. **Plan 2 의 실제 상태 머신도 false 로 둘 것.**
    include_execution_data = false
    level                  = "ALL" # 상태 전이는 계속 기록 — "어디서 실패했나"는 그대로 보인다
  }

  definition = jsonencode({
    Comment = "DR 승인 배선 검증 하네스 — 실제 페일오버 없음"
    StartAt = "RequestApproval"
    States = {
      RequestApproval = {
        Type     = "Task"
        Resource = "arn:aws:states:::lambda:invoke.waitForTaskToken"
        Parameters = {
          FunctionName = aws_lambda_function.dr_approval_request.arn
          Payload = {
            "taskToken.$" = "$$.Task.Token"
            # ⚠️ "mode.$" = "$.mode" 로 직접 뽑지 않는다 — 실재해 경로(failover-trigger)는 입력에
            # mode 를 넣지 않으므로 그 JSONPath 가 없어 States.Runtime 으로 즉시 죽는다.
            # 입력 전체를 넘기고 mode 판정은 Lambda 안에서 한다(Task 2).
            "input.$" = "$"
          }
        }
        TimeoutSeconds = 86400 # 24h — DynamoDB TTL 과 일치
        End            = true
      }
    }
  })
}
```

- [ ] **Step 2: 검증 — fmt/validate/부분 plan**

Run:
```bash
cd infra/terraform/aws
terraform fmt -check dr-orchestration.tf
terraform validate
terraform plan -target=aws_sfn_state_machine.dr_approval_test
```
Expected: 통과, `destroy` 0건. ASL 문법 오류는 `validate` 가 아니라 apply 시 드러나므로 Step 3 에서 확인.

- [ ] **Step 3: apply + 허용목록 밖 사용자 거부 실측**

먼저 `dr_approver_ids` 를 **빈 목록으로 둔 채**(Task 1 의 기본값 `[]`) 거부 경로를 본다.

Run:
```bash
cd infra/terraform/aws
terraform apply -target=aws_iam_role.dr_sfn -target=aws_sfn_state_machine.dr_approval_test
ARN=$(aws stepfunctions list-state-machines --region ap-northeast-2 \
  --query "stateMachines[?name=='cledyu-lab-dr-approval-test'].stateMachineArn" --output text)
aws stepfunctions start-execution --region ap-northeast-2 --state-machine-arn "$ARN" \
  --input '{"mode":"test"}'
```
그다음 **Discord 에서 `🧪 테스트 승인` 버튼을 직접 클릭**한다.

Expected: **"⛔ 승인 권한이 없습니다."** 가 본인에게만 보이는(ephemeral) 응답으로 뜨고,
실행은 계속 `RUNNING`(토큰 안 풀림). 이게 허용목록이 실제로 막는다는 증거다.

- [ ] **Step 4: 승인자 등록 → 정상 승인 경로 실측**

본인 Discord user ID 를 `variables.tf` 의 `dr_approver_ids` 기본값에 넣는다(개발자 모드 → 우클릭 → ID 복사):

```hcl
  default     = ["123456789012345678"] # <본인 ID>
```

Run:
```bash
cd infra/terraform/aws
terraform apply -target=aws_lambda_function.dr_interaction   # 환경변수 APPROVER_IDS 갱신
aws stepfunctions start-execution --region ap-northeast-2 --state-machine-arn "$ARN" \
  --input '{"mode":"test"}'
```

**(a) 드롭다운 미조작 승인** — 버튼만 클릭:
```bash
aws stepfunctions describe-execution --region ap-northeast-2 \
  --execution-arn <위 start-execution 이 반환한 ARN> --query '{status:status,output:output}'
```
Expected: `status: SUCCEEDED`, `output` 의 `snapshot` = **최신 스냅샷**(latestSnapshot 폴백).
Discord 메시지의 버튼이 **사라지고** `✅ <@본인> 가 승인함 · <시각> UTC` 가 붙는다.

**(b) 드롭다운 선택 후 승인** — 새 실행을 시작하고 드롭다운에서 **최신이 아닌 항목**을 고른 뒤 버튼 클릭:
Expected: `output` 의 `snapshot` = **고른 그 값**(폴백이 아님). 이게 §3.5 의 별개-interaction 배선이
실제로 동작한다는 증거다.

- [ ] **Step 5: Commit** (사용자가 실행)

```bash
git add infra/terraform/aws/dr-orchestration.tf infra/terraform/aws/variables.tf infra/terraform/aws/README.md
git commit -m "feat(dr): 승인 배선 검증용 테스트 상태 머신 + 승인자 등록"
```

---

### Task 5: EventBridge 규칙 + failover-trigger Lambda (us-east-1)

> 복합알람 ALARM → 크로스리전 hop → SFN 시작. **AND 3중 게이트**로 무장한다.

**Files:**
- Create: `infra/terraform/aws/dr-orchestration-lambda/failover-trigger/index.py`
- Modify: `infra/terraform/aws/dr-orchestration.tf`
- Modify: `infra/terraform/aws/README.md`

**Interfaces:**
- Consumes: `aws_cloudwatch_composite_alarm.disaster[0]`(`dr-detection.tf`, `count = local.pub`)
- Consumes: `var.dr_orchestration_armed`·`var.dr_detection_armed`·`local.pub`
- Produces: `aws_cloudwatch_event_rule.dr_disaster[0]` — Plan 2 가 타겟을 메인 SM 으로 바꾼다

- [ ] **Step 1: failover-trigger Lambda 코드**

`infra/terraform/aws/dr-orchestration-lambda/failover-trigger/index.py` 신규:

```python
"""복합알람 ALARM(us-east-1) → Step Functions 시작(ap-northeast-2).

EventBridge 크로스리전 버스를 엮는 대신 작은 Lambda 하나가 리전을 넘긴다(설계 §3.2).
복합알람과 그 상태변화 이벤트는 us-east-1 전용(Route53 HealthCheckStatus 메트릭 제약)이고,
Step Functions·EKS DR 은 ap-northeast-2 에 있다.
"""

import json
import os

import boto3

# 타겟 SM 이 있는 리전으로 명시 고정 — Lambda 자신은 us-east-1 에서 돈다.
_sfn = boto3.client("stepfunctions", region_name=os.environ["SFN_REGION"])


def handler(event, context):
    detail = event.get("detail", {})
    _sfn.start_execution(
        stateMachineArn=os.environ["STATE_MACHINE_ARN"],
        # mode 를 넣지 않는다 → approval-request 가 실재해로 렌더한다(fail-safe, 설계 §7.2 H3).
        input=json.dumps(
            {
                "alarmName": detail.get("alarmName"),
                "reason": (detail.get("state") or {}).get("reason"),
                "detectedAt": (detail.get("state") or {}).get("timestamp"),
            }
        ),
    )
    return {"started": True}
```

- [ ] **Step 2: terraform — Lambda(us-east-1) + EventBridge 규칙(AND 게이트)**

`infra/terraform/aws/dr-orchestration.tf` 에 추가:

```hcl
# ── failover-trigger Lambda (us-east-1 — 알람 이벤트가 여기 뜬다) ──
data "archive_file" "dr_failover_trigger" {
  type        = "zip"
  source_file = "${path.module}/dr-orchestration-lambda/failover-trigger/index.py"
  output_path = "${path.module}/dr-orchestration-lambda/failover-trigger/failover-trigger.zip"
}

resource "aws_iam_role" "dr_failover_trigger" {
  count              = local.pub
  name               = "${var.name_prefix}-dr-failover-trigger"
  assume_role_policy = data.aws_iam_policy_document.dr_lambda_assume.json
}

data "aws_iam_policy_document" "dr_failover_trigger" {
  statement {
    sid     = "StartFailover"
    actions = ["states:StartExecution"]
    # Plan 2 에서 메인 SM 으로 교체된다. 지금은 테스트 SM 을 가리켜 배선을 검증한다.
    resources = [aws_sfn_state_machine.dr_approval_test.arn]
  }
  statement {
    sid     = "Logs"
    actions = ["logs:CreateLogStream", "logs:PutLogEvents"]
    resources = [
      "arn:aws:logs:us-east-1:*:log-group:/aws/lambda/${var.name_prefix}-dr-failover-trigger",
      "arn:aws:logs:us-east-1:*:log-group:/aws/lambda/${var.name_prefix}-dr-failover-trigger:*",
    ]
  }
}

resource "aws_iam_role_policy" "dr_failover_trigger" {
  count  = local.pub
  name   = "${var.name_prefix}-dr-failover-trigger"
  role   = aws_iam_role.dr_failover_trigger[0].id
  policy = data.aws_iam_policy_document.dr_failover_trigger.json
}

resource "aws_cloudwatch_log_group" "dr_failover_trigger" {
  count             = local.pub
  provider          = aws.use1
  name              = "/aws/lambda/${var.name_prefix}-dr-failover-trigger"
  retention_in_days = 30
}

resource "aws_lambda_function" "dr_failover_trigger" {
  count            = local.pub
  provider         = aws.use1
  function_name    = "${var.name_prefix}-dr-failover-trigger"
  depends_on       = [aws_cloudwatch_log_group.dr_failover_trigger]
  filename         = data.archive_file.dr_failover_trigger.output_path
  source_code_hash = data.archive_file.dr_failover_trigger.output_base64sha256
  handler          = "index.handler"
  runtime          = "python3.12"
  role             = aws_iam_role.dr_failover_trigger[0].arn
  timeout          = 15
  environment {
    variables = {
      STATE_MACHINE_ARN = aws_sfn_state_machine.dr_approval_test.arn
      SFN_REGION        = var.region
    }
  }
}

# ── EventBridge 규칙 — 3중 AND 게이트 ──
# ⚠️ local.pub 이 반드시 들어간다: 복합알람은 count = local.pub 이라 enable_public_ingress=false 면
# 존재하지 않는다 → event_pattern 의 disaster[0] 참조가 깨져 apply 전체가 중단된다. 이는 e68064b
# ("감지 스택 enable_public_ingress count 게이트 — precondition 전체중단 제거")가 제거한 실패 모드와
# 같은 클래스다.
# ⚠️ dr_detection_armed 도 들어간다: 그 플래그는 actions_enabled 라 SNS 발행만 억제하고 CloudWatch 는
# 알람 상태변화 이벤트를 기본 버스로 계속 쏜다. 없으면 "감지를 껐다"고 믿는 bring-up 창에서 알림은 안
# 뜨는데 승인 버튼이 뜬다(설계 §7.4).
resource "aws_cloudwatch_event_rule" "dr_disaster" {
  count       = (local.pub == 1 && var.dr_detection_armed && var.dr_orchestration_armed) ? 1 : 0
  provider    = aws.use1
  name        = "${var.name_prefix}-dr-disaster"
  description = "복합알람 ALARM → DR 페일오버 승인 요청(Step Functions)"
  event_pattern = jsonencode({
    source      = ["aws.cloudwatch"]
    detail-type = ["CloudWatch Alarm State Change"]
    detail = {
      alarmName = [aws_cloudwatch_composite_alarm.disaster[0].alarm_name]
      state     = { value = ["ALARM"] }
    }
  })
}

resource "aws_cloudwatch_event_target" "dr_disaster" {
  count     = (local.pub == 1 && var.dr_detection_armed && var.dr_orchestration_armed) ? 1 : 0
  provider  = aws.use1
  rule      = aws_cloudwatch_event_rule.dr_disaster[0].name
  target_id = "failover-trigger"
  arn       = aws_lambda_function.dr_failover_trigger[0].arn
}

resource "aws_lambda_permission" "dr_disaster" {
  count         = (local.pub == 1 && var.dr_detection_armed && var.dr_orchestration_armed) ? 1 : 0
  provider      = aws.use1
  statement_id  = "AllowEventBridgeInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.dr_failover_trigger[0].function_name
  principal     = "events.amazonaws.com"
  source_arn    = aws_cloudwatch_event_rule.dr_disaster[0].arn
}
```

- [ ] **Step 3: 미무장 상태에서 규칙이 안 생기는지 확인**

Run:
```bash
cd infra/terraform/aws
terraform fmt -check dr-orchestration.tf
terraform validate
# 기본값(dr_orchestration_armed=false) → 규칙 0개
terraform plan -target=aws_cloudwatch_event_rule.dr_disaster
```
Expected: `No changes.` 또는 규칙 관련 `add` **0건** — 무장 게이트가 실제로 막는다는 증거.

- [ ] **Step 4: 무장 + 크로스리전 트리거 실측**

Run:
```bash
cd infra/terraform/aws
terraform apply -target=aws_lambda_function.dr_failover_trigger \
  -var dr_detection_armed=true -var dr_orchestration_armed=true \
  -target=aws_cloudwatch_event_rule.dr_disaster -target=aws_cloudwatch_event_target.dr_disaster \
  -target=aws_lambda_permission.dr_disaster

# 알람을 실제로 울리지 않고 이벤트만 흉내내 배선을 검증한다(알람 상태 조작 불요).
aws events put-events --region us-east-1 --entries '[{
  "Source":"aws.cloudwatch",
  "DetailType":"CloudWatch Alarm State Change",
  "Detail":"{\"alarmName\":\"cledyu-lab-dr-disaster\",\"state\":{\"value\":\"ALARM\",\"reason\":\"배선 검증용 합성 이벤트\",\"timestamp\":\"2026-07-15T00:00:00Z\"}}"
}]'
```
Expected: `FailedEntryCount: 0`. 그리고 **Discord 에 `🚨 DR 페일오버 승인 요청`**(테스트 표식 **없이** —
`mode` 를 안 넣으므로 실재해 렌더) 메시지가 뜬다. `aws stepfunctions list-executions --state-machine-arn "$ARN"`
에 새 실행이 `RUNNING` 으로 보인다.

**확인 후 반드시 실행을 정지한다**(승인 누르지 말 것):
```bash
aws stepfunctions stop-execution --region ap-northeast-2 --execution-arn <새 실행 ARN>
```

- [ ] **Step 5: 무장 해제로 원복**

드릴이 끝났으므로 기본값(미무장)으로 되돌린다 — Plan 2 완료 전에 진짜 알람이 울려도 승인 요청이 나가지 않게.

Run:
```bash
cd infra/terraform/aws
terraform apply -target=aws_cloudwatch_event_rule.dr_disaster -target=aws_cloudwatch_event_target.dr_disaster -target=aws_lambda_permission.dr_disaster
aws events list-rules --region us-east-1 --name-prefix cledyu-lab-dr-disaster
```
Expected: 규칙이 **삭제**되고 `Rules: []`. (변수 기본값이 false 라 `-var` 없이 apply 하면 count=0 이 된다.)

- [ ] **Step 6: Commit** (사용자가 실행)

```bash
git add infra/terraform/aws/dr-orchestration.tf infra/terraform/aws/dr-orchestration-lambda/failover-trigger/index.py infra/terraform/aws/README.md
git commit -m "feat(dr): EventBridge 규칙(3중 AND 게이트) + 크로스리전 failover-trigger"
```

---

## 운영자 apply — 검증된 `-target` 목록 (2026-07-15 실측)

각 Task 의 `-target` 은 IAM 정책이 빠져 있다(Global Constraints 참조). **아래가 실제로 통과한 전체 목록**이다:

```bash
cd ~/Cledyu/infra/terraform/aws
terraform apply \
  -target=aws_dynamodb_table.dr_approvals \
  -target=aws_iam_role_policy.dr_approval_request \
  -target=aws_lambda_function.dr_approval_request \
  -target=aws_secretsmanager_secret.discord_pubkey \
  -target=aws_secretsmanager_secret.discord_bot_token \
  -target=aws_iam_role_policy.dr_interaction \
  -target=aws_lambda_function.dr_interaction \
  -target=aws_lambda_function_url.dr_interaction \
  -target=aws_lambda_permission.dr_interaction_url_invoke \
  -target=aws_iam_role_policy.dr_sfn \
  -target=aws_sfn_state_machine.dr_approval_test \
  -target=aws_iam_role_policy.dr_failover_trigger \
  -target=aws_lambda_function.dr_failover_trigger
```

EventBridge 규칙(무장)은 별도 — 드릴 때만:
```bash
terraform apply -var dr_orchestration_armed=true \
  -target=aws_cloudwatch_event_rule.dr_disaster \
  -target=aws_cloudwatch_event_target.dr_disaster \
  -target=aws_lambda_permission.dr_disaster
# 드릴 후 -var 없이 재실행하면 count=0 으로 삭제된다(무장 해제)
```

### 합성 EventBridge 이벤트는 불가 — `set-alarm-state` 를 쓴다 (실측 정정)

초안의 `aws events put-events --entries '[{"Source":"aws.cloudwatch",...}]'` 는
**`NotAuthorizedForSourceException`** 으로 실패한다 — **`aws.` 접두는 AWS 예약 네임스페이스**다.

→ 복합알람을 직접 발동시킨다(오히려 실제 경로를 그대로 타는 더 나은 테스트):
```bash
aws cloudwatch set-alarm-state --region us-east-1 --alarm-name cledyu-lab-dr-disaster \
  --state-value ALARM --state-reason "배선 검증 드릴 — 실제 재해 아님"
```

> **⚠️ 복원이 필수다.** AWS 문서: "If you use SetAlarmState on a composite alarm, the composite alarm is
> **not guaranteed to return to its actual state**. It returns to its actual state only once any of its
> children alarms change state." 자식(pull·push)이 안정적이면 **ALARM 에 눌러앉고, 그 상태는 진짜 재해를
> 가린다**(EventBridge 는 상태 *변화*에만 반응 → 이미 ALARM 이면 진짜 재해에 안 쏨).
> ```bash
> aws cloudwatch set-alarm-state --region us-east-1 --alarm-name cledyu-lab-dr-disaster \
>   --state-value OK --state-reason "드릴 종료 — 상태 복원"
> aws cloudwatch describe-alarms --region us-east-1 --alarm-names cledyu-lab-dr-disaster \
>   --alarm-types CompositeAlarm --query 'CompositeAlarms[].StateValue' --output text   # → OK 확인
> ```

## 완료 기준

이 계획이 끝나면 다음이 **실측으로** 증명되어 있어야 한다:

- [ ] 잘못된 서명 → **401** (Discord 엔드포인트 등록 유지 조건, Task 3 Step 5)
- [ ] Interactions Endpoint URL **저장 성공** (= 유효 서명 PING/PONG 통과, Task 3 Step 6)
- [ ] 허용목록 **밖** 사용자 클릭 → 거부 + 실행 계속 RUNNING (Task 4 Step 3)
- [ ] 허용목록 **안** 사용자 클릭 → `SUCCEEDED` + 버튼 비활성화 + 승인 기록 (Task 4 Step 4a)
- [ ] 드롭다운 미조작 승인 → **최신 스냅샷** 폴백 (Task 4 Step 4a)
- [ ] 드롭다운 선택 승인 → **고른 값**이 output 에 (Task 4 Step 4b)
- [ ] `dr_orchestration_armed=false` → EventBridge 규칙 **미생성** (Task 5 Step 3)
- [ ] 합성 알람 이벤트 → 크로스리전 SFN 시작 + Discord 실재해 렌더 (Task 5 Step 4)
- [ ] 최종 상태: **무장 해제**(Plan 2 전까지 자동 트리거 없음, Task 5 Step 5)

**Plan 2 로 넘길 것:** `aws_iam_role.dr_sfn`(재사용), `aws_cloudwatch_event_target.dr_disaster` 의 타겟을
테스트 SM → 메인 SM 으로 교체, `aws_iam_policy_document.dr_failover_trigger` 의 `StartFailover` 리소스도 함께 교체.
