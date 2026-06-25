# Cledyu 공개 진입점(public ingress) — auth.cledyu.com.
#
# 학습자 social 로그인(구글)을 켜려면 Keycloak 이 공개+공개신뢰 TLS 로 도달 가능해야
# 한다(브라우저가 구글로 갔다가 auth.cledyu.com/broker/.../endpoint 로 콜백). 홈랩
# 클러스터는 NAT 뒤라 ALB 가 in-cluster Keycloak 을 직접 타겟할 수 없으므로:
#
#   브라우저 → Route53(auth.cledyu.com) → ALB(443, ACM TLS 종단)
#            → tailnet 가입 프록시 EC2(:80) → (tailnet) → Keycloak
#
# 프록시는 Host=auth.cledyu.com 를 보존해 전달하고, Keycloak 은 proxy.headers=xforwarded
# + hostname strict=false 로 X-Forwarded-Host 를 신뢰해 공개 도메인 기준 URL 을 만든다.
#
# 이 스택 전체는 var.enable_public_ingress 로 게이트된다(기본 false → 미생성).
#
# [초안 주의] 이 모듈은 AWS apply 로만 검증 가능하다. 적용 전 런북
# (docs/RUNBOOK/learner-auth.md '공개 노출')에서 다음을 채운다:
#   - public_domain NS 를 도메인 등록기관에 위임(아니면 ACM DNS 검증이 멈춘다)
#   - keycloak_upstream_url 을 환경 tailnet 토폴로지에 맞게 설정
#   - TF_VAR_tailscale_auth_key 주입(프록시 tailnet 가입용)

locals {
  pub = var.enable_public_ingress ? 1 : 0
}

# ── Route53 공개 hosted zone ──────────────────────────────────────────────
# 생성 후 출력된 NS 4개를 도메인 등록기관(또는 상위 zone)에 위임해야 공개 해석된다.
resource "aws_route53_zone" "public" {
  count = local.pub
  name  = var.public_domain

  tags = {
    Name = "${var.name_prefix}-public"
  }
}

# ── ACM 인증서(auth.cledyu.com, DNS 검증) ──────────────────────────────────
resource "aws_acm_certificate" "auth" {
  count             = local.pub
  domain_name       = var.public_keycloak_host
  validation_method = "DNS"

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_route53_record" "acm_validation" {
  for_each = var.enable_public_ingress ? {
    for dvo in aws_acm_certificate.auth[0].domain_validation_options : dvo.domain_name => {
      name   = dvo.resource_record_name
      type   = dvo.resource_record_type
      record = dvo.resource_record_value
    }
  } : {}

  zone_id         = aws_route53_zone.public[0].zone_id
  name            = each.value.name
  type            = each.value.type
  records         = [each.value.record]
  ttl             = 60
  allow_overwrite = true
}

# NS 위임 전에는 검증이 완료되지 않아 apply 가 대기/타임아웃한다 — 런북의 위임 단계 선행 필수.
resource "aws_acm_certificate_validation" "auth" {
  count                   = local.pub
  certificate_arn         = aws_acm_certificate.auth[0].arn
  validation_record_fqdns = [for r in aws_route53_record.acm_validation : r.fqdn]
}

