# ── SNS 알림 토픽(이메일 구독) ─────────────────────────────────────────────
resource "aws_sns_topic" "public_alerts" {
  count = local.pub
  name  = "${var.name_prefix}-public-alerts"
}

resource "aws_sns_topic_subscription" "public_alerts_email" {
  count     = local.pub
  topic_arn = aws_sns_topic.public_alerts[0].arn
  protocol  = "email"
  endpoint  = var.alert_email
}

# ── 프록시 OS 행(impaired) → 자동 리부트 ───────────────────────────────────
# 이번 인시던트(InstanceStatus impaired)를 사람 없이 ~5분 내 자동 복구.
# non-ephemeral 키라 리부트 후 tailscaled 가 tailnet 에 자동 재가입한다.
resource "aws_cloudwatch_metric_alarm" "proxy_impaired_reboot" {
  count               = local.pub
  alarm_name          = "${var.name_prefix}-kc-proxy-instance-impaired"
  namespace           = "AWS/EC2"
  metric_name         = "StatusCheckFailed_Instance"
  dimensions          = { InstanceId = aws_instance.proxy[0].id }
  statistic           = "Maximum"
  period              = 60
  evaluation_periods  = 3
  threshold           = 1
  comparison_operator = "GreaterThanOrEqualToThreshold"
  alarm_actions       = ["arn:aws:automate:${var.region}:ec2:reboot", aws_sns_topic.public_alerts[0].arn]
  treat_missing_data  = "breaching"
}

# ── ALB 타겟 unhealthy → 알림 ──────────────────────────────────────────────
resource "aws_cloudwatch_metric_alarm" "proxy_unhealthy" {
  count       = local.pub
  alarm_name  = "${var.name_prefix}-kc-proxy-unhealthy-host"
  namespace   = "AWS/ApplicationELB"
  metric_name = "UnHealthyHostCount"
  dimensions = {
    LoadBalancer = aws_lb.public[0].arn_suffix
    TargetGroup  = aws_lb_target_group.keycloak_proxy[0].arn_suffix
  }
  statistic           = "Maximum"
  period              = 60
  evaluation_periods  = 3
  threshold           = 1
  comparison_operator = "GreaterThanOrEqualToThreshold"
  alarm_actions       = [aws_sns_topic.public_alerts[0].arn]
  ok_actions          = [aws_sns_topic.public_alerts[0].arn]
}

# ── upstream 장애(502 등 5XX) → 알림 ───────────────────────────────────────
# 얕은 HC 로는 tailnet drop 을 못 잡으므로, ELB 5XX 로 upstream 이상을 탐지.
resource "aws_cloudwatch_metric_alarm" "proxy_5xx" {
  count               = local.pub
  alarm_name          = "${var.name_prefix}-kc-proxy-elb-5xx"
  namespace           = "AWS/ApplicationELB"
  metric_name         = "HTTPCode_ELB_5XX_Count"
  dimensions          = { LoadBalancer = aws_lb.public[0].arn_suffix }
  statistic           = "Sum"
  period              = 300
  evaluation_periods  = 1
  threshold           = 5
  comparison_operator = "GreaterThanThreshold"
  alarm_actions       = [aws_sns_topic.public_alerts[0].arn]
  treat_missing_data  = "notBreaching"
}
