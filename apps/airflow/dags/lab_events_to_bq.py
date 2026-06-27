"""lab-events Kafka → GCS → BigQuery 적재 DAG (수동 트리거).

흐름: confluent-kafka 로 lab-events 배치 소비 → GCS NDJSON 랜딩 → BQ load(append)
→ D3 뷰 CREATE OR REPLACE. 공개 배포 전이라 schedule=None(데모 시 수동 트리거).
자격증명: airflow-gcp-sa(GOOGLE_APPLICATION_CREDENTIALS), airflow-kafka-cert(mTLS).
"""

import os
from datetime import datetime, timezone

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

        if not rows:
            return ""  # 소비 0건 — 다운스트림 스킵

        run_id = context["run_id"].replace(":", "-")
        blob_path = f"lab-events/dt={datetime.now(timezone.utc):%Y-%m-%d}/run={run_id}.ndjson"  # noqa: UP017
        storage.Client().bucket(BUCKET).blob(blob_path).upload_from_string(
            rows_to_ndjson(rows), content_type="application/x-ndjson"
        )
        # GCS 랜딩이 성공한 뒤에만 offset 커밋 — 업로드 실패 시 재시도가 같은 배치를 재소비한다.
        consumer.commit(asynchronous=False)
        return f"gs://{BUCKET}/{blob_path}"
    finally:
        consumer.close()


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
    template_searchpath=os.path.dirname(os.path.abspath(__file__)),
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
