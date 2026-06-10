locals {
  # staging_bucket_name 미지정 시 "cledyu-bq-staging-<project_id>" 로 자동 생성
  staging_bucket = var.staging_bucket_name != "" ? var.staging_bucket_name : "cledyu-bq-staging-${var.project_id}"
}

# ── BigQuery 데이터셋 ────────────────────────────────────────────────────────

resource "google_bigquery_dataset" "raw" {
  dataset_id  = var.raw_dataset_id
  location    = var.region
  description = "Kafka Connect가 적재하는 원시 학습 이벤트"
}

resource "google_bigquery_dataset" "analytics" {
  dataset_id  = var.analytics_dataset_id
  location    = var.region
  description = "dbt 변환 결과 — 완료율·드롭아웃·힌트 패턴 mart"
}

# ── BigQuery 테이블 ──────────────────────────────────────────────────────────
# 스키마는 apps/api/internal/events/events.go Event 구조체와 정렬.
# Kafka Connect autoCreateTables 로도 생성 가능하나, Terraform으로 명시해
# 파티션·클러스터 설정을 코드로 보장한다.

resource "google_bigquery_table" "lab_events" {
  dataset_id          = google_bigquery_dataset.raw.dataset_id # raw 데이터셋 안에 생성
  table_id            = "lab_events"
  deletion_protection = false # 개발 중이라 삭제 허용, 운영 전환 시 true로 변경 (true는 terraform destroy 쳤을 때 데이터 날아가는 거 방지)

  time_partitioning {
    type  = "DAY"
    field = "ts"
  }

  # 파티션 안에서 데이터를 lab_id → event_type 순서로 정렬해서 저장,  랩 완료율이나 힌트 몇 번 같은 쿼리를 이 조합으로 핕터링
  clustering = ["lab_id", "event_type"]

  schema = jsonencode([
    { name = "event_type", type = "STRING", mode = "REQUIRED" },
    { name = "user_id", type = "STRING", mode = "REQUIRED" },
    { name = "session_id", type = "STRING", mode = "REQUIRED" },
    { name = "lab_id", type = "STRING", mode = "REQUIRED" },
    { name = "step_id", type = "INT64", mode = "NULLABLE" },
    { name = "hint_level", type = "INT64", mode = "NULLABLE" },
    { name = "hint_source", type = "STRING", mode = "NULLABLE" },
    { name = "vm_provisioned_source", type = "STRING", mode = "NULLABLE" },
    { name = "ts", type = "TIMESTAMP", mode = "REQUIRED" },
  ])
}

# GCS 스테이징 버킷 (Kafka Connect가 BigQuery에 데이터 넣는 방식은 두 가지)
# 스트리밍 Insert -> Kafka에서 오자마자 바로 BigQuery에 꽂아 빠르지만 비싸다.
# 배치 Load Jop -> GCS에 모아뒀다가 한 번에 BigQuery에 적재, 약간 느리지만 빠름

# GCS를 중간 경유지로 써서 비용을 $0으로 만들기 위한 버킷

# Kafka Connect BigQuery Sink 배치 모드 전용
resource "google_storage_bucket" "staging" {
  name          = local.staging_bucket
  location      = var.region
  force_destroy = true # GCS에 있는 파일은 BigQuery에 들어가고 나면 더 이상 필요 없는 임시 파일만 들어있으므로 삭제 허용

  lifecycle_rule {
    action { type = "Delete" }
    # 커넥터가 Load Job 완료 후 파일을 지우지만, 실패 시 잔재 파일을 2일 후 자동 삭제
    condition { age = 2 }
  }
}
