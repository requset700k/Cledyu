# D2 데이터 파이프라인 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `lab-events` Kafka 이벤트를 Airflow DAG(수동 트리거)로 GCS→BigQuery에 적재하고, D3용 BigQuery 뷰를 제공하는 파이프라인을 구축한다.

**Architecture:** Airflow(LocalExecutor, git-sync)가 `apps/airflow/dags/` 의 DAG 를 자동 로드한다. DAG 는 confluent-kafka 로 lab-events 를 배치 소비 → GCS NDJSON 랜딩 → BigQuery raw 테이블 load → D3 뷰 갱신. GCP 리소스(버킷·데이터셋·SA·뷰)는 신규 `infra/terraform/gcp` 모듈로 선언, 자격증명은 Vault→ESO 로 airflow ns 에 주입.

**Tech Stack:** Terraform(google provider), Apache Airflow 2.10.2 + apache-airflow-providers-google + confluent-kafka, BigQuery/GCS, Strimzi KafkaUser, External Secrets, Python/pytest/ruff.

## Global Constraints

- 문서/주석 한국어, 코드 식별자·CLI·키 영어. 이모지 금지.
- 커밋 subject 소문자 시작, Conventional Commits. scope: `data`(파이프라인/Airflow/BQ) / `infra`(terraform). body 줄당 ≤100자.
- Event JSON 필드(producer 계약, 변경 금지): `event_type, user_id, session_id, lab_id, step_id, hint_level, hint_source, vm_provisioned_source, ts`. step_id/hint_level/hint_source/vm_provisioned_source 는 omitempty → BQ NULLABLE.
- BQ: 데이터셋 `cledyu_analytics`(location asia-northeast3), raw 테이블 `lab_events`(PARTITION BY DATE(ts), CLUSTER BY event_type, lab_id). 뷰 `v_lab_completion`/`v_step_funnel`/`v_hint_usage`.
- 실행: DAG `schedule=None`(수동 트리거). dag_id `lab_events_to_bq`.
- 자격증명: Vault→ESO→airflow ns Secret. GCP SA 키(`cledyu/gcp/airflow-analytics-sa`) + Kafka 클라이언트 인증서(`cledyu/kafka/airflow-analytics`). 하드코딩 금지.
- terraform: `required_version >= 1.5.0`, backend `gcs`(bucket `cledyu-tf-state`, prefix `gcp`).
- 종료프로젝트([[project_deadline_terminating]]) — 속도·데모 우선. 라이브 e2e(apply·SA키·vault put·DAG 트리거)는 **김용균님 수동 단계**(체크리스트로 명시), 자동화 범위는 작성 + 정적검증(fmt/validate/ruff/pytest/yamllint)까지.
- 검증 게이트: `terraform fmt -check` + `terraform validate`(backend=false), `ruff check`, `pytest`(DAG helper), `pre-commit`(yamllint).

**작업 디렉터리:** repo 루트 `/Users/kylekim1223/request700k/cledyu`. 브랜치: `feat/data-pipeline-d2`(생성됨).

---

### Task 1: GCP Terraform 모듈 — 버킷·데이터셋·raw 테이블·SA

GCS 버킷, BigQuery 데이터셋/raw 테이블, Airflow 서비스 계정과 IAM 을 선언하는 신규 terraform 모듈. 산출물: `terraform fmt -check` + `terraform validate`(backend 미초기화) 통과. 실제 apply 는 김용균님 단계(Task 6 체크리스트).

**Files:**
- Create: `infra/terraform/gcp/versions.tf`
- Create: `infra/terraform/gcp/variables.tf`
- Create: `infra/terraform/gcp/main.tf`
- Create: `infra/terraform/gcp/outputs.tf`
- Create: `infra/terraform/gcp/terraform.tfvars.example`
- Create: `infra/terraform/gcp/schema/lab_events.json` (BQ raw 스키마)

**Interfaces:**
- Produces: GCS 버킷 `${var.bucket_name}`, BQ 데이터셋 `cledyu_analytics`, 테이블 `lab_events`, SA `airflow-analytics@<project>.iam.gserviceaccount.com`. outputs: `bucket_name`, `dataset_id`, `sa_email`.

- [ ] **Step 1: versions.tf 작성**

Create `infra/terraform/gcp/versions.tf`:

```hcl
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
```

- [ ] **Step 2: variables.tf 작성**

Create `infra/terraform/gcp/variables.tf`:

```hcl
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
```

- [ ] **Step 3: BQ raw 스키마 파일 작성**

Create `infra/terraform/gcp/schema/lab_events.json`:

```json
[
  { "name": "event_type", "type": "STRING", "mode": "NULLABLE" },
  { "name": "user_id", "type": "STRING", "mode": "NULLABLE" },
  { "name": "session_id", "type": "STRING", "mode": "NULLABLE" },
  { "name": "lab_id", "type": "STRING", "mode": "NULLABLE" },
  { "name": "step_id", "type": "INTEGER", "mode": "NULLABLE" },
  { "name": "hint_level", "type": "INTEGER", "mode": "NULLABLE" },
  { "name": "hint_source", "type": "STRING", "mode": "NULLABLE" },
  { "name": "vm_provisioned_source", "type": "STRING", "mode": "NULLABLE" },
  { "name": "ts", "type": "TIMESTAMP", "mode": "NULLABLE" },
  { "name": "_ingested_at", "type": "TIMESTAMP", "mode": "NULLABLE" }
]
```

- [ ] **Step 4: main.tf 작성**

Create `infra/terraform/gcp/main.tf`:

```hcl
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
```

- [ ] **Step 5: outputs.tf + tfvars.example 작성**

Create `infra/terraform/gcp/outputs.tf`:

```hcl
output "bucket_name" {
  value = google_storage_bucket.lab_events.name
}

output "dataset_id" {
  value = google_bigquery_dataset.analytics.dataset_id
}

output "sa_email" {
  value = google_service_account.airflow_analytics.email
}
```

Create `infra/terraform/gcp/terraform.tfvars.example`:

```hcl
project_id  = "cledyu-project"
bucket_name = "cledyu-lab-events-analytics"
```

- [ ] **Step 6: fmt + validate**

Run: `cd infra/terraform/gcp && terraform fmt -check && terraform init -backend=false && terraform validate`
Expected: fmt 무출력, init/validate 성공("Success! The configuration is valid."). (네트워크로 google provider 다운로드 — 실패 시 `terraform fmt -check` 만이라도 통과 확인하고 validate 는 환경 제약으로 보고.)

- [ ] **Step 7: 커밋**

```bash
cd /Users/kylekim1223/request700k/cledyu
git add infra/terraform/gcp/
git commit -m "feat(infra): add gcp terraform module for lab-events analytics" \
  -m "GCS landing bucket, cledyu_analytics dataset, partitioned lab_events raw table,
and an airflow-analytics service account with BigQuery and bucket IAM."
```

---

### Task 2: Strimzi KafkaUser + ESO 자격증명 매니페스트

Airflow 분석 소비자용 KafkaUser(lab-events Read)와, GCP SA 키·Kafka 클라이언트 인증서를 airflow ns 로 주입하는 ExternalSecret 을 추가한다. 산출물: yamllint/pre-commit 통과. 실제 vault put·apply 는 Task 6 체크리스트.

**Files:**
- Create: `gitops/apps/kafka-cluster/kafkauser-airflow-analytics.yaml`
- Create: `gitops/apps/airflow/externalsecret-gcp-sa.yaml`
- Create: `gitops/apps/airflow/externalsecret-kafka-cert.yaml`

**Interfaces:**
- Produces: KafkaUser `airflow-analytics`(kafka ns, tls, lab-events Read + group Read) → Strimzi 가 `airflow-analytics` 시크릿(클라이언트 인증서) 생성. ESO 가 airflow ns 에 Secret `airflow-gcp-sa`(키 `key.json`)·`airflow-kafka-cert`(키 `user.crt`/`user.key`/`ca.crt`) 생성.

- [ ] **Step 1: KafkaUser 작성**

Create `gitops/apps/kafka-cluster/kafkauser-airflow-analytics.yaml`:

```yaml
---
# Airflow 분석 파이프라인 소비자 — lab-events 읽기 전용 + 컨슈머 그룹.
apiVersion: kafka.strimzi.io/v1beta2
kind: KafkaUser
metadata:
  name: airflow-analytics
  namespace: kafka
  labels:
    strimzi.io/cluster: cledyu-kafka
spec:
  authentication:
    type: tls
  authorization:
    type: simple
    acls:
      - resource:
          type: topic
          name: lab-events
          patternType: literal
        operations: [Read, Describe]
      - resource:
          type: group
          name: airflow-analytics
          patternType: literal
        operations: [Read, Describe]
```

