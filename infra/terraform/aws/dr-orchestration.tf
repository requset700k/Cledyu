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

  tags = local.eks_dr_tags
}

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
    sid       = "ReadBotToken"
    actions   = ["secretsmanager:GetSecretValue"]
    resources = [aws_secretsmanager_secret.discord_bot_token.arn]
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
  function_name = "${var.name_prefix}-dr-approval-request"
  # 역할 정책도 명시적 의존으로 묶는다 — role 만 참조하면 정책이 숨은 의존이라
  # -target=aws_lambda_function.dr_approval_request 로 배포할 때 정책이 안 끌려와
  # 권한 없는 Lambda 가 배포된다(실측 2026-07-15). bastion·proxy 와 동일 패턴.
  depends_on       = [aws_cloudwatch_log_group.dr_approval_request, aws_iam_role_policy.dr_approval_request]
  filename         = data.archive_file.dr_approval_request.output_path
  source_code_hash = data.archive_file.dr_approval_request.output_base64sha256
  handler          = "index.handler"
  runtime          = "python3.12"
  role             = aws_iam_role.dr_approval_request.arn
  timeout          = 30
  environment {
    variables = {
      APPROVALS_TABLE      = aws_dynamodb_table.dr_approvals.name
      BACKUP_BUCKET        = aws_s3_bucket.dr_backups.id
      BOT_TOKEN_SECRET_ARN = aws_secretsmanager_secret.discord_bot_token.arn
      DISCORD_CHANNEL_ID   = var.dr_discord_channel_id
    }
  }
}

# Discord 봇 토큰. 값은 TF 밖에서 넣는다(평문 state 회피 — 웹훅과 동일 패턴).
#
# ⚠️ 왜 웹훅이 아니라 봇인가: 채널 설정에서 만든 일반 incoming 웹훅(dr-alert, #310)은 버튼·드롭다운을
# 보낼 수 없다 — Discord 공식 문서 "Non-application-owned webhooks cannot send interactive components,
# and the components field will be ignored". 게다가 **에러가 아니라 2xx 를 주고 components 만 조용히
# 버린다**(실측 2026-07-15: Lambda 성공·DDB 저장까지 정상인데 메시지에 버튼만 없음 → 정적검증·리뷰로는
# 잡을 수 없고 실제 POST 를 쏴야 드러남). 승인 메시지는 application 소유 주체(=봇)로만 보낼 수 있다.
# 평문 알림(dr-alert)은 컴포넌트가 없어 웹훅 그대로 둔다.
resource "aws_secretsmanager_secret" "discord_bot_token" {
  name = "${var.name_prefix}-dr-discord-bot-token"
}


# Discord Application Public Key(hex). 값은 TF 밖에서 넣는다 — 평문 state 회피(웹훅과 동일 패턴).
# 이 키로 X-Signature-Ed25519 를 검증한다. 시크릿은 아니지만(공개키) 로테이션 편의를 위해 SM 에 둔다.
resource "aws_secretsmanager_secret" "discord_pubkey" {
  name = "${var.name_prefix}-dr-discord-pubkey"
}

# ── interaction Lambda (nodejs20 — Ed25519, 설계 §3.4) ──
# data.aws_iam_policy_document.dr_lambda_assume 는 위 approval-request Lambda 절에서
# 이미 정의됨(lambda.amazonaws.com 신뢰) — 재사용한다.
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
  function_name = "${var.name_prefix}-dr-interaction"
  # 역할 정책을 명시적 의존으로(위 dr_approval_request 와 동일 사유).
  depends_on       = [aws_cloudwatch_log_group.dr_interaction, aws_iam_role_policy.dr_interaction]
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

