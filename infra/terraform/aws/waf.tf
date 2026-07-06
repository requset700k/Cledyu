# AWS WAF — 공개 ALB 앞단 보호. enable_public_ingress 게이트.
# 관리형 룰은 초기 count 모드(override count)로 배포해 정상 학습자 오탐을 CloudWatch
# sampled requests 로 관측한 뒤 block(override none)으로 전환한다(런북 참고). rate-based
# 는 처음부터 block(2000/5분이라 정상 트래픽 영향 낮음).
resource "aws_wafv2_web_acl" "public" {
  count = local.pub
  name  = "${var.name_prefix}-public"
  scope = "REGIONAL"

  default_action {
    allow {}
  }

  rule {
    name     = "common-rule-set"
    priority = 1
    override_action {
      count {}
    }
    statement {
      managed_rule_group_statement {
        name        = "AWSManagedRulesCommonRuleSet"
        vendor_name = "AWS"
      }
    }
    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "${var.name_prefix}-common"
      sampled_requests_enabled   = true
    }
  }

  rule {
    name     = "known-bad-inputs"
    priority = 2
    override_action {
      count {}
    }
    statement {
      managed_rule_group_statement {
        name        = "AWSManagedRulesKnownBadInputsRuleSet"
        vendor_name = "AWS"
      }
    }
    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "${var.name_prefix}-known-bad"
      sampled_requests_enabled   = true
    }
  }

  rule {
    name     = "ip-reputation"
    priority = 3
    override_action {
      count {}
    }
    statement {
      managed_rule_group_statement {
        name        = "AWSManagedRulesAmazonIpReputationList"
        vendor_name = "AWS"
      }
    }
    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "${var.name_prefix}-ip-rep"
      sampled_requests_enabled   = true
    }
  }

  rule {
    name     = "rate-limit"
    priority = 10
    action {
      block {}
    }
    statement {
      rate_based_statement {
        limit              = var.waf_rate_limit
        aggregate_key_type = "IP"
      }
    }
    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "${var.name_prefix}-rate"
      sampled_requests_enabled   = true
    }
  }

  visibility_config {
    cloudwatch_metrics_enabled = true
    metric_name                = "${var.name_prefix}-public"
    sampled_requests_enabled   = true
  }

  tags = { Name = "${var.name_prefix}-public" }
}

resource "aws_wafv2_web_acl_association" "public" {
  count        = local.pub
  resource_arn = aws_lb.public[0].arn
  web_acl_arn  = aws_wafv2_web_acl.public[0].arn
}
