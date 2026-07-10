# EKS DR bastion — private-only 엔드포인트(eks-dr.tf)로 전환하면서 운영자가 클러스터
# API 에 도달할 유일한 경로. 프라이빗 서브넷에 두고 SSM Session Manager 로만 진입한다
# (인바운드 22 불필요, 퍼블릭 IP 없음). NAT(eks-dr.tf enable_nat_gateway)로 SSM/패키지 egress.
# 드릴(enable_eks_dr=true)에서만 생성 → 평시 비용 0.
#
# 접근 흐름: 운영자 → aws ssm start-session → bastion(eks_dr_bastion SG) → 클러스터 SG 443 허용
#           → private EKS API. kubectl 은 instance profile 롤로 인증되고, 그 롤은 아래
#           access_entries(eks-dr.tf)로 EKSClusterAdmin 에 매핑된다.

# 최신 Amazon Linux 2023 x86_64 AMI (노드와 동일 amd64).
data "aws_ssm_parameter" "eks_dr_bastion_ami" {
  count = local.eks_dr_enabled
  name  = "/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-x86_64"
}

data "aws_iam_policy_document" "eks_dr_bastion_assume" {
  count = local.eks_dr_enabled
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "eks_dr_bastion" {
  count              = local.eks_dr_enabled
  name               = "${local.eks_dr_name}-bastion"
  assume_role_policy = data.aws_iam_policy_document.eks_dr_bastion_assume[0].json
  tags               = local.eks_dr_tags
}

resource "aws_iam_role_policy_attachment" "eks_dr_bastion_ssm" {
  count      = local.eks_dr_enabled
  role       = aws_iam_role.eks_dr_bastion[0].name
  policy_arn = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

# update-kubeconfig 가 호출하는 eks:DescribeCluster 만 부여(read). 클러스터 ARN 은
# 값으로 구성해 module.eks_dr 를 참조하지 않는다 — access_entries(module)↔롤 간 순환 방지.
data "aws_iam_policy_document" "eks_dr_bastion_describe" {
  count = local.eks_dr_enabled
  statement {
    actions   = ["eks:DescribeCluster"]
    resources = ["arn:aws:eks:${var.region}:${data.aws_caller_identity.current.account_id}:cluster/${local.eks_dr_name}"]
  }
}

resource "aws_iam_role_policy" "eks_dr_bastion_describe" {
  count  = local.eks_dr_enabled
  name   = "eks-describe"
  role   = aws_iam_role.eks_dr_bastion[0].id
  policy = data.aws_iam_policy_document.eks_dr_bastion_describe[0].json
}

resource "aws_iam_instance_profile" "eks_dr_bastion" {
  count = local.eks_dr_enabled
  name  = "${local.eks_dr_name}-bastion"
  role  = aws_iam_role.eks_dr_bastion[0].name
}

resource "aws_instance" "eks_dr_bastion" {
  count                  = local.eks_dr_enabled
  ami                    = data.aws_ssm_parameter.eks_dr_bastion_ami[0].value
  instance_type          = "t3.small" # 드릴용 kubectl 발판, 최소 사양
  subnet_id              = module.eks_dr_vpc[0].private_subnets[0]
  vpc_security_group_ids = [aws_security_group.eks_dr_bastion[0].id]
  iam_instance_profile   = aws_iam_instance_profile.eks_dr_bastion[0].name

  # SSM 정책 attachment 를 명시적 의존으로 묶어 -target 재생성 시 SSM 접속이 끊기지 않게 한다
  # (proxy 인스턴스와 동일 패턴).
  depends_on = [aws_iam_role_policy_attachment.eks_dr_bastion_ssm]

  metadata_options {
    http_tokens   = "required"
    http_endpoint = "enabled"
  }

  root_block_device {
    volume_size = 10
    volume_type = "gp3"
    encrypted   = true
  }

  # 드릴 발판을 kubectl/awscli 로 준비. NAT egress 로 패키지 취득.
  user_data = base64encode(<<-EOT
    #!/bin/bash
    set -euxo pipefail
    dnf install -y unzip
    curl -sSLo /usr/local/bin/kubectl "https://dl.k8s.io/release/v${var.eks_dr_cluster_version}.0/bin/linux/amd64/kubectl"
    chmod +x /usr/local/bin/kubectl
    curl -sSL "https://awscli.amazonaws.com/awscli-exe-linux-x86_64.zip" -o /tmp/awscliv2.zip
    unzip -q /tmp/awscliv2.zip -d /tmp
    /tmp/aws/install
  EOT
  )

  tags = merge(local.eks_dr_tags, { Name = "${local.eks_dr_name}-bastion" })
}
