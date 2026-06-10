# AGENTS.md

## 응답 언어

- PR 리뷰, 요약, 질문, 제안은 한국어로 작성합니다.
- 코드 식별자, 파일 경로, 명령어, 에러 메시지, API 이름은 원문을 유지합니다.
- 리뷰 코멘트는 정중하고 간결하게 작성하며, 가능한 경우 수정 방향을 함께 제안합니다.
- 스타일만 지적하기보다 버그, 보안 회귀, 배포 위험, 누락된 검증을 우선합니다.

## 저장소 컨텍스트

Cledyu는 KodeKloud 스타일의 브라우저 기반 Hands-on 교육 플랫폼입니다.
학습자는 웹 브라우저만으로 격리된 KubeVirt 또는 EC2 VM에 접속해 Linux, Ansible,
Terraform, Kubernetes를 실습합니다. AI 학습 도우미는 정답을 직접 제공하지 않고
소크라테스식 단계별 힌트를 제공하며, Validation Engine은 실습 결과를 자동 채점합니다.

이 저장소는 플랫폼 인프라, GitOps 매니페스트, CI/CD, 보안 정책, 서비스 애플리케이션,
데이터/AI 컴포넌트, 운영 문서를 함께 관리합니다.

`main`은 항상 배포 가능한 상태여야 합니다. 특히 `gitops/` 아래 변경은 ArgoCD 자동 동기화,
prune, self-heal에 의해 실제 클러스터에 반영될 수 있으므로 운영 영향도를 반드시 확인합니다.

## 코드베이스 읽기 순서

PR 리뷰 시 변경 파일만 보지 말고 관련 컨텍스트를 함께 확인합니다.

- 공통 규칙: `CONTRIBUTING.md`, `.github/PULL_REQUEST_TEMPLATE.md`, `.commitlintrc.json`
- CI/CD: `.github/workflows/`
- GitOps/클러스터: `gitops/argocd/apps/`, `gitops/apps/`, `docs/RUNBOOK/`
- API: `apps/api/`, 관련 API 문서와 인증/인가 코드
- Validation Engine: `apps/validation-engine/`, Lab DSL 문서, Kafka 이벤트 계약
- AI Tutor: `apps/ai-tutor/`, RAG/Guardrails/모델 fallback 문서
- Web: `apps/web/`, 인증/권한/터미널/강사 모드 관련 코드
- 인프라: `infra/`, `ansible/`, 관련 runbook
- 아키텍처 변경: `docs/ADR/`

## 리뷰 방식과 심각도 기준

리뷰 코멘트는 아래 구조를 따릅니다.

- 무엇이 문제인지 한 문장으로 설명합니다.
- Cledyu 제품/운영 맥락에서 왜 위험한지 설명합니다.
- 가능하면 수정 방향이나 검증 방법을 제안합니다.

심각도는 다음 기준으로 판단합니다.

- P1: 보안 회귀, 권한 우회, 시크릿 노출, 실클러스터 장애 가능성, 데이터 유출, VM/EC2 정리 실패, 비용 폭증, SLO 명백한 악화
- P2: 중요한 검증 누락, rollback 불명확, 운영 문서 누락, 호환성 위험, 관측성/감사로그 누락
- P3: 유지보수성, 중복, 네이밍, 문서 표현 개선

P3 스타일 코멘트는 리뷰 소음을 만들 수 있으므로 꼭 필요한 경우에만 남깁니다.

## 영역별 리뷰 플레이북

변경 경로를 기준으로 관련 영역을 고르고, 여러 영역이 겹치면 모두 적용합니다.

### 플랫폼 / GitOps / 클러스터

함께 볼 컨텍스트:

- `gitops/argocd/apps/`
- `gitops/apps/`
- `docs/RUNBOOK/`
- `.github/workflows/lint.yml`
- `.github/workflows/build-image.yml`

리뷰 관점:

