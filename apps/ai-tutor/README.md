# AI 학습 도우미 BFF (ai-tutor)

실습 중 막힌 학생에게 **소크라테스식 힌트**를 제공하는 FastAPI 서비스입니다.
Session API(Go)가 `POST /v1/hints` 로 호출하는 내부 전용 BFF 이며, 학생 브라우저에
직접 노출되지 않습니다.

## 요청 흐름

```
학생 → web → Session API(POST /api/v1/sessions/:id/hint)
                  └→ ai-tutor(POST /v1/hints)
                       ├─ Guardrails: rate limit(분당 6회/세션당 15회)
                       ├─ RAG: ChromaDB [org collection + public] 검색 (미설정 시 skip)
                       ├─ Gemini 티어링: gemini-3-pro → 3-flash → 2.5-flash
                       └─ Guardrails: 정답 누출 마스킹(레벨 1~2)
              ← 503(ai_unavailable) 이면 Session API 가 Lab DSL hint_levels 정적 폴백
```

## 환경변수

| 변수 | 기본값 | 설명 |
|---|---|---|
| `GEMINI_API_KEY` | (없음) | 비어 있으면 503 → Session API 정적 폴백 |
| `AI_TUTOR_GEMINI_MODELS` | `gemini-3-pro,gemini-3-flash,gemini-2.5-flash` | 티어 순서 |
| `AI_TUTOR_CHROMA_HOST` | (없음) | 비어 있으면 RAG 생략 |
| `AI_TUTOR_CHROMA_PORT` | `8000` | |
| `AI_TUTOR_RAG_TOP_K` | `4` | 힌트당 검색 청크 수 |
| `AI_TUTOR_RATE_LIMIT_PER_MINUTE` | `6` | 수강생당 분당 힌트 한도 |
| `AI_TUTOR_RATE_LIMIT_PER_SESSION` | `15` | 세션당 누적 힌트 한도 |
| `AI_TUTOR_REQUEST_TIMEOUT_SECONDS` | `10` | Gemini 호출 타임아웃 |

## 로컬 실행 / 테스트

```bash
cd apps/ai-tutor
python3 -m venv .venv && .venv/bin/pip install -r requirements-dev.txt
.venv/bin/pytest tests/

# 실제 Gemini 로 띄우기
.venv/bin/pip install -r requirements.txt
GEMINI_API_KEY=... .venv/bin/uvicorn app.main:app --port 8080
```

## RAG 인덱싱 (오프라인 배치)

```bash
python scripts/index_docs.py --host <chroma-host> --collection public \
  --source ./corpus/k8s --title-prefix "Kubernetes Docs" --base-url https://kubernetes.io/docs
```

조직별 문서는 `--collection org-<이름>` 으로 분리 적재합니다(조직 중립성).

## 운영 메모

- rate limit 카운터는 in-memory 입니다. 레플리카 확장 시 Redis(ElastiCache) 카운터로
  이관해야 합니다.
- 감사로그는 JSON 한 줄 로그로 stdout 에 남기며 Loki 가 수집합니다. S3 90일 보관
  파이프라인은 후속 작업입니다.
- 메트릭: `ai_hint_requests_total{outcome,model}`, `ai_hint_latency_seconds` (`/metrics`).