- [ ] **Step 2: GCP SA ExternalSecret 작성**

Create `gitops/apps/airflow/externalsecret-gcp-sa.yaml`:

```yaml
---
# GCP 서비스 계정 키 — DAG 의 GOOGLE_APPLICATION_CREDENTIALS 로 마운트.
# vault kv put cledyu/gcp/airflow-analytics-sa key.json=@sa-key.json
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: airflow-gcp-sa
  namespace: airflow
  annotations:
    argocd.argoproj.io/sync-options: SkipDryRunOnMissingResource=true
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: vault-backend
    kind: ClusterSecretStore
  target:
    name: airflow-gcp-sa
  data:
    - secretKey: key.json
      remoteRef:
        key: gcp/airflow-analytics-sa
        property: key.json
```

- [ ] **Step 3: Kafka 인증서 ExternalSecret 작성**

Create `gitops/apps/airflow/externalsecret-kafka-cert.yaml`:

```yaml
---
# Kafka mTLS 클라이언트 인증서(KafkaUser airflow-analytics 시크릿에서 Vault 로 복사한 값).
# vault kv put cledyu/kafka/airflow-analytics user.crt=@user.crt user.key=@user.key ca.crt=@ca.crt
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: airflow-kafka-cert
  namespace: airflow
  annotations:
    argocd.argoproj.io/sync-options: SkipDryRunOnMissingResource=true
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: vault-backend
    kind: ClusterSecretStore
  target:
    name: airflow-kafka-cert
  data:
    - secretKey: user.crt
      remoteRef:
        key: kafka/airflow-analytics
        property: user.crt
    - secretKey: user.key
      remoteRef:
        key: kafka/airflow-analytics
        property: user.key
    - secretKey: ca.crt
      remoteRef:
        key: kafka/airflow-analytics
        property: ca.crt
```

- [ ] **Step 4: yamllint/pre-commit 검증**

Run: `cd /Users/kylekim1223/request700k/cledyu && pre-commit run --files gitops/apps/kafka-cluster/kafkauser-airflow-analytics.yaml gitops/apps/airflow/externalsecret-gcp-sa.yaml gitops/apps/airflow/externalsecret-kafka-cert.yaml`
Expected: yamllint·check-yaml·gitleaks 등 통과(시크릿 값 없음 — 참조만).

- [ ] **Step 5: 커밋**

```bash
cd /Users/kylekim1223/request700k/cledyu
git add gitops/apps/kafka-cluster/kafkauser-airflow-analytics.yaml gitops/apps/airflow/externalsecret-gcp-sa.yaml gitops/apps/airflow/externalsecret-kafka-cert.yaml
git commit -m "feat(data): add kafka user and eso secrets for analytics pipeline" \
  -m "KafkaUser airflow-analytics (lab-events read) and ExternalSecrets injecting the GCP
SA key and Kafka client cert into the airflow namespace from Vault."
```

---

### Task 3: DAG 변환 헬퍼 — Kafka 메시지 → BQ row (TDD)

DAG 에서 분리한 순수 함수: Kafka 메시지 JSON → BQ row dict, 그리고 row 리스트 → NDJSON 문자열. airflow/kafka/gcp import 없이 pytest 로 검증한다.

**Files:**
- Create: `apps/airflow/dags/lab_events_lib.py`
- Create: `apps/airflow/tests/test_lab_events_lib.py`

**Interfaces:**
- Produces: `event_to_row(msg_value: bytes | str) -> dict` (Event JSON → BQ row + `_ingested_at`), `rows_to_ndjson(rows: list[dict]) -> str`.

- [ ] **Step 1: 실패하는 테스트 작성**

Create `apps/airflow/tests/test_lab_events_lib.py`:

```python
import json
import sys
from pathlib import Path

# dags 폴더를 import 경로에 추가(git-sync 가 dags/ 만 동기화하므로 lib 는 거기 있다).
sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "dags"))

from lab_events_lib import event_to_row, rows_to_ndjson  # noqa: E402


def test_event_to_row_maps_all_fields():
    raw = json.dumps(
        {
            "event_type": "hint_requested",
            "user_id": "u1",
            "session_id": "s1",
            "lab_id": "lab-docker-basics",
            "step_id": 2,
            "hint_level": 1,
            "hint_source": "ai",
            "ts": "2026-06-26T09:00:00Z",
        }
    )
    row = event_to_row(raw)
    assert row["event_type"] == "hint_requested"
    assert row["user_id"] == "u1"
    assert row["step_id"] == 2
    assert row["hint_source"] == "ai"
    assert row["ts"] == "2026-06-26T09:00:00Z"
    assert "_ingested_at" in row and row["_ingested_at"]
    # omitempty 로 빠진 필드는 None 으로 채워 BQ NULLABLE 에 맞춘다.
    assert row["vm_provisioned_source"] is None


def test_event_to_row_accepts_bytes():
    row = event_to_row(b'{"event_type":"lab_started","user_id":"u2","ts":"2026-06-26T10:00:00Z"}')
    assert row["event_type"] == "lab_started"
    assert row["user_id"] == "u2"
    assert row["step_id"] is None


def test_rows_to_ndjson_one_line_per_row():
    out = rows_to_ndjson([{"a": 1}, {"a": 2}])
    lines = out.splitlines()
    assert len(lines) == 2
    assert json.loads(lines[0])["a"] == 1
    assert json.loads(lines[1])["a"] == 2
```

- [ ] **Step 2: 테스트 실패 확인**

Run: `cd apps/airflow && python -m pytest tests/test_lab_events_lib.py -q`
Expected: FAIL — `ModuleNotFoundError: No module named 'lab_events_lib'`.

- [ ] **Step 3: 헬퍼 구현**

Create `apps/airflow/dags/lab_events_lib.py`:

```python
"""lab-events Kafka 메시지를 BigQuery row 로 변환하는 순수 헬퍼.

DAG(lab_events_to_bq.py)에서 import 한다. airflow/kafka/gcp 에 의존하지 않아
독립적으로 pytest 로 검증된다.
"""

import json
from datetime import datetime, timezone

# BQ raw 테이블 lab_events 의 컬럼(스키마와 1:1). omitempty 로 빠진 필드는 None.
_FIELDS = (
    "event_type",
    "user_id",
    "session_id",
    "lab_id",
    "step_id",
    "hint_level",
    "hint_source",
    "vm_provisioned_source",
    "ts",
)


def event_to_row(msg_value):
    """Kafka 메시지(JSON bytes 또는 str)를 BQ row dict 로 변환한다."""
    if isinstance(msg_value, (bytes, bytearray)):
        msg_value = msg_value.decode("utf-8")
    event = json.loads(msg_value)
    row = {field: event.get(field) for field in _FIELDS}
    row["_ingested_at"] = datetime.now(timezone.utc).isoformat()
    return row


def rows_to_ndjson(rows):
    """row dict 리스트를 BQ load 용 NDJSON(줄당 1 객체) 문자열로 직렬화한다."""
    return "\n".join(json.dumps(row, ensure_ascii=False) for row in rows)
```

- [ ] **Step 4: 테스트 통과 확인**

Run: `cd apps/airflow && python -m pytest tests/test_lab_events_lib.py -q`
Expected: 3 passed.

- [ ] **Step 5: ruff**

Run: `cd /Users/kylekim1223/request700k/cledyu && ruff check apps/airflow/dags/lab_events_lib.py apps/airflow/tests/test_lab_events_lib.py`
Expected: 무출력(통과).

- [ ] **Step 6: 커밋**

```bash
cd /Users/kylekim1223/request700k/cledyu
git add apps/airflow/dags/lab_events_lib.py apps/airflow/tests/test_lab_events_lib.py
git commit -m "feat(data): add lab-events to bigquery row transform helpers" \
  -m "Pure event_to_row and rows_to_ndjson helpers (no airflow/kafka/gcp imports),
covered by pytest, used by the lab_events_to_bq DAG."
```

---

### Task 4: Airflow DAG — consume → GCS → BQ load → views

헬퍼를 사용해 Kafka 배치 소비 → GCS NDJSON → BQ load → 뷰 갱신을 오케스트레이션하는 DAG. 산출물: ruff 통과 + DAG 파싱(가능 시). 실제 실행은 Task 6 체크리스트.

**Files:**
- Create: `apps/airflow/dags/lab_events_to_bq.py`
- Modify: `gitops/apps/airflow/values.yaml` (Python 의존성 + GCP SA 마운트 env)

**Interfaces:**
- Consumes: `lab_events_lib.event_to_row`, `rows_to_ndjson` (Task 3); Secret `airflow-gcp-sa`/`airflow-kafka-cert` (Task 2); BQ `cledyu_analytics.lab_events` (Task 1).

