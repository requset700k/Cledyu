# AGENTS.md

## 기본 원칙

- PR 리뷰, 요약, 질문, 제안은 한국어로 작성합니다.
- 코드 식별자, 파일 경로, 명령어, 에러 메시지, API 이름은 원문을 유지합니다.
- 리뷰는 버그, 보안 회귀, 배포 위험, 누락된 검증을 우선합니다.
- 스타일, 네이밍, 문서 표현 같은 낮은 위험도 코멘트는 꼭 필요한 경우에만 남깁니다.
- 코멘트는 "문제", "왜 위험한지", "수정 또는 검증 방향"이 드러나게 간결하게 작성합니다.

## 저장소 컨텍스트

Cledyu는 브라우저 기반 Hands-on 교육 플랫폼입니다. 학습자는 격리된 KubeVirt 또는 EC2 VM에 접속해 Linux, Ansible, Terraform, Kubernetes를 실습하고, AI 튜터는 정답 대신 단계별 힌트를 제공하며, Validation Engine은 실습 결과를 자동 채점합니다.

이 저장소는 플랫폼 인프라, GitOps 매니페스트, CI/CD, 보안 정책, 서비스 애플리케이션, 데이터/AI 컴포넌트, 운영 문서를 함께 관리합니다.

`main`은 배포 가능한 상태여야 합니다. 특히 `gitops/` 변경은 ArgoCD를 통해 실제 클러스터에 반영될 수 있으므로 운영 영향과 rollback 가능성을 확인합니다.

## 먼저 확인할 컨텍스트

PR diff만 보지 말고 변경 영역에 맞는 기존 코드와 문서를 함께 확인합니다.

- 공통 규칙: `CONTRIBUTING.md`, `.github/PULL_REQUEST_TEMPLATE.md`, `.commitlintrc.json`
- CI/CD: `.github/workflows/`
- GitOps/클러스터: `gitops/argocd/apps/`, `gitops/apps/`, `docs/RUNBOOK/`
- API: `apps/api/`
- Validation Engine: `apps/validation-engine/`
- AI Tutor: `apps/ai-tutor/`
- Web: `apps/web/`
- 인프라: `infra/`, `ansible/`
- 아키텍처 결정: `docs/ADR/`

## PR / 커밋 규칙

- PR 제목은 Conventional Commits 형식이어야 합니다: `<type>(<scope>): <subject>`.
- 허용 type: `feat`, `fix`, `refactor`, `perf`, `docs`, `test`, `chore`, `ci`, `build`, `revert`, `security`.
- 허용 scope: `infra`, `k8s`, `terraform`, `ansible`, `gitops`, `kafka`, `airflow`, `dbt`, `nlp`, `llm`, `ai`, `api`, `web`, `obs`, `sec`, `dr`, `data`, `deps`.
- 이 repo는 squash-merge를 사용하므로 PR 제목이 main 히스토리에 남습니다. 제목이 변경 내용을 오해하게 만들면 지적합니다.
- PR 본문에 변경 요약, 변경 유형, 테스트, 체크리스트가 변경 내용과 맞게 작성됐는지 확인합니다.

## 영역별 리뷰 기준

### GitOps / 클러스터

- `gitops/` 변경은 실제 클러스터 반영 가능성을 기준으로 리뷰합니다.
- ArgoCD `Application` 변경은 sync wave, namespace 생성, prune/selfHeal, retry, rollback 영향을 확인합니다.
- Helm values와 Kubernetes manifest 변경은 live drift, 기본값 변경, OutOfSync 가능성을 확인합니다.
- Kyverno 정책은 Audit/Warn/Enforce 의도, rule type과 필드의 정합성, PolicyReport 커버리지 변화를 확인합니다.
- securityContext, RBAC, ServiceAccount, NetworkPolicy 변경은 최소권한과 workload 호환성을 함께 확인합니다.

### 보안 / 인증 / 멀티테넌시

- 시크릿, 토큰, kubeconfig, cloud credential, Vault token, unseal key, API key가 커밋되면 높은 우선순위로 지적합니다.
- Keycloak, Vault, External Secrets, Kyverno, 인증/인가 변경은 권한 경계와 rollback 설명을 확인합니다.
- student, instructor, admin 역할 경계가 깨지거나 API 권한 검사가 빠지면 높은 우선순위로 지적합니다.
- 조직별 문서, RAG collection, 사용자 데이터가 다른 조직이나 다른 사용자에게 노출될 가능성을 확인합니다.

### API / Session / VM Orchestrator

