# AGENTS.md

## 기본 원칙

- PR 리뷰, 요약, 질문, 제안은 한국어로 작성합니다.
- 코드 식별자, 파일 경로, 명령어, 에러 메시지, API 이름은 원문을 유지합니다.
- 리뷰는 정확성, 보안, 배포 안정성, 테스트 누락, 유지보수성을 우선합니다.
- 스타일, 네이밍, 문장 표현 같은 낮은 위험도 코멘트는 꼭 필요한 경우에만 남깁니다.
- 코멘트는 문제가 무엇인지, 왜 위험한지, 어떻게 수정하거나 검증하면 좋은지 간결하게 설명합니다.

## 저장소 컨텍스트

Cledyu는 브라우저 기반 Hands-on 교육 플랫폼입니다. 이 저장소는 인프라, GitOps 매니페스트, CI/CD, 보안 정책, 서비스 코드, 데이터/AI 컴포넌트, 운영 문서를 함께 관리합니다.

기획과 아키텍처는 변경될 수 있습니다. 리뷰할 때는 미래 계획을 확정된 요구사항처럼 강제하지 말고, 현재 코드와 문서에 이미 반영된 계약을 기준으로 판단합니다.

## 먼저 확인할 컨텍스트

PR diff만 보지 말고 변경 영역에 맞는 기존 코드와 문서를 함께 확인합니다.

- 공통 규칙: `CONTRIBUTING.md`, `.github/PULL_REQUEST_TEMPLATE.md`, `.commitlintrc.json`
- CI/CD: `.github/workflows/`
- GitOps/클러스터: `gitops/argocd/apps/`, `gitops/apps/`
- 서비스 코드: `apps/`
- 인프라: `infra/`, `ansible/`
- 운영/설계 문서: `docs/RUNBOOK/`, `docs/ADR/`

## PR / 커밋 규칙

- PR 제목은 Conventional Commits 형식이어야 합니다: `<type>(<scope>): <subject>`.
- 허용 type: `feat`, `fix`, `refactor`, `perf`, `docs`, `test`, `chore`, `ci`, `build`, `revert`, `security`.
- 허용 scope: `infra`, `k8s`, `terraform`, `ansible`, `gitops`, `kafka`, `airflow`, `dbt`, `nlp`, `llm`, `ai`, `api`, `web`, `obs`, `sec`, `dr`, `data`, `deps`.
- 이 repo는 squash-merge를 사용하므로 PR 제목이 main 히스토리에 남습니다.
- PR 본문에 변경 요약, 변경 유형, 테스트, 체크리스트가 변경 내용과 맞게 작성됐는지 확인합니다.

## 리뷰 기준

### 공통

- 기존 구조와 패턴을 우선합니다. 새 추상화나 의존성은 필요한 이유가 분명해야 합니다.
- 사용자 입력, 외부 API, 파일/명령 실행, 네트워크 요청은 실패 경로와 예외 처리를 확인합니다.
- 핵심 로직 변경에는 적절한 테스트나 검증 결과가 있어야 합니다.
- 사용자 대면 변경이나 운영 절차 변경은 관련 문서 업데이트 여부를 확인합니다.
- Breaking change는 PR 본문에 영향 범위와 마이그레이션 경로가 명확해야 합니다.

### 보안

- 시크릿, 토큰, kubeconfig, cloud credential, API key가 커밋되면 높은 우선순위로 지적합니다.
- 인증/인가 변경은 권한 경계가 깨지지 않는지 확인합니다.
- RBAC, ServiceAccount, IAM, NetworkPolicy, Vault/secret 관련 변경은 최소권한 원칙을 확인합니다.
- 사용자 데이터, 조직별 문서, 로그, 감사 데이터가 다른 사용자나 조직에 노출될 가능성을 확인합니다.

### GitOps / 인프라

- `gitops/` 변경은 실제 배포 영향, rollback 가능성, ArgoCD sync/prune/selfHeal 영향을 확인합니다.
- Kubernetes manifest와 Helm values 변경은 drift, 기본값 변경, OutOfSync 가능성을 확인합니다.
- securityContext, RBAC, ServiceAccount, NetworkPolicy 변경은 보안과 workload 호환성을 함께 확인합니다.
- 인프라 변경은 적용 순서와 실패 시 복구 방법이 설명되어 있는지 확인합니다.

### 서비스 코드

- API, Web, Validation, AI 컴포넌트 변경은 서로의 계약을 깨지 않는지 확인합니다.
- 세션, 작업, VM, 외부 리소스 같은 lifecycle이 있는 코드는 생성/실패/취소/timeout/cleanup 경로를 확인합니다.
- 비동기 처리, retry, timeout, background worker는 중복 실행, 누수, race 가능성을 확인합니다.
- 모델 호출, RAG, 로그 처리처럼 외부로 데이터가 나가는 경로는 최소 정보만 전달하는지 확인합니다.

## 검증 기준

- 인프라/GitOps 변경은 `pre-commit`, `kubectl apply --dry-run=server`, `helm template`, `kubeconform`, `terraform fmt`, `terraform validate` 중 해당 검증을 확인합니다.
- Go 변경은 `go test`, `go test -race`, `gofmt`, `golangci-lint` 중 해당 검증을 확인합니다.
- Python 변경은 `ruff check`, `ruff format --check`, 관련 테스트를 확인합니다.
- Web 변경은 `pnpm lint`, 관련 테스트를 확인합니다.
- 문서만 변경한 PR에는 불필요하게 광범위한 테스트를 요구하지 않습니다.

## 확장 방식

- 루트 `AGENTS.md`는 공통 가드레일만 유지합니다.
- 특정 디렉터리의 규칙이 충분히 반복되면 그때 `gitops/AGENTS.md`, `apps/api/AGENTS.md`, `apps/web/AGENTS.md`처럼 가까운 위치에 세부 지침을 추가합니다.
- 더 가까운 `AGENTS.md`가 있으면 해당 디렉터리의 지침을 우선합니다.

## 현재 클러스터 상태

- 현재 단계에서는 Codex 리뷰가 실클러스터에 직접 접속하지 않습니다.
- PR diff, repo 문서, GitOps manifest, CI 결과를 기준으로 리뷰합니다.
- 향후 `docs/ops/cluster-state.md` 같은 클러스터 상태 스냅샷이 추가되면 그때 리뷰 기준에 반영합니다.
- 시크릿, 토큰, kubeconfig, Pod 환경변수, 민감 로그를 요청하거나 리뷰에 노출하지 않습니다.

## 구현 전 보류 기준

아래 기준은 아직 구현이 완료되지 않은 단계이므로 현재 PR 리뷰 기준으로 강하게 적용하지 않습니다. 해당 기능이 실제로 도입되면 주석을 해제하고 구체화합니다.

<!--
- EC2, Gemini, BigQuery, GKE, S3, OpenSearch 등 클라우드 전용 비용/서비스 운영 규칙
- Velero RPO, GKE Autopilot RTO 등 DR 목표 기반 리뷰 규칙
-->
