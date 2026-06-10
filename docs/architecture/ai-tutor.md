# AI 학습 도우미 (ai-tutor)

기획서 3.5 'AI 학습 도우미 + RAG 파이프라인'의 구현입니다. 실습 중 막힌 학생에게
**소크라테스식 힌트**(정답 직접 제시 금지, 단계별 유도)를 제공합니다.

## 구성

```
학생 브라우저 (AiTutorPanel)
   │  POST /api/v1/sessions/:id/hint  { step_id, level?, terminal_tail? }
   ▼
Session API (apps/api)
   │  - stepStore 에서 lab/스텝 확인, 힌트 레벨 자동 상승(1→2→3)
   │  - internal/ai 클라이언트로 BFF 호출
   │  POST /v1/hints
   ▼
ai-tutor BFF (apps/ai-tutor, FastAPI)
   ├─ Guardrails(사전): rate limit — 수강생당 분당 6회, 세션당 15회
   ├─ RAG: ChromaDB [org collection + public] 검색 (CHROMA_HOST 미설정 시 생략)
   ├─ Gemini 티어링: gemini-3-pro → gemini-3-flash → gemini-2.5-flash
   └─ Guardrails(사후): 레벨 1~2 응답에서 모범답안 명령 원문 마스킹
```

## 다층 Fallback (기획서 3.5)

| 단계 | 장애 | 동작 |
|---|---|---|
| 1 | gemini-3-pro 실패(429/5xx) | gemini-3-flash 로 자동 전환 |
| 2 | flash 도 실패 | gemini-2.5-flash (무료 티어) |
| 3 | 전 티어 실패 / API 키 미설정 / BFF 다운 | Session API 가 Lab DSL `hint_levels[level-1]` 정적 힌트 반환 |

429(힌트 한도 초과)는 폴백하지 않고 그대로 사용자에게 전달합니다(한도 우회 방지).

## 힌트 레벨

- **레벨 1(개념)**: 봐야 할 개념/원리만. 명령 이름 미언급
- **레벨 2(방향)**: 명령 이름 + 핵심 옵션의 존재까지
- **레벨 3(구체)**: placeholder(`<>`) 포함 명령 구조까지. 완성 명령은 금지

레벨은 Session API 의 stepStore 가 스텝별로 추적하며, 레벨 미지정 요청은 직전
사용 레벨 +1 (최대 3)로 자동 상승합니다. Lab DSL 의 `hint_levels`(3개)는 동일한
레벨 의미를 갖는 정적 폴백입니다.

## RAG 멀티테넌트 (조직 중립성)

ChromaDB collection 을 조직 단위로 분리합니다 — `public`, `org-kt-cloud`, `org-<기업>`.
힌트 생성 시 [요청 org, public] 을 함께 검색하므로, KT Cloud 사내 문서 확보 시
`org-kt-cloud` collection 에 주입만 하면 코드 변경 없이 반영됩니다.

인덱싱(오프라인 배치, 주 1회):

```bash
python apps/ai-tutor/scripts/index_docs.py --host <chroma> --collection public \
  --source ./corpus/k8s --title-prefix "Kubernetes Docs" --base-url https://kubernetes.io/docs
```

임베딩은 chromadb 기본 ONNX `all-MiniLM-L6-v2`(기획서의 sentence-transformers 와
동일 모델)를 사용합니다.

## 배포

- `gitops/apps/ai-tutor` 차트 → ArgoCD `service-ai-tutor` (ns: `ai-tutor`)
- 이미지: `ghcr.io/requset700k/ai-tutor` (build-apps.yml 매트릭스가 빌드/스캔/태그 bump)
- `GEMINI_API_KEY`: Vault kv `ai/gemini`(property `api_key`) → ESO → Secret
  `ai-tutor-gemini`. **Vault 에 키 등록 전까지는** Secret 미생성 → 정적 힌트 폴백으로
  동작(서비스는 정상 기동).
- Session API 연동: `CLEDYU_AI_BASE_URL=http://ai-tutor.ai-tutor.svc:8080`

### Vault 키 등록 (운영자)

```bash
vault kv put secret/ai/gemini api_key=<GEMINI_API_KEY>
```

## 관측성

- 메트릭(`/metrics`): `ai_hint_requests_total{outcome,model}`,
  `ai_hint_latency_seconds` — SLI 'AI 힌트 지연 < 5s'(기획서 2.3)의 소스
- 감사로그: JSON 한 줄(stdout) → Loki 수집. `audit=ai_hint` 필드로 검색

## 제약 / 후속 과제

- rate limit 카운터 in-memory → 레플리카 확장 시 Redis(ElastiCache) 이관
- 감사로그 S3 90일 보관 파이프라인
- `hint_requested` 학습 이벤트의 Kafka `lab-events` 발행 (학습 분석 파이프라인과 함께)
- Context Caching(시스템 프롬프트 + RAG 컨텍스트 캐시로 Pro 입력 토큰 절감)
- ChromaDB 클러스터 배포(현재 CHROMA_HOST 빈 값 — RAG 미가동, 힌트는 스텝 컨텍스트만 사용)
