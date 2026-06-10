# Kafka Connect·dbt·Airflow가 BigQuery와 GCS에 접근할 때 사용하는 SA.
# K8s Secret(ExternalSecret → Vault)으로 주입된다.

resource "google_service_account" "analytics" {
  account_id   = "cledyu-analytics"
  display_name = "Cledyu Analytics Pipeline"
  description  = "Kafka Connect BigQuery Sink + dbt + Airflow 전용"
}

# BigQuery 쓰기·읽기 권한
resource "google_bigquery_dataset_iam_member" "analytics_raw_editor" {
  dataset_id = google_bigquery_dataset.raw.dataset_id
  role       = "roles/bigquery.dataEditor"
  member     = "serviceAccount:${google_service_account.analytics.email}"
}

resource "google_bigquery_dataset_iam_member" "analytics_analytics_editor" {
  dataset_id = google_bigquery_dataset.analytics.dataset_id
  role       = "roles/bigquery.dataEditor"
  member     = "serviceAccount:${google_service_account.analytics.email}"
}

# dbt·Airflow가 쿼리를 실행할 때 필요한 Job 생성 권한
resource "google_project_iam_member" "analytics_job_user" {
  project = var.project_id
  role    = "roles/bigquery.jobUser"
  member  = "serviceAccount:${google_service_account.analytics.email}"
}

# GCS 스테이징 버킷 쓰기·삭제 권한 (Kafka Connect 배치 모드)
resource "google_storage_bucket_iam_member" "analytics_staging" {
  bucket = google_storage_bucket.staging.name
  role   = "roles/storage.objectAdmin"
  member = "serviceAccount:${google_service_account.analytics.email}"
}
