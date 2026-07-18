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
    actions   = ["states:StartExecution", "states:ListExecutions"] # ListExecutions=RUNNING 중복 판정
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
  statement {
    sid       = "ReadGeneration" # CheckGeneration — 승인 후 현재 active 세대 대조
    actions   = ["ssm:GetParameter"]
    resources = ["arn:aws:ssm:${var.region}:${data.aws_caller_identity.current.account_id}:parameter/cledyu-dr/failover/active"]
  }
  statement {
    sid       = "DnsChangeStatus" # PollDnsChange — Route53 변경 INSYNC 폴링(GetChange 는 리소스 한정 미지원)
    actions   = ["route53:GetChange"]
    resources = ["*"]
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
  fb_catch = { for s in ["RevertDNS", "PollDnsChange", "ListNodegroup", "ScaleToZero", "PollScaleUpdate", "CleanupOrphans", "TeardownHot", "VerifyNoOrphans", "ClearFlags"] :
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
        # 승인 대기 24h(= approval-request/DynamoDB TTL 과 일치). 없으면 waitForTaskToken 이 최대 1년
        # callback 을 기다려 active 플래그가 영구 점유되고 자동복귀가 멈춘다(failover RequestApproval 대칭).
        # States.Timeout 은 아래 States.ALL Catch 가 잡아 Mark_RequestApproval_Failed → 실패 알림으로 간다.
        TimeoutSeconds = 86400
        ResultPath     = "$.approval"
        Catch          = [{ ErrorEquals = ["States.ALL"], ResultPath = "$.error", Next = "Mark_RequestApproval_Failed" }]
        Next           = "CheckGeneration"
      }

      # [1.5] 세대 검증 — 승인 대기(24h) 중 active(failover 세대)가 바뀌었는지 확인(2026-07-18 리뷰 P2).
      # 수동 failback 이 active 를 지우고 새 failover 가 다른 세대를 세팅한 뒤 옛 승인을 누르면 그 승인으로
      # 최신 hot 레이어를 회수하게 된다 → 트리거가 입력에 고정한 $.active 와 현재 SSM active 를 대조한다.
      CheckGeneration = {
        Type           = "Task"
        Resource       = "arn:aws:states:::aws-sdk:ssm:getParameter"
        Parameters     = { Name = "/cledyu-dr/failover/active" }
        ResultSelector = { "current.$" = "$.Parameter.Value" }
        ResultPath     = "$.gen"
        # ParameterNotFound(수동 failback 이 클리어) = 세대 없음 → 이 승인은 stale → 스킵(무해 종료).
        Catch = [{ ErrorEquals = ["States.ALL"], ResultPath = "$.genError", Next = "StaleSkipped" }]
        Next  = "GenerationPresent"
      }
      # 입력에 $.active 가 없는(이 수정 전 트리거된) 옛 실행 방어 — StringEqualsPath 는 비교경로 부재 시
      # States.Runtime 을 낼 수 있어, 값 비교 전에 IsPresent 로 먼저 거른다. ($.gen.current 는 위에서 항상 채워짐.)
      GenerationPresent = {
        Type    = "Choice"
        Choices = [{ Variable = "$.active", IsPresent = true, Next = "GenerationMatch" }]
        Default = "StaleSkipped"
      }
      GenerationMatch = {
        Type    = "Choice"
        Choices = [{ Variable = "$.gen.current", StringEqualsPath = "$.active", Next = "RevertDNS" }]
        Default = "StaleSkipped"
      }
      # 세대 불일치 = 정상 no-op(최신 세대의 failback 이 소유). 실패 아님 → 알림 후 Succeed 로 종료.
      StaleSkipped = {
        Type     = "Task"
        Resource = "arn:aws:states:::lambda:invoke"
        Parameters = {
          FunctionName = aws_lambda_function.dr_notify.arn
          Payload = {
            outcome          = "failback-skipped"
            "executionArn.$" = "$$.Execution.Id"
          }
        }
        Retry = [{ ErrorEquals = ["States.ALL"], IntervalSeconds = 5, MaxAttempts = 3, BackoffRate = 2.0 }]
        End   = true
      }

      # [2] DNS 원복(→온프렘 *-public ALB) — 맨 앞. 트래픽부터 온프렘으로.
      RevertDNS = {
        Type           = "Task"
        Resource       = "arn:aws:states:::lambda:invoke"
        Parameters     = { FunctionName = aws_lambda_function.dr_dns_revert.arn }
        ResultSelector = { "alb.$" = "$.Payload.alb", "changeId.$" = "$.Payload.changeId" }
        ResultPath     = "$.dns"
        Catch          = local.fb_catch["RevertDNS"]
        Next           = "WaitDnsInsync"
      }

      # [2.5] DNS drain — Route53 변경이 INSYNC 되고 옛 EKS ALB 캐시(alias TTL ~60s)가 만료된 뒤 teardown
      # 하도록 대기한다(2026-07-18 리뷰 P2). 안 그러면 resolver 가 아직 EKS 를 가리키는 창에 파드·ALB 를
      # 회수해 그 사용자들이 단절된다. INSYNC 폴링 후 고정 drain.
      WaitDnsInsync = {
        Type    = "Wait"
        Seconds = 10
        Next    = "PollDnsChange"
      }
      PollDnsChange = {
        Type           = "Task"
        Resource       = "arn:aws:states:::aws-sdk:route53:getChange"
        Parameters     = { "Id.$" = "$.dns.changeId" }
        ResultSelector = { "status.$" = "$.ChangeInfo.Status" }
        ResultPath     = "$.dnsChange"
        Catch          = local.fb_catch["PollDnsChange"]
        Next           = "DnsInsync"
      }
      DnsInsync = {
        Type    = "Choice"
        Choices = [{ Variable = "$.dnsChange.status", StringEquals = "INSYNC", Next = "DrainTtl" }]
        Default = "WaitDnsInsync"
      }
      DrainTtl = {
        Type    = "Wait"
        Seconds = 60 # alias 레코드 TTL(타깃 ALB ~60s) drain — resolver 캐시 만료 대기 후 teardown 진입
        Next    = "ListNodegroup"
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
        ResultSelector = { "updateId.$" = "$.Update.Id" }
        ResultPath     = "$.scaleUpdate"
        Catch          = local.fb_catch["ScaleToZero"]
        Next           = "WaitScaleApplied"
      }

      # [4.5] [NEW-2 개정 2026-07-18] updateNodegroupConfig 는 비동기 → 고정 대기가 아니라 describeUpdate 로
      # Successful 을 폴링한 뒤 cleanup 한다. 고정 30s 로는 ASG desired=0 반영을 보장 못 해, 진행 중에
      # CleanupOrphans 가 강제종료하면 관리형 노드그룹이 대체 노드를 재생성한다(EC2/EBS 누수, 리뷰 P2).
      WaitScaleApplied = {
        Type    = "Wait"
        Seconds = 15
        Next    = "PollScaleUpdate"
      }
      PollScaleUpdate = {
        Type     = "Task"
        Resource = "arn:aws:states:::aws-sdk:eks:describeUpdate"
        Parameters = {
          Name              = local.eks_dr_name
          "NodegroupName.$" = "$.ng.name"
          "UpdateId.$"      = "$.scaleUpdate.updateId"
        }
        ResultSelector = { "status.$" = "$.Update.Status" }
        ResultPath     = "$.scaleStatus"
        Catch          = local.fb_catch["PollScaleUpdate"]
        Next           = "ScaleUpdateDone"
      }
      ScaleUpdateDone = {
        Type = "Choice"
        Choices = [
          { Variable = "$.scaleStatus.status", StringEquals = "Successful", Next = "CleanupOrphans" },
          { Variable = "$.scaleStatus.status", StringEquals = "Failed", Next = "Mark_ScaleToZero_Failed" },
          { Variable = "$.scaleStatus.status", StringEquals = "Cancelled", Next = "Mark_ScaleToZero_Failed" },
        ]
        Default = "WaitScaleApplied" # InProgress → 계속 폴링
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

      # [7] [R8] 고아 검증 — teardown-cleanup 을 verify-only 로 재호출해 DR VPC 잔존 **TargetGroup**·EBS 확인.
      # 구: aws-sdk describeVolumes 로 EBS 만 봤다 → CleanupOrphans 의 TG 삭제가 detach 지연 재시도 소진으로
      # 조용히 누수되면(2026-07-18 리뷰 P2) 잔존 TG 를 못 잡고 SUCCESS+active삭제로 넘어갔다. 이제 TG 도 본다.
      VerifyNoOrphans = {
        Type     = "Task"
        Resource = "arn:aws:states:::lambda:invoke"
        Parameters = {
          FunctionName = aws_lambda_function.dr_teardown_cleanup.arn
          Payload      = { verify_only = true }
        }
        ResultSelector = {
          "leftoverTargetGroups.$" = "$.Payload.leftoverTargetGroups"
          "leftoverVolumes.$"      = "$.Payload.leftoverVolumes"
        }
        ResultPath = "$.verify"
        Catch      = local.fb_catch["VerifyNoOrphans"]
        Next       = "OrphanCheck"
      }
      OrphanCheck = {
        Type = "Choice"
        Choices = [{
          Or = [
            { Variable = "$.verify.leftoverTargetGroups[0]", IsPresent = true },
            { Variable = "$.verify.leftoverVolumes[0]", IsPresent = true },
          ]
          Next = "MarkOrphanWarning"
        }]
        Default = "MarkClean"
      }
      # 고아 잔존 = teardown 미완 → **active 를 지우지 않고 Fail 로 끝낸다**(수동 정리·재-failback 가능하게).
      # 경고만 세팅 후 ClearFlags 로 가면 잔존 TG/EBS 가 있어도 성공+active삭제라 재시도가 막힌다(리뷰 P2).
      MarkOrphanWarning = {
        Type       = "Pass"
        Result     = { orphanWarning = "⚠️ 고아 잔존 — DR VPC 에 TargetGroup 또는 cluster태그 EBS 남음. 수동 정리 후 재-failback 필요(active 보존됨)." }
        ResultPath = "$.warn"
        Next       = "NotifyOrphansRemain"
      }
      # active 를 보존한 채 알림 후 Fail — ClearFlags 를 건너뛰어 /cledyu-dr/failover/active 가 남는다.
      NotifyOrphansRemain = {
        Type     = "Task"
        Resource = "arn:aws:states:::lambda:invoke"
        Parameters = {
          FunctionName = aws_lambda_function.dr_notify.arn
          Payload = {
            outcome           = "failback-orphans"
            "orphanWarning.$" = "$.warn.orphanWarning"
            "executionArn.$"  = "$$.Execution.Id"
          }
        }
        Retry = [{ ErrorEquals = ["States.ALL"], IntervalSeconds = 5, MaxAttempts = 3, BackoffRate = 2.0 }]
        Catch = [{ ErrorEquals = ["States.ALL"], ResultPath = null, Next = "OrphansRemain" }]
        Next  = "OrphansRemain"
      }
      OrphansRemain = {
        Type  = "Fail"
        Error = "DrFailbackOrphansRemain"
        Cause = "failback 후 DR VPC 에 TargetGroup/EBS 잔존 — active 보존(수동 정리·재-failback 위해). Discord 알림·실행이력 $.verify 참조."
      }
      # 클린일 때만 $.warn.orphanWarning="" 세팅(NotifyFailbackComplete 의 JSONPath 가 항상 존재하도록).
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
      { for s in ["RequestApproval", "RevertDNS", "PollDnsChange", "ListNodegroup", "ScaleToZero", "PollScaleUpdate", "CleanupOrphans", "TeardownHot", "VerifyNoOrphans", "ClearFlags"] :
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
          # 알림 후 Fail 로 종료 — End=true 면 알림 Lambda 성공 시 실행이 SUCCEEDED 로 기록돼 RevertDNS/
          # TeardownHot 실패가 콘솔·알람·운영자에게 안 보인다(무음 실패). failover 대칭으로 Fail 종단 추가.
          # notify 자체가 재시도 소진돼도 Catch 로 Fail 에 도달시킨다(이미 실패 경로라 Catch 안전).
          Catch = [{ ErrorEquals = ["States.ALL"], ResultPath = null, Next = "FailbackFailed" }]
          Next  = "FailbackFailed"
        }
        FailbackFailed = {
          Type  = "Fail"
          Error = "DrFailbackFailed"
          Cause = "failback 실패 — Discord 알림과 실행 이력 참조. DNS 원복 여부는 알림의 dnsReverted 참조."
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
