import app.main as main_mod
import pytest
from app.gemini import GeminiUnavailable
from fastapi.testclient import TestClient


@pytest.fixture()
def client(monkeypatch):
    # 테스트 간 rate limit 상태 격리.
    from app.guardrails import RateLimiter

    monkeypatch.setattr(main_mod, "limiter", RateLimiter(6, 15))
    return TestClient(main_mod.app)


def hint_payload(**over):
    base = {
        "user_id": "u1",
        "session_id": "s1",
        "lab_id": "lab-linux-basics",
        "step": {
            "id": 1,
            "title": "디렉터리와 파일 만들기",
            "description": "work 폴더와 notes.txt 생성",
            "commands": ["mkdir -p ~/work", "touch ~/work/notes.txt"],
        },
        "hint_level": 1,
    }
    base.update(over)
    return base


def test_healthz(client):
    res = client.get("/healthz")
    assert res.status_code == 200
    assert res.json() == {"status": "ok"}


def test_hint_ok(client, monkeypatch):
    monkeypatch.setattr(
        main_mod.gemini, "generate_hint", lambda sys, usr: ("폴더를 만드는 명령을 떠올려 보세요.", "gemini-3-pro")
    )
    monkeypatch.setattr(main_mod.retriever, "search", lambda org, q: [])
    res = client.post("/v1/hints", json=hint_payload())
    assert res.status_code == 200
    body = res.json()
    assert body["model"] == "gemini-3-pro"
    assert body["hint_level"] == 1
    assert "힌트" not in body.get("error", "")


def test_hint_masks_answer_leak(client, monkeypatch):
    leaked = "정답은 mkdir -p ~/work 입니다."
    monkeypatch.setattr(main_mod.gemini, "generate_hint", lambda sys, usr: (leaked, "gemini-3-pro"))
    monkeypatch.setattr(main_mod.retriever, "search", lambda org, q: [])
    res = client.post("/v1/hints", json=hint_payload(hint_level=1))
    assert res.status_code == 200
    assert "mkdir -p ~/work" not in res.json()["hint"]


def test_hint_unavailable_returns_503(client, monkeypatch):
    def boom(sys, usr):
        raise GeminiUnavailable("no key")

    monkeypatch.setattr(main_mod.gemini, "generate_hint", boom)
    monkeypatch.setattr(main_mod.retriever, "search", lambda org, q: [])
    res = client.post("/v1/hints", json=hint_payload())
    assert res.status_code == 503
    assert res.json()["code"] == "ai_unavailable"


def test_hint_rate_limited(client, monkeypatch):
    monkeypatch.setattr(main_mod.gemini, "generate_hint", lambda sys, usr: ("h", "m"))
    monkeypatch.setattr(main_mod.retriever, "search", lambda org, q: [])
    for _ in range(6):
        assert client.post("/v1/hints", json=hint_payload()).status_code == 200
    res = client.post("/v1/hints", json=hint_payload())
    assert res.status_code == 429
    assert res.json()["code"] == "rate_limited"


def test_hint_sources_deduped(client, monkeypatch):
    chunks = [
        {"text": "a", "title": "K8s Docs", "url": "https://kubernetes.io/docs"},
        {"text": "b", "title": "K8s Docs", "url": "https://kubernetes.io/docs"},
    ]
    monkeypatch.setattr(main_mod.gemini, "generate_hint", lambda sys, usr: ("h", "m"))
    monkeypatch.setattr(main_mod.retriever, "search", lambda org, q: chunks)
    res = client.post("/v1/hints", json=hint_payload())
    assert res.status_code == 200
    assert len(res.json()["sources"]) == 1


def test_invalid_level_rejected(client):
    res = client.post("/v1/hints", json=hint_payload(hint_level=4))
    assert res.status_code == 422