- Lab session lifecycle이 생성, VM 프로비저닝, 터미널 연결, Validation, 이벤트 발행, cleanup까지 유지되는지 확인합니다.
- 온프렘 KubeVirt 우선, 리소스 부족 시 EC2 fallback 정책이 깨지지 않는지 확인합니다.
- 실패, 취소, timeout, retry 경로에서 VM, namespace, EC2 instance, DB/Redis 상태가 누수되지 않는지 확인합니다.
- 핵심 비즈니스 로직, 권한 거부 경로, cleanup 경로에는 테스트가 있어야 합니다.

### Validation Engine / Lab DSL

- Validation Engine은 수강생 VM 내부가 아니라 외부 worker에서 검증해야 합니다.
- Lab DSL 변경은 기존 Lab과 하위호환되는지 확인합니다.
- command 검증에는 timeout, 출력 제한, 실패 사유 기록, 민감 정보 노출 방지가 필요합니다.
- `validation-requests`, `validation-results`, `learning-events` 계약이 깨지지 않는지 확인합니다.

### AI Tutor / RAG

- AI 튜터는 정답을 직접 제공하지 않고 소크라테스식 단계별 힌트를 제공해야 합니다.
- Hint Level 1/2/3 구조와 정적 fallback hint 흐름이 유지되는지 확인합니다.
- RAG는 public collection과 조직별 collection을 분리해야 합니다.
- 모델 호출에는 필요한 최소 컨텍스트만 전달하고, 터미널 히스토리나 사용자 정보가 과도하게 로그에 남지 않도록 확인합니다.
- rate limit, fallback, 감사로그 같은 안전장치를 우회하지 않는지 확인합니다.

### Web / 강사 모드

- 학습자가 브라우저만으로 Lab 시작, 터미널 사용, 힌트 요청, Validation 결과 확인을 할 수 있어야 합니다.
- terminal reconnect, loading, error, empty state를 확인합니다.
- 강사 모드는 read-only 관전을 기본으로 하고, 명령 주입 기능은 권한과 감사로그가 있어야 합니다.
- 프론트에서 숨기는 것만으로 권한이 보장된다고 가정하지 않습니다.

### 데이터 / 호환성

- Kafka topic 또는 schema 변경은 consumer 하위호환성을 확인합니다.
- OpenAPI 변경은 프론트 타입 재생성 포함 여부를 확인합니다.
- DB 또는 persistence 변경은 migration, rollback, backward compatibility 설명을 확인합니다.
- Breaking change는 PR 변경 요약에 명확히 표시되어야 합니다.

## 검증 기준

- 인프라/GitOps 변경은 `pre-commit run -a`, `kubectl apply --dry-run=server`, `helm template`, `kubeconform`, `terraform fmt`, `terraform validate` 중 해당 검증을 확인합니다.
- Go 변경은 `go test ./...`, `go test -race`, `gofmt`, `golangci-lint` 적용 여부를 확인합니다.
- Python 변경은 `ruff check`, `ruff format --check`, 관련 테스트를 확인합니다.
- Web 변경은 `pnpm lint`, 관련 테스트를 확인합니다.
- 문서만 변경한 PR에는 불필요하게 광범위한 테스트를 요구하지 않습니다.

## 현재 클러스터 상태

- 현재 단계에서는 Codex 리뷰가 실클러스터에 직접 접속하지 않습니다.
- PR diff, repo 문서, GitOps manifest, CI 결과를 기준으로 리뷰합니다.
- 향후 `docs/ops/cluster-state.md` 같은 클러스터 상태 스냅샷이 추가되면 그때 리뷰 기준에 반영합니다.
- 시크릿, 토큰, kubeconfig, Pod 환경변수, 민감 로그를 요청하거나 리뷰에 노출하지 않습니다.

## 구현 전 보류 기준

아래 기준은 아직 구현이 완료되지 않은 단계이므로 현재 PR 리뷰 기준으로 강하게 적용하지 않습니다. 해당 기능이 실제로 도입되면 주석을 해제하고 구체화합니다.

<!--
### 클라우드 비용 / 서비스 운영 기준

- EC2 오버플로우, Gemini API, BigQuery, GKE Autopilot, S3/Glacier, OpenSearch 변경은 비용 영향을 확인합니다.
- AI Pro/GCP 크레딧 소진, Gemini fallback, 주간 토큰 대시보드 흐름을 확인합니다.

### DR 기준

- Velero RPO 1h, GKE Autopilot RTO 4h DR 전략에 영향이 있으면 PR에 명시해야 합니다.
- 백업, 스토리지, 클러스터 상태, 배포 토폴로지 변경은 DR 영향과 복구 경로를 확인합니다.
-->
