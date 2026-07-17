output "bucket_name" {
  value = google_storage_bucket.lab_events.name
}

output "dataset_id" {
  value = google_bigquery_dataset.analytics.dataset_id
}

output "sa_email" {
  value = google_service_account.airflow_analytics.email
}
