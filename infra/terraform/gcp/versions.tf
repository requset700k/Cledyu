terraform {
  required_version = ">= 1.5.0"

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 5.0"
    }
  }

  # 원격 암호화 state — keycloak/aws 와 같은 버킷, prefix 로 분리.
  backend "gcs" {
    bucket = "cledyu-tf-state"
    prefix = "gcp"
  }
}

provider "google" {
  project = var.project_id
  region  = var.region
}
