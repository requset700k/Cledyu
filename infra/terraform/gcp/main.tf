# ── 랜딩/아카이브 버킷 ─────────────────────────────────────────────────────────
resource "google_storage_bucket" "lab_events" {
  name                        = var.bucket_name
  location                    = var.region
  uniform_bucket_level_access = true
  force_destroy               = true # 종료프로젝트 — 정리 편의

  labels = {
    "cledyu-io-managed-by" = "terraform"
    "cledyu-io-component"  = "lab-analytics"
  }
}

# ── 분석 데이터셋 + raw 테이블 ─────────────────────────────────────────────────
resource "google_bigquery_dataset" "analytics" {
  dataset_id  = "cledyu_analytics"
  location    = var.dataset_location
  description = "lab-events 분석 웨어하우스(D2 파이프라인)"

  labels = {
    "cledyu-io-managed-by" = "terraform"
    "cledyu-io-component"  = "lab-analytics"
  }
}

resource "google_bigquery_table" "lab_events" {
  dataset_id          = google_bigquery_dataset.analytics.dataset_id
  table_id            = "lab_events"
  schema              = file("${path.module}/schema/lab_events.json")
  deletion_protection = false

  time_partitioning {
    type  = "DAY"
    field = "ts"
  }
  clustering = ["event_type", "lab_id"]
}

# ── Airflow 서비스 계정 + IAM ─────────────────────────────────────────────────
resource "google_service_account" "airflow_analytics" {
  account_id   = "airflow-analytics"
  display_name = "Airflow lab-events analytics pipeline"
}

resource "google_project_iam_member" "bq_data_editor" {
  project = var.project_id
  role    = "roles/bigquery.dataEditor"
  member  = "serviceAccount:${google_service_account.airflow_analytics.email}"
}

resource "google_project_iam_member" "bq_job_user" {
  project = var.project_id
  role    = "roles/bigquery.jobUser"
  member  = "serviceAccount:${google_service_account.airflow_analytics.email}"
}

resource "google_storage_bucket_iam_member" "bucket_object_admin" {
  bucket = google_storage_bucket.lab_events.name
  role   = "roles/storage.objectAdmin"
  member = "serviceAccount:${google_service_account.airflow_analytics.email}"
}
