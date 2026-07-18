# ═══════════════════════════════════════════════════════════════════════════
# DR Failback 오케스트레이션 — failover(dr-orchestration.tf)의 정반대.
# 트리거: push 하트비트 OK 복귀 + /cledyu-dr/failover/active 게이트.
# 실행: 승인 → DNS 원복 → DR 데이터 폐기 → 노드0 → hot teardown → 플래그 클리어 → 알림.
# ═══════════════════════════════════════════════════════════════════════════

# ── failback-trigger Lambda (us-east-1, push OK 이벤트 수신) ──
data "archive_file" "dr_failback_trigger" {
  type        = "zip"
  source_file = "${path.module}/dr-orchestration-lambda/failback-trigger/index.py"
  output_path = "${path.module}/dr-orchestration-lambda/failback-trigger/failback-trigger.zip"
}

resource "aws_iam_role" "dr_failback_trigger" {
  name               = "${var.name_prefix}-dr-failback-trigger"
  assume_role_policy = data.aws_iam_policy_document.dr_lambda_assume.json
}

data "aws_iam_policy_document" "dr_failback_trigger" {
  statement {
    sid       = "ReadActiveFlag"
    actions   = ["ssm:GetParameter"]
    resources = ["arn:aws:ssm:${var.region}:${data.aws_caller_identity.current.account_id}:parameter/cledyu-dr/failover/active"]
  }
  statement {
    sid       = "StartFailback"
    actions   = ["states:StartExecution"]
    resources = [aws_sfn_state_machine.dr_failback.arn]
  }
  statement {
    sid     = "Logs"
    actions = ["logs:CreateLogStream", "logs:PutLogEvents"]
    resources = [
      "arn:aws:logs:us-east-1:*:log-group:/aws/lambda/${var.name_prefix}-dr-failback-trigger",
      "arn:aws:logs:us-east-1:*:log-group:/aws/lambda/${var.name_prefix}-dr-failback-trigger:*",
    ]
  }
}

resource "aws_iam_role_policy" "dr_failback_trigger" {
  name   = "${var.name_prefix}-dr-failback-trigger"
  role   = aws_iam_role.dr_failback_trigger.id
  policy = data.aws_iam_policy_document.dr_failback_trigger.json
}

resource "aws_cloudwatch_log_group" "dr_failback_trigger" {
  provider          = aws.use1
  name              = "/aws/lambda/${var.name_prefix}-dr-failback-trigger"
  retention_in_days = 30
}

resource "aws_lambda_function" "dr_failback_trigger" {
  provider         = aws.use1 # push 알람 이벤트가 us-east-1
  depends_on       = [aws_cloudwatch_log_group.dr_failback_trigger, aws_iam_role_policy.dr_failback_trigger]
  function_name    = "${var.name_prefix}-dr-failback-trigger"
  filename         = data.archive_file.dr_failback_trigger.output_path
  source_code_hash = data.archive_file.dr_failback_trigger.output_base64sha256
  handler          = "index.handler"
  runtime          = "python3.12"
  role             = aws_iam_role.dr_failback_trigger.arn
  timeout          = 30
  environment {
    variables = {
      SFN_REGION        = var.region
      STATE_MACHINE_ARN = aws_sfn_state_machine.dr_failback.arn
      ACTIVE_PARAM      = "/cledyu-dr/failover/active"
    }
  }
}

# ── dns-revert Lambda (ap-northeast-2) ──
data "archive_file" "dr_dns_revert" {
  type        = "zip"
  source_file = "${path.module}/dr-orchestration-lambda/dns-revert/index.py"
  output_path = "${path.module}/dr-orchestration-lambda/dns-revert/dns-revert.zip"
}

resource "aws_iam_role" "dr_dns_revert" {
  name               = "${var.name_prefix}-dr-dns-revert"
  assume_role_policy = data.aws_iam_policy_document.dr_lambda_assume.json
}

