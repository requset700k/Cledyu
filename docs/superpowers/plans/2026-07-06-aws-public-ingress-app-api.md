# AWS 공개 노출 A안 (app/api/auth.cledyu.com) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 외부 학습자가 `app.cledyu.com`에서 구글 로그인 후 랩을 시작하는 E2E 흐름을, 와일드카드 ACM + AWS WAF를 갖춘 공개 ALB로 배선한다.

**Architecture:** ALB(공개 TLS 종단 + WAF) → 단일 타깃그룹 → tailnet Caddy 프록시(Host 보존) → (tailnet) → Traefik LB(10.10.0.101)가 Host 기준으로 app/api/auth를 in-cluster 라우팅. ALB는 L7 라우팅을 하지 않고, 호스트 분기는 기존 Traefik이 담당한다.

**Tech Stack:** Terraform(AWS provider, wafv2/acm/route53/alb/ec2 + keycloak provider), Helm(gitops charts), Caddy v2, Tailscale.

## Global Constraints

- 리전: `ap-northeast-2` 단일(스택 전체가 pin된 ami_id·S3 state·KMS에 리전 종속). `var.region` validation을 건드리지 않는다.
- 게이팅: 공개 진입점 리소스는 전부 `var.enable_public_ingress`(기본 false)로 opt-in. WAF·신규 레코드·와일드카드 ACM 모두 이 플래그 하위.
- AWS 계정: `504284203153`, 프로파일 `cledyu`. 백엔드 state는 S3(`AWS_PROFILE=cledyu AWS_REGION=ap-northeast-2`).
- 네이밍: hackathon 류 금지. 리소스 prefix는 `var.name_prefix`(기본 `cledyu-lab`).
- 문서·주석은 한국어, 코드 식별자·CLI·키는 영어.
- commitlint: subject 소문자 시작, body/footer 줄당 100자 wrap. 이모지 금지.
- 작업 위치: 격리 worktree `/Users/kylekim1223/request700k/cledyu-aws-ingress`(브랜치 `feat/aws-public-ingress-app-api`). 메인 체크아웃은 병렬 잡이 사용 중이므로 절대 건드리지 않는다.
- 인증 세션은 `.cledyu.com` 단일. `.local` 인증 병행은 하지 않는다(라우팅만 잔존).
- web의 `CLEDYU_BACKEND_URL`(in-cluster)은 변경하지 않는다. 브라우저는 OAuth 콜백에서만 `api.cledyu.com`을 직접 도달한다.

**검증 도구 주의:** terraform `plan`/`apply`와 keycloak provider plan은 AWS 자격증명·Tailscale(클러스터 도달)이 필요하므로 Task 8(게이트 apply)에서 수행한다. Task 1~7의 per-task 검증은 자격증명이 필요 없는 `terraform fmt`/`terraform validate`와 `helm template`으로 한정한다.

---

### Task 1: 와일드카드 ACM + Route53 3-host(app/api/auth) + 검증레코드 dedupe

**Files:**
- Modify: `infra/terraform/aws/public-ingress.tf` (ACM 34-42, acm_validation 44-59, route53 auth 233-244)
- Modify: `infra/terraform/aws/variables.tf` (신규 변수 추가)

**Interfaces:**
- Consumes: `data.aws_route53_zone.public[0]`, `aws_lb.public[0]`(기존).
- Produces: `aws_acm_certificate.auth[0]`(도메인 `*.cledyu.com`), `aws_route53_record.public`(키 `auth`/`app`/`api`), 로컬 `local.public_hosts`. Task 4 outputs가 `aws_route53_record.public["app"|"api"]`을 참조한다.

- [ ] **Step 1: 신규 변수 2개 추가** — `infra/terraform/aws/variables.tf` 끝(`github_repo` 블록 뒤)에 추가

```hcl
variable "public_app_host" {
  description = "학습자 web 앱 공개 FQDN(ALB→프록시→Traefik→web). 와일드카드 ACM(*.public_domain)로 커버."
  type        = string
  default     = "app.cledyu.com"
}

variable "public_api_host" {
  description = <<-EOT
    학습자 api(BFF) 공개 FQDN. 브라우저는 OAuth 콜백(api.cledyu.com/api/v1/auth/callback)에서만
    직접 도달하고, 일반 데이터 호출은 web 이 in-cluster(http://api.api.svc.cluster.local)로 프록시한다.
  EOT
  type        = string
  default     = "api.cledyu.com"
}
```