- 이 PR이 merge되면 ArgoCD가 무엇을 실제 클러스터에 적용하는지 확인합니다.
- `prune`, `selfHeal`, `CreateNamespace`, sync wave, retry 설정이 의도에 맞는지 봅니다.
- Helm chart 버전, values 변경, live default 명시가 drift를 줄이는지 또는 새 drift를 만드는지 확인합니다.
- `kubectl apply --dry-run=server`, `helm template`, `kubeconform`, `pre-commit` 등 필요한 검증이 PR 본문에 있는지 확인합니다.

P1로 볼 문제:

- ArgoCD prune/selfHeal로 운영 리소스가 의도치 않게 삭제될 수 있음
- 운영 manifest가 `latest` 또는 mutable tag에 의존함
- namespace, CRD, sync wave 순서 문제로 첫 배포가 실패할 가능성이 큼
- rollback 방법 없이 Vault, Keycloak, Kyverno, KubeVirt, networking 경로를 변경함

### 보안 / 인증 / 멀티테넌시

함께 볼 컨텍스트:

- Keycloak, Vault, External Secrets, Kyverno 관련 manifest
- RBAC, ServiceAccount, NetworkPolicy, securityContext
- 인증/인가 middleware와 API route group

리뷰 관점:

- 사용자, 강사, 관리자, worker, cluster component 사이의 trust boundary가 유지되는지 봅니다.
- ServiceAccount와 Vault/Kubernetes auth role이 실제 workload와 일치하는지 확인합니다.
- `default` ServiceAccount 사용, wildcard RBAC, broad Vault path, 넓은 NetworkPolicy를 의심합니다.
- 감사로그와 보안 이벤트가 필요한 경로에서 빠지지 않았는지 확인합니다.

P1로 볼 문제:

- JWT 검증이나 RBAC를 우회할 수 있음
- student가 instructor/admin 기능에 접근할 수 있음
- 조직별 RAG 문서, 토큰, credential, kubeconfig, 개인정보가 노출될 수 있음
- 수강생 VM 간 통신 격리 또는 VM-to-host 방어가 약해짐

### API / Session / VM Orchestrator

함께 볼 컨텍스트:

- `apps/api/`
- 인증/인가 middleware
- session lifecycle, cleanup, provider abstraction
- 관련 OpenAPI 또는 API 문서

리뷰 관점:

- Lab session lifecycle이 생성, provisioning, terminal 연결, validation, event 발행, cleanup까지 끊기지 않는지 봅니다.
- 온프렘 KubeVirt 우선, EC2 fallback, 운영자 강제 지정 같은 routing policy가 유지되는지 확인합니다.
- 실패, 취소, timeout, 재시도 경로에서 VM, namespace, EC2 instance, Redis/DB 상태가 누수되지 않는지 확인합니다.
- provider별 구현이 API business logic에 과도하게 새지 않는지 봅니다.

P1로 볼 문제:

- 실패/취소 시 EC2 instance 또는 KubeVirt VM이 남음
- 사용자가 다른 사용자의 session, terminal, validation result에 접근 가능함
- timeout/retry가 중복 provisioning 또는 중복 billing을 만들 수 있음
- admin route가 인증 또는 최소 역할 검사를 빠뜨림

### Validation Engine / Lab DSL

함께 볼 컨텍스트:

- `apps/validation-engine/`
- Lab DSL 문서와 샘플 Lab
- Kafka `validation-requests`, `validation-results`, `learning-events` 계약

리뷰 관점:

- 검증 로직이 수강생 VM 내부가 아니라 외부 worker의 신뢰 경계 안에서 실행되는지 봅니다.
- DSL 변경이 기존 Lab과 하위호환되는지 확인합니다.
- command validation에 timeout, 출력 제한, 실패 사유, 민감 정보 필터링이 있는지 확인합니다.
- KubeVirt `virtctl ssh`와 EC2 SSM 경로가 같은 DSL을 일관되게 실행하는지 봅니다.

P1로 볼 문제:

- 학생이 VM 내부에서 validation 결과를 조작할 수 있음
- 무제한 command 실행, 무제한 출력, timeout 없음
- validation 실패 사유에 secret, token, 환경변수, 내부 endpoint가 노출됨
- 이벤트 계약 변경으로 학습 분석 또는 UI가 깨짐

