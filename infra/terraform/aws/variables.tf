variable "region" {
  description = <<-EOT
    EC2 오버플로우 리전. 온프렘과 가까운 서울 리전을 기본값으로 둔다.
    이 스택은 ap-northeast-2 단일 리전 전제다(S3 state·백업 버킷·KMS·서브넷·pin 된 var.ami_id 가
    모두 리전 종속). 다른 리전을 쓰려면 ami_id 등 리전 종속 값을 함께 교체해야 하므로 validation
    으로 막아 둔다 — 의도적 멀티리전 시 이 validation 을 완화하고 리전별 값을 정비할 것.
  EOT
  type        = string
  default     = "ap-northeast-2"

  validation {
    condition     = var.region == "ap-northeast-2"
    error_message = "이 스택은 ap-northeast-2 전용이다(pin 된 ami_id·리전 종속 리소스). 다른 리전은 ami_id 등 리전 값 정비 후 이 validation 을 완화해 사용."
  }
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
    세션(lab) 런치 템플릿 AMI(ap-northeast-2). 운영 tfvars 는 베이크된 lab-base 이미지로 설정한다
    (EC2 오버플로우 세션 VM 이 code-server·tailscale 등 프리베이크를 쓰기 위함). 빈 값이면 Canonical
    Ubuntu 22.04(amd64) '최신' 을 자동 조회하는데(data.aws_ami.ubuntu, most_recent), 신규 릴리스마다
    lab_session 런치 템플릿이 드리프트하므로 현재 AMI 로 pin 한다(2026-07). **프록시는 이 값을 쓰지
    않는다** — 경량 Caddy 프록시는 stock Ubuntu(var.proxy_ami_id)로 분리했다. lab-base 이미지 위에
    프록시를 띄우면 lab 자체 cloud-init 과 충돌하거나 불필요하게 무겁다. README 의 'AMI 전략' 참고.
  EOT
  type        = string
  default     = "ami-0afe1fd15675c3f15"
}