- [ ] **Step 2: ACM을 와일드카드로 변경** — `public-ingress.tf`의 `aws_acm_certificate.auth` 블록을 교체

```hcl
resource "aws_acm_certificate" "auth" {
  count                     = local.pub
  domain_name               = "*.${var.public_domain}"
  subject_alternative_names = [var.public_domain]
  validation_method         = "DNS"

  lifecycle {
    create_before_destroy = true
  }
}
```

- [ ] **Step 3: 검증레코드 for_each를 resource_record_name으로 dedupe** — `aws_route53_record.acm_validation`의 for_each 키를 `dvo.domain_name` → `dvo.resource_record_name`으로 변경(와일드카드 `*.cledyu.com`와 apex `cledyu.com`이 동일 검증 CNAME을 공유할 수 있어 중복 생성/충돌 방지)

```hcl
resource "aws_route53_record" "acm_validation" {
  for_each = var.enable_public_ingress ? {
    for dvo in aws_acm_certificate.auth[0].domain_validation_options :
    dvo.resource_record_name => {
      name   = dvo.resource_record_name
      type   = dvo.resource_record_type
      record = dvo.resource_record_value
    }
  } : {}

  zone_id         = data.aws_route53_zone.public[0].zone_id
  name            = each.value.name
  type            = each.value.type
  records         = [each.value.record]
  ttl             = 60
  allow_overwrite = true
}
```

- [ ] **Step 4: Route53 A 레코드를 3-host for_each로 확장** — 파일 맨 끝의 `aws_route53_record.auth` 블록을 아래로 교체하고, `local.pub` 로컬 근처(파일 상단 `locals` 블록)에 `public_hosts`를 추가

`locals` 블록(파일 상단, 기존 `pub = ...` 옆)에 추가:

```hcl
locals {
  pub = var.enable_public_ingress ? 1 : 0

  # 공개 노출 3-host. 전부 같은 ALB(alias)로 보내고 호스트 분기는 Traefik이 담당한다.
  public_hosts = var.enable_public_ingress ? {
    auth = var.public_keycloak_host
    app  = var.public_app_host
    api  = var.public_api_host
  } : {}
}
```

파일 끝 `aws_route53_record.auth` → 교체:

```hcl
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
```

- [ ] **Step 5: fmt + validate**

Run: `cd /Users/kylekim1223/request700k/cledyu-aws-ingress/infra/terraform/aws && terraform fmt && terraform validate`
Expected: `Success! The configuration is valid.` (fmt는 변경 파일 정렬만)

- [ ] **Step 6: 커밋**

```bash
cd /Users/kylekim1223/request700k/cledyu-aws-ingress
git add infra/terraform/aws/public-ingress.tf infra/terraform/aws/variables.tf
git commit -m "feat(infra): acm 를 *.cledyu.com 와일드카드로 + route53 app/api/auth 3-host

와일드카드 acm(apex san 포함) 1장으로 세 공개 호스트를 커버하고, route53 a(alias)
레코드를 auth/app/api 3개로 for_each 확장한다. 검증레코드는 resource_record_name 으로
dedupe 해 와일드카드+apex 공유 cname 충돌을 막고, moved 블록으로 기존 auth 레코드를
무중단 이관한다."
```

---

### Task 2: 프록시 Caddy 멀티호스트 일반화 + ALB 헬스체크 /healthz

**Files:**
- Modify: `infra/terraform/aws/cloud-init/keycloak-proxy.yaml.tftpl` (Caddyfile write_files 블록)
- Modify: `infra/terraform/aws/public-ingress.tf` (타깃그룹 health_check 125-130, templatefile 인자 212-220)

