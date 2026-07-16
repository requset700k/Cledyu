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
    actions   = ["ssm:DeleteParameter"]
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
    # 가장 긴 스크립트(08=3600) + 폴링 여유. 초과 시 States.Timeout → 부모의 Catch 가 잡는다.
    TimeoutSeconds = 4200
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

      Failed = {
        Type  = "Fail"
        Error = "BastionScriptFailed"
        Cause = "SSM 명령 실패 — 자식 SM 실행 이력의 GetResult 결과에서 commandId 를 찾아 CloudWatch 로그그룹 ${aws_cloudwatch_log_group.dr_bastion_commands.name} 에서 전문 확인: aws logs tail <그룹> --log-stream-name-prefix <commandId>"
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
    sid     = "CheckWaf"
    actions = ["wafv2:GetWebACLForResource"]
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