- [ ] **Step 1: DAG 작성**

Create `apps/airflow/dags/lab_events_to_bq.py`:

```python
"""lab-events Kafka → GCS → BigQuery 적재 DAG (수동 트리거).

흐름: confluent-kafka 로 lab-events 배치 소비 → GCS NDJSON 랜딩 → BQ load(append)
→ D3 뷰 CREATE OR REPLACE. 공개 배포 전이라 schedule=None(데모 시 수동 트리거).
자격증명: airflow-gcp-sa(GOOGLE_APPLICATION_CREDENTIALS), airflow-kafka-cert(mTLS).
"""

import os
from datetime import datetime

from airflow import DAG
from airflow.operators.python import PythonOperator
from airflow.providers.google.cloud.operators.bigquery import BigQueryInsertJobOperator

from lab_events_lib import event_to_row, rows_to_ndjson

PROJECT = os.environ.get("GCP_PROJECT", "cledyu-project")
DATASET = "cledyu_analytics"
BUCKET = os.environ.get("LAB_EVENTS_BUCKET", "cledyu-lab-events-analytics")
KAFKA_BOOTSTRAP = os.environ.get("KAFKA_BOOTSTRAP", "cledyu-kafka-kafka-bootstrap.kafka:9093")
CERT_DIR = "/etc/airflow-kafka-cert"
MAX_MESSAGES = 5000  # 한 트리거당 소비 상한(빈/소량 토픽 데모 기준)


def consume_to_gcs(**context):
    """lab-events 를 배치 소비해 GCS 에 NDJSON 으로 랜딩한다. 소비 0건이면 빈 경로 반환."""
    from confluent_kafka import Consumer
    from google.cloud import storage

    consumer = Consumer(
        {
            "bootstrap.servers": KAFKA_BOOTSTRAP,
            "group.id": "airflow-analytics",
            "auto.offset.reset": "earliest",
            "enable.auto.commit": False,
            "security.protocol": "SSL",
            "ssl.ca.location": f"{CERT_DIR}/ca.crt",
            "ssl.certificate.location": f"{CERT_DIR}/user.crt",
            "ssl.key.location": f"{CERT_DIR}/user.key",
        }
    )
    consumer.subscribe(["lab-events"])
    rows = []
    try:
        while len(rows) < MAX_MESSAGES:
            msg = consumer.poll(timeout=5.0)
            if msg is None:
                break
            if msg.error():
                continue
            rows.append(event_to_row(msg.value()))
        if rows:
            consumer.commit(asynchronous=False)
    finally:
        consumer.close()

    if not rows:
        return ""  # 소비 0건 — 다운스트림 스킵

    run_id = context["run_id"].replace(":", "-")
    blob_path = f"lab-events/dt={datetime.utcnow():%Y-%m-%d}/run={run_id}.ndjson"
    storage.Client().bucket(BUCKET).blob(blob_path).upload_from_string(
        rows_to_ndjson(rows), content_type="application/x-ndjson"
    )
    return f"gs://{BUCKET}/{blob_path}"


def _branch_has_data(**context):
    uri = context["ti"].xcom_pull(task_ids="consume_to_gcs")
    return "load_to_bq" if uri else "skip_no_data"


with DAG(
    dag_id="lab_events_to_bq",
    description="lab-events Kafka → GCS → BigQuery 적재(수동 트리거)",
    schedule=None,
    start_date=datetime(2026, 6, 1),
    catchup=False,
    tags=["analytics", "lab-events"],
) as dag:
    from airflow.operators.empty import EmptyOperator
    from airflow.operators.python import BranchPythonOperator

    consume = PythonOperator(task_id="consume_to_gcs", python_callable=consume_to_gcs)

    branch = BranchPythonOperator(task_id="branch_has_data", python_callable=_branch_has_data)

    skip = EmptyOperator(task_id="skip_no_data")

    load = BigQueryInsertJobOperator(
        task_id="load_to_bq",
        configuration={
            "load": {
                "sourceUris": ["{{ ti.xcom_pull(task_ids='consume_to_gcs') }}"],
                "destinationTable": {
                    "projectId": PROJECT,
                    "datasetId": DATASET,
                    "tableId": "lab_events",
                },
                "sourceFormat": "NEWLINE_DELIMITED_JSON",
                "writeDisposition": "WRITE_APPEND",
            }
        },
    )

    refresh_views = BigQueryInsertJobOperator(
        task_id="refresh_views",
        configuration={
            "query": {
                "query": "{% include 'sql/d3_views.sql' %}",
                "useLegacySql": False,
            }
        },
    )

    consume >> branch
    branch >> skip
    branch >> load >> refresh_views
```