### AI Tutor / RAG / Guardrails

함께 볼 컨텍스트:

- `apps/ai-tutor/`
- RAG indexing/retrieval 코드
- prompt, guardrail, model fallback, rate limit 코드
- Lab DSL의 `hint_levels`

리뷰 관점:

- AI 튜터가 정답을 직접 제공하지 않고 단계별 힌트로 유도하는지 봅니다.
- Gemini Pro, Flash, 정적 hint fallback 경로가 유지되는지 확인합니다.
- public collection과 조직별 collection이 분리되고, tenant context가 누락되지 않는지 봅니다.
- 터미널 히스토리, RAG 컨텍스트, 사용자 정보가 필요한 최소 범위만 모델과 로그에 전달되는지 확인합니다.
- rate limit, token usage, 감사로그, 비용 downgrade 경로가 유지되는지 봅니다.

P1로 볼 문제:

- AI가 정답 전체, credential, exploit 절차를 직접 제공함
- 조직별 문서가 다른 조직 사용자에게 검색됨
- rate limit 또는 비용 보호 장치를 우회함
- 모델 장애 시 fallback 없이 학습 흐름이 중단됨

### Web / 학습자 UI / 강사 모드

함께 볼 컨텍스트:

- `apps/web/`
- 인증/권한 guard
- xterm.js terminal, reconnect, loading/error state
- instructor dashboard와 session viewer

리뷰 관점:

- 학습자가 브라우저만으로 Lab 시작, terminal 사용, hint 요청, validation 결과 확인을 할 수 있는지 봅니다.
- provisioning 중 상태, 실패 메시지, retry UX가 있는지 확인합니다.
- 강사 모드는 read-only 관전을 기본으로 하고, 명령 주입은 권한/감사로그/일회성 제한이 있는지 확인합니다.
- 프론트 권한 숨김만으로 보안이 성립한다고 가정하지 않는지 봅니다.

P1로 볼 문제:

- 학생이 다른 학생 terminal/session을 볼 수 있음
- 강사 명령 주입이 감사로그 없이 반복 실행 가능함
- token 또는 개인정보가 browser storage/log에 과도하게 남음
- reconnect 실패가 Lab 진행 상태 손실로 이어짐

### 데이터 / 분석 / Kafka / DB

함께 볼 컨텍스트:

- Kafka topic/schema 정의
- Airflow/dbt/BigQuery/DataHub 관련 코드
- DB migration과 persistence layer

리뷰 관점:

- 이벤트 schema 변경이 기존 consumer와 하위호환되는지 봅니다.
- Lab 완료율, 막힘 분포, 평균 소요 시간, 온프렘/EC2 사용 비율 분석에 필요한 필드가 유지되는지 확인합니다.
- DB 변경에는 migration, rollback, backward compatibility 설명이 있는지 확인합니다.
- 개인정보와 학습 이벤트가 필요한 목적에 맞게 최소 수집되는지 봅니다.

P1로 볼 문제:

- 이벤트 또는 DB schema 변경으로 기존 consumer/API/UI가 깨짐
- 개인정보가 analytics/logging 경로에 불필요하게 들어감
- migration rollback 경로 없이 운영 테이블을 파괴적으로 변경함

### 문서 / ADR / Runbook

함께 볼 컨텍스트:

- `docs/ADR/`
- `docs/RUNBOOK/`
- `README.md`
- `.github/PULL_REQUEST_TEMPLATE.md`

리뷰 관점:

- 운영자가 실제로 배포, rollback, 장애 대응을 수행할 수 있는 수준인지 봅니다.
- 아키텍처 의사결정은 ADR로 남아야 하는지 확인합니다.
- 사용자 대면 변경은 README/docs/runbook 업데이트와 함께 가는지 확인합니다.

P1/P2로 볼 문제:

- 파괴적 운영 작업인데 rollback/runbook이 없음
- DR, 비용, SLO 영향이 있는데 PR 본문이나 문서에 설명이 없음
- 문서가 실제 manifest, workflow, API 동작과 다름

