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

# CNPG DR 복원 IRSA — DB(prefix)별로 롤·정책을 분리해 blast radius를 각 prefix로 한정한다.
# 한 롤이 두 DB를 신뢰하면 postgres 파드 탈취 시 keycloak 계정 백업까지 읽히므로(진도↔계정 교차),
# backup.tf의 prefix별 writer 분리와 동일하게 복원 롤도 DB별로 나눈다.
# recovery = 자기 원본 프리픽스 read, backup = 자기 -dr 프리픽스 write. 원본 삭제 권한 없음.
locals {
  eks_dr_cnpg_restore = var.enable_eks_dr ? {
    postgres = { namespace = "postgres", sa = "cledyu-pg", prefix = "postgres" }
    keycloak = { namespace = "keycloak", sa = "keycloak-pg", prefix = "keycloak" }
  } : {}
}

data "aws_iam_policy_document" "eks_dr_cnpg_restore" {
  for_each = local.eks_dr_cnpg_restore
  statement {
    sid       = "ListBucket"
    actions   = ["s3:ListBucket", "s3:GetBucketLocation"]
    resources = [aws_s3_bucket.dr_backups.arn]
  }
  statement {
    sid       = "ReadSource"
    actions   = ["s3:GetObject"]
    resources = ["${aws_s3_bucket.dr_backups.arn}/${each.value.prefix}/*"]
  }
  statement {
    sid       = "WriteDrPrefix"
    actions   = ["s3:PutObject", "s3:GetObject"]
    resources = ["${aws_s3_bucket.dr_backups.arn}/${each.value.prefix}-dr/*"]
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
  for_each = local.eks_dr_cnpg_restore
  name     = "${local.eks_dr_name}-cnpg-restore-${each.key}"
  policy   = data.aws_iam_policy_document.eks_dr_cnpg_restore[each.key].json
}

module "eks_dr_cnpg_restore_irsa" {
  source   = "terraform-aws-modules/iam/aws//modules/iam-role-for-service-accounts-eks"
  version  = "~> 5.44"
  for_each = local.eks_dr_cnpg_restore

  role_name        = "${local.eks_dr_name}-cnpg-restore-${each.key}"
  role_policy_arns = { restore = aws_iam_policy.eks_dr_cnpg_restore[each.key].arn }
  oidc_providers = {
    main = {
      provider_arn               = module.eks_dr[0].oidc_provider_arn
      namespace_service_accounts = ["${each.value.namespace}:${each.value.sa}"]
    }
  }
  tags = local.eks_dr_tags
}
