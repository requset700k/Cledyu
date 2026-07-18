import importlib.util
import pathlib

_spec = importlib.util.spec_from_file_location(
    "fb_trigger", pathlib.Path(__file__).parent / "index.py"
)
t = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(t)


def test_gate_open_only_when_failover_active():
    assert t.should_trigger("arn:aws:states:...:execution:dr-failover:e1") is True
    assert t.should_trigger(None) is False


def test_exec_name_deterministic_from_failover_id():
    a = t.exec_name("arn:aws:states:...:execution:dr-failover:e1")
    b = t.exec_name("arn:aws:states:...:execution:dr-failover:e1")
    c = t.exec_name("arn:aws:states:...:execution:dr-failover:e2")
    assert a == b  # 같은 failover → 같은 이름(중복 트리거 멱등)
    assert a != c  # 다른 failover → 다른 이름
    assert a.startswith("failback-") and len(a) <= 80