data "aws_iam_policy_document" "dr_dns_revert" {
  statement {
    sid       = "DescribeAlb"
    actions   = ["elasticloadbalancing:DescribeLoadBalancers", "elasticloadbalancing:DescribeTargetGroups", "elasticloadbalancing:DescribeTargetHealth"]
    resources = ["*"] # Describe* 는 리소스 한정 미지원(AWS 문서)
  }
  statement {
    sid       = "ListZones"
    actions   = ["route53:ListHostedZonesByName"]
    resources = ["*"]
  }
  statement {
    sid       = "ChangeRecords"
    actions   = ["route53:ChangeResourceRecordSets"]
    resources = ["arn:aws:route53:::hostedzone/*"]
  }
  statement {
    sid     = "Logs"
    actions = ["logs:CreateLogStream", "logs:PutLogEvents"]
    resources = [
      "arn:aws:logs:${var.region}:*:log-group:/aws/lambda/${var.name_prefix}-dr-dns-revert",
      "arn:aws:logs:${var.region}:*:log-group:/aws/lambda/${var.name_prefix}-dr-dns-revert:*",
    ]
  }
}

resource "aws_iam_role_policy" "dr_dns_revert" {
  name   = "${var.name_prefix}-dr-dns-revert"
  role   = aws_iam_role.dr_dns_revert.id
  policy = data.aws_iam_policy_document.dr_dns_revert.json
}

resource "aws_cloudwatch_log_group" "dr_dns_revert" {
  name              = "/aws/lambda/${var.name_prefix}-dr-dns-revert"
  retention_in_days = 30
}

resource "aws_lambda_function" "dr_dns_revert" {
  depends_on       = [aws_cloudwatch_log_group.dr_dns_revert, aws_iam_role_policy.dr_dns_revert]
  function_name    = "${var.name_prefix}-dr-dns-revert"
  filename         = data.archive_file.dr_dns_revert.output_path
  source_code_hash = data.archive_file.dr_dns_revert.output_base64sha256
  handler          = "index.handler"
  runtime          = "python3.12"
  role             = aws_iam_role.dr_dns_revert.arn
  timeout          = 60
  environment {
    # 온프렘 공개 ALB 정확 이름(리전 내 유일) — 접미 매칭 오-선택 방지
    variables = { PUBLIC_ALB_NAME = "${var.name_prefix}-public" }
  }
}

# ── CleanupOrphans Lambda (ap-northeast-2): 노드 종료 + AWS 레벨 고아 삭제 ──
data "archive_file" "dr_teardown_cleanup" {
  type        = "zip"
  source_file = "${path.module}/dr-orchestration-lambda/teardown-cleanup/index.py"
  output_path = "${path.module}/dr-orchestration-lambda/teardown-cleanup/teardown-cleanup.zip"
}

resource "aws_iam_role" "dr_teardown_cleanup" {
  name               = "${var.name_prefix}-dr-teardown-cleanup"
  assume_role_policy = data.aws_iam_policy_document.dr_lambda_assume.json
}

data "aws_iam_policy_document" "dr_teardown_cleanup" {
  statement {
    sid = "DiscoverAndDelete"
    actions = [
      "eks:DescribeCluster",
      "ec2:DescribeInstances", "ec2:TerminateInstances",
      "ec2:DescribeVolumes", "ec2:DeleteVolume",
      "ec2:DescribeNetworkInterfaces", "ec2:DeleteNetworkInterface",
      "ec2:DescribeSecurityGroups", "ec2:DeleteSecurityGroup",
      "ec2:DescribeVpcEndpoints", "ec2:DeleteVpcEndpoints",
      "elasticloadbalancing:DescribeLoadBalancers", "elasticloadbalancing:DescribeTargetGroups",
      "elasticloadbalancing:DeleteLoadBalancer", "elasticloadbalancing:DeleteTargetGroup",
    ]
    resources = ["*"] # 대상은 DR VPC 필터로 코드에서 한정(Describe/Delete 리소스레벨 제약)
  }
  statement {
    sid     = "Logs"
    actions = ["logs:CreateLogStream", "logs:PutLogEvents"]
    resources = [
      "arn:aws:logs:${var.region}:*:log-group:/aws/lambda/${var.name_prefix}-dr-teardown-cleanup",
      "arn:aws:logs:${var.region}:*:log-group:/aws/lambda/${var.name_prefix}-dr-teardown-cleanup:*",
    ]
  }
}

