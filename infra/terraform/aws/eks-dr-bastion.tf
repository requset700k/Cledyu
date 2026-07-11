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

# Vault raft 스냅샷 수동 복원(런북 §Vault 스냅샷 복원)용. 런북 절차가 bastion 에서
# `aws s3 cp s3://cledyu-lab-dr-backups/vault/...` 를 실행하므로 instance profile 에
# vault/ 프리픽스 read + SSE-KMS(dr_backups CMK) Decrypt 가 있어야 한다. 정적 키를 심지
# 않고(no-static-key 계약) 복원하려면 이 권한을 롤로 줘야 한다. bastion 은 이미 EKS
# ClusterAdmin(복원된 Vault 전체 시크릿 접근 가능)이라 vault/ 스냅샷 read 는 추가 blast
# radius 를 만들지 않는다. vault/ 프리픽스로 한정해 다른 프리픽스(postgres/keycloak) read 는 차단.
data "aws_iam_policy_document" "eks_dr_bastion_vault_restore" {
  count = local.eks_dr_enabled
  statement {
    sid       = "ReadVaultSnapshots"
    actions   = ["s3:GetObject"]
    resources = ["${aws_s3_bucket.dr_backups.arn}/vault/*"]
  }
  # `aws s3 ls s3://.../vault/` 는 prefix=vault/ 로 ListObjectsV2 를 호출한다.
  statement {
    sid       = "ListVaultPrefix"
    actions   = ["s3:ListBucket"]
    resources = [aws_s3_bucket.dr_backups.arn]
    condition {
      test     = "StringLike"
      variable = "s3:prefix"
      values   = ["vault/*", "vault"]
    }
  }
  statement {
    sid       = "BucketLocation"
    actions   = ["s3:GetBucketLocation"]
    resources = [aws_s3_bucket.dr_backups.arn]
  }
  # SSE-KMS 버킷이라 객체 read 에 봉투 키 Decrypt 가 필요(없으면 GetObject 가 KMS 거부).
  statement {
    sid       = "DecryptBackups"
    actions   = ["kms:Decrypt", "kms:DescribeKey"]
    resources = [aws_kms_key.dr_backups.arn]
  }
  # force restore 후 원본 root/recovery 토큰(런북 §4)을 DR 부트스트랩 시크릿에서 취득한다.
  # 정확한 이름은 cledyu/vault/bootstrap(Vault seal 마이그레이션 스택이 관리, terraform 밖).
  # Secrets Manager ARN 은 6자 접미사가 붙어 와일드카드로 매칭하고, vault 프리픽스로 한정한다.
  # 이 시크릿이 기본 aws/secretsmanager 관리형 키로 암호화됐다면 추가 kms 권한 불필요하나,
  # CMK 라면 그 키에 kms:Decrypt 를 별도 부여해야 한다(키 ARN 은 그 스택에서 확인).
  statement {
    sid       = "ReadVaultBootstrapSecret"
    actions   = ["secretsmanager:GetSecretValue"]
    resources = ["arn:aws:secretsmanager:${var.region}:${data.aws_caller_identity.current.account_id}:secret:cledyu/vault/*"]
  }
}

resource "aws_iam_role_policy" "eks_dr_bastion_vault_restore" {
  count  = local.eks_dr_enabled
  name   = "vault-snapshot-read"
  role   = aws_iam_role.eks_dr_bastion[0].id
  policy = data.aws_iam_policy_document.eks_dr_bastion_vault_restore[0].json
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

  # 드릴 발판을 kubectl/git/helm/awscli 로 준비. NAT egress 로 패키지 취득.
  # user_data 에는 raw 텍스트를 넣는다 — provider 가 EC2 로 보낼 때 base64 인코딩하므로
  # base64encode() 로 감싸면 이중 인코딩돼 cloud-init 이 스크립트 대신 base64 문자열을 받아
  # 실행하지 않는다(kubectl 미설치 → bastion kubectl 경로 붕괴). 이미 인코딩된 값은 user_data_base64 로.
  #
  # ⚠️ 부팅 직후엔 NAT/라우트 프로비저닝이 아직이라 egress 가 안 될 수 있다(드릴 실측: curl dl.k8s.io
  # 132s 타임아웃 → kubectl 미설치). 그래서 ① egress 준비까지 대기 루프 ② 모든 curl 에 --retry 를 건다.
  # git/helm 도 여기서 깐다(런북 ArgoCD seed·repo clone 에 필요 — 수동 설치 스텝 제거).
  user_data = <<-EOT
    #!/bin/bash
    set -euxo pipefail
    # ① egress(NAT) 준비까지 대기 — 라우트/NAT 프로비저닝 레이스 흡수(최대 ~5분)
    for i in $(seq 1 60); do curl -fsS -m 5 https://dl.k8s.io >/dev/null 2>&1 && break; echo "wait egress $i/60"; sleep 5; done
    # unzip(awscli), git(repo clone), jq(런북 generate-root/시크릿 파싱) — 명시 설치(AMI 기본 의존 X)
    dnf install -y unzip git jq
    # ② kubectl (--retry 로 일시적 실패 흡수)
    curl -fsSL --retry 8 --retry-delay 5 --retry-connrefused -o /usr/local/bin/kubectl "https://dl.k8s.io/release/v${var.eks_dr_cluster_version}.0/bin/linux/amd64/kubectl"
    chmod +x /usr/local/bin/kubectl
    # ③ awscli v2
    curl -fsSL --retry 8 --retry-delay 5 "https://awscli.amazonaws.com/awscli-exe-linux-x86_64.zip" -o /tmp/awscliv2.zip
    unzip -q /tmp/awscliv2.zip -d /tmp
    /tmp/aws/install
    # ④ helm (ArgoCD seed 에 필요) — 버전 핀. get-helm-3 기본은 latest 라 드릴 시점마다 달라져 재현성이 없다.
    # 레포 표준(infra/images/lab-base/provisioners/50-ansible-helm.sh)과 동일한 v3.21.2 로 고정한다.
    curl -fsSL --retry 8 --retry-delay 5 https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | DESIRED_VERSION=v3.21.2 bash
  EOT

  # user_data(cloud-init)는 인스턴스 최초 launch 때만 실행된다. user_data 만 바꾸고 apply 하면
  # AWS 는 속성만 갱신하고 재부팅·재실행하지 않아, 이미 뜬 bastion 엔 kubectl/git/jq/helm·retry 보강이
  # 반영되지 않는다(실패한 드릴로 인스턴스가 남으면 복구 경로가 계속 깨짐). → user_data 변경 시 강제 교체.
  # (public-ingress.tf proxy 인스턴스와 동일 패턴.)
  user_data_replace_on_change = true

  tags = merge(local.eks_dr_tags, { Name = "${local.eks_dr_name}-bastion" })
}