- [ ] **Step 2: values.yaml 에 Python 의존성 + SA env/마운트 추가**

`gitops/apps/airflow/values.yaml` 에 추가(기존 `executor`/`images` 블록과 별개 최상위 키). 정확한 위치는 파일 끝부분 적절한 곳:

```yaml
# ── DAG 런타임 Python 의존성(D2 파이프라인) ──────────────────────────────────
# LocalExecutor 라 스케줄러에 설치된다. 데모용 _PIP_ADDITIONAL_REQUIREMENTS.
extraEnv: |
  - name: _PIP_ADDITIONAL_REQUIREMENTS
    value: "apache-airflow-providers-google==10.* confluent-kafka==2.*"
  - name: GOOGLE_APPLICATION_CREDENTIALS
    value: /etc/airflow-gcp-sa/key.json
  - name: GCP_PROJECT
    value: cledyu-project
  - name: LAB_EVENTS_BUCKET
    value: cledyu-lab-events-analytics

# GCP SA 키와 Kafka 인증서 Secret 을 스케줄러에 마운트.
scheduler:
  extraVolumes:
    - name: airflow-gcp-sa
      secret:
        secretName: airflow-gcp-sa
    - name: airflow-kafka-cert
      secret:
        secretName: airflow-kafka-cert
  extraVolumeMounts:
    - name: airflow-gcp-sa
      mountPath: /etc/airflow-gcp-sa
      readOnly: true
    - name: airflow-kafka-cert
      mountPath: /etc/airflow-kafka-cert
      readOnly: true
```

(참고: chart 버전 1.15.0 의 정확한 키 이름은 apply 단계에서 `helm show values` 로 대조 — Task 6 체크리스트.)

- [ ] **Step 3: ruff + DAG 구문 확인**

Run: `cd /Users/kylekim1223/request700k/cledyu && ruff check apps/airflow/dags/lab_events_to_bq.py && python -c "import ast; ast.parse(open('apps/airflow/dags/lab_events_to_bq.py').read()); print('parse ok')"`
Expected: ruff 무출력, `parse ok`. (전체 DAG import 는 airflow+providers 설치 필요 — 환경에 있으면 `python -c "from airflow.models import DagBag"` 로 추가 검증, 없으면 구문 파싱까지 보고.)

- [ ] **Step 4: yamllint(values.yaml)**

Run: `cd /Users/kylekim1223/request700k/cledyu && pre-commit run --files gitops/apps/airflow/values.yaml`
Expected: 통과.

- [ ] **Step 5: 커밋**

```bash
cd /Users/kylekim1223/request700k/cledyu
git add apps/airflow/dags/lab_events_to_bq.py gitops/apps/airflow/values.yaml
git commit -m "feat(data): add lab_events_to_bq airflow dag" \
  -m "Manual-trigger DAG: consume lab-events (mTLS) to GCS NDJSON, branch on data,
load to BigQuery, refresh D3 views. Wires GCP SA and Kafka cert secrets in values."
```

---

### Task 5: D3 BigQuery 뷰 SQL

DAG 의 refresh_views 가 실행하는 D3용 집계 뷰 DDL. raw 위 GROUP BY + dedup.

**Files:**
- Create: `apps/airflow/dags/sql/d3_views.sql`

**Interfaces:**
- Consumes: `cledyu_analytics.lab_events` (Task 1). DAG 의 `refresh_views` 태스크가 `{% include 'sql/d3_views.sql' %}` 로 포함.

- [ ] **Step 1: 뷰 DDL 작성**

Create `apps/airflow/dags/sql/d3_views.sql`:

