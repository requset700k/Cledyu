# EKS Cold DR 기반. enable_eks_dr=false 면 count=0 → 평시 리소스 없음(비용 0).
locals {
  eks_dr_enabled = var.enable_eks_dr ? 1 : 0
  eks_dr_active  = var.eks_dr_active ? 1 : 0 # pilot-light hot(NAT·엔드포인트·bastion 인스턴스)
  eks_dr_name    = "cledyu-dr"
  eks_dr_tags    = { Project = "cledyu", Purpose = "dr", ManagedBy = "terraform" }

  # ⚠️ 클러스터 admin·KMS 키 관리자를 **명시**한다 — caller identity 를 쓰면 안 된다.
  #
  # eks 모듈은 둘 다 "지금 terraform 을 치는 principal"로 기본 대체한다:
  #   · enable_cluster_creator_admin_permissions=true → access entry 를 caller 로 생성
  #   · kms_key_administrators=[]                     → coalescelist(..., [session_context.issuer_arn])
  # 지금까지는 런북대로 **사람이** apply 해서 user/kcy 였는데, DR 페일오버 [2] 가 **CodeBuild** 로
  # apply 하게 되면서 2026-07-15 T1 실측에서 둘 다 CodeBuild 롤로 넘어갔다(access entry 2 destroyed +
  # KMS 키 정책 1 changed). 사람이 apply 하면 다시 뒤집혀 **terraform 이 영원히 수렴하지 않는다.**
  #
  # 페일오버 자체는 안 깨진다(bastion entry 는 아래 access_entries 로 명시, KMS 는 root 문이 있어
  # 계정 락아웃 없음). 진짜 문제는 **페일오버 경로에 destroy 가 상시로 뜨는 것**이다 — 운영자가
  # destroy 줄을 안 읽게 되고, 그건 `-var` 누락으로 warm DR 129개가 날아가는 사고를 놓치는 훈련이 된다.
  #
  # account_id 는 누가 apply 하든 같으므로(같은 계정) 이 값은 결정적이다.
  eks_dr_admin_arn = "arn:aws:iam::${data.aws_caller_identity.current.account_id}:user/kcy"
}

module "eks_dr_vpc" {
  source  = "terraform-aws-modules/vpc/aws"
  version = "~> 5.13"
  count   = local.eks_dr_enabled

  name = "${local.eks_dr_name}-vpc"
  cidr = "10.90.0.0/16"

  azs             = ["ap-northeast-2a", "ap-northeast-2b", "ap-northeast-2c"]
  private_subnets = ["10.90.1.0/24", "10.90.2.0/24", "10.90.3.0/24"]
  public_subnets  = ["10.90.101.0/24", "10.90.102.0/24", "10.90.103.0/24"]

  enable_nat_gateway = var.eks_dr_active # pilot-light: 평시 미생성(노드 0이라 egress 불요), 재해 시 생성
  single_nat_gateway = true

  # ALB Controller / EKS 서브넷 자동 발견 태그
  public_subnet_tags  = { "kubernetes.io/role/elb" = "1" }
  private_subnet_tags = { "kubernetes.io/role/internal-elb" = "1" }
  tags                = local.eks_dr_tags
}

module "eks_dr" {
  source  = "terraform-aws-modules/eks/aws"
  version = "~> 20.24"
  count   = local.eks_dr_enabled

  cluster_name    = local.eks_dr_name
  cluster_version = var.eks_dr_cluster_version

  # 퍼블릭은 차단하고 프라이빗 엔드포인트만 활성화
  cluster_endpoint_public_access  = false
  cluster_endpoint_private_access = true

  # Bastion 보안 그룹에서 오는 443(HTTPS) 트래픽을 EKS API 서버가 허용하도록 설정
  cluster_security_group_additional_rules = {
    ingress_bastion = {
      description              = "Allow Bastion host to access K8s API server"
      protocol                 = "tcp"
      from_port                = 443
      to_port                  = 443
      type                     = "ingress"
      source_security_group_id = aws_security_group.eks_dr_bastion[0].id
    }
  }

  # 컨트롤 플레인 로깅 활성화
  cluster_enabled_log_types = ["audit", "authenticator"]

