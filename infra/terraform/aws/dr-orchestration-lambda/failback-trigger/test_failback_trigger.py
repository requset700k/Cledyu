import importlib.util
import os
import pathlib

# 모듈 최상단이 os.environ 을 읽으므로(클라이언트·SM_ARN) import 전에 채운다.
os.environ.setdefault("SFN_REGION", "ap-northeast-2")
os.environ.setdefault(
    "STATE_MACHINE_ARN", "arn:aws:states:ap-northeast-2:0:stateMachine:cledyu-lab-dr-failback"
)
os.environ.setdefault("ACTIVE_PARAM", "/cledyu-dr/failover/active")
os.environ.setdefault("PUSH_ALARM_NAME", "cledyu-lab-dr-push")

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


def test_verify_alarm_skips_when_onprem_still_down(monkeypatch):
    # 직접 호출(failover 재확인/주기 reconcile)인데 push 아직 ALARM → 시작 안 함(정상 회복은 EventBridge).
    monkeypatch.setattr(t, "_active_value", lambda: _E1)
    monkeypatch.setattr(t, "_push_ok", lambda: False)
    out = t.handler({"verify_alarm": True}, None)
    assert out["started"] is False and out["reason"] == "onprem-still-down"


def test_verify_alarm_starts_when_onprem_recovered(monkeypatch):
    # active+push OK+RUNNING없음 → 놓친/실패한 자동 failback 을 여기서 재개(레이스·재시도 보정).
    captured = {}
    monkeypatch.setattr(t, "_active_value", lambda: _E1)
    monkeypatch.setattr(t, "_push_ok", lambda: True)
    monkeypatch.setattr(t, "_running_exists", lambda prefix: False)
    monkeypatch.setattr(t._sfn, "start_execution", lambda **kw: captured.update(kw))
    out = t.handler({"verify_alarm": True}, None)
    assert out["started"] is True and captured["name"].startswith(t.name_prefix(_E1))


def test_normal_eventbridge_path_skips_push_recheck(monkeypatch):
    # verify_alarm 아니면(정규 EventBridge OK 이벤트) _push_ok 를 부르지 않는다(이벤트가 이미 OK 전이).
    monkeypatch.setattr(t, "_active_value", lambda: _E1)
    monkeypatch.setattr(t, "_running_exists", lambda prefix: False)
    monkeypatch.setattr(t._sfn, "start_execution", lambda **kw: None)

    def _boom():
        raise AssertionError("정규 경로에서 _push_ok 를 부르면 안 된다")

    monkeypatch.setattr(t, "_push_ok", _boom)
    out = t.handler({"detail": {}}, None)
    assert out["started"] is True