**Interfaces:**
- Consumes: `var.keycloak_upstream_url`(기본 `https://10.10.0.101` = Traefik LB), `var.tailscale_auth_key`.
- Produces: 프록시가 `*.cledyu.com` 요청의 Host를 보존해 Traefik로 전달하고 `/healthz`에 200을 반환. 타깃그룹 헬스체크가 `/healthz`를 본다.

- [ ] **Step 1: Caddyfile을 Host 보존 + /healthz로 교체** — `keycloak-proxy.yaml.tftpl`의 `write_files:` 안 `/etc/caddy/Caddyfile` content를 교체(단일 호스트 Host 오버라이드 제거, 헬스체크 분리)

```yaml
  - path: /etc/caddy/Caddyfile
    permissions: "0644"
    content: |
      # ALB→프록시 hop 은 평문 :80. 공개 TLS 는 ALB(ACM)가 종단한다.
      # 공개 Host(app|api|auth.cledyu.com)를 그대로 보존해 tailnet 너머 Traefik LB
      # (${upstream_url})로 전달한다. ALB 는 원본 Host 를 타겟까지 넘기고, Caddy
      # reverse_proxy 는 기본적으로 클라이언트 Host 를 업스트림에 전달하므로 Host 를
      # 특정 호스트로 고정하지 않는다(고정하면 Traefik 이 한 ingress 로만 라우팅됨).
      :80 {
        # ALB 헬스체크 전용 — 프록시 생존만 판정(Traefik/업스트림 상태와 분리).
        handle /healthz {
          respond "ok" 200
        }

        handle {
          reverse_proxy ${upstream_url} {
            header_up X-Forwarded-Proto https
%{ if upstream_tls ~}
            # Traefik(내부 CA) HTTPS upstream 전용 — tailnet(WireGuard) 암호화 hop 이라
            # 인증서 검증은 생략한다. tls_* 옵션은 upstream 스킴과 무관하게 TLS 를 켜므로
            # http upstream 일 때는 이 블록을 렌더하지 않는다(평문 upstream 502 방지).
            transport http {
              tls_insecure_skip_verify
            }
%{ endif ~}
          }
        }
      }
```

- [ ] **Step 2: templatefile 인자에서 public_host 제거** — `public-ingress.tf`의 `aws_instance.proxy` user_data를 교체(더 이상 Host를 고정하지 않으므로 `public_host` 인자 불필요)

```hcl
  user_data = base64encode(templatefile("${path.module}/cloud-init/keycloak-proxy.yaml.tftpl", {
    tailscale_auth_key = var.tailscale_auth_key
    upstream_url       = var.keycloak_upstream_url
    hostname           = "${var.name_prefix}-kc-proxy"
    # tls transport 블록은 https upstream 일 때만 렌더(http upstream 평문 502 방지).
    upstream_tls = startswith(var.keycloak_upstream_url, "https://")
  }))
```

- [ ] **Step 3: 타깃그룹 헬스체크를 /healthz로 변경** — `aws_lb_target_group.keycloak_proxy`의 `health_check` 블록 교체(Keycloak 종속 경로 제거, 프록시 로컬 응답만)

```hcl
  health_check {
    path                = "/healthz"
    port                = "traffic-port"
    protocol            = "HTTP"
    matcher             = "200"
    interval            = 30
    healthy_threshold   = 2
    unhealthy_threshold = 3
  }
```

- [ ] **Step 4: fmt + validate**

Run: `cd /Users/kylekim1223/request700k/cledyu-aws-ingress/infra/terraform/aws && terraform fmt && terraform validate`
Expected: `Success! The configuration is valid.`

- [ ] **Step 5: 커밋**

```bash
cd /Users/kylekim1223/request700k/cledyu-aws-ingress
git add infra/terraform/aws/cloud-init/keycloak-proxy.yaml.tftpl infra/terraform/aws/public-ingress.tf
git commit -m "feat(infra): 프록시 caddy 를 host 보존 멀티호스트로 + alb 헬스체크 /healthz

caddy 의 host 고정을 제거해 app|api|auth.cledyu.com 요청 host 를 보존한 채 traefik 로
전달한다(traefik 이 host 로 라우팅). /healthz 로컬 200 응답을 추가하고 타깃그룹 헬스체크를
keycloak 종속 경로에서 /healthz 로 바꿔, 프록시 생존과 업스트림 상태를 분리한다."
```