  # KMS Envelope 암호화 활성화 (Secret 자원 보호)
  create_kms_key = true
  cluster_encryption_config = {
    resources = ["secrets"]
  }
  # ⚠️ 비우면 모듈이 caller identity 로 대체한다(main.tf:316 coalescelist) → apply 주체마다 키 정책이
  # 뒤집힌다. 명시 필수(§locals.eks_dr_admin_arn).
  kms_key_administrators = [local.eks_dr_admin_arn]

  enable_irsa = true

  vpc_id     = module.eks_dr_vpc[0].vpc_id
  subnet_ids = module.eks_dr_vpc[0].private_subnets

  eks_managed_node_groups = {
    dr = {
      instance_types = [var.eks_dr_node_instance_type]
      capacity_type  = "ON_DEMAND"
      desired_size   = var.eks_dr_node_desired # 최초 생성값(기본 0). 모듈이 이후 desired 변경을 ignore → 실제 스케일은 CLI(§Global P1)
      min_size       = 0
      max_size       = var.eks_dr_node_max
    }
  }

  # ⚠️ false 필수 — true 면 모듈이 "terraform apply principal"을 admin 으로 넣는다(main.tf:243).
  # DR 페일오버 [2] 는 CodeBuild 가 apply 하므로 사람↔CodeBuild 롤로 매 apply 마다 뒤집힌다.
  # 운영자는 아래 access_entries.operator 로 **명시**한다(§locals.eks_dr_admin_arn).
  enable_cluster_creator_admin_permissions = false

  # private-only 엔드포인트라 kubectl 은 bastion(eks-dr-bastion.tf)에서만 가능하고,
  # 그 kubectl 은 bastion instance profile 롤로 인증된다 → 이 롤을 cluster admin 에 매핑.
  access_entries = {
    bastion = {
      principal_arn = aws_iam_role.eks_dr_bastion[0].arn
      policy_associations = {
        admin = {
          policy_arn   = "arn:aws:eks::aws:cluster-access-policy/AmazonEKSClusterAdminPolicy"
          access_scope = { type = "cluster" }
        }
      }
    }
    # 운영자(사람). private 엔드포인트라 노트북 kubectl 은 어차피 불가하지만, EKS 콘솔 가시성과
    # bastion 경유 디버깅의 근거가 된다. 예전엔 enable_cluster_creator_admin_permissions 가
    # 암묵적으로 만들어주던 것을 명시로 바꾼 것이다(2026-07-15 T1 실측).
    operator = {
      principal_arn = local.eks_dr_admin_arn
      policy_associations = {
        admin = {
          policy_arn   = "arn:aws:eks::aws:cluster-access-policy/AmazonEKSClusterAdminPolicy"
          access_scope = { type = "cluster" }
        }
      }
    }
  }

  # ⚠️ CodeBuild 롤(cledyu-lab-dr-failover-tf)은 **일부러 넣지 않는다.** versions.tf 에 kubernetes
  # provider 가 없어 terraform 이 k8s API 를 호출하지 않는다 → 클러스터 접근이 필요 없다.
  # T1 실측에서 받아간 admin 은 애초에 불필요한 권한이었다.
  tags = local.eks_dr_tags

  # pilot-light warm(node0): coredns·aws-ebs-csi-driver 는 Deployment 라 스케줄할 노드가 없으면
  # DEGRADED(InsufficientNumberOfReplicas) → terraform aws_eks_addon 이 ACTIVE 대기 중 ~20분 후
  # 타임아웃해 warm apply 를 블록한다. 그래서 warm 에는 DaemonSet 애드온(kube-proxy·vpc-cni —
  # node0 이면 desired 0 이라 ACTIVE)만 두고, coredns·ebs-csi 는 재해 시 노드 스케일 직후 CLI
  # (aws eks create-addon)로 설치한다(노드 스케일이 이미 out-of-band CLI 인 것과 동일 패턴, §런북
  # Phase 1). ebs-csi 는 warm 으로 상시 유지되는 IRSA(eks_dr_ebs_csi_irsa)를
  # --service-account-role-arn 으로 참조한다(롤명 cledyu-dr-ebs-csi 결정적).
  cluster_addons = {
    kube-proxy = {}
    vpc-cni    = {}
  }
}