## PR / 커밋 규칙

- PR 제목은 Conventional Commits 형식이어야 합니다: `<type>(<scope>): <subject>`.
- 허용 type: `feat`, `fix`, `refactor`, `perf`, `docs`, `test`, `chore`, `ci`, `build`, `revert`, `security`.
- 허용 scope: `infra`, `k8s`, `terraform`, `ansible`, `gitops`, `kafka`, `airflow`, `dbt`, `nlp`, `llm`, `ai`, `api`, `web`, `obs`, `sec`, `dr`, `data`, `deps`.
- 이 repo는 squash-merge를 사용하므로 PR 제목이 main 히스토리에 남습니다. 제목이 변경 내용을 오해하게 만들면 지적합니다.
- PR 본문에 변경 요약, 관련 이슈, 변경 유형, 테스트, 체크리스트, 해당 시 인프라 가드레일이 채워졌는지 확인합니다.
- 기존 패턴과 다른 구현, 새 추상화, 새 의존성이 들어오면 PR 본문에 이유가 설명되어 있는지 확인합니다.

## CI / 검증 기준

- 인프라 또는 GitOps 변경은 `pre-commit run -a`, `kubectl apply --dry-run=server`, `helm template`, `kubeconform`, `terraform fmt`, `terraform validate` 중 해당 검증을 확인합니다.
- Go 변경은 `go test ./...`, `go test -race`, `gofmt`, `golangci-lint` 적용 여부를 확인합니다.
- Python 변경은 `ruff check`, `ruff format --check`, 관련 테스트를 확인합니다.
- Web 변경은 `pnpm lint`, 관련 테스트를 확인합니다.
- Dockerfile 또는 의존성 변경은 Trivy HIGH/CRITICAL 취약점 영향을 확인합니다.
- 문서만 변경한 PR에는 불필요하게 광범위한 테스트를 요구하지 않습니다.
- CI가 이미 잡는 단순 형식 문제보다 CI가 놓칠 수 있는 운영 리스크, 권한 리스크, 호환성 리스크를 우선합니다.

## 보안 공통 기준

아래 항목은 높은 우선순위로 리뷰합니다.

- 시크릿, 토큰, kubeconfig, cloud credential, Vault root token, unseal key, API key가 커밋된 경우
- 신규 IAM, RBAC, Vault policy, Kubernetes auth role, GitHub App 권한, NetworkPolicy가 최소권한을 벗어나는 경우
- `default` ServiceAccount를 불필요하게 사용하거나, 명시적 ServiceAccount 변경으로 워크로드 접근이 깨질 수 있는 경우
- Keycloak, Vault, External Secrets, Kyverno, 인증/인가 변경에 rollout/rollback 설명이 없는 경우
- 사용자 인증 또는 API 인가 변경에 거부 경로 테스트가 없는 경우
- 멀티테넌트 VM 격리 4중 방어가 약화되는 경우: namespace/ResourceQuota, Cilium NetworkPolicy, KubeVirt seccomp/AppArmor, Kyverno 정책

## 제품 원칙

- Lab 세션은 생성, VM 프로비저닝, cloud-init 초기화, xterm.js 연결, 단계별 검증, 이벤트 발행, VM 정리 흐름을 유지해야 합니다.
- 온프렘 KubeVirt 우선, 리소스 부족 시 AWS EC2 fallback 정책이 깨지지 않는지 확인합니다.
- Lab 종료, 실패, 취소 경로에서 KubeVirt namespace/VM 또는 EC2 instance가 정리되는지 확인합니다.
- 교육과정/조직 중립성을 유지합니다. 특정 조직 문서나 설정이 public/default 경로에 섞이지 않도록 확인합니다.
- 사용자 대면 변경이나 운영 절차 변경은 같은 PR에 README, docs, runbook 중 필요한 문서를 포함해야 합니다.

## GitOps / 클러스터 리뷰 기준

