variable "project_id" {
  description = "GCP 프로젝트 ID (cledyu-project)"
  type        = string
}

variable "region" {
  description = "리소스 리전"
  type        = string
  default     = "asia-northeast3"
}

variable "bucket_name" {
  description = "lab-events raw NDJSON 랜딩/아카이브 버킷 이름(전역 유일)"
  type        = string
}

variable "dataset_location" {
  description = "BigQuery 데이터셋 location"
  type        = string
  default     = "asia-northeast3"
}
