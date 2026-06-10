# Gemini 모델 티어링(기획서 3.5): pro → flash → 2.5-flash 순 다층 fallback.
# 모든 티어 실패 시 GeminiUnavailable — Session API 가 Lab DSL 정적 힌트로 최종 폴백한다.
import logging

from pydantic import BaseModel

from .config import Settings

logger = logging.getLogger(__name__)


class GeminiUnavailable(Exception):
    """API 키 미설정 또는 전 티어 호출 실패."""


class HintOut(BaseModel):
    """structured output 스키마 — JSON 응답 강제(기획서 2.3)."""

    hint: str


class GeminiClient:
    def __init__(self, settings: Settings):
        self._settings = settings
        self._client = None

    def _connect(self):
        if not self._settings.gemini_api_key:
            raise GeminiUnavailable("GEMINI_API_KEY is not configured")
        if self._client is None:
            # google-genai 는 배포 이미지에만 설치되는 선택 의존 — lazy import.
            from google import genai

            self._client = genai.Client(api_key=self._settings.gemini_api_key)
        return self._client

    def generate_hint(self, system_prompt: str, user_prompt: str) -> tuple[str, str]:
        """티어 순서대로 시도해 (힌트 텍스트, 사용 모델)을 반환한다."""
        client = self._connect()
        from google.genai import types

        config = types.GenerateContentConfig(
            system_instruction=system_prompt,
            temperature=0.4,
            max_output_tokens=1024,
            response_mime_type="application/json",
            response_schema=HintOut,
            # http_options.timeout 단위는 ms.
            http_options=types.HttpOptions(
                timeout=int(self._settings.request_timeout_seconds * 1000)
            ),
        )

        last_err: Exception | None = None
        for model in self._settings.model_tiers:
            try:
                resp = client.models.generate_content(
                    model=model, contents=user_prompt, config=config
                )
                parsed = resp.parsed  # response_schema 지정 시 SDK 가 파싱해 준다.
                if isinstance(parsed, HintOut) and parsed.hint.strip():
                    return parsed.hint.strip(), model
                # 파싱 실패 시 원문 텍스트로 폴백.
                if resp.text and resp.text.strip():
                    return HintOut.model_validate_json(resp.text).hint.strip(), model
                raise ValueError("empty response")
            except Exception as e:  # rate limit/장애 → 다음 티어로 전환(안전망)
                last_err = e
                logger.warning("gemini 호출 실패 — 다음 티어로 전환", extra={"model": model})
        raise GeminiUnavailable(f"all model tiers failed: {last_err}")
