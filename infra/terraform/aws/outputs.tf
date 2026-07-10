output "launch_template_id" {
  description = "apps/api 의 CLEDYU_AWS_LAUNCH_TEMPLATE_ID 로 주입할 Launch Template ID."
  value       = aws_launch_template.lab_session.id
}

output "launch_template_latest_version" {
  description = "Launch Template 최신 버전(api 는 $Latest 를 쓰지만 참고용으로 노출)."
  value       = aws_launch_template.lab_session.latest_version
}

output "security_group_id" {
  description = "세션 인스턴스 Security Group ID(인바운드 0)."
  value       = aws_security_group.lab_session.id
}

output "instance_profile_arn" {
  description = "세션 인스턴스 IAM 인스턴스 프로파일 ARN(SSM Core)."
  value       = aws_iam_instance_profile.lab_instance.arn
}

output "api_iam_user" {
  description = "apps/api 용 IAM 사용자명 — 이 사용자의 액세스 키를 발급해 Vault 에 보관한다."
  value       = aws_iam_user.api.name
}

output "validation_engine_iam_user" {
  description = "validation-engine 용 IAM 사용자명 — 이 사용자의 액세스 키를 발급해 Vault 에 보관한다."
  value       = aws_iam_user.engine.name
}

output "region" {
  description = "EC2 오버플로우 리전(apps/api 의 CLEDYU_AWS_REGION)."
  value       = var.region
}

# ── 공개 진입점(enable_public_ingress=true 일 때만 값이 채워진다) ──────────
output "public_zone_name_servers" {
  description = "public_domain hosted zone 의 NS(참고용). registrar=Route53 면 자동 연결돼 수동 위임 불필요."
  value       = var.enable_public_ingress ? data.aws_route53_zone.public[0].name_servers : []
}

output "public_alb_dns_name" {
  description = "공개 ALB 의 DNS 이름(app/api/auth.cledyu.com A ALIAS 타겟). 디버깅·검증용."
  value       = var.enable_public_ingress ? aws_lb.public[0].dns_name : ""
}

output "keycloak_proxy_instance_id" {
  description = "tailnet Keycloak 프록시 인스턴스 ID(tailnet 가입·로그 확인용)."
  value       = var.enable_public_ingress ? aws_instance.proxy[0].id : ""
}

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

output "gha_baker_role_arn" {
  description = "GitHub Action 이 assume 할 베이커 role ARN."
  value       = aws_iam_role.gha_baker.arn
}

output "baker_bucket" {
  description = "이미지 베이커 S3 버킷명."
  value       = aws_s3_bucket.baker.bucket
}

output "baker_instance_profile" {
  description = "metal 베이커 인스턴스 프로파일명."
  value       = aws_iam_instance_profile.baker_instance.name
}

# ── DR/백업 (backup.tf) ──────────────────────────────────────────────────
output "backup_bucket" {
  description = "DR 백업 S3 버킷명(Postgres·Vault·Velero 백업 대상; Longhorn은 별도 버킷)."
  value       = aws_s3_bucket.dr_backups.bucket
}

output "backup_iam_users" {
  description = "프리픽스별 백업 IAM 사용자명 맵(키: postgres, vault, velero) — 각 사용자의 액세스 키를 발급해 Vault 경로 cledyu/aws/backup-<키>(backup-postgres·backup-vault·backup-velero)에 보관한다."
  value       = { for k, u in aws_iam_user.backup : k => u.name }
}

# ── EKS Cold DR (eks-dr.tf, enable_eks_dr=true 일 때만 값이 채워진다) ─────
output "eks_dr_cluster_name" {
  description = "DR EKS 클러스터명(kubectl/드릴 스크립트 참고용)."
  value       = var.enable_eks_dr ? module.eks_dr[0].cluster_name : null
}

output "eks_dr_oidc_provider_arn" {
  description = "DR EKS OIDC provider ARN — IRSA role trust policy가 소비."
  value       = var.enable_eks_dr ? module.eks_dr[0].oidc_provider_arn : null
}

output "eks_dr_oidc_provider" {
  description = "DR EKS OIDC provider issuer(호스트 부분) — IRSA role trust policy가 소비."
  value       = var.enable_eks_dr ? module.eks_dr[0].oidc_provider : null
}

output "eks_dr_alb_controller_role_arn" {
  description = "AWS Load Balancer Controller IRSA 롤 ARN"
  value       = var.enable_eks_dr ? module.eks_dr_alb_irsa[0].iam_role_arn : null
}

output "eks_dr_vault_unseal_role_arn" {
  description = "Vault unseal(awskms) IRSA 롤 ARN"
  value       = var.enable_eks_dr ? module.eks_dr_vault_unseal_irsa[0].iam_role_arn : null
}

output "eks_dr_cnpg_restore_role_arn" {
  description = "CNPG restore(S3 barman) IRSA 롤 ARN"
  value       = var.enable_eks_dr ? module.eks_dr_cnpg_restore_irsa[0].iam_role_arn : null
}
