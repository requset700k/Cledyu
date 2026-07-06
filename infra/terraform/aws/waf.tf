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

  # 공개 API 의 /metrics(무인증 Prometheus·process 지표)를 인터넷에서 차단한다.
  # api.cledyu.com 은 ingress 의 "/" Prefix 로 API 전체를 노출하는데 router 의 /metrics 는
  # JWT 밖이라, 공개되는 순간 지표가 그대로 조회된다. in-cluster ServiceMonitor 는 Service
  # ClusterIP 를 직접 스크랩(프록시·ALB 미경유)하므로 이 edge 차단의 영향을 받지 않는다.
  # 관리형 룰이 count(비차단)여도 이 커스텀 block 은 즉시 적용된다.
  rule {
    name     = "block-public-metrics"
    priority = 5
    action {
      block {}
    }
    statement {
      byte_match_statement {
        search_string         = "/metrics"
        positional_constraint = "STARTS_WITH"
        field_to_match {
          uri_path {}
        }
        text_transformation {
          priority = 0
          type     = "LOWERCASE"
        }
      }
    }
    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "${var.name_prefix}-block-metrics"
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
