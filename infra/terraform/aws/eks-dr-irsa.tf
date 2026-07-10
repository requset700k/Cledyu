# Vault SA(vault:vault)가 awskms seal 로 unseal — 정적 키 없이 IRSA.
data "aws_iam_policy_document" "eks_dr_vault_unseal" {
  count = local.eks_dr_enabled
  statement {
    actions   = ["kms:Encrypt", "kms:Decrypt", "kms:DescribeKey"]
    resources = ["arn:aws:kms:ap-northeast-2:504284203153:key/e29e3ec2-f5e0-4308-af6f-5b576cc99f52"]
  }
}

module "eks_dr_vault_unseal_irsa" {
  source  = "terraform-aws-modules/iam/aws//modules/iam-role-for-service-accounts-eks"
  version = "~> 5.44"
  count   = local.eks_dr_enabled

  role_name        = "${local.eks_dr_name}-vault-unseal"
  role_policy_arns = { unseal = aws_iam_policy.eks_dr_vault_unseal[0].arn }
  oidc_providers = {
    main = {
      provider_arn               = module.eks_dr[0].oidc_provider_arn
      namespace_service_accounts = ["vault:vault"]
    }
  }
  tags = local.eks_dr_tags
}

resource "aws_iam_policy" "eks_dr_vault_unseal" {
  count  = local.eks_dr_enabled
  name   = "${local.eks_dr_name}-vault-unseal"
  policy = data.aws_iam_policy_document.eks_dr_vault_unseal[0].json
}

# CNPG DR 클러스터 SA 가 barman inheritFromIAMRole 로 S3 접근.
# recovery = 원본 프리픽스 read, backup = -dr 프리픽스 write. 원본 삭제 권한 없음.
data "aws_iam_policy_document" "eks_dr_cnpg_restore" {
  count = local.eks_dr_enabled
  statement {
    sid       = "ListBucket"
    actions   = ["s3:ListBucket", "s3:GetBucketLocation"]
    resources = ["arn:aws:s3:::cledyu-lab-dr-backups"]
  }
  statement {
    sid     = "ReadSource"
    actions = ["s3:GetObject"]
    resources = [
      "arn:aws:s3:::cledyu-lab-dr-backups/postgres/*",
      "arn:aws:s3:::cledyu-lab-dr-backups/keycloak/*",
    ]
  }
  statement {
    sid     = "WriteDrPrefix"
    actions = ["s3:PutObject", "s3:GetObject"]
    resources = [
      "arn:aws:s3:::cledyu-lab-dr-backups/postgres-dr/*",
      "arn:aws:s3:::cledyu-lab-dr-backups/keycloak-dr/*",
    ]
  }
  # 버킷이 SSE-KMS(aws_kms_key.dr_backups)라 객체 read엔 Decrypt, write엔 GenerateDataKey가 필요.
  # 없으면 recovery GetObject 시 KMS 거부로 PITR 실패, -dr 재백업 PutObject도 막힌다(backup.tf 주석 참조).
  statement {
    sid       = "BackupBucketKms"
    actions   = ["kms:Decrypt", "kms:GenerateDataKey"]
    resources = [aws_kms_key.dr_backups.arn]
  }
}

resource "aws_iam_policy" "eks_dr_cnpg_restore" {
  count  = local.eks_dr_enabled
  name   = "${local.eks_dr_name}-cnpg-restore"
  policy = data.aws_iam_policy_document.eks_dr_cnpg_restore[0].json
}

# CNPG 클러스터별 SA: postgres 네임스페이스의 cledyu-pg, keycloak 의 keycloak-pg.
module "eks_dr_cnpg_restore_irsa" {
  source  = "terraform-aws-modules/iam/aws//modules/iam-role-for-service-accounts-eks"
  version = "~> 5.44"
  count   = local.eks_dr_enabled

  role_name        = "${local.eks_dr_name}-cnpg-restore"
  role_policy_arns = { restore = aws_iam_policy.eks_dr_cnpg_restore[0].arn }
  oidc_providers = {
    main = {
      provider_arn = module.eks_dr[0].oidc_provider_arn
      namespace_service_accounts = [
        "postgres:cledyu-pg",
        "keycloak:keycloak-pg",
      ]
    }
  }
  tags = local.eks_dr_tags
}
