variable "region" {
  description = "EC2 오버플로우 리전. 온프렘과 가까운 서울 리전을 기본값으로 둔다."
  type        = string
  default     = "ap-northeast-2"
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

variable "instance_type" {
  description = "세션 인스턴스 타입. Launch Template 기본값이며 api 가 런타임에 오버라이드할 수 있다."
  type        = string
  default     = "t3.medium"
}

variable "ami_id" {
  description = <<-EOT
    세션 인스턴스 AMI. 빈 값이면 Canonical Ubuntu 22.04(amd64) 최신 AMI 를 자동 조회한다.
    운영에서는 SSM Agent·tailscale·code-server 를 미리 구운 커스텀 AMI(packer) ID 를 넣는 것을 권장한다
    (런타임 설치 시간 단축). README 의 'AMI 전략' 참고.
  EOT
  type        = string
  default     = ""
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