# ⚠️ AWS 는 2025-10 부터 Function URL 호출에 lambda:InvokeFunctionUrl **과** lambda:InvokeFunction 을
# 둘 다 요구한다(공식 문서: "Starting in October 2025, new function URLs will require both ...").
# 그런데 provider(aws v5.100.0)의 aws_lambda_function_url 은 구 동작대로 InvokeFunctionUrl statement
# 하나만 자동 생성한다 → InvokeFunction 이 없어 **403 Forbidden**(AccessDeniedException)이 나고
# Lambda 가 아예 호출되지 않는다(우리 코드의 401 조차 도달 못 함 — 실측 2026-07-15).
#
# ⚠️ AWS 는 2025-10 부터 Function URL 호출에 lambda:InvokeFunctionUrl **과** lambda:InvokeFunction 을
# 둘 다 요구한다(공식 문서). 그런데 provider(aws v5.100.0)의 aws_lambda_function_url 은 구 동작대로
# InvokeFunctionUrl statement 하나만 자동 생성한다 → 이 리소스가 없으면 **403 Forbidden**
# (AccessDeniedException)이 나고 Lambda 가 아예 호출되지 않는다. 우리 코드의 401 조차 도달 못 해서
# "서명 검증이 동작하는지"를 확인할 수 없다(실측 2026-07-15: 403 + 로그 스트림 0건).
#
# **조건을 걸 수 없다 — 셋 다 막혀 있다(실측):**
#   - lambda:FunctionUrlAuthType → AWS AddPermission 이 400 거부("only supported for
#     lambda:InvokeFunctionUrl action").
#   - lambda:InvokedViaFunctionUrl(AWS 권장) → provider 미지원, 오픈 이슈
#     hashicorp/terraform-provider-aws#44829. aws_lambda_permission 스키마에 인자 자체가 없다.
#   - principal_org_id → 이 계정은 Organization 소속이 아니다.
#
# **그래서 조건 없이 연다. 감수하는 노출과 그 근거:**
#   이 statement 는 "아무 AWS 계정이나 이 함수를 직접 Invoke 할 수 있음"을 뜻하고, 이 레포가 PUBLIC 이라
#   ARN(계정ID·함수명)이 사실상 공개다. 그러나 (1) 서명 검증이 코드 안에 있어 헤더 없는 호출은 401 로
#   떨어지므로 **로직은 안전**하고, (2) 실피해는 reserved_concurrent_executions(5) 소진 → 재해 순간
#   승인 버튼 429 인데, 그때도 CLI 우회(aws stepfunctions send-task-success)가 있어 DR 자체는 안 막힌다.
#   → 이 우회는 런북에 반드시 명시할 것(이게 이 결정의 안전망이다).
#
# provider 가 #44829 를 구현하면 invoked_via_function_url = true 를 얹어 이 노출을 없앨 것.
resource "aws_lambda_permission" "dr_interaction_url_invoke" {
  statement_id  = "FunctionURLInvokeAllowPublicAccess"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.dr_interaction.function_name
  principal     = "*"
}

output "dr_interaction_url" {
  description = "Discord Developer Portal 의 Interactions Endpoint URL 에 등록할 값."
  value       = aws_lambda_function_url.dr_interaction.function_url
}

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

  # 역할 정책을 명시적 의존으로 묶는다. role 만 참조하면 정책은 **숨은 의존**이라 두 가지가 깨진다
  # (bastion·proxy 인스턴스와 동일 패턴 — eks-dr-bastion.tf:127, public-ingress.tf:218):
  #   (1) 전체 apply 에서 정책과 SM 이 형제라 terraform 이 병렬 생성 → 정책보다 먼저 CreateStateMachine
  #       이 나가면 logging_configuration 에 필요한 CloudWatch Logs 권한이 없어 AccessDenied.
  #   (2) -target=aws_sfn_state_machine.dr_approval_test 로 재생성할 때 정책이 안 끌려온다
  #       (-target 은 의존성만 따라가고 의존하는 것은 안 따라감) → 권한 없는 롤로 생성 시도 →
  #       AccessDenied 재시도 루프로 2분+ hang 후 실패(실측 2026-07-15).
  # depends_on 이 둘 다 해소한다.
  depends_on = [aws_iam_role_policy.dr_sfn]

  logging_configuration {
    log_destination = "${aws_cloudwatch_log_group.dr_sfn.arn}:*"
    # ⚠️ false 로 고정 — true 면 LambdaFunctionScheduled 이벤트의 input 에 해석된 실제 taskToken 이
    # 로그에 평문으로 남는다. 토큰은 SendTaskSuccess 의 유일한 bearer 자격증명이라(그래서 IAM 이
    # resources=["*"]), 로그 읽기 권한 + states:SendTaskSuccess 만으로 서명·허용목록·arming 3겹을
    # 전부 우회할 수 있게 된다. level=ALL 은 유지 — 상태 전이 자체는 계속 기록되어 "어디서
    # 실패했나"는 그대로 보인다. Plan 2 의 실제 상태 머신도 이 값을 그대로 상속해야 한다.
    include_execution_data = false
    level                  = "ALL"
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
  count         = local.pub
  provider      = aws.use1
  function_name = "${var.name_prefix}-dr-failover-trigger"
  # 역할 정책을 명시적 의존으로(위 dr_approval_request 와 동일 사유).
  depends_on       = [aws_cloudwatch_log_group.dr_failover_trigger, aws_iam_role_policy.dr_failover_trigger]
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