---

### Task 3: AWS WAF WebACL(관리형 count-first + rate-based) + ALB 연결

**Files:**
- Create: `infra/terraform/aws/waf.tf`
- Modify: `infra/terraform/aws/variables.tf` (waf_rate_limit)

**Interfaces:**
- Consumes: `local.pub`, `aws_lb.public[0].arn`, `var.name_prefix`, `var.waf_rate_limit`.
- Produces: `aws_wafv2_web_acl.public[0]`(Task 4 output이 ARN 참조), `aws_wafv2_web_acl_association.public[0]`.

- [ ] **Step 1: waf_rate_limit 변수 추가** — `infra/terraform/aws/variables.tf`에 추가

```hcl
variable "waf_rate_limit" {
  description = "WAF rate-based 룰의 IP당 5분(기본 평가창) 요청 상한. 초과 시 block. 데모 부하 기준 2000."
  type        = number
  default     = 2000
}
```

- [ ] **Step 2: waf.tf 생성** — 관리형 3종은 초기 `count`(차단 아님), rate-based는 `block`. 데모 검증 후 count→none 전환(Task 8).

```hcl
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
```

- [ ] **Step 3: fmt + validate**

Run: `cd /Users/kylekim1223/request700k/cledyu-aws-ingress/infra/terraform/aws && terraform fmt && terraform validate`
Expected: `Success! The configuration is valid.`

- [ ] **Step 4: 커밋**

```bash
cd /Users/kylekim1223/request700k/cledyu-aws-ingress
git add infra/terraform/aws/waf.tf infra/terraform/aws/variables.tf
git commit -m "feat(infra): 공개 alb 에 aws waf 부착(관리형 count-first + rate-based)

관리형 룰 3종(common/known-bad-inputs/ip-reputation)을 초기 count 모드로, rate-based
(ip당 2000/5분)를 block 으로 두는 wafv2 webacl 을 만들고 공개 alb 에 연결한다. 데모
검증에서 오탐 0 확인 후 관리형 룰을 block 으로 전환한다(런북). enable_public_ingress 게이트."
```

---

### Task 4: outputs — app/api 레코드 + WAF WebACL ARN

**Files:**
- Modify: `infra/terraform/aws/outputs.tf` (공개 진입점 output 섹션)

**Interfaces:**
- Consumes: `aws_route53_record.public["app"|"api"]`(Task 1), `aws_wafv2_web_acl.public[0]`(Task 3).
- Produces: 검증용 output `public_app_record`, `public_api_record`, `public_waf_web_acl_arn`.

- [ ] **Step 1: 공개 진입점 output 3개 추가** — `outputs.tf`의 `keycloak_proxy_instance_id` output 뒤에 추가

```hcl
output "public_app_record" {
  description = "학습자 web 공개 FQDN(app.cledyu.com). 검증용."
  value       = var.enable_public_ingress ? aws_route53_record.public["app"].fqdn : ""
}

output "public_api_record" {
  description = "학습자 api 공개 FQDN(api.cledyu.com). OAuth 콜백 도달점. 검증용."
  value       = var.enable_public_ingress ? aws_route53_record.public["api"].fqdn : ""
}

output "public_waf_web_acl_arn" {
  description = "공개 ALB 에 연결된 WAF WebACL ARN(CloudWatch 메트릭·sampled requests 확인용)."
  value       = var.enable_public_ingress ? aws_wafv2_web_acl.public[0].arn : ""
}
```

- [ ] **Step 2: 기존 auth output 주석 정합** — `public_alb_dns_name` output의 description에서 `auth.cledyu.com A ALIAS` → `app/api/auth.cledyu.com A ALIAS`로 수정(단순 문구, value 불변)

- [ ] **Step 3: fmt + validate**

Run: `cd /Users/kylekim1223/request700k/cledyu-aws-ingress/infra/terraform/aws && terraform fmt && terraform validate`
Expected: `Success! The configuration is valid.`

- [ ] **Step 4: 커밋**

