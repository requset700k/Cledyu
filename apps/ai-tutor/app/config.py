# 우선순위: 환경변수 > 기본값. Go API(config.go)와 동일하게 env 주입을 1급으로 둔다.
from functools import lru_cache

from pydantic import AliasChoices, Field
from pydantic_settings import BaseSettings


class Settings(BaseSettings):
    """AI Tutor BFF 설정.

    GEMINI_API_KEY 가 비어 있으면 힌트 생성은 503(ai_unavailable)을 반환하고,
    Session API 가 Lab DSL 의 정적 hint_levels 로 폴백한다(다층 fallback의 최종 단계).
    """

    # google-genai SDK 표준 env 이름을 그대로 받되, AI_TUTOR_ prefix 도 허용한다.
    gemini_api_key: str = Field(
        default="",
        validation_alias=AliasChoices("GEMINI_API_KEY", "AI_TUTOR_GEMINI_API_KEY"),
    )

    # 모델 티어링(기획서 3.5): 기본 gemini-3-pro → flash → 2.5-flash 순으로 폴백.
    # 쉼표 구분 문자열 — 운영 중 모델 교체/순서 변경을 재배포 없이 env 로 처리.
    gemini_models: str = Field(
        default="gemini-3-pro,gemini-3-flash,gemini-2.5-flash",
        validation_alias=AliasChoices("AI_TUTOR_GEMINI_MODELS", "GEMINI_MODELS"),
    )

    # ChromaDB (RAG). host 가 비어 있으면 RAG 검색을 건너뛴다(힌트는 컨텍스트만으로 생성).
    chroma_host: str = Field(default="", validation_alias=AliasChoices("AI_TUTOR_CHROMA_HOST"))
    chroma_port: int = Field(default=8000, validation_alias=AliasChoices("AI_TUTOR_CHROMA_PORT"))
    # 조직 중립성(기획서 1.1): 항상 public collection + 요청 org collection 을 함께 검색.
    rag_top_k: int = Field(default=4, validation_alias=AliasChoices("AI_TUTOR_RAG_TOP_K"))

    # Guardrails — 기획서 3.5: 수강생당 분당 6회, Lab(세션)당 15회.
    rate_limit_per_minute: int = Field(
        default=6, validation_alias=AliasChoices("AI_TUTOR_RATE_LIMIT_PER_MINUTE")
    )
    rate_limit_per_session: int = Field(
        default=15, validation_alias=AliasChoices("AI_TUTOR_RATE_LIMIT_PER_SESSION")
    )

    request_timeout_seconds: float = Field(
        default=10.0, validation_alias=AliasChoices("AI_TUTOR_REQUEST_TIMEOUT_SECONDS")
    )

    # Rate limit 카운터 백엔드. 비면 in-memory(단일 레플리카 전제), 설정 시 Redis 공유 카운터
    # (다중 레플리카 안전). 예: redis://redis.redis.svc:6379/1
    redis_url: str = Field(default="", validation_alias=AliasChoices("AI_TUTOR_REDIS_URL"))

    @property
    def model_tiers(self) -> list[str]:
        return [m.strip() for m in self.gemini_models.split(",") if m.strip()]


@lru_cache
def get_settings() -> Settings:
    return Settings()
