variable "project_id" {
  description = "GCP 프로젝트 ID"
  type        = string
}

variable "region" {
  description = "GCP 리전. BigQuery dataset·GCS bucket 위치로 쓰인다."
  type        = string
  default     = "asia-northeast3" # 서울
}

variable "raw_dataset_id" {
  description = "Kafka Connect가 적재하는 원시 이벤트 데이터셋"
  type        = string
  default     = "cledyu_raw"
}

variable "analytics_dataset_id" {
  description = "dbt 변환 결과가 저장되는 분석 데이터셋"
  type        = string
  default     = "cledyu_analytics"
}

variable "staging_bucket_name" {
  description = "Kafka Connect BigQuery Sink GCS 배치 스테이징 버킷. 전역 고유해야 하므로 project_id를 suffix로 쓴다."
  type        = string
  default     = ""
}
