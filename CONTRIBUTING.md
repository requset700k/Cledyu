# Cledyu 기여 가이드

이 레포는 **온프레미스 K8s + KubeVirt + AWS/GCP 하이브리드 교육 플랫폼** 저장소입니다.
빠른 피드백 루프와 운영 안정성 둘 다 지키는 것이 목표입니다.

## 1. 소통

| 용도 | 채널 |
|---|---|
| 일상 논의 / 빠른 질문 | Discord `#general`, 팀별 채널 |
| 인시던트 대응 | Discord `#incident` (GitHub Issue는 사후 기록용) |
| 설계 의사결정 | GitHub Issue + `docs/ADR/` |
| 작업 트래킹 | GitHub Issues + Projects 보드 |

## 2. 브랜치 전략 (Trunk-based + short-lived)

- 기본 브랜치: `main` (항상 배포 가능 상태)
- 작업 브랜치: `<type>/<scope>-<slug>` — 예: `feat/kubevirt-cdi-install`, `fix/validation-timeout`
- 수명: **최대 3일**. 길어지면 잘게 쪼개서 머지
- rebase 우선, `--no-ff` merge는 릴리스 태그에만

## 3. 커밋 / PR 제목 규칙 — Conventional Commits

```
<type>(<scope>): <subject>
```

- `type`: `feat | fix | refactor | perf | docs | test | chore | ci | build | revert | security`
- `scope`: `infra | k8s | terraform | ansible | gitops | kafka | airflow | dbt | nlp | llm | ai | api | web | obs | sec | dr | data | deps`
- `subject`: 명령형, 50자 권장(100자 한도), 소문자 시작, 마침표 X
- 본문은 한 줄 120자 이내로 wrap, Breaking change는 본문에 `BREAKING CHANGE:` 블록 추가

> scope/type 목록의 단일 기준은 [`.commitlintrc.json`](.commitlintrc.json)입니다. KubeVirt·EC2 Orchestrator·Validation Engine 같은 컴포넌트는 별도 scope가 아니라 `api`·`infra`·`data` 등 상위 scope로 묶습니다.

커밋 메시지 틀은 [`.gitmessage`](.gitmessage) 템플릿으로 제공합니다. 레포 루트에서 한 번 활성화하세요:

```bash
git config commit.template .gitmessage
```

검증은 `pre-commit install --hook-type commit-msg` + CI `commitlint` 워크플로가 담당합니다.

## 4. 로컬 환경 셋업

```bash
# 필수 도구
brew install pre-commit terraform tflint ansible ansible-lint shellcheck shfmt \
             yamllint gitleaks pnpm uv go

# 훅 설치 + 커밋 메시지 템플릿
pre-commit install
pre-commit install --hook-type commit-msg
git config commit.template .gitmessage

# Python 환경 (uv 권장)
uv sync

# Node (Next.js)
pnpm install

# Go (Session API, Validation Engine)
go mod download

# 전체 린트/포맷 한 번에
pre-commit run -a
```

## 5. PR 프로세스

1. **이슈 먼저** — 트리비얼(<20줄, docs) 제외, 이슈 또는 Discord 합의 선행
2. **작업 브랜치에서 PR** — PR 템플릿이 자동으로 뜹니다. 체크박스를 모두 의식적으로 채우세요
3. **Reviewers 수동 지정** — 아래 "리뷰 매트릭스"를 참고해 영향 레이어 담당자 1명 이상
4. **CI green + 리뷰 1 LGTM 후 Squash-merge**
5. **머지 후 ArgoCD / 배포 채널을 직접 확인**

### 리뷰 매트릭스 (수동 지정)