```sql
-- D3 강사 분석용 BigQuery 뷰. raw lab_events 위 집계 + dedup.
-- dedup: append 적재라 동일 이벤트가 중복될 수 있어 (user,session,event_type,step,ts) 기준 1건만.

CREATE OR REPLACE VIEW `cledyu_analytics.v_dedup_events` AS
SELECT * EXCEPT (rn) FROM (
  SELECT *, ROW_NUMBER() OVER (
    PARTITION BY user_id, session_id, event_type, step_id, ts
    ORDER BY _ingested_at
  ) AS rn
  FROM `cledyu_analytics.lab_events`
)
WHERE rn = 1;

-- 랩별 시작/완료/완료율.
CREATE OR REPLACE VIEW `cledyu_analytics.v_lab_completion` AS
SELECT
  lab_id,
  COUNTIF(event_type = 'lab_started')   AS started,
  COUNTIF(event_type = 'lab_completed') AS completed,
  SAFE_DIVIDE(COUNTIF(event_type = 'lab_completed'),
              COUNTIF(event_type = 'lab_started')) AS completion_rate
FROM `cledyu_analytics.v_dedup_events`
WHERE lab_id IS NOT NULL
GROUP BY lab_id;

-- 랩·스텝별 검증 실패(이탈 지점).
CREATE OR REPLACE VIEW `cledyu_analytics.v_step_funnel` AS
SELECT
  lab_id,
  step_id,
  COUNT(*) AS validation_failures
FROM `cledyu_analytics.v_dedup_events`
WHERE event_type = 'validation_failed'
GROUP BY lab_id, step_id
ORDER BY validation_failures DESC;

-- 랩·스텝별 힌트 사용 패턴(ai/static).
CREATE OR REPLACE VIEW `cledyu_analytics.v_hint_usage` AS
SELECT
  lab_id,
  step_id,
  hint_source,
  COUNT(*) AS hint_count
FROM `cledyu_analytics.v_dedup_events`
WHERE event_type = 'hint_requested'
GROUP BY lab_id, step_id, hint_source;
```

- [ ] **Step 2: SQL 구문 정적 점검**

Run: `cd /Users/kylekim1223/request700k/cledyu && python -c "s=open('apps/airflow/dags/sql/d3_views.sql').read(); assert s.count('CREATE OR REPLACE VIEW')==4, s.count('CREATE OR REPLACE VIEW'); assert 'v_dedup_events' in s and 'v_lab_completion' in s and 'v_step_funnel' in s and 'v_hint_usage' in s; print('sql shape ok')"`
Expected: `sql shape ok`. (BQ 문법 실검증은 dry-run — 자격증명 필요, Task 6 체크리스트.)

- [ ] **Step 3: 커밋**

```bash
cd /Users/kylekim1223/request700k/cledyu
git add apps/airflow/dags/sql/d3_views.sql
git commit -m "feat(data): add d3 bigquery views over lab_events" \
  -m "Dedup view plus v_lab_completion, v_step_funnel, v_hint_usage aggregates that the
DAG refreshes; consumed by D3 instructor analytics."
```

---

### Task 6: 운영 런북 + 전체 게이트 + PR

라이브 적용(김용균님 수동 단계)을 위한 런북과 전체 정적 게이트 검증, PR 생성.

**Files:**
- Create: `docs/RUNBOOK/d2-data-pipeline.md`

- [ ] **Step 1: 런북 작성**

Create `docs/RUNBOOK/d2-data-pipeline.md`:

```markdown
# D2 데이터 파이프라인 — 적용 런북

자동화 범위는 코드 작성 + 정적검증까지다. 아래는 라이브 적용을 위한 수동 단계(GCP/Vault
인증 필요 — 김용균/owner onimami1110).

## 1. GCP 인프라 apply
\`\`\`
cd infra/terraform/gcp
cp terraform.tfvars.example terraform.tfvars   # bucket_name 등 확인
terraform init
terraform apply
\`\`\`

## 2. 서비스 계정 키 발급 + Vault 저장
\`\`\`
gcloud iam service-accounts keys create sa-key.json \\
  --iam-account="$(terraform -chdir=infra/terraform/gcp output -raw sa_email)"
vault kv put cledyu/gcp/airflow-analytics-sa key.json=@sa-key.json
rm sa-key.json
\`\`\`

## 3. Kafka 클라이언트 인증서 → Vault
KafkaUser airflow-analytics 시크릿(kafka ns)에서 인증서를 꺼내 Vault 에 저장:
\`\`\`
kubectl -n kafka get secret airflow-analytics -o jsonpath='{.data.user\\.crt}' | base64 -d > user.crt
kubectl -n kafka get secret airflow-analytics -o jsonpath='{.data.user\\.key}' | base64 -d > user.key
kubectl -n kafka get secret airflow-analytics -o jsonpath='{.data.ca\\.crt}'   | base64 -d > ca.crt
vault kv put cledyu/kafka/airflow-analytics user.crt=@user.crt user.key=@user.key ca.crt=@ca.crt
rm user.crt user.key ca.crt
\`\`\`

## 4. ArgoCD 동기화 확인
- KafkaUser airflow-analytics, ExternalSecret airflow-gcp-sa/airflow-kafka-cert 가 Synced 인지.
- airflow ns 에 Secret airflow-gcp-sa/airflow-kafka-cert 생성됐는지.

## 5. DAG 트리거 + 검증
- Airflow UI 에서 lab_events_to_bq 수동 트리거.
- 이벤트가 있으면: BQ \`cledyu_analytics.lab_events\` 에 행, GCS 에 NDJSON, 뷰 4개 생성 확인.
- 이벤트가 없으면: 실제 랩 세션을 몇 개 돌려 lab-events 를 생성한 뒤 재트리거.
\`\`\`
bq query --use_legacy_sql=false 'SELECT event_type, COUNT(*) FROM cledyu_analytics.lab_events GROUP BY 1'
\`\`\`
```

