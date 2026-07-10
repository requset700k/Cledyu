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
# 적용 전 런북(docs/RUNBOOK/learner-auth.md '공개 노출')에서 다음을 채운다:
#   - public_domain 을 Route53 Registrar 로 등록(hosted zone·NS 자동 연결, 수동 위임 불필요)
#   - keycloak_upstream_url 을 환경 tailnet 토폴로지에 맞게 설정(기본 https://10.10.0.101)
#   - TF_VAR_tailscale_auth_key 주입(프록시 tailnet 가입용)

locals {
  pub = var.enable_public_ingress ? 1 : 0

  # 공개 노출 3-host. 전부 같은 ALB(alias)로 보내고 호스트 분기는 Traefik이 담당한다.
  public_hosts = var.enable_public_ingress ? {
    auth = var.public_keycloak_host
    app  = var.public_app_host
    api  = var.public_api_host
  } : {}
}

# ── Route53 공개 hosted zone (기존 것 참조) ───────────────────────────────
# public_domain 을 Route53 Registrar 로 등록하면 hosted zone 이 자동 생성되고 도메인
# NS 도 거기로 자동 연결된다(수동 위임 불필요). 따라서 zone 을 새로 만들지 않고
# 기존 zone 을 data 로 참조한다 — 새로 만들면 NS 가 달라 ACM 검증이 전파되지 않는다.
data "aws_route53_zone" "public" {
  count = local.pub
  name  = "${var.public_domain}."
}

# ── ACM 와일드카드 인증서 (기존 발급, terraform 이 재발급하지 않도록) ───────────────
# 라이브는 별도 발급한 와일드카드 *.cledyu.com cert(app+auth 공통)를 쓴다. 인증서와
# 그 DNS validation CNAME 은 이 모듈 밖에서 관리하며, terraform 은 data 로 읽기만 한다.
#
# 마이그레이션 주의(기존 state): 이전 버전은 auth.cledyu.com 전용 cert 를 관리했다
# (aws_acm_certificate.auth / aws_acm_certificate_validation.auth /
#  aws_route53_record.acm_validation). 그 리소스들이 config 에서 사라지면 apply 가
# validation CNAME 까지 destroy 하려 하는데, 이 CNAME 은 ACM 와일드카드 인증서의
# managed renewal 검증에 계속 쓰이므로 삭제되면 갱신 실패로 공개 TLS 가 만료된다.
# 따라서 destroy 가 아니라 state 에서만 제거해 AWS 리소스(cert+CNAME)를 보존한다:
#   terraform state rm 'aws_route53_record.acm_validation["*.cledyu.com"]' \
#     'aws_acm_certificate_validation.auth[0]' 'aws_acm_certificate.auth[0]'
# (본 배포는 2026-07-09 위 절차로 정리 완료 — cert ISSUED, CNAME 보존 확인.)
data "aws_acm_certificate" "wildcard" {
  count       = local.pub
  domain      = "*.${var.public_domain}"
  statuses    = ["ISSUED"]
  most_recent = true
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
    path                = "/healthz"
    port                = "traffic-port"
    protocol            = "HTTP"
    matcher             = "200"
    interval            = 30
    healthy_threshold   = 2
    unhealthy_threshold = 3
  }

  tags = { Name = "${var.name_prefix}-kc-proxy" }
}

resource "aws_lb_listener" "https" {
  count             = local.pub
  load_balancer_arn = aws_lb.public[0].arn
  port              = 443
  protocol          = "HTTPS"
  ssl_policy        = "ELBSecurityPolicy-TLS13-1-2-2021-06"
  certificate_arn   = data.aws_acm_certificate.wildcard[0].arn

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
  ami                    = var.proxy_ami_id
  instance_type          = var.proxy_instance_type
  subnet_id              = local.subnet_id
  vpc_security_group_ids = [aws_security_group.proxy[0].id]
  iam_instance_profile   = aws_iam_instance_profile.proxy_ssm[0].name

  # SSM 정책 attachment 를 명시적 의존으로 묶는다. instance profile 만 참조하면
  # role 에 붙는 attachment 는 숨은 의존이라, 운영자가 -target=aws_instance.proxy[0]
  # 로 재생성할 때 attachment 가 빠져 SSM 접속이 안 될 수 있다. depends_on 으로
  # -target 이 attachment 까지 함께 끌어오게 한다.
  depends_on = [aws_iam_role_policy_attachment.proxy_ssm_core]

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
    hostname           = "${var.name_prefix}-kc-proxy"
    # Caddy host 매처 allowlist — 이 공개 Host 만 Traefik 으로 전달하고 나머지는 404
    # (내부 .local Ingress 로의 Host 주입 차단).
    allowed_hosts = join(" ", [var.public_keycloak_host, var.public_app_host, var.public_api_host])
    # tls transport 블록은 https upstream 일 때만 렌더(http upstream 평문 502 방지).
    upstream_tls = startswith(var.keycloak_upstream_url, "https://")
  }))

  # user_data(cloud-init)는 최초 launch 때만 실행되고, aws_instance 는 user_data 변경 시
  # 기본적으로 stop/start 만 해 기존 인스턴스에 새 Caddyfile/헬스체크가 반영되지 않는다.
  # 강제 교체로 cloud-init 을 새로 돌려 변경분을 실제 적용한다(그렇지 않으면 타깃그룹 unhealthy).
  user_data_replace_on_change = true

  tags = { Name = "${var.name_prefix}-kc-proxy" }
}

resource "aws_lb_target_group_attachment" "proxy" {
  count            = local.pub
  target_group_arn = aws_lb_target_group.keycloak_proxy[0].arn
  target_id        = aws_instance.proxy[0].id
  port             = 80
}

# ── Route53 A(ALIAS) → ALB (auth/app/api 3-host) ──────────────────────────
resource "aws_route53_record" "public" {
  for_each = local.public_hosts

  zone_id = data.aws_route53_zone.public[0].zone_id
  name    = each.value
  type    = "A"

  alias {
    name                   = aws_lb.public[0].dns_name
    zone_id                = aws_lb.public[0].zone_id
    evaluate_target_health = true
  }
}

# 기존 단일 auth 레코드를 파괴/재생성 없이 map 키 auth 로 이관.
moved {
  from = aws_route53_record.auth[0]
  to   = aws_route53_record.public["auth"]
}