# EBS CSI 애드온용 IRSA — gp3 StorageClass(apps-eks)가 이 SA 로 EBS 볼륨을 프로비저닝한다.
module "eks_dr_ebs_csi_irsa" {
  source  = "terraform-aws-modules/iam/aws//modules/iam-role-for-service-accounts-eks"
  version = "~> 5.44"
  count   = local.eks_dr_enabled

  role_name             = "${local.eks_dr_name}-ebs-csi"
  attach_ebs_csi_policy = true
  oidc_providers = {
    main = {
      provider_arn               = module.eks_dr[0].oidc_provider_arn
      namespace_service_accounts = ["kube-system:ebs-csi-controller-sa"]
    }
  }
  tags = local.eks_dr_tags
}

# AWS Load Balancer Controller IRSA — Helm 차트 자체는 Phase 2(apps-eks)에서 배포, 이 롤 ARN 을 SA 에 annotation.
module "eks_dr_alb_irsa" {
  source  = "terraform-aws-modules/iam/aws//modules/iam-role-for-service-accounts-eks"
  version = "~> 5.44"
  count   = local.eks_dr_enabled

  role_name                              = "${local.eks_dr_name}-alb-controller"
  attach_load_balancer_controller_policy = true
  oidc_providers = {
    main = {
      provider_arn               = module.eks_dr[0].oidc_provider_arn
      namespace_service_accounts = ["kube-system:aws-load-balancer-controller"]
    }
  }
  tags = local.eks_dr_tags
}

# 인터페이스 엔드포인트(KMS/STS) 전용 SG — VPC 내부에서 443 인바운드 허용.
# 노드 SG를 그대로 쓰면 self-443 ingress 규칙이 없어(ephemeral 1025-65535·coredns 53만 self 허용)
# 노드가 엔드포인트 ENI:443 에 도달 못 해 KMS/STS 호출이 막힌다 → Vault unseal·IRSA 실패.
resource "aws_security_group" "eks_dr_endpoints" {
  count       = local.eks_dr_enabled
  name_prefix = "${local.eks_dr_name}-vpce-"
  description = "EKS DR interface endpoints - 443 inbound from VPC"
  vpc_id      = module.eks_dr_vpc[0].vpc_id

  ingress {
    description = "HTTPS from VPC (nodes to KMS/STS interface endpoints)"
    from_port   = 443
    to_port     = 443
    protocol    = "tcp"
    cidr_blocks = [module.eks_dr_vpc[0].vpc_cidr_block]
  }

  tags = local.eks_dr_tags
}

resource "aws_security_group" "eks_dr_bastion" {
  count       = local.eks_dr_enabled
  name        = "${local.eks_dr_name}-bastion-sg"
  description = "Security group for EKS DR Bastion host"
  vpc_id      = module.eks_dr_vpc[0].vpc_id

  # AWS SSM Session Manager를 사용하면 인바운드 규칙(Port 22 등)을 모두 비워둬도 된다.
  # 만약 특정 IP에서 SSH 접근을 해야 한다면 인바운드를 추가

  egress {
    description = "Allow all outbound traffic"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = local.eks_dr_tags
}

# 프라이빗 서브넷 S3/KMS/STS 엔드포인트. github/ghcr(이미지)는 NAT 로 egress(ECR 미사용, 자격 불필요 — Plan B 스펙 F5).
module "eks_dr_endpoints" {
  source  = "terraform-aws-modules/vpc/aws//modules/vpc-endpoints"
  version = "~> 5.13"
  count   = local.eks_dr_active

  vpc_id = module.eks_dr_vpc[0].vpc_id
  endpoints = {
    s3 = {
      service         = "s3"
      service_type    = "Gateway"
      route_table_ids = module.eks_dr_vpc[0].private_route_table_ids
    }
    kms = {
      service             = "kms"
      private_dns_enabled = true
      subnet_ids          = module.eks_dr_vpc[0].private_subnets
      security_group_ids  = [aws_security_group.eks_dr_endpoints[0].id]
    }
    sts = {
      service             = "sts"
      private_dns_enabled = true
      subnet_ids          = module.eks_dr_vpc[0].private_subnets
      security_group_ids  = [aws_security_group.eks_dr_endpoints[0].id]
    }
  }
  tags = local.eks_dr_tags
}
