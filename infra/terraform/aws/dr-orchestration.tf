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

# ── interaction Lambda 웜 유지 ──
# Discord 는 버튼 인터랙션에 3초 내 응답을 요구한다. 콜드스타트면(init+SecretsManager pubkey+DDB+SFN)
# 3초를 넘겨 "애플리케이션이 적시에 응답하지 않음"이 뜨고, 승인자가 다시 눌러 두 번째 클릭이
# TaskTimedOut(토큰 이미 소비)으로 에러난다(2026-07-18 실측). warm 은 ~140ms 라 여유. 5분 핑으로 상시 warm.
resource "aws_cloudwatch_event_rule" "dr_interaction_warm" {
  name                = "${var.name_prefix}-dr-interaction-warm"
  description         = "interaction Lambda 웜 유지(Discord 3s 인터랙션 타임아웃 방지)"
  schedule_expression = "rate(5 minutes)"
}

resource "aws_cloudwatch_event_target" "dr_interaction_warm" {
  rule      = aws_cloudwatch_event_rule.dr_interaction_warm.name
  target_id = "interaction-warm"
  arn       = aws_lambda_function.dr_interaction.arn
  input     = jsonencode({ warmup = true })
}

resource "aws_lambda_permission" "dr_interaction_warm" {
  statement_id  = "AllowWarmPing"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.dr_interaction.function_name
  principal     = "events.amazonaws.com"
  source_arn    = aws_cloudwatch_event_rule.dr_interaction_warm.arn
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

  # ── [2] TerraformApply — 없으면 AccessDenied 로 페일오버가 승인 직후 멈춘다 ──
  statement {
    sid       = "StartCodeBuild"
    actions   = ["codebuild:StartBuild", "codebuild:StopBuild", "codebuild:BatchGetBuilds"]
    resources = [aws_codebuild_project.dr_failover_tf.arn]
  }

  statement {
    sid = "CodeBuildSyncEvents"
    # ⚠️ codebuild:startBuild.sync 는 EventBridge 관리형 규칙으로 완료를 감지한다(AWS 문서) —
    # .sync 통합의 숨은 IAM 요구다. 액션이 아니라 **규칙 생성 권한**이라 놓치기 쉽다.
    actions   = ["events:PutTargets", "events:PutRule", "events:DescribeRule"]
    resources = ["arn:aws:events:${var.region}:${data.aws_caller_identity.current.account_id}:rule/StepFunctionsGetEventForCodeBuildStartBuildRule"]
  }

  # ── 자식 SM 이 bastion 에 명령을 보내기 위한 권한 ──
  statement {
    sid = "RunOnBastion"
    actions = [
      "ssm:SendCommand",
      "ssm:GetCommandInvocation",
      "ssm:DescribeInstanceInformation",
    ]
    # SendCommand 는 문서·인스턴스 양쪽에 권한이 필요하고, GetCommandInvocation 은 command ARN 을
    # 런타임에야 알 수 있다. DescribeInstanceInformation 은 리소스 한정을 지원하지 않는다(AWS 문서).
    resources = ["*"]
  }

  # ── [2.4]·[2.5]·[4] — 메인 SM(T5)이 쓸 SDK 상태들. 여기서 미리 넣어둔다 ──
  statement {
    sid       = "ResolveBastion"
    actions   = ["ec2:DescribeInstances"]
    resources = ["*"] # DescribeInstances 는 리소스 한정을 지원하지 않는다(AWS 문서)
  }

  statement {
    # [2.4] ClearAlbParam — stale ALB 파라미터 방어(설계 §5.1.2). [9] 가 쓰기 전에 항상 비운다.
    sid       = "ClearAlbParam"
    actions   = ["ssm:DeleteParameter", "ssm:PutParameter"]
    resources = ["arn:aws:ssm:${var.region}:${data.aws_caller_identity.current.account_id}:parameter/cledyu-dr/failover/*"]
  }

  statement {
    # [4] ScaleNodes — warm(desired 0) → hot(desired 3).
    sid       = "ScaleNodes"
    actions   = ["eks:UpdateNodegroupConfig", "eks:DescribeNodegroup", "eks:ListNodegroups"]
    resources = ["*"] # 노드그룹 이름을 런타임에 조회하므로 사전 특정 불가
  }

  # ── [5]·[10]·[13] Lambda 호출 — 없으면 이 셋과 NotifyFailed 가 전부 AccessDenied ──
  # ⚠️ notify 를 빠뜨리면 **실패가 무음이 된다**: 모든 Catch 가 NotifyFailed 로 가는데 그게
  # AccessDenied 면 SFN 은 FAILED 로 끝나고 Discord 엔 아무것도 안 온다 → 재해 중 "승인 눌렀는데
  # 소식이 없다". 이 설계의 마지막 방어선이라 T4 Step 5 에서 가장 먼저 확인한다.
  #
  # 사이클 없음 — Lambda 3개는 자기 실행 롤만 보고 aws_iam_role_policy.dr_sfn 을 depends_on 하지
  # 않는다. 사이클이 나는 건 자식 SM 뿐이고 그건 dr_sfn_child 로 분리했다(위 주석).
  statement {
    sid     = "InvokeFailoverLambdas"
    actions = ["lambda:InvokeFunction"]
    resources = [
      aws_lambda_function.dr_addon_install.arn,
      aws_lambda_function.dr_dns_switch.arn,
      aws_lambda_function.dr_notify.arn,
    ]
  }
}

# ⚠️ 자식 SM 을 참조하는 statement 는 **별도 정책**이어야 한다 — dr_sfn 에 두면 terraform 사이클이다:
#   dr_run_on_bastion --depends_on--> aws_iam_role_policy.dr_sfn --policy--> data.dr_sfn
#     --resources--> dr_run_on_bastion.arn   ← 순환 (terraform validate 가 "Error: Cycle" 로 거부)
# 자식 SM 은 dr_sfn 만 depends_on 하고 이 정책은 depends_on 하지 않으므로 여기선 안전하다.
data "aws_iam_policy_document" "dr_sfn_child" {
  statement {
    sid       = "StartChildSm"
    actions   = ["states:StartExecution"]
    resources = [aws_sfn_state_machine.dr_run_on_bastion.arn]
  }
  statement {
    sid = "ChildSmSync"
    # states:startExecution.sync 는 자식 실행을 폴링·중단하기 위해 아래가 필요하다(AWS 문서).
    actions   = ["states:DescribeExecution", "states:StopExecution"]
    resources = ["*"]
  }
  statement {
    sid = "ChildSmSyncEvents"
    # .sync 통합은 EventBridge 관리형 규칙으로 완료를 감지한다(AWS 문서) — CodeBuild .sync 와 동일 요구.
    actions   = ["events:PutTargets", "events:PutRule", "events:DescribeRule"]
    resources = ["arn:aws:events:${var.region}:${data.aws_caller_identity.current.account_id}:rule/StepFunctionsGetEventsForStepFunctionsExecutionRule"]
  }
}