```bash
cd /Users/kylekim1223/request700k/cledyu-aws-ingress
git add infra/terraform/aws/outputs.tf
git commit -m "feat(infra): 공개 진입점 output 추가(app/api 레코드·waf acl arn)

검증용으로 public_app_record/public_api_record(fqdn)과 public_waf_web_acl_arn 을
노출하고, public_alb_dns_name description 을 3-host 로 정합한다."
```

---

### Task 5: gitops web/api Ingress+Certificate 멀티호스트(host→hosts)

**Files:**
- Modify: `gitops/apps/web/values.yaml:13-15` (ingress 블록)
- Modify: `gitops/apps/web/templates/ingress.yaml`
- Modify: `gitops/apps/web/templates/certificate.yaml`
- Modify: `gitops/apps/api/values.yaml:11-13` (ingress 블록)
- Modify: `gitops/apps/api/templates/ingress.yaml`
- Modify: `gitops/apps/api/templates/certificate.yaml`

**Interfaces:**
- Consumes: `.Values.ingress.hosts`(리스트), `.Values.ingress.tlsSecret`.
- Produces: web는 `[app.cledyu.local, app.cledyu.com]`, api는 `[api.cledyu.local, api.cledyu.com]` 두 호스트에 대해 Ingress rule·TLS·Certificate dnsNames를 렌더. backend 포트는 기존값(80) 유지.

- [ ] **Step 1: web values.yaml ingress를 hosts 리스트로** — `gitops/apps/web/values.yaml`의 ingress 블록 교체

```yaml
ingress:
  # .local = Tailscale 내부(비인증 라우팅 확인용), .com = 공개(ALB→프록시→Traefik).
  # 인증 세션 config(cookieDomain 등)는 api values 에서 .cledyu.com 단일.
  hosts:
    - app.cledyu.local
    - app.cledyu.com
  tlsSecret: web-tls
```

- [ ] **Step 2: web ingress.yaml을 range로** — `gitops/apps/web/templates/ingress.yaml`의 spec 교체(rule을 hosts 수만큼, backend 포트 80 유지)

```yaml
spec:
  ingressClassName: traefik
  tls:
    - hosts:
        {{- range .Values.ingress.hosts }}
        - {{ . }}
        {{- end }}
      secretName: {{ .Values.ingress.tlsSecret }}
  rules:
    {{- range .Values.ingress.hosts }}
    - host: {{ . }}
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: {{ $.Release.Name }}
                port:
                  number: 80
    {{- end }}
```

- [ ] **Step 3: web certificate.yaml을 range로** — `gitops/apps/web/templates/certificate.yaml`의 commonName/dnsNames 교체(내부 CA cledyu-ca가 .com도 발급, 공개신뢰 불필요)

```yaml
  commonName: {{ index .Values.ingress.hosts 0 }}
  dnsNames:
    {{- range .Values.ingress.hosts }}
    - {{ . }}
    {{- end }}
```

- [ ] **Step 4: api values.yaml ingress를 hosts 리스트로** — `gitops/apps/api/values.yaml`의 ingress 블록 교체

```yaml
ingress:
  # .com 은 OAuth 콜백(api.cledyu.com/api/v1/auth/callback) 도달점. 일반 데이터 호출은
  # web 이 in-cluster 로 프록시하므로 .com 은 콜백 경로 위주로 쓰인다.
  hosts:
    - api.cledyu.local
    - api.cledyu.com
  tlsSecret: api-tls
```

- [ ] **Step 5: api ingress.yaml을 range로** — `gitops/apps/api/templates/ingress.yaml`의 spec을 Step 2와 동일 형태로 교체(backend 포트 80 유지)

```yaml
spec:
  ingressClassName: traefik
  tls:
    - hosts:
        {{- range .Values.ingress.hosts }}
        - {{ . }}
        {{- end }}
      secretName: {{ .Values.ingress.tlsSecret }}
  rules:
    {{- range .Values.ingress.hosts }}
    - host: {{ . }}
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: {{ $.Release.Name }}
                port:
                  number: 80
    {{- end }}
```

- [ ] **Step 6: api certificate.yaml을 range로** — `gitops/apps/api/templates/certificate.yaml`의 commonName/dnsNames를 Step 3과 동일 형태로 교체