# ── ALB 보안그룹(공개 443/80 인바운드) ────────────────────────────────────
resource "aws_security_group" "alb" {
  count       = local.pub
  name_prefix = "${var.name_prefix}-alb-"
  description = "Cledyu public ALB - 443/80 inbound"
  vpc_id      = data.aws_vpc.selected.id

  ingress {
    description = "HTTPS"
    from_port   = 443
    to_port     = 443
    protocol    = "tcp"
    cidr_blocks = var.public_ingress_allowed_cidrs
  }
  ingress {
    description = "HTTP (redirect to HTTPS)"
    from_port   = 80
    to_port     = 80
    protocol    = "tcp"
    cidr_blocks = var.public_ingress_allowed_cidrs
  }
  egress {
    description = "To proxy targets"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = { Name = "${var.name_prefix}-alb" }

  lifecycle {
    create_before_destroy = true
  }
}

# ── ALB ───────────────────────────────────────────────────────────────────
# 기본 VPC 서브넷(퍼블릭)을 사용. ALB 는 최소 2개 AZ 서브넷이 필요하다.
resource "aws_lb" "public" {
  count              = local.pub
  name               = "${var.name_prefix}-public"
  internal           = false
  load_balancer_type = "application"
  security_groups    = [aws_security_group.alb[0].id]
  subnets            = data.aws_subnets.selected.ids

  tags = { Name = "${var.name_prefix}-public" }
}

resource "aws_lb_target_group" "keycloak_proxy" {
  count       = local.pub
  name        = "${var.name_prefix}-kc-proxy"
  port        = 80
  protocol    = "HTTP"
  vpc_id      = data.aws_vpc.selected.id
  target_type = "instance"

  health_check {
    path     = "/realms/cledyu-learn"
    port     = "traffic-port"
    protocol = "HTTP"
    matcher  = "200-399"
  }

  tags = { Name = "${var.name_prefix}-kc-proxy" }
}

resource "aws_lb_listener" "https" {
  count             = local.pub
  load_balancer_arn = aws_lb.public[0].arn
  port              = 443
  protocol          = "HTTPS"
  ssl_policy        = "ELBSecurityPolicy-TLS13-1-2-2021-06"
  certificate_arn   = aws_acm_certificate_validation.auth[0].certificate_arn

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.keycloak_proxy[0].arn
  }
}

resource "aws_lb_listener" "http_redirect" {
  count             = local.pub
  load_balancer_arn = aws_lb.public[0].arn
  port              = 80
  protocol          = "HTTP"

  default_action {
    type = "redirect"
    redirect {
      port        = "443"
      protocol    = "HTTPS"
      status_code = "HTTP_301"
    }
  }
}

# ── tailnet 리버스프록시 인스턴스 ─────────────────────────────────────────
resource "aws_security_group" "proxy" {
  count       = local.pub
  name_prefix = "${var.name_prefix}-kc-proxy-"
  description = "Cledyu Keycloak tailnet proxy - 80 from ALB, egress all"
  vpc_id      = data.aws_vpc.selected.id

  ingress {
    description     = "HTTP from ALB"
    from_port       = 80
    to_port         = 80
    protocol        = "tcp"
    security_groups = [aws_security_group.alb[0].id]
  }
  egress {
    description = "All outbound (tailnet, apt)"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = { Name = "${var.name_prefix}-kc-proxy" }

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_instance" "proxy" {
  count                  = local.pub
  ami                    = local.ami_id
  instance_type          = var.proxy_instance_type
  subnet_id              = local.subnet_id
  vpc_security_group_ids = [aws_security_group.proxy[0].id]

  metadata_options {
    http_tokens   = "required"
    http_endpoint = "enabled"
  }

  root_block_device {
    volume_size = 10
    volume_type = "gp3"
    encrypted   = true
  }

  user_data = base64encode(templatefile("${path.module}/cloud-init/keycloak-proxy.yaml.tftpl", {
    tailscale_auth_key = var.tailscale_auth_key
    upstream_url       = var.keycloak_upstream_url
    public_host        = var.public_keycloak_host
    hostname           = "${var.name_prefix}-kc-proxy"
    # tls_* 옵션은 upstream 스킴과 무관하게 Caddy 의 backend TLS 를 켜므로, https
    # upstream 일 때만 transport 블록을 렌더한다(http upstream 평문 502 방지).
    upstream_tls = startswith(var.keycloak_upstream_url, "https://")
  }))

  tags = { Name = "${var.name_prefix}-kc-proxy" }
}

resource "aws_lb_target_group_attachment" "proxy" {
  count            = local.pub
  target_group_arn = aws_lb_target_group.keycloak_proxy[0].arn
  target_id        = aws_instance.proxy[0].id
  port             = 80
}

# ── Route53 A(ALIAS) → ALB ────────────────────────────────────────────────
resource "aws_route53_record" "auth" {
  count   = local.pub
  zone_id = aws_route53_zone.public[0].zone_id
  name    = var.public_keycloak_host
  type    = "A"

  alias {
    name                   = aws_lb.public[0].dns_name
    zone_id                = aws_lb.public[0].zone_id
    evaluate_target_health = true
  }
}
