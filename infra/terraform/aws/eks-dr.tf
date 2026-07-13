# EKS Cold DR 기반. enable_eks_dr=false 면 count=0 → 평시 리소스 없음(비용 0).
locals {
  eks_dr_enabled = var.enable_eks_dr ? 1 : 0
  eks_dr_active  = var.eks_dr_active ? 1 : 0 # pilot-light hot(NAT·엔드포인트·bastion 인스턴스)
  eks_dr_name    = "cledyu-dr"
  eks_dr_tags    = { Project = "cledyu", Purpose = "dr", ManagedBy = "terraform" }
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

  # 부트스트랩 운영자(terraform apply principal)가 관리자로 접근.
  enable_cluster_creator_admin_permissions = true

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
  }
  tags = local.eks_dr_tags

  cluster_addons = {
    aws-ebs-csi-driver = {
      service_account_role_arn = module.eks_dr_ebs_csi_irsa[0].iam_role_arn
    }
    coredns    = {}
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
