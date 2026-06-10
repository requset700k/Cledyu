output "sa_email" {
  description = "분석 파이프라인 SA 이메일 — Kafka Connect·dbt·Airflow 설정에 사용"
  value       = google_service_account.analytics.email
}

output "raw_dataset_id" {
  value = google_bigquery_dataset.raw.dataset_id
}

output "analytics_dataset_id" {
  value = google_bigquery_dataset.analytics.dataset_id
}

output "staging_bucket" {
  description = "Kafka Connect GCS 배치 스테이징 버킷 이름"
  value       = google_storage_bucket.staging.name
}
