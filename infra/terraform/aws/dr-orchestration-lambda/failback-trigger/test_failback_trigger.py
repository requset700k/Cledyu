import importlib.util
import os
import pathlib

# 모듈 최상단이 os.environ 을 읽으므로(클라이언트·SM_ARN) import 전에 채운다.
os.environ.setdefault("SFN_REGION", "ap-northeast-2")
os.environ.setdefault(
    "STATE_MACHINE_ARN", "arn:aws:states:ap-northeast-2:0:stateMachine:cledyu-lab-dr-failback"
)
os.environ.setdefault("ACTIVE_PARAM", "/cledyu-dr/failover/active")

_spec = importlib.util.spec_from_file_location(
    "fb_trigger", pathlib.Path(__file__).parent / "index.py"
)
t = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(t)

_E1 = "arn:aws:states:...:execution:dr-failover:e1"
_E2 = "arn:aws:states:...:execution:dr-failover:e2"


def test_gate_open_only_when_failover_active():
    assert t.should_trigger(_E1) is True
    assert t.should_trigger(None) is False


def test_name_prefix_deterministic_from_failover_id():
    a = t.name_prefix(_E1)
    b = t.name_prefix(_E1)
    c = t.name_prefix(_E2)
    assert a == b  # 같은 failover → 같은 접두(RUNNING 중복 판정이 안정적)
    assert a != c  # 다른 failover → 다른 접두
    assert a.startswith("failback-") and len(a) <= 80


def test_running_execution_blocks_duplicate(monkeypatch):
    # 진행 중(RUNNING) failback 이 있으면 하트비트 flapping 이 새 실행을 안 만든다.
    monkeypatch.setattr(t, "_active_value", lambda: _E1)
    monkeypatch.setattr(t, "_running_exists", lambda prefix: True)
    out = t.handler({"detail": {}}, None)
    assert out["started"] is False and out["reason"] == "already-running"


def test_no_running_starts_new_attempt(monkeypatch):
    # RUNNING 이 없으면(첫 실행 또는 이전 failback 이 실패/종료) 새 이름으로 시작 → 재시도 가능.
    captured = {}
    monkeypatch.setattr(t, "_active_value", lambda: _E1)
    monkeypatch.setattr(t, "_running_exists", lambda prefix: False)
    monkeypatch.setattr(t._sfn, "start_execution", lambda **kw: captured.update(kw))
    out = t.handler({"detail": {"alarmName": "cledyu-lab-dr-push"}}, None)
    assert out["started"] is True
    # 전체 이름은 접두 + 타임스탬프라 시도마다 유니크(닫힌 이름 90일 잠김 회피).
    assert captured["name"].startswith(t.name_prefix(_E1))
    assert captured["name"] != t.name_prefix(_E1)


def test_not_failed_over_is_noop(monkeypatch):
    monkeypatch.setattr(t, "_active_value", lambda: None)
    out = t.handler({"detail": {}}, None)
    assert out["started"] is False and out["reason"] == "not-failed-over"
