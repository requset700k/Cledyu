import importlib.util
import pathlib

# index.py 를 모듈로 로드(핸들러 실행 없이 _render 만 검사)
_spec = importlib.util.spec_from_file_location(
    "notify_index", pathlib.Path(__file__).parent / "index.py"
)
notify = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(notify)


def test_failover_success_has_no_failback_prep_tail():
    text = notify._render(
        {
            "outcome": "success",
            "detectedAt": "2026-07-17T00:00:00Z",
            "approvedAt": "2026-07-17T00:05:00Z",
            "alb": "dr-alb.example",
        }
    )
    assert "✅" in text and "페일오버 완료" in text
    assert "페일백" not in text
    assert "backupEnabled" not in text


def test_failback_success_message():
    text = notify._render(
        {
            "outcome": "failback-success",
            "approvedAt": "2026-07-17T00:00:00Z",
            "detectedAt": "2026-07-17T00:00:00Z",
        }
    )
    assert "페일백 완료" in text
    assert "온프렘" in text


def test_failback_failed_dns_reverted_flag():
    reverted = notify._render(
        {"outcome": "failback-failed", "failedState": "TeardownHot", "dnsReverted": True}
    )
    not_reverted = notify._render(
        {"outcome": "failback-failed", "failedState": "RevertDNS", "dnsReverted": False}
    )
    assert "온프렘" in reverted
    assert "EKS" in not_reverted
