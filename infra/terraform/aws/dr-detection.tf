# ─────────────────────────────────────────────────────────────────────────
# DR 재해 감지 (Plan C 감지 계층) — us-east-1 앵커.
# Route53 health check 메트릭이 us-east-1 전용이라, 복합알람 멤버 동일 리전
# 요건을 맞추려 감지 알람 스택 전체를 us-east-1 에 둔다(스펙 § 결정 5).
# ─────────────────────────────────────────────────────────────────────────
provider "aws" {
  alias  = "use1"
  region = "us-east-1"

  # 기본 provider(ap-northeast-2)와 동일한 태그 거버넌스를 us-east-1 리소스에도 적용.
  # component 는 이 스택에 맞게 dr-detection 으로 둔다.
  default_tags {
    tags = {
      "cledyu.io/managed-by" = "terraform"
      "cledyu.io/component"  = "dr-detection"
    }
  }
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

# pull 프로브: 공개 엔드포인트를 딥 HTTP 로 감시. 온프렘이 죽으면 ALB→tailnet 프록시
# 업스트림이 끊겨 5xx → search_string 불일치로 health check 실패.
resource "aws_route53_health_check" "onprem_pull" {
  fqdn              = var.public_keycloak_host # auth.cledyu.com
  type              = "HTTPS_STR_MATCH"
  resource_path     = "/realms/cledyu-learn"
  search_string     = "cledyu-learn"
  port              = 443
  request_interval  = 30
  failure_threshold = 5 # 제안값(드릴 튜닝)
  tags              = { Name = "${var.name_prefix}-dr-pull" }

  # 이 감지 스택은 공개 진입점(auth.cledyu.com ALB/Route53, public-ingress.tf)을 전제한다.
  # enable_public_ingress=false 면 pull 대상이 존재하지 않아 pull 알람이 상시 ALARM 이 되고,
  # heartbeat 동기화 전 push 도 missing→breaching 이라 복합알람이 "구성 미완료"를 재해로 오탐한다.
  # 그래서 공개 진입점이 켜져 있을 때만 감지 스택을 apply 하도록 강제한다.
  lifecycle {
    precondition {
      condition     = var.enable_public_ingress
      error_message = "DR 감지 스택은 enable_public_ingress=true(auth.cledyu.com 공개 진입점)를 전제한다. 공개 진입점 없이 배포하면 pull 알람이 상시 ALARM 이 되어 복합알람이 구성 미완료를 재해로 오탐한다. 공개 진입점을 먼저 켜라."
    }
  }
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
  evaluation_periods  = 3 # 3분(제안값)
  period              = 60
  statistic           = "Sum"
  treat_missing_data  = "breaching"
}

# 복합알람: 둘 다 ALARM 일 때만(AND) → 단일 신호 오탐 차단.
# actions_enabled=var.dr_detection_armed: 최초 배포 시 false 라 bring-up(heartbeat 동기화 지연·
# pull 미준비)에 복합알람이 ALARM 이 돼도 알림을 안 쏜다. 두 신호 healthy 확인 후 armed=true 로
# 재apply 해 무장한다(§ 배포 arming 절차).
resource "aws_cloudwatch_composite_alarm" "disaster" {
  provider        = aws.use1
  alarm_name      = "${var.name_prefix}-dr-disaster"
  alarm_rule      = "ALARM(${aws_cloudwatch_metric_alarm.pull.alarm_name}) AND ALARM(${aws_cloudwatch_metric_alarm.push.alarm_name})"
  alarm_actions   = [aws_sns_topic.dr_alert.arn]
  actions_enabled = var.dr_detection_armed
}