- [ ] **Step 2: 전체 정적 게이트**

Run:
```
cd /Users/kylekim1223/request700k/cledyu
ruff check apps/airflow/
cd apps/airflow && python -m pytest tests/ -q
cd ../../infra/terraform/gcp && terraform fmt -check
cd /Users/kylekim1223/request700k/cledyu && pre-commit run --files docs/RUNBOOK/d2-data-pipeline.md apps/airflow/dags/sql/d3_views.sql
```
Expected: ruff 무출력, pytest pass, fmt 무출력, pre-commit 통과.

- [ ] **Step 3: 커밋 + 푸시 + PR**

```bash
cd /Users/kylekim1223/request700k/cledyu
git add docs/RUNBOOK/d2-data-pipeline.md
git commit -m "docs(data): add d2 data pipeline apply runbook"
git push -u origin feat/data-pipeline-d2
gh pr create --base main --head feat/data-pipeline-d2 \
  --title "feat(data): add kafka to bigquery analytics pipeline" \
  --body "설계: docs/superpowers/specs/2026-06-26-data-pipeline-d2-design.md (D2)

Airflow 수동트리거 DAG(lab-events Kafka → GCS NDJSON → BigQuery raw load → D3 뷰),
신규 infra/terraform/gcp 모듈(버킷·데이터셋·SA), Vault/ESO 자격증명, KafkaUser.

자동화 범위 = 작성 + 정적검증(terraform validate/fmt, ruff, pytest, yamllint).
라이브 적용(terraform apply, SA 키, vault put, DAG 트리거)은 docs/RUNBOOK/d2-data-pipeline.md
의 수동 단계(GCP/Vault 인증 필요). 데모 데이터는 실 랩 세션으로 생성.

테스트: pytest(transform helpers), ruff, terraform fmt 통과."
gh pr edit --add-assignee ykgoesdumb || true
```

Expected: PR 생성, CI 시작.

---

## Self-Review (작성자 점검 결과)

- **Spec coverage:** 토폴로지(Task 4 DAG), GCS→BQ raw(Task 1·4), 뷰(Task 5), 인프라 terraform(Task 1), 자격증명 Vault/ESO+KafkaUser(Task 2), 멱등성 dedup 뷰(Task 5), 수동트리거(Task 4 schedule=None), 런북/라이브 체크리스트(Task 6) 모두 커버.
- **Placeholder scan:** 모든 코드/매니페스트/HCL/SQL 단계에 실제 내용 포함. "플랜에서 결정"이던 Python 의존성 주입(_PIP)·뷰 위치(SQL include)·SA 마운트는 구체화됨.
- **Type consistency:** Event JSON 필드(event_type…ts) → BQ 스키마(lab_events.json) → event_to_row `_FIELDS` → 뷰 컬럼 전 구간 일치. dag_id/dataset/bucket/secret 이름이 terraform·ESO·DAG·values 에서 동일.
- **알려진 한계(정직):** terraform validate/DAG full-import/BQ SQL dry-run 은 provider 다운로드·airflow 설치·GCP 자격증명이 필요해 환경에 따라 정적검증까지만 가능. 실 동작은 Task 6 런북의 수동 적용으로 검증. chart 1.15.0 values 키(extraEnv/scheduler.extraVolumes)는 apply 시 `helm show values` 대조 필요.
