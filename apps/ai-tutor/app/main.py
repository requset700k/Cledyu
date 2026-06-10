# AI 학습 도우미 BFF 엔트리포인트.
#
# 흐름(기획서 3.5 온라인 단계): Context 수집 → RAG 검색 → 프롬프트 조합 →
# Gemini 호출(티어링) → Guardrails 후처리 → 응답. 감사로그는 구조화 JSON 로그로
# 남기고 Loki 가 수집한다(S3 90일 보관은 후속 — docs/architecture/ai-tutor.md 참고).
import json
import logging
import time

from fastapi import FastAPI, Response
from fastapi.responses import JSONResponse
from prometheus_client import (
    CONTENT_TYPE_LATEST,
    Counter,
    Histogram,
    generate_latest,
)

from .config import get_settings
from .gemini import GeminiClient, GeminiUnavailable
from .guardrails import RateLimiter, RateLimitExceeded, mask_answer_leak
from .models import DocSource, ErrorResponse, HintRequest, HintResponse
from .prompts import SYSTEM_PROMPT, build_user_prompt
from .rag import Retriever

logger = logging.getLogger("ai_tutor")
logging.basicConfig(level=logging.INFO, format="%(message)s")

HINT_REQUESTS = Counter("ai_hint_requests_total", "AI 힌트 요청 수", ["outcome", "model"])
HINT_LATENCY = Histogram(
    "ai_hint_latency_seconds",
    "AI 힌트 생성 지연(SLO: p95 < 5s)",
    buckets=(0.5, 1, 2, 3, 5, 8, 13, 21),
)

app = FastAPI(title="cledyu-ai-tutor", docs_url=None, redoc_url=None)

settings = get_settings()


def _build_limiter():
    """REDIS_URL 설정 시 공유 카운터(다중 레플리카 안전), 아니면 in-memory."""
    if settings.redis_url:
        try:
            from .ratelimit_redis import RedisRateLimiter

            rl = RedisRateLimiter(
                settings.redis_url,
                settings.rate_limit_per_minute,
                settings.rate_limit_per_session,
            )
            logger.info(json.dumps({"event": "rate_limiter", "backend": "redis"}))
            return rl
        except Exception:
            logger.warning("redis rate limiter 초기화 실패 — in-memory 폴백", exc_info=True)
    return RateLimiter(settings.rate_limit_per_minute, settings.rate_limit_per_session)


limiter = _build_limiter()
retriever = Retriever(settings)
gemini = GeminiClient(settings)


@app.get("/healthz")
def healthz() -> dict:
    return {"status": "ok"}


@app.get("/metrics")
def metrics() -> Response:
    return Response(generate_latest(), media_type=CONTENT_TYPE_LATEST)


@app.post("/v1/hints", response_model=HintResponse, responses={429: {"model": ErrorResponse}})
def create_hint(req: HintRequest):
    start = time.monotonic()
    try:
        limiter.check(req.user_id, req.session_id)
    except RateLimitExceeded as e:
        HINT_REQUESTS.labels(outcome="rate_limited", model="").inc()
        msg = (
            "힌트 요청이 너무 잦습니다. 잠시 후 다시 시도하세요."
            if e.scope == "user_minute"
            else "이 세션의 힌트 사용 한도(15회)에 도달했습니다."
        )
        return JSONResponse(status_code=429, content={"error": msg, "code": "rate_limited"})

    query = f"{req.step.title}\n{req.step.description}".strip()
    chunks = retriever.search(req.org_id, query)
    prompt = build_user_prompt(req, chunks)

    try:
        hint, model = gemini.generate_hint(SYSTEM_PROMPT, prompt)
    except GeminiUnavailable:
        # 최종 폴백(정적 hint_levels)은 Session API 책임 — 503 으로 신호만 보낸다.
        HINT_REQUESTS.labels(outcome="unavailable", model="").inc()
        return JSONResponse(
            status_code=503,
            content={"error": "AI 도우미를 사용할 수 없습니다", "code": "ai_unavailable"},
        )

    hint = mask_answer_leak(hint, req.step.commands, req.hint_level)
    elapsed = time.monotonic() - start
    HINT_LATENCY.observe(elapsed)
    HINT_REQUESTS.labels(outcome="ok", model=model).inc()

    # 감사로그(JSON 한 줄) — 사용자/랩/스텝/모델/지연. 힌트 본문은 품질 리뷰용으로 포함.
    logger.info(
        json.dumps(
            {
                "audit": "ai_hint",
                "user_id": req.user_id,
                "session_id": req.session_id,
                "lab_id": req.lab_id,
                "step_id": req.step.id,
                "hint_level": req.hint_level,
                "org_id": req.org_id,
                "model": model,
                "rag_chunks": len(chunks),
                "latency_ms": int(elapsed * 1000),
                "hint": hint,
            },
            ensure_ascii=False,
        )
    )

    sources = [
        DocSource(title=c["title"], url=c["url"]) for c in chunks if c.get("title") or c.get("url")
    ]
    # 같은 문서가 여러 청크로 잡히면 링크가 중복되므로 (title,url) 기준으로 dedup.
    seen: set[tuple[str, str]] = set()
    unique_sources = []
    for s in sources:
        key = (s.title, s.url)
        if key not in seen:
            seen.add(key)
            unique_sources.append(s)

    return HintResponse(hint=hint, hint_level=req.hint_level, model=model, sources=unique_sources)