- `gitops/` 변경은 실제 클러스터에 반영될 수 있으므로 배포 영향, rollback, 검증 결과를 확인합니다.
- `gitops/argocd/apps/` 아래 파일은 파일당 하나의 ArgoCD `Application` 또는 `ApplicationSet`을 정의해야 합니다.
- 신규 ArgoCD 앱은 sync wave, `CreateNamespace=true`, retry, prune/selfHeal 영향, rollback 방법을 검토합니다.
- Helm values 변경은 live drift, 기본값 변경, ArgoCD OutOfSync 가능성을 확인합니다.
- 운영 manifest의 이미지 태그는 가능한 한 immutable `sha-<short>` 태그를 사용해야 합니다.
- Kyverno 정책은 Audit/Warn/Enforce 전환 의도가 PR에 명확해야 합니다.
- Kyverno rule type과 맞지 않는 필드를 사용하면 지적합니다. 예를 들어 generate-only 정책에 validate-only 필드를 넣지 않습니다.
- Kyverno autogen 변경 시 ReplicaSet/ReplicationController/Pod PolicyReport 커버리지 변화가 의도된 것인지 확인합니다.
- securityContext 변경은 non-root 실행, privilege escalation, hostPath, hostNetwork, capabilities 영향을 확인합니다.
- KubeVirt/VM 관련 변경은 namespace/ResourceQuota, Cilium NetworkPolicy, seccomp/AppArmor, Kyverno 가드레일을 유지해야 합니다.
- EC2 overflow 관련 변경은 Security Group egress, IAM 최소권한, SSM 감사 경로를 확인합니다.

## API / Session / VM Orchestrator 리뷰 기준

- 모든 사용자 API는 Keycloak JWT 검증과 역할 기반 인가를 우회하지 않아야 합니다.
- admin, instructor, student 역할 경계가 깨지면 높은 우선순위로 지적합니다.
- 권한 거부 경로 테스트가 없으면 지적합니다.
- Lab Session API는 생성, VM 프로비저닝, 터미널 연결, Validation, 이벤트 발행, cleanup 경로를 유지해야 합니다.
- KubeVirt client-go와 AWS SDK 추상화가 특정 provider에 과도하게 결합되지 않도록 확인합니다.
- 실패, 취소, timeout, retry 경로에서 리소스 누수와 중복 세션 가능성을 확인합니다.
- 동시성, timeout, retry, cleanup 로직은 race나 누수 가능성을 확인합니다.

## Validation Engine / Lab DSL 리뷰 기준

- Validation Engine은 수강생 VM 내부에 신뢰 로직을 두지 않고 외부 worker에서 검증해야 합니다.
- Lab DSL 변경은 기존 Lab과 하위호환되는지 확인합니다.
- `command`, `file_exists`, `file_content`, `process_running`, `http_response` 검증이 안전하게 실행되는지 확인합니다.
- 명령 실행에는 timeout, 출력 제한, 에러 사유 기록이 있어야 합니다.
- KubeVirt는 `virtctl ssh`, EC2는 SSM 경로를 통한 공통 추상화가 유지되어야 합니다.
- `validation-requests`, `validation-results`, `learning-events` 계약이 깨지지 않는지 확인합니다.
- 실패 사유는 학습자와 강사가 이해할 수 있어야 하며, 민감 정보가 포함되면 안 됩니다.

## AI Tutor / RAG 리뷰 기준

- AI 튜터는 정답을 직접 제공하지 않고 소크라테스식 단계별 힌트를 제공해야 합니다.
- Hint Level 1/2/3 구조와 Lab DSL의 정적 fallback hint를 유지합니다.
- Gemini Pro, Flash, 정적 힌트 fallback 경로를 깨뜨리지 않습니다.
- ChromaDB collection은 public과 조직별 namespace를 분리해야 합니다.
- 조직별 문서가 다른 조직 사용자에게 노출될 수 있으면 높은 우선순위로 지적합니다.
- RAG 컨텍스트, 터미널 히스토리, 사용자 정보는 필요한 최소 범위만 모델에 전달해야 합니다.
- 수강생별 rate limit, Lab별 hint limit, 토큰 사용량 추적을 우회하지 않아야 합니다.
- 감사로그에는 민감 정보가 남지 않아야 합니다.
- AI Pro/GCP 크레딧 소진 시 fallback 또는 downgrade 경로가 유지되어야 합니다.

