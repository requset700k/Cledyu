terraform {
  required_version = ">= 1.5.0"

  required_providers {
    keycloak = {
      source  = "mrparkers/keycloak"
      version = "~> 4.4"
    }
  }

  # 원격 암호화 state — AWS S3(504284203153, ap-northeast-2).
  # auth(Keycloak)도 DR-크리티컬이라 복구에 필요한 state 를 복구 대상 클라우드(AWS)에
  # 자기완결적으로 둔다(GCS 는 만료형 GCP 크레딧이라 복구-크리티컬 자산을 두지 않음).
  # state 에 client secret 등 민감값이 sensitive 로 들어가므로 versioning·BlockPublicAccess·
  # SSE(AES256) 적용, DynamoDB 로 상태 락(aws 스택과 동일 버킷·락 테이블).
  # 이전 이력: 원래 GCS(gs://cledyu-tf-state, prefix keycloak) 였고 terraform init
  # -migrate-state 로 S3 로 옮겼다(GCS 사본은 롤백용으로 남아 있음). README 참고.
  # TF 1.10+ 로 올리면 dynamodb_table 대신 use_lockfile=true(S3 네이티브 락) 권장.
  backend "s3" {
    bucket         = "cledyu-tf-state"
    key            = "keycloak/terraform.tfstate"
    region         = "ap-northeast-2"
    encrypt        = true
    dynamodb_table = "cledyu-tf-lock"
  }
}