```yaml
  commonName: {{ index .Values.ingress.hosts 0 }}
  dnsNames:
    {{- range .Values.ingress.hosts }}
    - {{ . }}
    {{- end }}
```

- [ ] **Step 7: helm template 렌더 검증** — 두 앱이 각각 2개 호스트를 렌더하는지 확인

Run:
```bash
cd /Users/kylekim1223/request700k/cledyu-aws-ingress
helm template web gitops/apps/web  | grep -E "host:|dnsNames|- app\.cledyu" 
helm template api gitops/apps/api  | grep -E "host:|dnsNames|- api\.cledyu"
```
Expected: web는 `host: app.cledyu.local`과 `host: app.cledyu.com` 둘 다, Certificate dnsNames에 두 항목. api도 `.local`/`.com` 둘 다.

- [ ] **Step 8: 커밋**

```bash
cd /Users/kylekim1223/request700k/cledyu-aws-ingress
git add gitops/apps/web/values.yaml gitops/apps/web/templates/ingress.yaml gitops/apps/web/templates/certificate.yaml \
        gitops/apps/api/values.yaml gitops/apps/api/templates/ingress.yaml gitops/apps/api/templates/certificate.yaml
git commit -m "feat(k8s): web/api ingress·certificate 를 .com 병행 멀티호스트로

ingress.host(단일 문자열)를 ingress.hosts(리스트)로 바꿔 app/api.cledyu.local 에
app/api.cledyu.com 을 추가한다. traefik 이 두 호스트를 같은 백엔드로 라우팅하고 내부 CA
가 .com san 을 발급한다(공개 tls 는 alb acm 이 종단하므로 공개신뢰 불필요). 인증 세션은
.com 단일이며 .local 은 비인증 라우팅만 잔존."
```

---

### Task 6: api Keycloak config .local → .com 플립

**Files:**
- Modify: `gitops/apps/api/values.yaml` (keycloak 블록: redirectUri/cookieDomain/frontendUrl)

**Interfaces:**
- Consumes: 없음(값 변경만). `keycloak.url`은 이미 `https://auth.cledyu.com`(불변).
- Produces: api Pod env `CLEDYU_KEYCLOAK_REDIRECT_URI`/`CLEDYU_KEYCLOAK_COOKIE_DOMAIN`/frontendUrl가 `.cledyu.com` 기준. 세션쿠키 도메인 `.cledyu.com`.

- [ ] **Step 1: keycloak 블록의 3개 값 플립** — `gitops/apps/api/values.yaml`의 keycloak 블록에서 아래 3줄만 교체(다른 키·주석 유지)

```yaml
  redirectUri: "https://api.cledyu.com/api/v1/auth/callback"
  cookieDomain: ".cledyu.com"
  frontendUrl: "https://app.cledyu.com"
```

- [ ] **Step 2: helm template로 env 반영 확인**

Run:
```bash
cd /Users/kylekim1223/request700k/cledyu-aws-ingress
helm template api gitops/apps/api | grep -A1 -E "CLEDYU_KEYCLOAK_REDIRECT_URI|CLEDYU_KEYCLOAK_COOKIE_DOMAIN"
```
Expected: redirect uri = `https://api.cledyu.com/api/v1/auth/callback`, cookie domain = `.cledyu.com`.

- [ ] **Step 3: 커밋**

```bash
cd /Users/kylekim1223/request700k/cledyu-aws-ingress
git add gitops/apps/api/values.yaml
git commit -m "feat(k8s): api keycloak 세션 config 를 .cledyu.com 으로 플립

공개 학습자 e2e 를 위해 redirectUri/cookieDomain/frontendUrl 을 .cledyu.local →
.cledyu.com 으로 전환한다(keycloak.url 은 이미 auth.cledyu.com). 세션쿠키 도메인이
.cledyu.com 이 되어 app/api.cledyu.com 간 쿠키가 공유된다. .local 인증 경로는 은퇴."
```

---

### Task 7: Keycloak web 클라이언트 redirect URIs에 .com 추가 (tracked example만; 실제 apply는 Task 8)