## Web / 강사 모드 리뷰 기준

- 브라우저만으로 Lab 카탈로그, 터미널, 단계 체크리스트, AI 도우미, Validation 결과를 사용할 수 있어야 합니다.
- xterm.js 터미널 연결 상태, reconnect, loading, error, empty state를 확인합니다.
- Lab 시작/VM 프로비저닝 중에는 진행 상태를 명확히 보여야 합니다.
- 강사 모드는 기본적으로 read-only 관전을 우선합니다.
- 명령 주입 기능은 권한, 감사로그, 일회성 실행 제한이 있어야 합니다.
- 여러 학생 세션을 보여줄 때 사용자/세션 데이터가 섞이면 안 됩니다.
- 학생, 강사, 관리자 UI 경계가 API 권한과 일치해야 합니다.
- 프론트만으로 권한을 숨기는 방식에 의존하지 않습니다.

## 데이터 / 호환성 리뷰 기준

- Kafka topic 또는 schema 변경은 consumer 하위호환성을 확인합니다.
- Lab YAML DSL 변경은 validator 결과와 기존 Lab 회귀 테스트를 확인합니다.
- OpenAPI 변경은 프론트 타입 재생성 포함 여부를 확인합니다.
- DB 또는 persistence 변경은 migration, rollback, backward compatibility 설명을 확인합니다.
- Breaking change는 PR 변경 요약에 명확히 표시되어야 합니다.
- 학습 이벤트는 Lab 완료율, 막힘 분포, 평균 소요 시간, 온프렘/EC2 사용 비율 분석에 필요한 필드를 유지해야 합니다.

## SLO / 비용 / DR 리뷰 기준

다음 목표에 영향을 주는 변경은 PR 본문에 영향 분석이나 검증 결과가 있어야 합니다.

- Lab 시작 지연: 온프렘 <60s, EC2 <90s
- VM 부팅 성공률: >99.5%
- Validation 응답: <10s
- AI 힌트 지연: <5s
- WebSocket 끊김률: <1%

인프라 변경은 비용 영향이 `+$0`이어도 PR 본문에 명시되어야 합니다.
EC2 오버플로우, Gemini API, BigQuery, GKE Autopilot, S3/Glacier, OpenSearch 변경은 비용 영향을 확인합니다.
백업, 스토리지, 클러스터 상태, 배포 토폴로지 변경은 DR 영향과 Velero RPO 1h / GKE Autopilot RTO 4h 유지 여부를 확인합니다.

## 현재 클러스터 상태

- 현재 단계에서는 Codex 리뷰가 실클러스터에 직접 접속하지 않습니다.
- PR diff, repo 문서, GitOps manifest, CI 결과를 기준으로 리뷰합니다.
- 향후 `docs/ops/cluster-state.md` 같은 클러스터 상태 스냅샷이 추가되면 GitOps, Kubernetes, 보안 정책, Vault, Keycloak, KubeVirt, 서비스 배포 관련 PR에서 먼저 참고합니다.
- 클러스터 상태 스냅샷은 관측 정보이며 source of truth는 GitOps manifest입니다. 둘이 다르면 drift 가능성을 지적합니다.
- 시크릿, 토큰, kubeconfig, Pod 환경변수, 민감 로그를 요청하거나 리뷰에 노출하지 않습니다.

## 문서화 기준

- 사용자 대면 변경이나 운영 절차 변경은 같은 PR에 README, docs, runbook 중 필요한 문서를 포함해야 합니다.
- 아키텍처 결정은 `docs/ADR/`에 기록합니다.
- 운영 절차는 `docs/RUNBOOK/`에 기록합니다.
- Lab 콘텐츠 추가나 DSL 변경은 콘텐츠 작성자와 운영자가 이해할 수 있는 문서를 포함해야 합니다.
