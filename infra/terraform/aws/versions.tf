terraform {
  required_version = ">= 1.9.0"

  required_providers {
    archive = {
      source  = "hashicorp/archive"
      version = "~> 2.4"
    }
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }

  # 원격 암호화 state — AWS S3(504284203153, ap-northeast-2).
  # DR이 AWS 기반이라 복구에 필요한 state를 복구 대상 클라우드에 자기완결적으로 둔다
  # (GCS는 만료형 GCP 크레딧이라 복구-크리티컬 자산을 두지 않음). 프록시 tailscale
  # authkey 등 민감값이 state에 들어가므로 versioning·BlockPublicAccess·SSE(AES256)
  # 적용, DynamoDB 로 상태 락. 버킷·락 테이블은 out-of-band 부트스트랩(README 참고).
  # TF 1.10+ 로 올리면 dynamodb_table 대신 use_lockfile=true(S3 네이티브 락) 권장.
  backend "s3" {
    bucket         = "cledyu-tf-state"
    key            = "aws/terraform.tfstate"
    region         = "ap-northeast-2"
    encrypt        = true
    dynamodb_table = "cledyu-tf-lock"
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
