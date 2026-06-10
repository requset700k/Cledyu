# Session API(Go) internal/ai 클라이언트와 주고받는 요청/응답 계약.
# 필드를 바꾸면 apps/api/internal/ai/client.go 의 구조체와 함께 바꿔야 한다.
from pydantic import BaseModel, Field


class StepInfo(BaseModel):
    """힌트 대상 스텝의 콘텐츠(Lab DSL Step 의 부분 집합)."""

    id: int
    title: str
    description: str = ""
    # 정답 명령 목록 — 프롬프트에는 '채점 기준'으로만 제공하고,
    # guardrails 가 레벨 1~2 응답에서 원문 그대로의 노출을 마스킹한다.
    commands: list[str] = Field(default_factory=list)


class HintRequest(BaseModel):
    user_id: str
    session_id: str
    lab_id: str
    org_id: str = "public"  # RAG 멀티테넌트 collection 선택(기획서 3.5)
    step: StepInfo
    hint_level: int = Field(default=1, ge=1, le=3)
    # 학생 터미널의 최근 출력 일부(옵션) — '막힌 지점' 파악용.
    terminal_tail: str = Field(default="", max_length=4000)


class DocSource(BaseModel):
    """힌트와 함께 보여줄 관련 문서 링크(RAG 출처)."""

    title: str
    url: str = ""


class HintResponse(BaseModel):
    hint: str
    hint_level: int
    model: str  # 실제 응답을 생성한 모델(티어링 결과). 정적 폴백은 Session API 가 처리.
    sources: list[DocSource] = Field(default_factory=list)


class ErrorResponse(BaseModel):
    error: str
    code: str = ""
