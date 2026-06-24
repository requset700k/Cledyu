variable "region" {
  description = "EC2 오버플로우 리전. 온프렘과 가까운 서울 리전을 기본값으로 둔다."
  type        = string
  default     = "ap-northeast-2"
}

variable "vpc_id" {
  description = "세션 인스턴스를 띄울 VPC. 빈 값이면 리전의 default VPC 를 사용한다."
  type        = string
  default     = ""
}

variable "subnet_id" {
  description = "세션 인스턴스 서브넷. 빈 값이면 선택된 VPC 의 서브넷 중 하나를 자동 선택한다."
  type        = string
  default     = ""
}

variable "assign_public_ip" {
  description = <<-EOT
    세션 인스턴스에 퍼블릭 IP 를 할당할지. Launch Template 이 network_interfaces 를 명시하면
    subnet 의 MapPublicIpOnLaunch 가 무시되어 기본 미할당이 되므로, default VPC(IGW) 환경에서는
    true 여야 인스턴스가 인터넷(tailscale 가입·SSM·패키지 설치)에 도달한다.
    private subnet + NAT 구성이면 false 로 둔다.
  EOT
  type        = bool
  default     = true
}

variable "instance_type" {
  description = "세션 인스턴스 타입. Launch Template 기본값이며 api 가 런타임에 오버라이드할 수 있다."
  type        = string
  default     = "t3.medium"
}

variable "ami_id" {
  description = <<-EOT
    세션 인스턴스 AMI. 빈 값이면 Canonical Ubuntu 22.04(amd64) 최신 AMI 를 자동 조회한다.
    운영에서는 SSM Agent·tailscale·code-server 를 미리 구운 커스텀 AMI(packer) ID 를 넣는 것을 권장한다
    (런타임 설치 시간 단축). README 의 'AMI 전략' 참고.
  EOT
  type        = string
  default     = ""
}

variable "root_volume_gb" {
  description = "세션 인스턴스 루트 볼륨 크기(GiB)."
  type        = number
  default     = 20
}

variable "name_prefix" {
  description = "생성 리소스 이름 prefix. 레거시 hackathon 류 금지(레포 네이밍 규칙)."
  type        = string
  default     = "cledyu-lab"
}

variable "budget_limit_usd" {
  description = "EC2 오버플로우 월 예산(USD). 0이면 예산 알람을 만들지 않는다."
  type        = number
  default     = 0
}

variable "budget_notification_emails" {
  description = "예산 임계 도달 시 알림 받을 이메일 목록(budget_limit_usd>0 일 때 사용)."
  type        = list(string)
  default     = []
}

# ─────────────────────────────────────────────────────────────────────────
# 공개 진입점(public ingress) — 학습자 social 로그인을 위해 Keycloak 을
# auth.cledyu.io 로 공개 노출하는 Route53 + ACM + ALB + tailnet 리버스프록시 스택.
# 홈랩 클러스터가 NAT 뒤라 ALB 가 직접 Keycloak 을 타겟할 수 없으므로, VPC 안에
# tailnet 가입 프록시 인스턴스를 두고 ALB → 프록시 → (tailnet) → Keycloak 으로 잇는다.
# enable_public_ingress=false 면 이 스택은 생성되지 않는다(opt-in). 절차는
# docs/RUNBOOK/learner-auth.md 의 '공개 노출(auth.cledyu.io)' 절 참고.
# ─────────────────────────────────────────────────────────────────────────

variable "enable_public_ingress" {
  description = "공개 진입점 스택(Route53/ACM/ALB/프록시) 생성 여부. 도메인 위임·tailscale authkey 준비 후 true."
  type        = bool
  default     = false
}

variable "public_domain" {
  description = "공개 루트 도메인(Route53 hosted zone 으로 관리). 예 cledyu.io. NS 를 도메인 등록기관에 위임해야 한다."
  type        = string
  default     = "cledyu.io"
}

variable "public_keycloak_host" {
  description = "Keycloak 공개 FQDN. 구글 OAuth redirect URI 의 호스트가 된다(.../realms/cledyu-learn/broker/google/endpoint)."
  type        = string
  default     = "auth.cledyu.io"
}

variable "keycloak_upstream_url" {
  description = <<-EOT
    프록시가 auth.cledyu.io 요청을 포워딩할 tailnet 상의 Keycloak 업스트림 URL.
    Cledyu 토폴로지에서는 하이퍼바이저 subnet router 가 10.10.0.0/24 를 tailnet 에
    광고하고 Traefik LB 가 10.10.0.101 이므로 "https://10.10.0.101" 를 쓴다(프록시는
    --accept-routes 로 이 라우트를 받고, Host=public_keycloak_host 로 보내 Traefik 이
    keycloak ingress 로 라우팅). pod/service ClusterIP 는 라우팅 불가하므로 Traefik LB
    경유 필수. Traefik 내부 CA 인증서는 프록시가 검증 생략(tailnet WireGuard 암호화).
  EOT
  type        = string
  default     = "https://10.10.0.101"
}

variable "tailscale_auth_key" {
  description = "프록시 인스턴스가 tailnet 에 가입할 때 쓰는 일회용/재사용 authkey. TF_VAR_tailscale_auth_key 로 주입(state 평문 저장 회피 위해 tfvars 금지)."
  type        = string
  default     = ""
  sensitive   = true
}

variable "proxy_instance_type" {
  description = "tailnet 리버스프록시 인스턴스 타입. 경량 프록시이므로 작게(비용 절감)."
  type        = string
  default     = "t3.nano"
}

variable "public_ingress_allowed_cidrs" {
  description = "ALB 443/80 인바운드 허용 CIDR. 기본은 공개(0.0.0.0/0) — 검증 단계에서 사무실 IP 로 좁힐 수 있다."
  type        = list(string)
  default     = ["0.0.0.0/0"]
}
