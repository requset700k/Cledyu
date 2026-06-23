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