**정정(실행 중 발견):** `infra/terraform/keycloak/terraform.tfvars`는 `.gitignore`(`*.tfvars`)로
**추적되지 않는 시크릿 파일**(admin/client/user 비밀번호 평문 포함)이라 커밋 불가·worktree 미체크아웃.
따라서 이 태스크는 **추적되는 템플릿 `terraform.tfvars.example`의 web 클라이언트만** 갱신해 의도를
문서화하고, **실제 realm 적용(untracked `terraform.tfvars` 동일 수정 + keycloak `terraform apply`)은
Task 8 수동 단계**로 넘긴다. (사용자 승인 2026-07-06.)

**Files:**
- Modify: `infra/terraform/keycloak/terraform.tfvars.example` (learn_oidc_clients.web 블록)

**Interfaces:**
- Consumes: 기존 `learn_oidc_clients` 변수 스키마(clients-learn.tf가 소비).
- Produces: 추적 템플릿이 `web` 클라이언트의 `.com` redirect/logout/origin을 문서화. 실제 허용은 Task 8 apply 후 적용.

- [ ] **Step 1: example의 web 클라이언트 URI 리스트에 .com 추가** — `terraform.tfvars.example`의 `learn_oidc_clients.web`에서 3줄 교체(기존 `.local` 유지하고 `.com` 추가). `root_url`은 `.local` 유지.

```hcl
    valid_redirect_uris             = ["https://api.cledyu.local/api/v1/auth/callback", "https://api.cledyu.com/api/v1/auth/callback"]
    valid_post_logout_redirect_uris = ["https://app.cledyu.local/", "https://app.cledyu.com/"]
    web_origins                     = ["https://app.cledyu.local", "https://app.cledyu.com"]
```

- [ ] **Step 2: fmt + grep 검증** — `.example`은 terraform이 자동 로드/validate하지 않으므로 fmt와 grep으로 확인

Run: `cd /Users/kylekim1223/request700k/cledyu-aws-ingress/infra/terraform/keycloak && terraform fmt terraform.tfvars.example`
그리고 web 블록에 `.local`/`.com`이 모두 들어갔는지 grep 확인.

- [ ] **Step 3: 커밋**

```bash
cd /Users/kylekim1223/request700k/cledyu-aws-ingress
git add infra/terraform/keycloak/terraform.tfvars.example
git commit -m "feat(sec): cledyu-learn web 클라이언트 예제에 .cledyu.com redirect 추가

공개 e2e 를 위해 terraform.tfvars.example 의 web 클라이언트 valid_redirect_uris/
post_logout/web_origins 에 api.cledyu.com 콜백과 app.cledyu.com 을 추가한다(.local
유지). 실제 적용은 gitignore 된 terraform.tfvars 를 동일하게 수정 후 keycloak
terraform apply 로 수행한다(Task 8, 시크릿 파일이라 커밋하지 않음)."
```

---

### Task 8: 게이트 apply + E2E 검증 (수동, 사용자 실행)

이 태스크는 실제 AWS·클러스터·구글 계정이 필요해 코드 변경이 아니라 **검증 체크리스트**다. PR 머지 전/후 순서로 사용자가 실행하고 결과 로그를 PR 본문(작업요약 포맷)에 첨부한다.

**Interfaces:**
- Consumes: Task 1~7의 커밋. `AWS_PROFILE=cledyu`, `TF_VAR_tailscale_auth_key`, kubeconfig, 구글 OAuth 클라이언트(auth.cledyu.com 등록됨).

- [ ] **Step 1: aws plan 리뷰** — 예상 변경만 나오는지 확인

Run:
```bash
cd /Users/kylekim1223/request700k/cledyu-aws-ingress/infra/terraform/aws
export AWS_PROFILE=cledyu AWS_REGION=ap-northeast-2
terraform init -input=false
TF_VAR_enable_public_ingress=true TF_VAR_tailscale_auth_key=$KEY terraform plan -input=false -no-color | tee /tmp/aws-ingress-plan.txt
```
Expected: ACM 교체(*.cledyu.com), Route53 app/api 신규 2개 + auth는 moved로 no-op, WAF WebACL+association 신규, 프록시 user_data 변경 → aws_instance.proxy replace, 타깃그룹 health_check 갱신. 그 외 무관 리소스 변경 없음.

