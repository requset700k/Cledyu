# ── SNS 알림 토픽(이메일 구독) ─────────────────────────────────────────────
resource "aws_sns_topic" "public_alerts" {
  count = local.pub
  name  = "${var.name_prefix}-public-alerts"
}

resource "aws_sns_topic_subscription" "public_alerts_email" {
  # alert_email 이 비면 구독을 만들지 않는다 — 빈 endpoint 로 apply 가 실패해 프록시 교체·
  # 알람 생성 전체가 중단되는 것을 막는다. 알람은 topic 을 참조하므로 이메일 미설정이어도 동작.
  count     = local.pub == 1 && var.alert_email != "" ? 1 : 0
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
  # missing 필수 — 리부트 액션 알람이라, 인스턴스 기동 직후 메트릭 공백을 breaching 으로
  # 잡으면 정상 인스턴스를 오리부트한다. 실제 StatusCheckFailed_Instance=1 일 때만 리부트.
  treat_missing_data = "missing"
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
  # 타겟 등록 공백/롤아웃 순간을 오탐하지 않도록 missing 은 정상 취급.
  treat_missing_data = "notBreaching"
}

# ── upstream 장애(Caddy 502 등 target 5XX) → 알림 ──────────────────────────
# 얕은 HC 로는 tailnet drop 을 못 잡는다. tailnet 이 끊기면 Caddy 가 502 를 반환하는데
# 이는 target 기원 5XX 이므로 HTTPCode_Target_5XX_Count(TG 차원)로 잡아야 한다.
# HTTPCode_ELB_5XX_Count 는 LB 자체 기원 5XX 만 세고 target 응답은 제외하므로 부적합.
resource "aws_cloudwatch_metric_alarm" "proxy_5xx" {
  count       = local.pub
  alarm_name  = "${var.name_prefix}-kc-proxy-target-5xx"
  namespace   = "AWS/ApplicationELB"
  metric_name = "HTTPCode_Target_5XX_Count"
  dimensions = {
    LoadBalancer = aws_lb.public[0].arn_suffix
    TargetGroup  = aws_lb_target_group.keycloak_proxy[0].arn_suffix
  }
  statistic           = "Sum"
  period              = 300
  evaluation_periods  = 1
  threshold           = 5
  comparison_operator = "GreaterThanThreshold"
  alarm_actions       = [aws_sns_topic.public_alerts[0].arn]
  treat_missing_data  = "notBreaching"
}