variable "proxy_ami_id" {
  description = <<-EOT
    tailnet 리버스프록시(Caddy) 전용 AMI(ap-northeast-2). 프록시는 경량 stock Ubuntu 로 충분하고
    세션 VM 용 lab-base 이미지(var.ami_id)와 무관하므로 분리해 pin 한다. 기본값은 현재 프록시가
    실행 중인 Canonical Ubuntu 22.04(amd64) — var.ami_id 를 lab-base 로 바꿔도 프록시는 이 값을
    유지해 강제 교체·잘못된 이미지(lab cloud-init 충돌) 부팅을 막는다. 최신 stock 으로 갱신하려면
    이 값을 새 Ubuntu AMI 로 교체한다.
  EOT
  type        = string
  default     = "ami-0afe1fd15675c3f15"
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
# auth.cledyu.com 로 공개 노출하는 Route53 + ACM + ALB + tailnet 리버스프록시 스택.
# 홈랩 클러스터가 NAT 뒤라 ALB 가 직접 Keycloak 을 타겟할 수 없으므로, VPC 안에
# tailnet 가입 프록시 인스턴스를 두고 ALB → 프록시 → (tailnet) → Keycloak 으로 잇는다.
# enable_public_ingress=false 면 이 스택은 생성되지 않는다(opt-in). 절차는
# docs/RUNBOOK/learner-auth.md 의 '공개 노출(auth.cledyu.com)' 절 참고.
# ─────────────────────────────────────────────────────────────────────────

variable "enable_public_ingress" {
  description = "공개 진입점 스택(Route53/ACM/ALB/프록시) 생성 여부. 도메인 위임·tailscale authkey 준비 후 true."
  type        = bool
  default     = false
}

variable "dr_detection_armed" {
  description = <<-EOT
    DR 복합알람(<prefix>-dr-disaster)의 알림 발동(SNS→Lambda→Discord) 무장 여부.
    false(기본): 알람은 상태를 평가하되 알림을 쏘지 않는다 — 최초 배포·bring-up 중 heartbeat
    동기화 지연이나 pull 미준비로 복합알람이 ALARM 이 돼도 거짓 재해 알림이 안 나간다.
    두 신호(pull·push)가 healthy 임을 실측 확인한 뒤 true 로 바꿔 재apply 해 무장한다.
    (런타임 enable-alarm-actions 대신 변수로 둬 이후 apply 에 되돌려지지 않게 한다.)
  EOT
  type        = bool
  default     = false
}

variable "public_domain" {
  description = "공개 루트 도메인(Route53 hosted zone 으로 관리). 예 cledyu.com. NS 를 도메인 등록기관에 위임해야 한다."
  type        = string
  default     = "cledyu.com"
}

variable "public_keycloak_host" {
  description = "Keycloak 공개 FQDN. 구글 OAuth redirect URI 의 호스트가 된다(.../realms/cledyu-learn/broker/google/endpoint)."
  type        = string
  default     = "auth.cledyu.com"
}

variable "keycloak_upstream_url" {
  description = <<-EOT
    프록시가 auth.cledyu.com 요청을 포워딩할 tailnet 상의 Keycloak 업스트림 URL.
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
  default     = "t3.micro"
}

variable "public_ingress_allowed_cidrs" {
  description = "ALB 443/80 인바운드 허용 CIDR. 기본은 공개(0.0.0.0/0) — 검증 단계에서 사무실 IP 로 좁힐 수 있다."
  type        = list(string)
  default     = ["0.0.0.0/0"]
}

variable "github_repo" {
  # OIDC sub 클레임은 레포 정식 대소문자(requset700k/Cledyu)를 쓰고 StringLike 는 대소문자를
  # 구분하므로, 소문자로 두면 AssumeRoleWithWebIdentity 가 거부된다(정식 대문자 C 사용).
  description = "베이커 워크플로를 실행하는 GitHub 레포(owner/name). OIDC sub 제한에 사용."
  type        = string
  default     = "requset700k/Cledyu"
}

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

variable "waf_rate_limit" {
  description = "WAF rate-based 룰의 IP당 5분(기본 평가창) 요청 상한. 초과 시 block. 데모 부하 기준 2000."
  type        = number
  default     = 2000
}

variable "alert_email" {
  description = "공개진입점 CloudWatch 알람 수신 이메일(SNS 구독). enable_public_ingress=true 면 TF_VAR_alert_email 또는 tfvars 로 반드시 지정(빈 값이면 SNS 구독 apply 실패)."
  type        = string
  default     = ""
}

# ─────────────────────────────────────────────────────────────────────────
# EKS Cold DR — 온프렘 전체 소실 시에만 기동하는 EKS 클러스터(eks-dr.tf).
# enable_eks_dr=false(기본)면 VPC/EKS/노드그룹 리소스가 전혀 생성되지 않는다(평시 비용 0).
# ─────────────────────────────────────────────────────────────────────────

variable "enable_eks_dr" {
  description = "pilot-light: 평시 true 로 warm 스택(컨트롤플레인·노드그룹 0) 상시. false 는 완전 폐기 시만."
  type        = bool
  default     = false
}

variable "eks_dr_cluster_version" {
  description = <<-EOT
    DR EKS 컨트롤플레인 쿠버네티스 버전. 온디맨드로 생성하는 DR 클러스터라 반드시 standard support
    범위로 유지한다 — extended support 버전은 컨트롤플레인 비용이 약 6배로 뛰고, extended 마저 끝나면
    해당 버전 신규 클러스터 생성이 막혀 DR 기동 자체가 실패한다. EKS 버전 캘린더 기준 주기적으로
    최신 standard 버전으로 갱신할 것.
  EOT
  type        = string
  default     = "1.34"
}

variable "eks_dr_node_instance_type" {
  description = "DR EKS 워커 노드 EC2 타입. CNPG(postgres·keycloak-pg)+Vault+api/web/keycloak/argocd/eso 수용, 드릴 신뢰성 위해 on-demand로 고정."
  type        = string
  default     = "m6i.xlarge"
}

variable "eks_dr_node_desired" {
  description = "DR EKS 워커 노드 개수. pilot-light: 평시 0(비용 0), 재해 시 N 으로 스케일. min=0, max=eks_dr_node_max."
  type        = number
  default     = 0
}

variable "eks_dr_node_max" {
  description = "DR EKS 노드그룹 max_size. 재해 시 eks_dr_node_desired 를 이 상한 내에서 올린다."
  type        = number
  default     = 6
}

variable "eks_dr_active" {
  description = <<-EOT
    pilot-light hot 리소스 스위치. 평시 false — NAT·VPC 인터페이스 엔드포인트·bastion 인스턴스 미생성(비용은
    컨트롤플레인만). 재해 시 true — 이들 생성(+ eks_dr_node_desired 로 노드 스케일). warm 스택(VPC·EKS·IRSA·SG·
    bastion 롤)은 enable_eks_dr 로 상시 유지되고 이 값과 무관.
  EOT
  type        = bool
  default     = false

  validation {
    condition     = !var.eks_dr_active || var.enable_eks_dr
    error_message = "eks_dr_active=true 는 warm 스택(enable_eks_dr=true)을 전제한다 — hot 리소스가 warm 리소스의 [0] 인덱스를 참조하기 때문. 두 변수는 함께 true 로만 apply 한다."
  }
}