- [ ] **Step 2: aws apply**

Run: `TF_VAR_enable_public_ingress=true TF_VAR_tailscale_auth_key=$KEY terraform apply -input=false` (plan 확인 후 yes)

- [ ] **Step 3: 인프라 실측**

```bash
# ACM
aws acm describe-certificate --certificate-arn $(terraform output -raw ...) --query 'Certificate.[Status,SubjectAlternativeNames]'
# DNS → ALB
for h in app api auth; do dig +short $h.cledyu.com; done
# 공개 TLS·상태코드
curl -vI https://app.cledyu.com 2>&1 | grep -E "issuer|subject|HTTP/"
# 프록시 헬스체크·tailnet
ssh <proxy> 'tailscale status | head; curl -s -o /dev/null -w "%{http_code}" localhost/healthz'
# WAF count 관측(오탐 확인)
aws wafv2 get-sampled-requests --web-acl-arn $(terraform output -raw public_waf_web_acl_arn) ...
```
Expected: ACM ISSUED + SAN에 `*.cledyu.com`/`cledyu.com`, 세 호스트 dig가 ALB, curl TLS가 ACM 체인·2xx/3xx, 프록시 `/healthz`=200 및 tailnet 라우트 수신.

- [ ] **Step 4: gitops PR 머지 → ArgoCD 롤아웃** — Task 5·6 커밋을 PR로(필수 리뷰→admin merge, 사용자 승인 하). ArgoCD가 web/api 롤아웃 후 api Pod env에 `.com` 값 반영 확인(memory: API server proxy로 실측, 레포 grep을 실재 확인으로 과장 금지).

- [ ] **Step 5: keycloak apply (untracked tfvars 수동 수정)** — Task 7은 추적 템플릿(`terraform.tfvars.example`)만 갱신했다. 실제 realm 반영은 **메인 체크아웃의 gitignore된 `infra/terraform/keycloak/terraform.tfvars`** 의 `learn_oidc_clients.web` 블록을 example과 동일하게(`.com` redirect/logout/origin 추가) 수정한 뒤 `cd infra/terraform/keycloak && terraform apply`. 이 파일은 시크릿을 포함하므로 커밋하지 않는다.

- [ ] **Step 6: E2E(성공 기준)** — 외부망(테더링 등, 비-Tailscale)에서 `https://app.cledyu.com` → "구글로 로그인" → 구글 동의 → `auth.cledyu.com` 콜백 → `app.cledyu.com` 복귀·로그인 유지 → 랩 시작 → `api.cledyu.com` 콜백·세션쿠키 `.cledyu.com`·랩 세션 기동. 실패 시 6장 실패모드 표로 진단.

- [ ] **Step 7: WAF block 전환** — sampled requests에서 정상 학습자 오탐 0 확인 후, `waf.tf`의 관리형 룰 3종 `override_action`을 `count {}` → `none {}`으로 바꿔 재apply. 커밋:

```bash
git commit -m "chore(infra): waf 관리형 룰을 count→block 으로 전환(오탐 0 확인 후)"
```

- [ ] **Step 8: 데모 종료 절차 문서화** — `docs/RUNBOOK/learner-auth.md`에 `enable_public_ingress=false` 원클릭 철거 절차 반영(노출·과금 종료).

---

## 검증 계획 요약(회귀/CI)

- terraform: CI에서 `fmt -check`/`validate`/`tflint`(deprecated interpolation·미사용 변수) 게이트.
- gitops: `helm template` 렌더 통과, web/api 단위테스트·`go build`·eslint·`next build`(dev 서버 중복 실행 금지 — 별도 디렉터리/포트).
- 산출물: 각 단계 실측 로그를 작업요약(작업설명/하위작업/작업내용) 포맷으로 PR 본문 첨부.

## 미해결/후속

- apex `cledyu.com` 랜딩페이지는 범위 밖(SAN만 확보).
- 프록시 단일 장애점 — 데모 규모 수용. 필요 시 후속으로 프록시 다중화/ASG.
