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
    if isinstance(msg_value, bytes | bytearray):
        msg_value = msg_value.decode("utf-8")
    event = json.loads(msg_value)
    row = {field: event.get(field) for field in _FIELDS}
    row["_ingested_at"] = datetime.now(timezone.utc).isoformat()  # noqa: UP017
    return row


def rows_to_ndjson(rows):
    """row dict 리스트를 BQ load 용 NDJSON(줄당 1 객체) 문자열로 직렬화한다."""
    return "\n".join(json.dumps(row, ensure_ascii=False) for row in rows)
