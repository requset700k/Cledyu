terraform {
  required_version = ">= 1.5.0"

  required_providers {
    keycloak = {
      source  = "mrparkers/keycloak"
      version = "~> 4.4"
    }
  }

  # 원격 암호화 state — GCS(cledyu-project, asia-northeast3, versioning, 기본 암호화).
  # state 에 client secret 등 민감값이 sensitive 로 들어가므로 로컬 평문 파일 대신
  # 접근통제·이력·암호화되는 원격 backend 를 쓴다.
  backend "gcs" {
    bucket = "cledyu-tf-state"
    prefix = "keycloak"
  }
}