| 영향 레이어 | 필수 리뷰어 | 보조 리뷰어 |
|---|---|---|
| 플랫폼 · KubeVirt · EC2 Orchestrator · CI · DR | 김용균 | — |
| 보안 / 시크릿 / IAM / VM 격리 | 윤승호 | 김용균 |
| 관측성 / SLO / 대시보드 | 조승연 | 김용균 |
| Lab DSL · Validation Engine · Kafka · Airflow · dbt | 김찬영 | 양성호 (AI 소비 시) |
| Gemini · RAG · ChromaDB · 임베딩 · 프롬프트 | 양성호 | 김찬영 |
| Go/Gin API · Next.js · xterm.js · 강사 모드 · Kong | 한정현 | 조승연 (성능) |
| 아키텍처 결정 (ADR) | 김용균 + 영향 담당자 | — |

## 6. 코드 스타일

언어별 자동 포매터/린터에 모두 위임합니다. 수동 정렬 금지.

| 언어 / 대상 | 도구 |
|---|---|
| Python | ruff (`ruff check`, `ruff format`) |
| Go | `gofmt`, `golangci-lint` |
| TypeScript / Next.js | ESLint + Prettier |
| Terraform | `terraform fmt`, `tflint`, `terraform-docs` |
| Ansible | `ansible-lint` |
| Shell | `shellcheck`, `shfmt -i 2 -ci -sr` |
| YAML | `yamllint` |
| K8s 매니페스트 | `kubeconform` |
| Markdown | `markdownlint` |
| 시크릿 스캔 | `gitleaks` |

## 7. 보안 원칙

- **시크릿은 절대 레포에 넣지 않는다** — Vault, GitHub Secrets 사용. `.env.example`만 허용
- 신규 IAM / RBAC / NetworkPolicy는 **최소 권한**
- **멀티테넌트 VM 격리 4중 방어**: K8s namespace + ResourceQuota → Cilium NetworkPolicy → KubeVirt seccomp/AppArmor → Kyverno 정책
- 외부 의존성 추가 시 라이선스 + CVE(Trivy) 확인
- 보안 취약점 발견 시 공개 이슈 대신 **GitHub Security Advisory** 제출 (`SECURITY.md` 참고)

## 8. 데이터 계약 변경

- Kafka 토픽 스키마 변경은 **하위 호환 먼저**: 기존 consumer 동작 보장 → 마이그레이션 → 구 필드 deprecation
- 주요 토픽: `learning-events`, `validation-requests`, `validation-results`, `security-logs`
- dbt 모델 변경은 `state:modified+`로 하류 영향 빌드 통과 필수
- OpenAPI 스키마 변경 시 프론트 타입 재생성 커밋 포함
- Lab YAML DSL 변경은 validator 통과 + 기존 Lab 회귀 테스트 통과 필수

## 9. 관측성 원칙

- 새 서비스는 **메트릭 + 구조화 로그(JSON) + trace 전파**를 기본 제공
- SLO에 영향을 주는 변경은 PR 템플릿의 SLO 섹션을 반드시 채움
  - Lab 시작 지연 < 60s (온프렘) / < 90s (EC2)
  - VM 부팅 성공률 > 99.5%
  - Validation 응답 < 10s
  - AI 힌트 지연 < 5s
  - WebSocket 끊김률 < 1%

## 10. 문서화

- 사용자 대면 변경은 `README.md` 또는 `docs/` 업데이트를 **같은 PR에** 포함
- 아키텍처 결정은 `docs/ADR/NNNN-<slug>.md` 템플릿으로 기록
- 운영 절차는 `docs/RUNBOOK/` 에 추가
- Lab 콘텐츠 추가는 `labs/<lab-id>.yaml` + PR 템플릿의 "Lab 콘텐츠" 체크리스트 채움

## 11. 의존성 업그레이드

- 수동 업그레이드 정책. 영역 담당자가 스프린트 단위로 점검
- 마이너·패치: `chore(deps)` 커밋으로 PR
- 메이저 버전: ADR 필요

---

질문이 있다면 Discord에서, 제안이 있다면 `type:chore` 이슈로!
