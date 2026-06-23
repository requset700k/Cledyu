# 비용 가드레일 — EC2 오버플로우가 예상보다 많이 켜지거나 인스턴스가 회수되지 않을 때를 대비한
# 월 예산 알람. apps/api 의 reaper(TTL/타임아웃 terminate)가 1차 방어선이고, 이 예산 알람은
# 그래도 비용이 새는 경우를 잡는 2차 방어선이다. budget_limit_usd=0 이면 생성하지 않는다.
resource "aws_budgets_budget" "lab_ec2" {
  count = var.budget_limit_usd > 0 ? 1 : 0

  name         = "${var.name_prefix}-ec2-monthly"
  budget_type  = "COST"
  limit_amount = tostring(var.budget_limit_usd)
  limit_unit   = "USD"
  time_unit    = "MONTHLY"

  # 실제 사용량 80% 도달 시 알림.
  notification {
    comparison_operator        = "GREATER_THAN"
    threshold                  = 80
    threshold_type             = "PERCENTAGE"
    notification_type          = "ACTUAL"
    subscriber_email_addresses = var.budget_notification_emails
  }

  # 예측 사용량 100% 도달 시 알림(추세 조기 경보).
  notification {
    comparison_operator        = "GREATER_THAN"
    threshold                  = 100
    threshold_type             = "PERCENTAGE"
    notification_type          = "FORECASTED"
    subscriber_email_addresses = var.budget_notification_emails
  }
}