resource "aws_iam_role_policy" "dr_teardown_cleanup" {
  name   = "${var.name_prefix}-dr-teardown-cleanup"
  role   = aws_iam_role.dr_teardown_cleanup.id
  policy = data.aws_iam_policy_document.dr_teardown_cleanup.json
}

resource "aws_cloudwatch_log_group" "dr_teardown_cleanup" {
  name              = "/aws/lambda/${var.name_prefix}-dr-teardown-cleanup"
  retention_in_days = 30
}

resource "aws_lambda_function" "dr_teardown_cleanup" {
  depends_on       = [aws_cloudwatch_log_group.dr_teardown_cleanup, aws_iam_role_policy.dr_teardown_cleanup]
  function_name    = "${var.name_prefix}-dr-teardown-cleanup"
  filename         = data.archive_file.dr_teardown_cleanup.output_path
  source_code_hash = data.archive_file.dr_teardown_cleanup.output_base64sha256
  handler          = "index.handler"
  runtime          = "python3.12"
  role             = aws_iam_role.dr_teardown_cleanup.arn
  timeout          = 600 # [R4] 노드 종료 + 볼륨 available 대기(내부 폴링) 흡수
  environment {
    variables = { CLUSTER_NAME = local.eks_dr_name }
  }
}

# ── dr_failback SFN 실행 롤 ──
resource "aws_iam_role" "dr_failback_sfn" {
  name = "${var.name_prefix}-dr-failback-sfn"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "states.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

data "aws_iam_policy_document" "dr_failback_sfn" {
  statement {
    sid     = "InvokeLambdas"
    actions = ["lambda:InvokeFunction"]
    resources = [
      aws_lambda_function.dr_approval_request.arn,
      aws_lambda_function.dr_dns_revert.arn,
      aws_lambda_function.dr_teardown_cleanup.arn, # approach B: 노드종료+고아정리
      aws_lambda_function.dr_notify.arn,
    ]
  }
  statement {
    sid       = "Teardown"
    actions   = ["codebuild:StartBuild", "codebuild:BatchGetBuilds", "codebuild:StopBuild"]
    resources = [aws_codebuild_project.dr_failover_tf.arn]
  }
  statement {
    sid       = "ScaleToZero" # SFN 이 노드그룹 desired 0(강제종료는 cleanup Lambda 가 함)
    actions   = ["eks:ListNodegroups", "eks:UpdateNodegroupConfig", "eks:DescribeNodegroup", "eks:DescribeUpdate"]
    resources = ["*"]
  }
  statement {
    sid       = "VerifyOrphans" # [R8] 잔존 ALB/EBS 확인
    actions   = ["elasticloadbalancing:DescribeLoadBalancers", "ec2:DescribeVolumes"]
    resources = ["*"]
  }
  statement {
    sid       = "ClearFlags"
    actions   = ["ssm:DeleteParameter", "ssm:DeleteParameters"]
    resources = ["arn:aws:ssm:${var.region}:${data.aws_caller_identity.current.account_id}:parameter/cledyu-dr/failover/*"]
  }
  # .sync(codebuild) 통합용 EventBridge 관리형 규칙
  statement {
    sid       = "SyncRules"
    actions   = ["events:PutTargets", "events:PutRule", "events:DescribeRule"]
    resources = ["arn:aws:events:${var.region}:${data.aws_caller_identity.current.account_id}:rule/StepFunctions*"]
  }
  # logging_configuration(vended logs) 필수 — 없으면 CreateStateMachine 이 AccessDenied
  # "not authorized to access the Log Destination" 로 실패한다(dr_sfn 롤과 동일). 리소스 한정 미지원.
  statement {
    sid = "Logs"
    actions = [
      "logs:CreateLogDelivery", "logs:GetLogDelivery", "logs:UpdateLogDelivery",
      "logs:DeleteLogDelivery", "logs:ListLogDeliveries", "logs:PutResourcePolicy",
      "logs:DescribeResourcePolicies", "logs:DescribeLogGroups",
    ]
    resources = ["*"]
  }
}

resource "aws_iam_role_policy" "dr_failback_sfn" {
  name   = "${var.name_prefix}-dr-failback-sfn"
  role   = aws_iam_role.dr_failback_sfn.id
  policy = data.aws_iam_policy_document.dr_failback_sfn.json
}

resource "aws_cloudwatch_log_group" "dr_failback_sfn" {
  name              = "/aws/vendedlogs/states/${var.name_prefix}-dr-failback"
  retention_in_days = 30
}

locals {
  # 각 상태 Catch → NotifyFailbackFailed. failedState 를 static 주입(failover dr_catch 패턴).
  fb_catch = { for s in ["RevertDNS", "ListNodegroup", "ScaleToZero", "CleanupOrphans", "TeardownHot", "VerifyNoOrphans", "ClearFlags"] :
    s => [{
      ErrorEquals = ["States.ALL"]
      ResultPath  = "$.error"
      Next        = "Mark_${s}_Failed"
    }]
  }
}

resource "aws_sfn_state_machine" "dr_failback" {
  name       = "${var.name_prefix}-dr-failback"
  role_arn   = aws_iam_role.dr_failback_sfn.arn
  depends_on = [aws_iam_role_policy.dr_failback_sfn]

  logging_configuration {
    log_destination        = "${aws_cloudwatch_log_group.dr_failback_sfn.arn}:*"
    include_execution_data = false
    level                  = "ALL"
  }

  definition = jsonencode({
    Comment = "DR failback(approach B) — 승인→DNS원복→노드0→AWS레벨 고아정리(ALB·EBS·ENI·GuardDuty)→hot teardown→고아검증→플래그클리어→알림"
    StartAt = "RequestApproval"
    States = merge({
      # [1] 승인(approval-request 재사용, mode=failback → 단일 버튼)
      RequestApproval = {
        Type     = "Task"
        Resource = "arn:aws:states:::lambda:invoke.waitForTaskToken"
        Parameters = {
          FunctionName = aws_lambda_function.dr_approval_request.arn
          Payload = {
            "taskToken.$" = "$$.Task.Token"
            "input.$"     = "$"
          }
        }
        ResultPath = "$.approval"
        Catch      = [{ ErrorEquals = ["States.ALL"], ResultPath = "$.error", Next = "Mark_RequestApproval_Failed" }]
        Next       = "RevertDNS"
      }

      # [2] DNS 원복(→온프렘 *-public ALB) — 맨 앞. 트래픽부터 온프렘으로.
      RevertDNS = {
        Type           = "Task"
        Resource       = "arn:aws:states:::lambda:invoke"
        Parameters     = { FunctionName = aws_lambda_function.dr_dns_revert.arn }
        ResultSelector = { "alb.$" = "$.Payload.alb" }
        ResultPath     = "$.dns"
        Catch          = local.fb_catch["RevertDNS"]
        Next           = "ListNodegroup"
      }

      # [3] 노드그룹 이름 발견(모듈이 이름 변형 가능 → 하드코딩 금지, failover ScaleNodes 미러)
      ListNodegroup = {
        Type           = "Task"
        Resource       = "arn:aws:states:::aws-sdk:eks:listNodegroups"
        Parameters     = { ClusterName = local.eks_dr_name }
        ResultSelector = { "name.$" = "$.Nodegroups[0]" }
        ResultPath     = "$.ng"
        Catch          = local.fb_catch["ListNodegroup"]
        Next           = "ScaleToZero"
      }

      # [4] 노드그룹 desired 0 (강제종료는 CleanupOrphans Lambda 가 함 → ASG 재생성 방지 위해 먼저)
      ScaleToZero = {
        Type     = "Task"
        Resource = "arn:aws:states:::aws-sdk:eks:updateNodegroupConfig"
        Parameters = {
          ClusterName       = local.eks_dr_name
          "NodegroupName.$" = "$.ng.name"
          ScalingConfig     = { MinSize = 0, MaxSize = var.eks_dr_node_max, DesiredSize = 0 }
        }
        ResultPath = null
        Catch      = local.fb_catch["ScaleToZero"]
        Next       = "WaitScaleApplied"
      }

      # [4.5] [NEW-2] ASG desired=0 이 반영될 짧은 대기 — 그 전에 CleanupOrphans 가 강제종료하면
      # ASG 가 종료한 노드를 재생성할 수 있다(desired 가 아직 N). Wait 는 실패 불가라 Catch 불요.
      WaitScaleApplied = {
        Type    = "Wait"
        Seconds = 30
        Next    = "CleanupOrphans"
      }

      # [5] AWS 레벨 고아 정리(approach B): 노드 강제종료 + 볼륨 available 대기 + ALB·EBS·ENI·SG·GuardDuty 삭제
      CleanupOrphans = {
        Type       = "Task"
        Resource   = "arn:aws:states:::lambda:invoke"
        Parameters = { FunctionName = aws_lambda_function.dr_teardown_cleanup.arn }
        ResultPath = "$.cleanup"
        Catch      = local.fb_catch["CleanupOrphans"]
        Next       = "TeardownHot"
      }

      # [6] hot teardown(CodeBuild + BuildspecOverride, eks_dr_active=false, 17-target)
      TeardownHot = {
        Type     = "Task"
        Resource = "arn:aws:states:::codebuild:startBuild.sync"
        Parameters = {
          ProjectName       = aws_codebuild_project.dr_failover_tf.name
          BuildspecOverride = "infra/terraform/aws/dr-failback-teardown-buildspec.yml"
        }
        ResultPath = null
        Catch      = local.fb_catch["TeardownHot"]
        Next       = "VerifyNoOrphans"
      }

      # [7] [R8] 고아 검증 — 잔존 cluster-태그 EBS 확인(있으면 경고 첨부)
      VerifyNoOrphans = {
        Type     = "Task"
        Resource = "arn:aws:states:::aws-sdk:ec2:describeVolumes"
        Parameters = {
          Filters = [{ Name = "tag:kubernetes.io/cluster/${local.eks_dr_name}", Values = ["owned"] }]
        }
        ResultSelector = { "volumes.$" = "$.Volumes" }
        ResultPath     = "$.verify"
        Catch          = local.fb_catch["VerifyNoOrphans"]
        Next           = "OrphanCheck"
      }
      OrphanCheck = {
        Type    = "Choice"
        Choices = [{ Variable = "$.verify.volumes[0]", IsPresent = true, Next = "MarkOrphanWarning" }]
        Default = "MarkClean"
      }
      # 두 분기 모두 $.warn.orphanWarning 을 세팅(notify payload JSONPath 가 항상 존재하도록 — 없으면 States.Runtime).
      MarkOrphanWarning = {
        Type       = "Pass"
        Result     = { orphanWarning = "⚠️ 고아 의심: cluster 태그 EBS 잔존 — 콘솔 확인 필요" }
        ResultPath = "$.warn"
        Next       = "ClearFlags"
      }
      MarkClean = {
        Type       = "Pass"
        Result     = { orphanWarning = "" }
        ResultPath = "$.warn"
        Next       = "ClearFlags"
      }

      # [8] 플래그 클리어
      ClearFlags = {
        Type       = "Task"
        Resource   = "arn:aws:states:::aws-sdk:ssm:deleteParameters"
        Parameters = { Names = ["/cledyu-dr/failover/active", "/cledyu-dr/failover/alb-hostname"] }
        Catch      = local.fb_catch["ClearFlags"]
        ResultPath = null
        Next       = "NotifyFailbackComplete"
      }

      # [9] 완료 알림 ([R7] RTO/RPO 라벨 없음, orphanWarning 있으면 첨부)
      NotifyFailbackComplete = {
        Type     = "Task"
        Resource = "arn:aws:states:::lambda:invoke"
        Parameters = {
          FunctionName = aws_lambda_function.dr_notify.arn
          Payload = {
            outcome           = "failback-success"
            "approvedAt.$"    = "$.approval.approvedAt"
            "orphanWarning.$" = "$.warn.orphanWarning"
          }
        }
        Retry = [{ ErrorEquals = ["States.ALL"], IntervalSeconds = 5, MaxAttempts = 3, BackoffRate = 2.0 }]
        End   = true
      }
      },
      # 각 단계 실패 마커 상태 → NotifyFailbackFailed.
      # dnsReverted 는 상태 이름으로 **정적** 판정한다 — RevertDNS 이후 단계가 실패했으면 DNS 는 이미
      # 온프렘(true), RequestApproval/RevertDNS 자체 실패면 아직 EKS(false). (States.IsPresent 는
      # intrinsic 이 아니라 Choice 연산자라 Parameters 에서 쓰면 AWS 가 SCHEMA_VALIDATION_FAILED 로 거부한다.)
      { for s in ["RequestApproval", "RevertDNS", "ListNodegroup", "ScaleToZero", "CleanupOrphans", "TeardownHot", "VerifyNoOrphans", "ClearFlags"] :
        "Mark_${s}_Failed" => {
          Type       = "Pass"
          Result     = { failedState = s, dnsReverted = !contains(["RequestApproval", "RevertDNS"], s) }
          ResultPath = "$.failed"
          Next       = "NotifyFailbackFailed"
        }
      },
      {
        NotifyFailbackFailed = {
          Type     = "Task"
          Resource = "arn:aws:states:::lambda:invoke"
          Parameters = {
            FunctionName = aws_lambda_function.dr_notify.arn
            Payload = {
              outcome          = "failback-failed"
              "failedState.$"  = "$.failed.failedState"
              "dnsReverted.$"  = "$.failed.dnsReverted" # 정적 판정(위 Mark_ 생성) — intrinsic 아님
              "executionArn.$" = "$$.Execution.Id"
            }
          }
          Retry = [{ ErrorEquals = ["States.ALL"], IntervalSeconds = 5, MaxAttempts = 3, BackoffRate = 2.0 }]
          End   = true
        }
    })
  })
}

# ── push OK 복귀 → failback-trigger (dr_disaster 규칙 미러) ──
resource "aws_cloudwatch_event_rule" "dr_recovery" {
  count       = (local.pub == 1 && var.dr_detection_armed && var.dr_orchestration_armed) ? 1 : 0
  provider    = aws.use1
  name        = "${var.name_prefix}-dr-recovery"
  description = "push 하트비트 ALARM→OK(온프렘 회복) → failback 트리거"
  event_pattern = jsonencode({
    source      = ["aws.cloudwatch"]
    detail-type = ["CloudWatch Alarm State Change"]
    detail = {
      alarmName = [aws_cloudwatch_metric_alarm.push.alarm_name]
      state     = { value = ["OK"] }
      # [R3] previousState 필터 없음 — ALARM→OK 뿐 아니라 INSUFFICIENT_DATA→OK 도 잡아야 회복을 놓치지 않는다.
      # 평상시 push 는 steady OK 라 →OK 전이 자체가 없으므로 오발 없음. 진짜 필터는 trigger 의 active 게이트.
    }
  })
}

resource "aws_cloudwatch_event_target" "dr_recovery" {
  count     = (local.pub == 1 && var.dr_detection_armed && var.dr_orchestration_armed) ? 1 : 0
  provider  = aws.use1
  rule      = aws_cloudwatch_event_rule.dr_recovery[0].name
  target_id = "failback-trigger"
  arn       = aws_lambda_function.dr_failback_trigger.arn
}

resource "aws_lambda_permission" "dr_recovery" {
  count         = (local.pub == 1 && var.dr_detection_armed && var.dr_orchestration_armed) ? 1 : 0
  provider      = aws.use1
  statement_id  = "AllowEventBridgeInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.dr_failback_trigger.function_name
  principal     = "events.amazonaws.com"
  source_arn    = aws_cloudwatch_event_rule.dr_recovery[0].arn
}