resource "aws_iam_role_policy" "dr_sfn_child" {
  name   = "${var.name_prefix}-dr-sfn-child"
  role   = aws_iam_role.dr_sfn.id
  policy = data.aws_iam_policy_document.dr_sfn_child.json
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
    # T5 에서 하네스(dr_approval_test) → **메인 SM** 으로 교체됐다.
    # ⚠️ 교체 전에는 실재해에 승인 버튼이 떠도 눌러봐야 **아무 일도 안 일어나고 실패 알림도 없었다**
    #   (하네스는 RequestApproval 하나로 End=true = 성공으로 끝난다) → 운영자는 "페일오버가 돌고 있다"고
    #   믿는다. 그래서 T5 전까지 dr_orchestration_armed 무장이 금지였다(잔여 #1).
    resources = [aws_sfn_state_machine.dr_failover.arn]
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
      STATE_MACHINE_ARN = aws_sfn_state_machine.dr_failover.arn # T5 에서 교체 (was: dr_approval_test)
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

# ── [2] TerraformApply 실행기 (CodeBuild) ─────────────────────────────────────
# Lambda 15분 제한(NAT 생성만 몇 분) + terraform 바이너리 필요 → CodeBuild 가 유일한 선택.
# AWS SDK 직접 생성은 state 밖 고아를 만들어 failback 의 `terraform apply` 전제를 깬다.
# VPC 연결 불요 — terraform 은 AWS API 만 호출한다(EKS 엔드포인트를 안 건드림).
# 과금: 빌드 중에만 발생한다(idle $0) → 승인 게이트 리소스처럼 count 게이트 없이 상시 둔다.

data "aws_iam_policy_document" "dr_codebuild_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["codebuild.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "dr_failover_tf" {
  name               = "${var.name_prefix}-dr-failover-tf"
  assume_role_policy = data.aws_iam_policy_document.dr_codebuild_assume.json
}

# ⚠️ 이 롤은 사실상 DR 범위 admin 이다(설계 §5.4 가 명시한 표면). terraform apply 가 무엇을 만들지는
# -target 인자가 정하지만 **-target 은 terraform 인자일 뿐 IAM 경계가 아니므로** 롤을 좁힐 수 없다.
# 방어선은 승인 게이트 3겹(Ed25519 서명·허용목록·armed)이고, 이 롤을 쓸 수 있는 것은 SFN 뿐이다.
resource "aws_iam_role_policy_attachment" "dr_failover_tf_admin" {
  role       = aws_iam_role.dr_failover_tf.name
  policy_arn = "arn:aws:iam::aws:policy/AdministratorAccess"
}

resource "aws_cloudwatch_log_group" "dr_failover_tf" {
  name              = "/aws/codebuild/${var.name_prefix}-dr-failover-tf"
  retention_in_days = 30
}

resource "aws_codebuild_project" "dr_failover_tf" {
  name         = "${var.name_prefix}-dr-failover-tf"
  service_role = aws_iam_role.dr_failover_tf.arn
  # -target 은 의존성만 따라가고 의존하는 것은 안 따라간다 → 정책을 명시 의존으로 묶는다
  # (eks-dr-bastion.tf:127 · public-ingress.tf:218 기존 패턴).
  depends_on = [aws_iam_role_policy_attachment.dr_failover_tf_admin]

  artifacts { type = "NO_ARTIFACTS" }

  environment {
    compute_type = "BUILD_GENERAL1_SMALL"
    image        = "aws/codebuild/amazonlinux2-x86_64-standard:5.0"
    type         = "LINUX_CONTAINER"

    environment_variable {
      name  = "TF_VERSION"
      value = "1.9.8" # versions.tf 의 required_version >= 1.9.0 을 만족하는 고정 버전
    }
  }

  source {
    type      = "GITHUB"
    location  = "https://github.com/requset700k/Cledyu.git"
    buildspec = "infra/terraform/aws/dr-failover-buildspec.yml"
  }

  # 실재해는 **검증된 main** 을 돌린다 — 재해 중에 브랜치를 굴리지 않는다.
  # ⚠️ 드릴은 buildspec 이 아직 main 에 없을 수 있다(머지 전) → 프로젝트를 고치지 말고
  #    `aws codebuild start-build --source-version <브랜치>` 로 **호출 시 오버라이드**한다.
  source_version = "main"

  logs_config {
    cloudwatch_logs {
      group_name = aws_cloudwatch_log_group.dr_failover_tf.name
    }
  }

  build_timeout = 30 # 분. NAT·엔드포인트·bastion 생성 ~3분 + 여유

  # ⚠️ 동시 빌드 금지 — terraform state 락은 **하나**다(S3 backend + DynamoDB cledyu-tf-lock).
  # 2026-07-15 T1 실측: 4초 간격으로 빌드 2개가 시작돼 뒤엣것이 `Error acquiring the state lock`
  # (ConditionalCheckFailedException)으로 죽었다. 락 에러는 20초 뒤에야 나고 메시지가 원인을 안 가리켜
  # 진단이 오래 걸린다 → 아예 **시작 자체를 막아** 빠르고 명확하게 실패시킨다.
  # 이건 빌드↔빌드 충돌만 막는다. **사람↔빌드 충돌**(운영자가 재해 중 terraform 을 만지는 경우)은
  # 여전히 가능하고, 그건 [2] 의 Retry 로 다뤄야 한다(T5 에서 판단).
  concurrent_build_limit = 1

  tags = local.eks_dr_tags
}

# ── 자식 SM: bastion 에서 스크립트 실행 (SSM 폴링) ────────────────────────────
# SFN 에 SSM `.sync` 통합이 **없다**(AWS optimized integrations 표에 SSM 이 없고, AWS SDK 통합엔
# .sync 가 Not supported). ssm:SendCommand 는 CommandId 만 즉시 반환하므로 폴링을 직접 만든다.
# SSM 단계가 6개([3][6][7][8][9][11][12])라 인라인하면 상태가 폭증하고, 통짜 스크립트로 합치면
# 실패 지점을 잃는다 → 폴링 로직을 여기 한 군데만 두고 메인 SM 이 states:startExecution.sync 로 호출한다.

# bastion 명령 출력(stdout/stderr)을 받는다.
#
# ⚠️ **S3(dr_backups)로 보내지 않는다** — 계획 초안은 OutputS3BucketName=dr_backups 였으나 3중으로 막혔다
# (2026-07-15 T2 착수 전 발견):
#   (1) bastion 롤에 s3:PutObject 가 없다(GetObject on vault/* 만) — SSM 은 **인스턴스 자격증명**으로 올린다
#   (2) 버킷이 SSE-KMS 인데 bastion 엔 kms:Decrypt 만 있고 쓰기용 GenerateDataKey 가 없다
#   (3) 버킷이 **Object Lock GOVERNANCE 30일** — 드릴 로그가 30일간 삭제 불가로 쌓인다.
#       dr_backups 는 "삭제·변조 불가로 굳혀 랜섬웨어·실수 삭제로부터 보호"하는 WORM 금고다(backup.tf:11).
#       **백업 금고와 운영 로그는 성격이 정반대다** — (1)(2)는 IAM 으로 고쳐지지만 (3)은 설계 문제다.
# → CloudWatch Logs 로 보낸다. 이 설계의 다른 로그(SFN·Lambda·CodeBuild)와 같은 곳이고, retention 으로
#   자동 정리되며 `aws logs tail` 로 바로 읽는다.
resource "aws_cloudwatch_log_group" "dr_bastion_commands" {
  name              = "/aws/ssm/${var.name_prefix}-dr-failover"
  retention_in_days = 30
}

# ⚠️ SSM 의 CloudWatch 출력은 **인스턴스(bastion)의 자격증명**으로 쓴다 — SFN 롤이 아니다.
# 붙어 있는 AmazonSSMManagedInstanceCore 는 logs 권한을 **하나도 주지 않는다**(실측 확인) →
# 없으면 stdout 전문이 유실되고 stdoutTail(잘림)만 남는다.
data "aws_iam_policy_document" "eks_dr_bastion_command_logs" {
  count = local.eks_dr_enabled

  # ⚠️ 에이전트는 **로그그룹이 이미 있어도** DescribeLogGroups → CreateLogGroup 을 먼저 호출한다
  # (2026-07-15 T2 실측 — 에이전트 로그에서 확인). "그룹은 terraform 이 만드니 CreateLogStream·
  # PutLogEvents 면 충분"이라는 추론은 틀렸다. 그리고 **DescribeLogGroups 는 리소스 한정이 안 된다** —
  # 에이전트 요청이 `log-group::log-stream:`(그룹명 비어 있음)으로 오므로 "*" 여야 한다.
  statement {
    sid       = "DiscoverLogGroups"
    actions   = ["logs:DescribeLogGroups"]
    resources = ["*"]
  }

  # CreateLogGroup 은 그룹이 이미 있으면 실제로는 no-op 이지만, **권한이 없으면 에이전트가 거기서
  # 중단하고 CloudWatch 출력을 통째로 포기한다**(명령 자체는 Success 로 끝나 조용히 유실된다).
  statement {
    sid = "WriteCommandLogs"
    actions = [
      "logs:CreateLogGroup",
      "logs:CreateLogStream",
      "logs:PutLogEvents",
      "logs:DescribeLogStreams",
    ]
    resources = [
      aws_cloudwatch_log_group.dr_bastion_commands.arn,
      "${aws_cloudwatch_log_group.dr_bastion_commands.arn}:*",
    ]
  }
}

resource "aws_iam_role_policy" "eks_dr_bastion_command_logs" {
  count  = local.eks_dr_enabled
  name   = "ssm-command-logs"
  role   = aws_iam_role.eks_dr_bastion[0].id
  policy = data.aws_iam_policy_document.eks_dr_bastion_command_logs[0].json
}

resource "aws_sfn_state_machine" "dr_run_on_bastion" {
  name       = "${var.name_prefix}-dr-run-on-bastion"
  role_arn   = aws_iam_role.dr_sfn.arn
  depends_on = [aws_iam_role_policy.dr_sfn]

  logging_configuration {
    log_destination = "${aws_cloudwatch_log_group.dr_sfn.arn}:*"
    # 부모와 동일 — 실행 데이터에 taskToken 이 실릴 수 있다(설계 §5.4).
    include_execution_data = false
    level                  = "ALL"
  }

  definition = jsonencode({
    Comment = "bastion 에서 스크립트 실행 — SSM 폴링(SFN 에 SSM .sync 통합 없음)"
    # ⚠️ 실행 전체 상한. WaitCmd→GetResult→Done?→WaitCmd 는 무한 루프이고 Done? 의 Default 가 Failed 지만
    # Status 가 InProgress 로 계속 오면 영원히 돈다. SSM 의 executionTimeout 이 먼저 걸려 TimedOut 을 주는
    # 게 정상 경로이나, 그마저 안 오는 경우(에이전트 죽음 등)의 backstop 이다.
    #
    # ⚠️ **가장 긴 스크립트의 timeoutSeconds 보다 커야 한다** — 안 그러면 이 백스톱이 먼저 걸려
    # SSM 의 TimedOut 대신 States.Timeout 이 나고, "어느 스크립트가 왜" 가 사라진다.
    # 가장 긴 것은 **09(4800)** 다(08=3600 이 아니다 — 2026-07-16 codex P2 로 09 를 3000→4800 재산정).
    # 4800 + 폴링 여유 600 = 5400. 초과 시 States.Timeout → 부모의 Catch 가 잡는다.
    #
    # 🔴 **스크립트의 timeoutSeconds 를 올릴 땐 이 값도 같이 본다.** 한쪽만 바꾸면 조용히 어긋난다
    #    — 아래 Step 3 의 대수 검증(계획서)이 그 정합성을 강제한다.
    TimeoutSeconds = 5400
    StartAt        = "WaitForSsmAgent"
    States = {
      # ⚠️ module.eks_dr_endpoints 는 s3/kms/sts 만 만든다 — ssm/ssmmessages/ec2messages 인터페이스
      # 엔드포인트가 없어 bastion 의 SSM 에이전트는 **NAT 로 나가서 등록**해야 한다. 그 NAT 는 [2] 의
      # 같은 apply 에서 방금 생겼다. 등록 전 SendCommand 는 동기 예외를 던져 Choice·Wait 를 타지
      # 못하므로 등록을 먼저 기다린다(런북 :280 이 같은 창을 기록).
      WaitForSsmAgent = {
        Type     = "Task"
        Resource = "arn:aws:states:::aws-sdk:ssm:describeInstanceInformation"
        Parameters = {
          Filters = [{ Key = "InstanceIds", "Values.$" = "States.Array($.instanceId)" }]
        }
        ResultPath = "$.agent"
        Retry = [{
          # ⚠️ 이 Retry 는 API 에러용이고 **미등록 인스턴스에는 안 걸린다** —
          # describeInstanceInformation 은 미등록 대상에 에러가 아니라 **빈 목록**을 준다.
          # 등록 대기는 아래 AgentReady?→WaitAgent 루프가 한다.
          ErrorEquals     = ["States.ALL"]
          IntervalSeconds = 20
          MaxAttempts     = 15
          BackoffRate     = 1.0
        }]
        Next = "AgentReady?"
      }

      # ⚠️ IsPresent 가드 필수. 에이전트 미등록이면 InstanceInformationList 가 **빈 배열**이라
      # $.agent.InstanceInformationList[0].PingStatus 경로 자체가 없다. 경로 없는 Variable 을 Choice 가
      # 어떻게 다루는지(States.Runtime vs Default 낙하)는 미확정이나, States.Runtime 이면 **어떤 Catch 로도
      # 못 잡는다** → IsPresent 를 먼저 두면 어느 쪽이든 안전하다.
      # ⚠️ 스모크 테스트는 이 분기를 **원리적으로 못 밟는다**(bastion 이 뜬 지 오래라 이미 Online).
      # 즉 드릴은 통과하고 **실재해(방금 만든 bastion)에서만** 터지는 자리라 코드로 막는다.
      "AgentReady?" = {
        Type = "Choice"
        Choices = [{
          And = [
            { Variable = "$.agent.InstanceInformationList[0].PingStatus", IsPresent = true },
            { Variable = "$.agent.InstanceInformationList[0].PingStatus", StringEquals = "Online" },
          ]
          Next = "BuildCommands"
        }]
        Default = "WaitAgent"
      }

      WaitAgent = {
        Type    = "Wait"
        Seconds = 20
        Next    = "WaitForSsmAgent"
      }

      # env 와 script 를 배열 2원소로 만든다 — **문자열 조립을 하지 않는다.**
      # States.Format 에 스크립트 전문을 넣으면 작은따옴표('sh -c ...')가 intrinsic 리터럴을 끊고,
      # 중괄호({ echo; exit 1; })가 플레이스홀더로 읽히며, 개행이 인자에 못 들어가 **정의가 거부된다.**
      # 배열 원소는 각각 온전한 JSON 문자열이라 전부 안전하다.
      # AWS-RunShellScript 는 commands 를 순서대로 **같은 셸**에서 실행하므로 env 의 export 가 script 에 걸린다.
      BuildCommands = {
        Type = "Pass"
        Parameters = {
          "instanceId.$"     = "$.instanceId"
          "timeoutSeconds.$" = "$.timeoutSeconds"
          "label.$"          = "$.label"
          "commands.$"       = "States.Array($.env, $.script)"
        }
        Next = "SendCommand"
      }

      SendCommand = {
        Type     = "Task"
        Resource = "arn:aws:states:::aws-sdk:ssm:sendCommand"
        Parameters = {
          "InstanceIds.$" = "States.Array($.instanceId)"
          DocumentName    = "AWS-RunShellScript"
          "Comment.$"     = "$.label"
          # ⚠️ SendCommand 의 TimeoutSeconds 는 **배달 타임아웃**이다 — AWS 문서: "If this time is reached
          # and the command hasn't already started running, it won't run." 실행 시간을 제한하지 **않는다.**
          # 에이전트가 Online 인 것을 WaitForSsmAgent 가 확인했으므로 60s 면 충분하고,
          # 실행 제한은 아래 executionTimeout 이 한다.
          TimeoutSeconds = 60
          CloudWatchOutputConfig = {
            CloudWatchLogGroupName  = aws_cloudwatch_log_group.dr_bastion_commands.name
            CloudWatchOutputEnabled = true
          }
          Parameters = {
            "commands.$" = "$.commands"
            # AWS-RunShellScript 의 executionTimeout 은 **문자열 배열**이다(SSM 문서 파라미터 규격).
            "executionTimeout.$" = "States.Array(States.Format('{}', $.timeoutSeconds))"
          }
        }
        ResultPath = "$.cmd"
        Retry = [{
          # 에이전트 등록 직후에도 잠깐 전파 지연으로 실패할 수 있다.
          # ⚠️ States.ALL 은 **단독**이어야 하고 마지막 retrier 여야 한다(AWS 문서) — 다른 에러명과
          # 같이 쓰면 CreateStateMachine 이 정의를 거부해 terraform apply 가 실패한다.
          ErrorEquals     = ["States.ALL"]
          IntervalSeconds = 20
          MaxAttempts     = 10
          BackoffRate     = 1.5
        }]
        Next = "WaitCmd"
      }

      WaitCmd = {
        Type    = "Wait"
        Seconds = 30
        Next    = "GetResult"
      }

      GetResult = {
        Type     = "Task"
        Resource = "arn:aws:states:::aws-sdk:ssm:getCommandInvocation"
        Parameters = {
          "CommandId.$"  = "$.cmd.Command.CommandId"
          "InstanceId.$" = "$.instanceId"
        }
        ResultPath = "$.result"
        Retry = [{
          # 명령 직후엔 InvocationDoesNotExist 가 날 수 있다(전파).
          ErrorEquals     = ["States.ALL"]
          IntervalSeconds = 10
          MaxAttempts     = 5
          BackoffRate     = 1.5
        }]
        Next = "Done?"
      }

      "Done?" = {
        Type = "Choice"
        Choices = [
          {
            Or = [
              { Variable = "$.result.Status", StringEquals = "Pending" },
              { Variable = "$.result.Status", StringEquals = "InProgress" },
              { Variable = "$.result.Status", StringEquals = "Delayed" },
            ]
            Next = "WaitCmd"
          },
          { Variable = "$.result.Status", StringEquals = "Success", Next = "Succeeded" },
        ]
        # Failed·TimedOut·Cancelled·Cancelling 등 나머지는 전부 실패로 본다(명시 성공만 통과).
        Default = "Failed"
      }

      Succeeded = {
        Type = "Pass"
        # ⚠️ stdout **전문을 반환하지 않는다** — GetCommandInvocation 의 StandardOutputContent 는
        # 잘린 값이고(24,000자), 그걸 7단계 누적하면 SFN 페이로드 상한(256KB)에 근접한다.
        # 전문은 CloudWatch 로그그룹에 있고 commandId 로 찾는다(스트림: <commandId>/<instanceId>/...).
        Parameters = {
          "status.$"       = "$.result.Status"
          "responseCode.$" = "$.result.ResponseCode"
          "stdoutTail.$"   = "$.result.StandardOutputContent"
          "commandId.$"    = "$.cmd.Command.CommandId"
          logGroup         = aws_cloudwatch_log_group.dr_bastion_commands.name
        }
        End = true
      }

      # ⚠️ **정적 Cause 가 아니라 CausePath 다**(스펙 §11.18 (d) 실측). 초안은 정적이라 commandId 를 못 실었고
      # §11.13 (d) 는 그걸 "B안의 비용 — Discord→SFN콘솔→자식실행→commandId→CloudWatch 3~4홉"으로 수용했다.
      # **그 전제가 실측으로 깨졌다**: CausePath + States.Format 이 정의에도 통과하고 값도 채워지며,
      # `.sync:2` 를 넘어 부모의 $.error.Cause 안에 그대로 실려 온다 → 알림에 **쳐야 할 명령어 전문**을 싣는다.
      #
      # B안의 안전성은 그대로다 — label 과 commandId 는 시크릿이 아니다. stderr(set -x 트레이스, §11.13 (b))
      # 는 여전히 알림 경로에 안 올린다. 그게 B안을 택한 이유였다(C6 가 07 에서 막은 표면을 안 넓힌다).
      #
      # $.label 은 BuildCommands 가, $.cmd 는 SendCommand 의 ResultPath 가 넣는다. Done? 의 Default 로
      # 여기 오는 경로는 SendCommand 성공 이후뿐이라 둘 다 반드시 있다. (SendCommand 자체가 실패하면
      # Retry 소진 후 자기 에러로 죽고 이 상태를 안 거친다 → 부모의 Catch 가 잡는다.)
      Failed = {
        Type      = "Fail"
        Error     = "BastionScriptFailed"
        CausePath = "States.Format('{} 실패 — aws logs tail ${aws_cloudwatch_log_group.dr_bastion_commands.name} --log-stream-name-prefix {}', $.label, $.cmd.Command.CommandId)"
      }
    }
  })
}

# ══ [5] InstallAddons — coredns·ebs-csi 멱등 설치 (Lambda) ═════════════════════
# warm(node0) 에선 이 둘이 Deployment 라 DEGRADED 로 apply 를 블록한다 → cluster_addons 에서 빼두고
# (eks-dr.tf:133-139) [4] ScaleNodes 직후 여기서 설치한다. Lambda 는 **시작만** 하고 ACTIVE 대기는
# SFN 의 WaitAddons→CheckAddons 폴링이 한다(900s 상한 회피 — [2] 를 CodeBuild 로 뺀 것과 같은 이유).
data "archive_file" "dr_addon_install" {
  type        = "zip"
  source_file = "${path.module}/dr-orchestration-lambda/addon-install/index.py"
  output_path = "${path.module}/dr-orchestration-lambda/addon-install/addon-install.zip"
}

resource "aws_iam_role" "dr_addon_install" {
  name               = "${var.name_prefix}-dr-addon-install"
  assume_role_policy = data.aws_iam_policy_document.dr_lambda_assume.json
}

data "aws_iam_policy_document" "dr_addon_install" {
  statement {
    sid     = "ManageAddons"
    actions = ["eks:DescribeAddon", "eks:CreateAddon", "eks:UpdateAddon"]
    resources = [
      "arn:aws:eks:${var.region}:${data.aws_caller_identity.current.account_id}:cluster/${local.eks_dr_name}",
      "arn:aws:eks:${var.region}:${data.aws_caller_identity.current.account_id}:addon/${local.eks_dr_name}/*/*",
    ]
  }
  statement {
    sid     = "PassEbsCsiRole"
    actions = ["iam:PassRole"]
    # create/update_addon 의 serviceAccountRoleArn 은 PassRole 을 요구한다 — 없으면 ebs-csi 만
    # AccessDenied 로 죽고 coredns 는 성공해 **절반만 설치된 상태**가 된다.
    #
    # ⚠️ module.eks_dr_ebs_csi_irsa[0].iam_role_arn 을 쓰지 않는다 — 그 모듈은 count =
    # local.eks_dr_enabled(기본 false) 라 이 게이트 없는 Lambda 가 참조하면 평시 apply 가
    # index out of range 로 깨진다. eks-dr.tf:139 가 "롤명 결정적"이라 명시한 게 이 용도다.
    resources = ["arn:aws:iam::${data.aws_caller_identity.current.account_id}:role/${local.eks_dr_name}-ebs-csi"]
    condition {
      test     = "StringEquals"
      variable = "iam:PassedToService"
      values   = ["eks.amazonaws.com"]
    }
  }
  statement {
    sid       = "WhoAmI"
    actions   = ["sts:GetCallerIdentity"]
    resources = ["*"] # GetCallerIdentity 는 리소스 한정을 지원하지 않는다(AWS 문서)
  }
  statement {
    sid     = "Logs"
    actions = ["logs:CreateLogStream", "logs:PutLogEvents"]
    resources = [
      "arn:aws:logs:${var.region}:*:log-group:/aws/lambda/${var.name_prefix}-dr-addon-install",
      "arn:aws:logs:${var.region}:*:log-group:/aws/lambda/${var.name_prefix}-dr-addon-install:*",
    ]
  }
}

resource "aws_iam_role_policy" "dr_addon_install" {
  name   = "${var.name_prefix}-dr-addon-install"
  role   = aws_iam_role.dr_addon_install.id
  policy = data.aws_iam_policy_document.dr_addon_install.json
}

resource "aws_cloudwatch_log_group" "dr_addon_install" {
  name              = "/aws/lambda/${var.name_prefix}-dr-addon-install"
  retention_in_days = 30
}

resource "aws_lambda_function" "dr_addon_install" {
  function_name    = "${var.name_prefix}-dr-addon-install"
  depends_on       = [aws_cloudwatch_log_group.dr_addon_install, aws_iam_role_policy.dr_addon_install]
  filename         = data.archive_file.dr_addon_install.output_path
  source_code_hash = data.archive_file.dr_addon_install.output_base64sha256
  handler          = "index.handler"
  runtime          = "python3.12"
  role             = aws_iam_role.dr_addon_install.arn
  timeout          = 60 # 시작만 한다 — 대기는 SFN 폴링
}

# ══ [10] SwitchDNS — api·app·auth 를 EKS ALB 로 (Lambda) ═══════════════════════
# bastion 롤엔 route53/wafv2/elbv2 권한이 없다(런북 명시: AccessDenied) → Lambda 가 자기 롤로 한다.
# Route53 은 공개 API 라 VPC 연결 불요. ALB 신원은 [9] 가 쓴 SSM 파라미터가 유일한 경로(설계 §5.1.2).
data "archive_file" "dr_dns_switch" {
  type        = "zip"
  source_file = "${path.module}/dr-orchestration-lambda/dns-switch/index.py"
  output_path = "${path.module}/dr-orchestration-lambda/dns-switch/dns-switch.zip"
}

resource "aws_iam_role" "dr_dns_switch" {
  name               = "${var.name_prefix}-dr-dns-switch"
  assume_role_policy = data.aws_iam_policy_document.dr_lambda_assume.json
}

data "aws_iam_policy_document" "dr_dns_switch" {
  statement {
    sid       = "ReadAlbParam"
    actions   = ["ssm:GetParameter"]
    resources = ["arn:aws:ssm:${var.region}:${data.aws_caller_identity.current.account_id}:parameter/cledyu-dr/failover/*"]
  }
  statement {
    sid     = "FindAlb"
    actions = ["elasticloadbalancing:DescribeLoadBalancers"]
    # Describe* 는 리소스 한정을 지원하지 않는다(AWS 문서) — ALB 목록에서 DNSName 으로 찾아야 한다.
    resources = ["*"]
  }
  statement {
    sid = "CheckWaf"
    # 🔴 **액션이 둘이다.** `GetWebACLForResource` API 한 번을 부르는데 IAM 은 두 가지를 본다:
    #   · wafv2:GetWebACLForResource → 조회 대상(ALB)에
    #   · wafv2:GetWebACL            → **반환값(WebACL)에** ← 이게 빠져 있었다
    # 바로 아래 주석이 "양쪽에 권한이 필요하다"고 정확히 적어놓고 actions 엔 하나만 넣었다.
    # 2026-07-16 T5 라이브 드릴에서 실측:
    #   AccessDeniedException ... not authorized to perform: wafv2:GetWebACL
    #   on resource: .../regional/webacl/cledyu-lab-public/...
    # **T4 드릴은 이걸 못 잡았다** — dns-switch fail-closed 시험이 index.py:37(SSM ParameterNotFound)에서
    # 죽어 :47 의 WAF 호출에 **도달조차 못 했다.** 테스트는 통과했는데 정작 검증하려던 경로를 한 번도
    # 안 밟은 것이다(§11.18 (g) 의 InvokeFailoverLambdas 와 같은 패턴).
    actions = ["wafv2:GetWebACLForResource", "wafv2:GetWebACL"]
    # ⚠️ ALB(조회 대상)와 WebACL(반환값) **양쪽**에 권한이 필요하다 — ALB ARN 은 런타임에 알고,
    # ACL 은 lab public 스택 소유라 여기서 특정하면 그 스택 게이트에 묶인다.
    resources = ["*"]
  }
  statement {
    sid     = "SwitchRecords"
    actions = ["route53:ChangeResourceRecordSets"]
    # 존 ID 는 런타임 조회다 — public-ingress 의 data.aws_route53_zone 은 enable_public_ingress
    # 게이트(기본 false)라 여기서 참조하면 DR apply 에서 index out of range 로 깨진다.
    resources = ["arn:aws:route53:::hostedzone/*"]
  }
  statement {
    sid       = "FindZone"
    actions   = ["route53:ListHostedZonesByName"]
    resources = ["*"] # List* 는 리소스 한정을 지원하지 않는다(AWS 문서)
  }
  statement {
    sid     = "Logs"
    actions = ["logs:CreateLogStream", "logs:PutLogEvents"]
    resources = [
      "arn:aws:logs:${var.region}:*:log-group:/aws/lambda/${var.name_prefix}-dr-dns-switch",
      "arn:aws:logs:${var.region}:*:log-group:/aws/lambda/${var.name_prefix}-dr-dns-switch:*",
    ]
  }
}

resource "aws_iam_role_policy" "dr_dns_switch" {
  name   = "${var.name_prefix}-dr-dns-switch"
  role   = aws_iam_role.dr_dns_switch.id
  policy = data.aws_iam_policy_document.dr_dns_switch.json
}

resource "aws_cloudwatch_log_group" "dr_dns_switch" {
  name              = "/aws/lambda/${var.name_prefix}-dr-dns-switch"
  retention_in_days = 30
}

resource "aws_lambda_function" "dr_dns_switch" {
  function_name    = "${var.name_prefix}-dr-dns-switch"
  depends_on       = [aws_cloudwatch_log_group.dr_dns_switch, aws_iam_role_policy.dr_dns_switch]
  filename         = data.archive_file.dr_dns_switch.output_path
  source_code_hash = data.archive_file.dr_dns_switch.output_base64sha256
  handler          = "index.handler"
  runtime          = "python3.12"
  role             = aws_iam_role.dr_dns_switch.arn
  timeout          = 30
}

# ══ [13] NotifyComplete + 모든 Catch 의 NotifyFailed (Lambda) ══════════════════
# 평문이라 components 가 없다 → dr-alert(#310)와 **같은 us-east-1 웹훅**을 공용한다.
# ⚠️ 시크릿은 us-east-1, 이 Lambda 는 ap-northeast-2 → 코드가 ARN 에서 리전을 파싱한다(스펙 §3.3).
data "archive_file" "dr_notify" {
  type        = "zip"
  source_file = "${path.module}/dr-orchestration-lambda/notify/index.py"
  output_path = "${path.module}/dr-orchestration-lambda/notify/notify.zip"
}

resource "aws_iam_role" "dr_notify" {
  name               = "${var.name_prefix}-dr-notify"
  assume_role_policy = data.aws_iam_policy_document.dr_lambda_assume.json
}

data "aws_iam_policy_document" "dr_notify" {
  statement {
    sid       = "ReadWebhook"
    actions   = ["secretsmanager:GetSecretValue"]
    resources = [aws_secretsmanager_secret.discord_webhook.arn] # us-east-1 (provider aws.use1)
  }
  statement {
    sid     = "Logs"
    actions = ["logs:CreateLogStream", "logs:PutLogEvents"]
    resources = [
      "arn:aws:logs:${var.region}:*:log-group:/aws/lambda/${var.name_prefix}-dr-notify",
      "arn:aws:logs:${var.region}:*:log-group:/aws/lambda/${var.name_prefix}-dr-notify:*",
    ]
  }
}

resource "aws_iam_role_policy" "dr_notify" {
  name   = "${var.name_prefix}-dr-notify"
  role   = aws_iam_role.dr_notify.id
  policy = data.aws_iam_policy_document.dr_notify.json
}

resource "aws_cloudwatch_log_group" "dr_notify" {
  name              = "/aws/lambda/${var.name_prefix}-dr-notify"
  retention_in_days = 30
}

resource "aws_lambda_function" "dr_notify" {
  function_name    = "${var.name_prefix}-dr-notify"
  depends_on       = [aws_cloudwatch_log_group.dr_notify, aws_iam_role_policy.dr_notify]
  filename         = data.archive_file.dr_notify.output_path
  source_code_hash = data.archive_file.dr_notify.output_base64sha256
  handler          = "index.handler"
  runtime          = "python3.12"
  role             = aws_iam_role.dr_notify.arn
  timeout          = 30
  environment {
    variables = {
      WEBHOOK_SECRET_ARN = aws_secretsmanager_secret.discord_webhook.arn
    }
  }
}

# ══ [1]~[13] 메인 상태 머신 — 페일오버 본체 (T5) ══════════════════════════════
#
# 상태 이름은 설계 §5 표 그대로다. **지어내지 말 것** — 런북·스펙·이 파일이 이 이름으로 서로를 참조하고,
# 아래 dr_failover_tasks 목록이 그 이름으로 실패 경로를 생성한다.

locals {
  # [4] 가 올릴 hot 노드 수. **단일 출처**다(잔여 #6) — UpdateNodegroup 의 DesiredSize 와
  # 04-wait-nodes-ready.sh 의 WANT_NODES env 가 **둘 다 여기서** 나온다. 한쪽만 바꾸는 게 불가능해진다.
  # (var.eks_dr_node_desired 는 노드그룹 **생성 시점** 값 0 이다 — 모듈이 이후 desired 를 ignore_changes
  #  하므로 스케일은 terraform 이 아니라 [4] 가 한다. buildspec 이 -var eks_dr_node_desired=0 을 넘기는 이유.)
  dr_hot_node_desired = 3

  # ── 실패 경로 ① 어느 단계인가 ──
  #
  # 🔴 **$.error.Error 를 쓰지 않는다.** `.sync:2` 가 자식 Error 를 감싸 **States.TaskFailed** 가 오고,
  #   `.sync` 를 쓰는 모든 상태가 같은 값이다(스펙 §11.18 (b) 실측). 이전 구현은 그걸 모르고
  #   notify 에 allowlist 를 뒀다가 **100% dead code** 가 됐다(§11.18 (c)).
  # → 각 Task 의 Catch 가 **자기 이름을 static 으로** $.failedStep 에 주입한다. 파싱이 없어 States.Runtime
  #   위험이 0이고, 상태 타입(CodeBuild·Lambda·자식SM·SDK)과 무관하게 정확하다.
  #
  # ⚠️ **Task 상태만** 대상이다 — Choice·Wait 는 Catch 를 지원하지 않고, **Pass 는 스키마가 거부한다**
  #   ("Field 'Catch' is not supported", §11.18 (e) 실측).
  # ⚠️ 이 목록은 아래 States 맵의 Task 와 **정확히 일치**해야 한다 — 빠지면 그 상태의 실패가 Catch 없이
  #   실행을 죽여 **알림이 안 간다.** Step 3 의 대수 검증이 이 일치를 강제한다.
  dr_failover_tasks = [
    "RequestApproval", "TerraformApply", "ClearAlbParam", "ResolveBastion",
    "CleanWarmEtcd", "ScaleNodes", "UpdateNodegroup", "WaitNodesReady",
    "InstallAddons", "CheckAddons", "BootstrapApps", "RestoreVault",
    "RestoreData", "WaitAppsReady", "SwitchDNS", "RestartApps", "VerifyServing",
  ]

  # ⚠️ States.ALL 은 **단독**이어야 하고 **마지막** retrier/catcher 여야 한다(AWS 문서) — 다른 에러명과
  #   같이 쓰면 CreateStateMachine 이 정의를 거부한다.
  # ⚠️ States.DataLimitExceeded 를 **명시**한다 — States.ALL 이 안 잡는다(AWS 문서). 우리가 stdout 을
  #   CloudWatch 로 뺀 이유가 바로 256KB 상한이라, 그게 터졌을 때 무음이면 원인을 못 찾는다.
  # ⚠️ States.Runtime 은 **어떤 Catch 로도 못 잡는다** — 방어는 Step 4 의 구간별 실측뿐이다.
  dr_catch = { for s in local.dr_failover_tasks : s => [
    { ErrorEquals = ["States.DataLimitExceeded"], ResultPath = "$.error", Next = "${s}Failed" },
    { ErrorEquals = ["States.ALL"], ResultPath = "$.error", Next = "${s}Failed" },
  ] }

  dr_failed_states = { for s in local.dr_failover_tasks : "${s}Failed" => {
    Type       = "Pass"
    Result     = s
    ResultPath = "$.failedStep" # $.error(= Catch 가 넣은 {Error,Cause})는 그대로 보존된다
    Next       = "DnsSwitched?"
  } }

  # ── 실패 경로 ② 트래픽이 어디 있나 — 이름 추론이 아니라 페이로드 실물 ──
  dr_dns_states = {
    # SwitchDNS 가 ResultPath="$.dns" 라 **$.dns.alb 의 존재 ⟺ [10] 통과**다. 지상 진실이다.
    #
    # ⚠️ IsPresent 가드 필수 — [10] 전에 죽으면 그 경로가 **아예 없다.** 경로 없는 Variable 을 Choice 가
    #   어떻게 다루는지는 미확정이나 States.Runtime 이면 어떤 Catch 로도 못 잡는다 → IsPresent 를 먼저
    #   두면 어느 쪽이든 안전하다(자식 SM 의 AgentReady? 가 같은 이유로 같은 가드를 쓴다 — 선례).
    # ⚠️ **allowlist(failedStep in [RestartApps, VerifyServing])로 되돌아가지 말 것.** $.failedStep 이
    #   정확해졌으니 이름 판정도 "작동은" 한다. 그러나 [10]↔[11] 사이에 상태가 하나 끼는 순간 **조용히**
    #   틀리고, 그건 운영자가 트래픽 위치를 오판하는 것이다. IsPresent 는 안 틀린다.
    "DnsSwitched?" = {
      Type    = "Choice"
      Choices = [{ Variable = "$.dns.alb", IsPresent = true, Next = "MarkPostDns" }]
      Default = "MarkPreDns"
    }
    MarkPostDns = {
      Type       = "Pass"
      Result     = { dnsSwitched = true }
      ResultPath = "$.flags"
      Next       = "NotifyFailed"
    }
    # SwitchDNS **자체** 실패도 여기로 온다 — [10] 은 fail-closed(SSM ALB·WAF·존 3중 게이트)라
    # 검증에 실패하면 Route53 을 안 건드린다 → "온프렘"이 참이다.
    MarkPreDns = {
      Type       = "Pass"
      Result     = { dnsSwitched = false }
      ResultPath = "$.flags"
      Next       = "NotifyFailed"
    }
  }
}

resource "aws_sfn_state_machine" "dr_failover" {
  name     = "${var.name_prefix}-dr-failover"
  role_arn = aws_iam_role.dr_sfn.arn

  # 두 정책 다 명시 의존 — dr_approval_test 주석의 (1)(2) 와 같은 이유(병렬 생성 시 AccessDenied,
  # -target 재생성 시 정책 미동반). 이 SM 은 자식 SM 도 부르므로 dr_sfn_child 까지 필요하다.
  # 사이클 없음 — 이 SM 을 참조하는 정책이 없다(자식 SM 만 그 문제가 있었고 dr_sfn_child 로 분리했다).
  depends_on = [aws_iam_role_policy.dr_sfn, aws_iam_role_policy.dr_sfn_child]

  logging_configuration {
    log_destination = "${aws_cloudwatch_log_group.dr_sfn.arn}:*"
    # ⚠️ false 고정 — true 면 LambdaFunctionScheduled 의 input 에 **해석된 taskToken 이 평문**으로 남는다.
    # 토큰은 SendTaskSuccess 의 유일한 bearer 자격증명이라 로그 읽기 + states:SendTaskSuccess 만으로
    # 서명·허용목록·arming 3겹을 전부 우회할 수 있다(설계 §5.4). dr_approval_test 와 같은 값이다.
    include_execution_data = false
    level                  = "ALL"
  }

  definition = jsonencode({
    Comment = "DR 페일오버 [1]~[13] — 설계 §5"
    # 승인 대기 24h(86400) + 복구 예산. AddonsDone? 폴링 루프가 영영 안 끝나는 경우의 backstop 이다
    # (자식 SM 의 4200 과 별개로 부모에도 상한이 필요하다).
    # 🔴 초안 90000 은 "복구 ~1h(3600)" 로 잡았으나 실측 바운드 예산은 ~16500 이다: CodeBuild 1800(build_timeout=30m)
    #    + bastion .sync 8단계 14700(600+1200+1200+1800+3600+4800+900+600). 승인이 86400 을 다 쓰면 3600 만 남아
    #    정상 진행 중인 복구를 top-level States.Timeout 이 끊는데, 그건 **어떤 state Catch 도 못 잡아** 실패 알림
    #    없이 무음 FAILED 로 죽는다(NotifyFailed 는 per-state Catch 로만 도달). WaitAppsReady 와 같은 "표를 믿고
    #    실물을 안 셈" 패턴이었다(codex P2, 2026-07-16). → 86400 + 16500 + 애드온 루프·여유 = 108000.
    TimeoutSeconds = 108000
    StartAt        = "RequestApproval"

    States = merge({
      # ── [1] 승인 — Plan 1 의 approval-request 를 그대로 재사용 ──
      RequestApproval = {
        Type     = "Task"
        Resource = "arn:aws:states:::lambda:invoke.waitForTaskToken"
        Parameters = {
          FunctionName = aws_lambda_function.dr_approval_request.arn
          Payload = {
            "taskToken.$" = "$$.Task.Token"
            # ⚠️ "mode.$"="$.mode" 금지 — 실재해(failover-trigger)는 입력에 mode 를 안 넣어 그 JSONPath 가
            # 없고 States.Runtime 으로 즉시 죽는다. 전체를 넘기고 mode 판정은 Lambda 안에서 한다.
            "input.$" = "$"
          }
        }
        # [7] 이 쓸 스냅샷과 [13] 이 쓸 RTO 기준점만 남긴다.
        ResultSelector = { "snapshot.$" = "$.snapshot", "approvedAt.$" = "$.approvedAt" }
        ResultPath     = "$.approval"
        TimeoutSeconds = 86400 # DynamoDB TTL 과 일치
        Catch          = local.dr_catch["RequestApproval"]
        Next           = "TerraformApply"
      }

      # ── [2] hot 리소스 기동 (CodeBuild .sync) ──
      # ⚠️ SourceVersion 을 넘기지 않는다 → 프로젝트 기본값 **main**. 실재해는 검증된 main 을 돌린다.
      # ⚠️ Retry 를 붙이지 않는다 — buildspec 의 -lock-timeout=5m 이 사람↔빌드 락 충돌을 빌드 안에서
      #   흡수하고(잔여 #2), CodeBuild .sync 실패는 락이든 -var 누락이든 SFN 엔 같은 에러로 와서 구분이
      #   불가능하다. 모든 실패를 재시도하면 진짜 실패가 재해 중 30분 늘어진다(계획서 착수 전 결정 (2)).
      TerraformApply = {
        Type       = "Task"
        Resource   = "arn:aws:states:::codebuild:startBuild.sync"
        Parameters = { ProjectName = aws_codebuild_project.dr_failover_tf.name }
        ResultPath = null # Build 객체가 크다 — 페이로드에 안 싣는다(256KB 상한)
        Catch      = local.dr_catch["TerraformApply"]
        Next       = "ClearAlbParam"
      }

      # ── [2.4] stale ALB 파라미터 삭제 — 설계 §5.1.2 의 2중 방어 ① ──
      # [9] 가 쓰기 전에 항상 비운다. 안 비우면 [10] 이 **이전 사이클의 ALB** 로 DNS 를 넘길 수 있다
      # (P1d stale hostAlias 와 같은 버그 클래스).
      ClearAlbParam = {
        Type       = "Task"
        Resource   = "arn:aws:states:::aws-sdk:ssm:deleteParameter"
        Parameters = { Name = "/cledyu-dr/failover/alb-hostname" }
        # ✅ **에러명은 실측 확정**이다(스펙 §11.18 (a) — 버릴 SM 하나로 드릴 없이 쟀다):
        #      error = Ssm.ParameterNotFoundException  /  cause = "...error code ParameterNotFound..."
        #    SFN 의 SDK 통합은 **와이어 코드가 아니라 SDK 예외 클래스명**을 에러명으로 쓴다.
        #    (CLI 가 "An error occurred (ParameterNotFound)" 로 찍는 걸 근거로 이 이름이 틀렸다고 의심했으나
        #     실측이 반증했다 — 그럴듯한 간접 증거로 확정된 것을 뒤집을 때도 재는 게 먼저다.)
        # ⚠️ 이 에러**만** 삼킨다. AccessDenied 까지 삼키면 stale 방어가 조용히 죽는다.
        Catch = concat([{
          # 첫 failover 엔 파라미터가 없는 게 정상이다 → 없으면 무시하고 진행("없으면 무시", §5.1.2).
          ErrorEquals = ["Ssm.ParameterNotFoundException"]
          ResultPath  = null
          Next        = "ResolveBastion"
        }], local.dr_catch["ClearAlbParam"])
        ResultPath = null
        Next       = "ResolveBastion"
      }

      # ── [2.5] bastion instance id — CodeBuild 에서 받지 않는다(exported-variables 결합 회피, §5.1.1a) ──
      ResolveBastion = {
        Type     = "Task"
        Resource = "arn:aws:states:::aws-sdk:ec2:describeInstances"
        Parameters = {
          Filters = [
            { Name = "tag:Name", Values = ["${local.eks_dr_name}-bastion"] },
            # ⚠️ running 필터 필수 — user_data_replace_on_change=true 라 교체 시 옛 인스턴스가
            # shutting-down 으로 남는다. 없으면 죽어가는 id 를 집어 이후 SSM 이 전부 실패한다.
            { Name = "instance-state-name", Values = ["running"] },
          ]
        }
        ResultSelector = { "instanceId.$" = "$.Reservations[0].Instances[0].InstanceId" }
        ResultPath     = "$.bastion"
        Catch          = local.dr_catch["ResolveBastion"]
        Next           = "CleanWarmEtcd"
      }

      # ── [3] 이전 사이클 잔존물 정리 ──
      # ⚠️ **[4] 보다 먼저다.** warm etcd 는 사이클 간 살아남아 고아 ALB webhook 이 남는데, 노드를 먼저
      # 올리면 coredns 애드온이 그 webhook 때문에 CREATE_FAILED 로 죽는다(P1c, 7/14 드릴). 순서 바꾸지 말 것.
      CleanWarmEtcd = {
        Type     = "Task"
        Resource = "arn:aws:states:::states:startExecution.sync:2"
        Parameters = {
          StateMachineArn = aws_sfn_state_machine.dr_run_on_bastion.arn
          Input = {
            "instanceId.$" = "$.bastion.instanceId"
            script         = file("${path.module}/scripts/bastion/03-clean-warm-etcd.sh")
            # ⚠️ env 는 **항상** 채운다 — 자식 SM 의 BuildCommands 가 States.Array($.env, $.script) 를
            # 하므로 없으면 States.Runtime 으로 즉시 죽는다. ":" 는 셸 no-op.
            env            = ":"
            timeoutSeconds = 600 # 내부 대기: cloud-init wait
            label          = "CleanWarmEtcd"
          }
        }
        ResultPath = null
        Catch      = local.dr_catch["CleanWarmEtcd"]
        Next       = "ScaleNodes"
      }

      # ── [4] warm(desired 0) → hot ──
      # 노드그룹 이름을 하드코딩하지 않는다 — 모듈이 접미사를 붙이므로(실물: dr-2026071308012714040000000f)
      # 런타임 조회가 안전하다.
      ScaleNodes = {
        Type           = "Task"
        Resource       = "arn:aws:states:::aws-sdk:eks:listNodegroups"
        Parameters     = { ClusterName = local.eks_dr_name }
        ResultSelector = { "name.$" = "$.Nodegroups[0]" }
        ResultPath     = "$.ng"
        Catch          = local.dr_catch["ScaleNodes"]
        Next           = "UpdateNodegroup"
      }
      UpdateNodegroup = {
        Type     = "Task"
        Resource = "arn:aws:states:::aws-sdk:eks:updateNodegroupConfig"
        Parameters = {
          ClusterName       = local.eks_dr_name
          "NodegroupName.$" = "$.ng.name"
          # ⚠️ 모듈이 desired 를 ignore_changes 하므로 [2] 의 terraform 이 아니라 **여기서** 올린다.
          ScalingConfig = {
            MinSize     = 0
            MaxSize     = var.eks_dr_node_max
            DesiredSize = local.dr_hot_node_desired
          }
        }
        ResultPath = null
        # 🔴 초안은 여기서 WaitNodes/CheckNodes/NodesActive? 로 갔다. **그 3상태는 삭제했다** —
        #    Nodegroup.Status 게이트는 아무것도 안 거른다(스펙 §11.14, 2회 측정. §11.17 (e) 가 메커니즘을
        #    정정했으나 "게이트 무효" 결론은 그대로다: 축소 시 명령 38초 만에 ACTIVE 인데 EC2 는 3대였다).
        Catch = local.dr_catch["UpdateNodegroup"]
        Next  = "WaitNodesReady"
      }

      # ── [4.5] 노드가 **k8s 에 Ready** 인지 — 🆕 T4 실측이 만들어낸 검문소 ──
      # 목적은 "기다리기"가 아니라 **DEGRADED 의 뜻을 하나로 만드는 것**이다: 노드를 확실히 세운 뒤면
      # [5] check 가 보는 DEGRADED 는 "노드 없음"이 아니라 **진짜 고장(P1c)** 뿐이라 치명 판정이 옳아진다.
      # SFN 은 private EKS 에 못 닿고 ASG InService 는 kubelet 조인이 아니다 → 클러스터 안의 bastion 만 안다.
      WaitNodesReady = {
        Type     = "Task"
        Resource = "arn:aws:states:::states:startExecution.sync:2"
        Parameters = {
          StateMachineArn = aws_sfn_state_machine.dr_run_on_bastion.arn
          Input = {
            "instanceId.$" = "$.bastion.instanceId"
            script         = file("${path.module}/scripts/bastion/04-wait-nodes-ready.sh")
            # 잔여 #6 — 04 가 기다릴 대수를 **[4] 가 명령한 그 숫자**로 준다(위 local 단일 출처).
            # 하드코딩 대조가 아니라서 한쪽만 바뀌어 조용히 어긋나는 일이 없다.
            env = "export WANT_NODES=${local.dr_hot_node_desired}"
            # 내부 합 900 = 노드 등장 루프 300 + Ready wait 600 → +300
            # 🔴 초안은 900 이었고 주석이 "노드 등장 600 + Ready wait 600(**직렬 아님**, 여유)" 였다.
            #    **직렬이 맞다**(루프로 등장을 기다린 뒤 wait 한다) → 실제 합 1200 > 선언 900 이었다.
            #    스스로 "직렬 아님" 이라 합리화해놓고 넘어간 것이다. 등장 루프를 300 으로 줄여 해소.
            timeoutSeconds = 1200
            label          = "WaitNodesReady"
          }
        }
        ResultPath = null
        Catch      = local.dr_catch["WaitNodesReady"]
        Next       = "InstallAddons"
      }

      # ── [5] 애드온 멱등 설치 — Lambda 는 **시작만**, ACTIVE 대기는 아래 폴링(900s 상한 회피) ──
      InstallAddons = {
        Type     = "Task"
        Resource = "arn:aws:states:::lambda:invoke"
        Parameters = {
          FunctionName = aws_lambda_function.dr_addon_install.arn
          # ⚠️ action 필수 — 없으면 check 경로로 빠져 미설치 상태에서 ResourceNotFoundException 으로 죽는다.
          Payload = { action = "start" }
        }
        ResultPath = null
        Catch      = local.dr_catch["InstallAddons"]
        Next       = "WaitAddons"
      }
      WaitAddons = {
        Type    = "Wait"
        Seconds = 20
        Next    = "CheckAddons"
      }
      CheckAddons = {
        Type     = "Task"
        Resource = "arn:aws:states:::lambda:invoke"
        Parameters = {
          FunctionName = aws_lambda_function.dr_addon_install.arn
          Payload      = { action = "check" }
        }
        ResultSelector = { "done.$" = "$.Payload.done" }
        ResultPath     = "$.addons"
        Catch          = local.dr_catch["CheckAddons"]
        Next           = "AddonsDone?"
      }
      # 이 루프는 **진짜 게이트**다(§11.14 (c) 실측: done=true 가 노드 Ready +70s — CREATING 을 제대로 기다렸다).
      # 상한은 SM 의 TimeoutSeconds(90000)이 잡는다. check 가 CREATE_FAILED/DEGRADED 에 raise 하므로
      # 진짜 고장이면 여기서 매달리지 않고 [5] 의 Catch 로 빠진다.
      "AddonsDone?" = {
        Type = "Choice"
        Choices = [{
          Variable      = "$.addons.done"
          BooleanEquals = true
          Next          = "BootstrapApps"
        }]
        Default = "WaitAddons"
      }

      # ── [6] ArgoCD·GitOps 부트스트랩 ──
      BootstrapApps = {
        Type     = "Task"
        Resource = "arn:aws:states:::states:startExecution.sync:2"
        Parameters = {
          StateMachineArn = aws_sfn_state_machine.dr_run_on_bastion.arn
          Input = {
            "instanceId.$" = "$.bastion.instanceId"
            script         = file("${path.module}/scripts/bastion/06-bootstrap-apps.sh")
            env            = ":"
            timeoutSeconds = 1200 # 내부: rollout 300 + wait 300 → ~2× 여유
            label          = "BootstrapApps"
          }
        }
        ResultPath = null
        Catch      = local.dr_catch["BootstrapApps"]
        Next       = "RestoreVault"
      }

      # ── [7] Vault 복원 — **승인 때 고른 스냅샷**을 주입한다(드롭다운의 존재 이유) ──
      RestoreVault = {
        Type     = "Task"
        Resource = "arn:aws:states:::states:startExecution.sync:2"
        Parameters = {
          StateMachineArn = aws_sfn_state_machine.dr_run_on_bastion.arn
          Input = {
            "instanceId.$" = "$.bastion.instanceId"
            script         = file("${path.module}/scripts/bastion/07-restore-vault.sh")
            # ⚠️ **스크립트 본문과 문자열 조립을 하지 않는다**(리뷰 C1). States.Format 에 07 전문을 넣으면
            #   (a) sh -c '...' 의 작은따옴표가 intrinsic 리터럴을 끊고 (b) { echo; exit 1; } 의 중괄호가
            #   플레이스홀더로 읽히며 (c) \n 이 인자에 못 들어간다 → **CreateStateMachine 이 정의를 거부**해
            #   apply 가 깨지고, terraform validate 는 이걸 못 잡는다.
            # → env 를 **별도 필드**로 넘기고 자식 SM 이 commands 배열의 두 원소로 싣는다.
            #   States.Format 은 **스냅샷 키에만** 쓴다 — S3 키엔 따옴표·중괄호·개행이 없어 안전하다.
            "env.$"        = "States.Format('export SNAPSHOT_KEY={}', $.approval.snapshot)"
            timeoutSeconds = 1800 # 내부: restore + generate-root + ESO rollout 120
            label          = "RestoreVault"
          }
        }
        ResultPath = null
        Catch      = local.dr_catch["RestoreVault"]
        Next       = "RestoreData"
      }

      # ── [8] CNPG 복원 (구 CR 제거 → ArgoCD 재생성 → bootstrap.recovery 가 S3 에서) ──
      RestoreData = {
        Type     = "Task"
        Resource = "arn:aws:states:::states:startExecution.sync:2"
        Parameters = {
          StateMachineArn = aws_sfn_state_machine.dr_run_on_bastion.arn
          Input = {
            "instanceId.$" = "$.bastion.instanceId"
            script         = file("${path.module}/scripts/bastion/08-restore-data.sh")
            env            = ":"
            # ⚠️ 내부 대기 합(최악) 3000 = ArgoCD 재생성 600 + 1200 + 1200 → +600 여유.
            #   **스크립트 내부 wait 합보다 커야 한다** — 초안은 1800 이라 느리지만 정상인 복원을 SSM 이 죽였다.
            timeoutSeconds = 3600
            label          = "RestoreData"
          }
        }
        ResultPath = null
        Catch      = local.dr_catch["RestoreData"]
        Next       = "WaitAppsReady"
      }

      # ── [9] 앱 Ready 대기 + [10] 이 쓸 ALB 호스트명 기록 ──
      # ⚠️ [9]→[10] 순서는 강제다(런북 명시): auth 는 Keycloak Ready 이후에만 넘긴다 — 조기 전환 시
      # ALB keycloak 타겟이 unhealthy 라 404/503 이 뜬다.
      WaitAppsReady = {
        Type     = "Task"
        Resource = "arn:aws:states:::states:startExecution.sync:2"
        Parameters = {
          StateMachineArn = aws_sfn_state_machine.dr_run_on_bastion.arn
          Input = {
            "instanceId.$" = "$.bastion.instanceId"
            script         = file("${path.module}/scripts/bastion/09-wait-apps-ready.sh")
            env            = ":"
            # 내부 합 4200 = 존재게이트 4×300 + kafka Ready 900 + topic 존재 300 + topic Ready 300
            #              + VE rollout 600 + KC Ready 600 + ALB 300  → +600
            #
            # 🔴 **초안은 3000 이고 주석이 "내부 합 2400" 이었는데 둘 다 틀렸다**(codex P2, 2026-07-16).
            #    실제 합은 **5700** 이었다 — `kubectl wait --timeout` 4개만 세고 **존재 게이트 5개
            #    (3000초)를 통째로 빠뜨렸다.** 원인: 계획서 표의 2400 을 베꼈는데 그 표는 존재 게이트가
            #    추가된 `79e9605`(§11.16 (b)) **이전**에 쓰인 것이다. 표를 믿고 실물을 안 셌다 —
            #    notify allowlist(§11.18 (c))와 **같은 실패 패턴**이다.
            #    → 존재 게이트를 600→300 으로 낮춰 합을 4200 으로 줄이고(09 주석 참조) 선언을 4800 으로 올렸다.
            timeoutSeconds = 4800
            label          = "WaitAppsReady"
          }
        }
        ResultPath = null
        Catch      = local.dr_catch["WaitAppsReady"]
        Next       = "SwitchDNS"
      }

      # ── [10] DNS 전환 (fail-closed) ──
      # ⚠️ ResultPath=null 금지 — alb 를 [13] 에 넘겨야 하고, **$.dns 의 존재가 실패 경로에서 "DNS 가
      #   넘어갔나"의 지상 진실**이 된다(위 dr_dns_states).
      SwitchDNS = {
        Type           = "Task"
        Resource       = "arn:aws:states:::lambda:invoke"
        Parameters     = { FunctionName = aws_lambda_function.dr_dns_switch.arn }
        ResultSelector = { "alb.$" = "$.Payload.alb" }
        ResultPath     = "$.dns"
        Catch          = local.dr_catch["SwitchDNS"]
        Next           = "RestartApps"
      }

      # ── [11] 앱 재기동 (startup 1회 초기화 → 스스로 복구 안 함) ──
      RestartApps = {
        Type     = "Task"
        Resource = "arn:aws:states:::states:startExecution.sync:2"
        Parameters = {
          StateMachineArn = aws_sfn_state_machine.dr_run_on_bastion.arn
          Input = {
            "instanceId.$" = "$.bastion.instanceId"
            script         = file("${path.module}/scripts/bastion/11-restart-apps.sh")
            env            = ":"
            timeoutSeconds = 900 # 내부: rollout 300×2 = 600 → +300
            label          = "RestartApps"
          }
        }
        ResultPath = null
        Catch      = local.dr_catch["RestartApps"]
        Next       = "VerifyServing"
      }

      # ── [12] 복원본이 실제로 서빙되는지 — 자격증명을 쓰지 않는다(설계 §5.1.4) ──
      # **페일오버 전체의 마지막 게이트**다. 여기서 잘못 죽으면 완벽히 복구된 DR 이 ❌ 실패 알림을 보낸다.
      VerifyServing = {
        Type     = "Task"
        Resource = "arn:aws:states:::states:startExecution.sync:2"
        Parameters = {
          StateMachineArn = aws_sfn_state_machine.dr_run_on_bastion.arn
          Input = {
            "instanceId.$" = "$.bastion.instanceId"
            script         = file("${path.module}/scripts/bastion/12-verify-serving.sh")
            env            = ":"
            timeoutSeconds = 600 # 내부: curl 재시도 30×10s = 300 + psql → +300
            label          = "VerifyServing"
          }
        }
        ResultPath = null
        Catch      = local.dr_catch["VerifyServing"]
        Next       = "MarkFailoverActive"
      }

      # ── [12.5] failover 활성 플래그 — failback 트리거의 게이트 ──
      # VerifyServing 통과(= failover 정상 완료) 후에만 세팅. failback-trigger 가 이 파라미터가
      # 있을 때만 발화 → 부분 실패·평상시 하트비트 깜빡임이 failback 을 유발하지 않는다.
      # dr_failback SFN 의 ClearFlags 가 failback 완료 시 삭제한다.
      MarkFailoverActive = {
        Type     = "Task"
        Resource = "arn:aws:states:::aws-sdk:ssm:putParameter"
        Parameters = {
          Name      = "/cledyu-dr/failover/active"
          "Value.$" = "$$.Execution.Id"
          Type      = "String"
          Overwrite = true
        }
        ResultPath = null
        # 플래그 세팅 실패로 완료 알림을 막지 않는다 — failover 는 이미 성공. 로깅만.
        # ⚠️ 회복 레이스(failover 중 온프렘이 이미 OK 복귀 → dr_recovery 의 OK 이벤트가 active 설정 전에
        #   지나가 자동 failback 을 놓침)는 **여기서 크로스리전 재확인하지 않는다** — Step Functions 는
        #   크로스리전 리소스 접근을 지원하지 않아(us-east-1 failback-trigger 를 ap-northeast-2 SFN 에서
        #   직접 invoke 불가) 어떤 ARN 형식이든 실패한다(2026-07-18 리뷰 P2 재지적). 대신 us-east-1 **로컬**
        #   주기 reconcile 규칙(dr_failback_reconcile, dr-failback.tf)이 active+push OK+RUNNING없음을 보고
        #   재개하며, 이 회복-레이스 케이스도 그 경로가 커버한다(즉시 대신 최대 reconcile 주기 내).
        Catch = [{ ErrorEquals = ["States.ALL"], ResultPath = null, Next = "NotifyComplete" }]
        Next  = "NotifyComplete"
      }

      # ── [13] 완료 알림 ──
      NotifyComplete = {
        Type     = "Task"
        Resource = "arn:aws:states:::lambda:invoke"
        Parameters = {
          FunctionName = aws_lambda_function.dr_notify.arn
          Payload = {
            outcome = "success"
            # ⚠️ $.detail.state.timestamp(알람 이벤트)가 **아니라** $$.Execution.StartTime 이다:
            #   (a) 실행 시작 = 알람→EventBridge→trigger→StartExecution 이라 감지와 몇 초 차 (RTO 보고엔 충분)
            #   (b) $.detail 은 **테스트 실행({"mode":"test"})에 없어** States.Runtime 이 나고, 그건 어떤
            #       Catch 로도 못 잡는다
            #   (c) 컨텍스트 객체는 **항상** 있다 → 실재해·드릴·테스트가 같은 경로를 탄다(C2 의 교훈)
            "detectedAt.$" = "$$.Execution.StartTime"
            "approvedAt.$" = "$.approval.approvedAt"
            "alb.$"        = "$.dns.alb"
          }
        }
        # 잔여 #4 — Discord 429/장애로 성공 알림이 유실되는 것을 막는다. urlopen 은 429·5xx 에
        # HTTPError 를 던지므로 Lambda 가 실패하고 여기서 재시도된다(Python 변경 0).
        Retry = [{
          ErrorEquals     = ["States.ALL"]
          IntervalSeconds = 5
          MaxAttempts     = 3
          BackoffRate     = 2.0
        }]
        # 🔴 **Catch 를 달지 않는다.** NotifyFailed 로 보내면 **13단계를 다 성공한 페일오버에 "❌ 실패"
        #    알림**이 간다 — C2 가 정확히 그 버그였다. 재시도가 소진되면 실행을 FAILED 로 끝내고,
        #    콘솔·CloudWatch 의 FAILED 가 "알림이 왜 안 왔나"의 단서로 남는다(설계 결정, 스펙 §11.18 (j)).
        End = true
      }

      # ── 모든 Catch 의 종착 — 롤백하지 않는다(설계 §5.3) ──
      # 재해 중엔 부분 완성이 0보다 낫고, 자동 롤백은 사람이 손댈 발판까지 치운다.
      NotifyFailed = {
        Type     = "Task"
        Resource = "arn:aws:states:::lambda:invoke"
        Parameters = {
          FunctionName = aws_lambda_function.dr_notify.arn
          Payload = {
            outcome = "failed"
            # ⚠️ $.approval·$.dns 를 **직접 참조하지 않는다** — [1]/[2] 에서 실패하면 그 경로가 아직 없어
            #   States.Runtime 이 나고 **실패 알림 자체가 무음으로 죽는다.**
            #   아래 셋은 실패 경로(<X>Failed → DnsSwitched? → Mark*)가 **항상** 채워준다.
            "failedState.$" = "$.failedStep"        # 진짜 상태 이름 (≠ States.TaskFailed)
            "dnsSwitched.$" = "$.flags.dnsSwitched" # $.dns.alb IsPresent 로 판정한 지상 진실
            # Cause 는 **날것 그대로**. 파싱은 notify(Python)의 try/except 가 한다 —
            # ASL 의 States.StringToJson 은 평문 Cause 에 States.Runtime 이고 Pass 는 Catch 를 못 단다
            # (스키마 거부) → 실패 경로에서 실패 = 무음(스펙 §11.18 (e) 재현).
            "stdoutTail.$"   = "$.error.Cause"
            "executionArn.$" = "$$.Execution.Id"
          }
        }
        Retry = [{
          ErrorEquals     = ["States.ALL"]
          IntervalSeconds = 5
          MaxAttempts     = 3
          BackoffRate     = 2.0
        }]
        # 재시도가 소진돼도 Fail 상태엔 도달시킨다 — 그래야 실행이 의도한 Error 로 끝난다.
        # (NotifyComplete 와 달리 여기선 Catch 가 안전하다 — 이미 실패 경로다.)
        Catch = [{
          ErrorEquals = ["States.ALL"]
          ResultPath  = null
          Next        = "Failed"
        }]
        Next = "Failed"
      }

      Failed = {
        Type  = "Fail"
        Error = "DrFailoverFailed"
        Cause = "페일오버 실패 — Discord 알림과 실행 이력 참조. 롤백하지 않았다(설계 §5.3)."
      }
    }, local.dr_failed_states, local.dr_dns_states)
  })
}
