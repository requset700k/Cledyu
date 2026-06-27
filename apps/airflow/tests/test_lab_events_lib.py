import json
import sys
from pathlib import Path

# dags 폴더를 import 경로에 추가(git-sync 가 dags/ 만 동기화하므로 lib 는 거기 있다).
sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "dags"))

from lab_events_lib import event_to_row, rows_to_ndjson


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
    assert row.get("_ingested_at")
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
