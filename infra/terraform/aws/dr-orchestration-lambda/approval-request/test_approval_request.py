import importlib.util
import pathlib

_spec = importlib.util.spec_from_file_location(
    "approval_index", pathlib.Path(__file__).parent / "index.py"
)
ar = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(ar)


def _button_types(msg):
    return [c["components"][0]["type"] for c in msg["components"]]


def test_failover_message_has_select_and_button():
    msg = ar._build_message(
        "abc123", is_test=False, mode=None, snapshots=["vault/a.snap", "vault/b.snap"]
    )
    # ActionRow 2개: String Select(3) + Button(2)
    assert _button_types(msg) == [3, 2]
    assert "dr-approve:abc123" in msg["components"][1]["components"][0]["custom_id"]


def test_failback_message_single_button_no_select():
    msg = ar._build_message("fb01", is_test=False, mode="failback", snapshots=None)
    # ActionRow 1개: Button(2)만
    assert _button_types(msg) == [2]
    btn = msg["components"][0]["components"][0]
    assert btn["custom_id"] == "dr-approve:fb01"
    assert "페일백" in msg["content"] or "온프렘" in msg["content"]
