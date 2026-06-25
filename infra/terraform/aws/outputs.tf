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
  description = "public_domain hosted zone 의 NS 4개. 도메인 등록기관에 이 값으로 위임해야 공개 해석된다."
  value       = var.enable_public_ingress ? aws_route53_zone.public[0].name_servers : []
}

output "public_alb_dns_name" {
  description = "공개 ALB 의 DNS 이름(auth.cledyu.com A ALIAS 타겟). 디버깅·검증용."
  value       = var.enable_public_ingress ? aws_lb.public[0].dns_name : ""
}

output "keycloak_proxy_instance_id" {
  description = "tailnet Keycloak 프록시 인스턴스 ID(tailnet 가입·로그 확인용)."
  value       = var.enable_public_ingress ? aws_instance.proxy[0].id : ""
}
