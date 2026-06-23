terraform {
  required_version = ">= 1.5.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }

  # 상태는 온프렘(kvm/keycloak)과 분리해 관리한다. 로컬 backend 가 기본이며,
  # 운영에서는 S3 backend(+DynamoDB lock)로 전환한다 — README 참고.
}

provider "aws" {
  region = var.region

  default_tags {
    tags = {
      "cledyu.io/managed-by" = "terraform"
      "cledyu.io/component"  = "lab-ec2-overflow"
    }
  }
}
