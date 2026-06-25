terraform {
  required_version = ">= 1.5.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }

  # 원격 암호화 state — GCS(cledyu-project, asia-northeast3, versioning, 기본 암호화).
  # 프록시 tailscale authkey 등 민감값이 state 에 들어가므로 로컬 평문 파일 대신 원격
  # backend 를 쓴다. keycloak 과 같은 버킷, prefix 로 분리.
  backend "gcs" {
    bucket = "cledyu-tf-state"
    prefix = "aws"
  }
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
