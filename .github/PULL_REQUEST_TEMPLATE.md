<!--
PR 제목 = Conventional Commits. 이 레포는 Squash-merge라 PR 제목만 main 히스토리에 남습니다.
  <type>(<scope>): <subject>
  type:  feat fix refactor perf docs test chore ci build revert security
  scope: infra k8s terraform ansible gitops kafka airflow dbt nlp llm ai api web obs sec dr data deps
  예) feat(api): 세션 생성 시 유저당 활성 세션 1개 제한
main 동기화는 rebase 권장: git fetch origin main && git rebase origin/main && git push --force-with-lease
-->

## 변경 요약
<!-- 무엇을, 왜 바꿨는지 2-3줄. "어떻게"는 diff가 설명하므로 생략합니다. -->

## 관련 이슈
- Closes #

## 변경 유형
- [ ] `feat` 기능   [ ] `fix` 버그   [ ] `refactor` 동작 변경 없는 개선   [ ] `perf` 성능
- [ ] `infra` 인프라   [ ] `security` 보안/시크릿/정책   [ ] `docs` 문서   [ ] `ci` 파이프라인
- [ ] ⚠️ BREAKING CHANGE — 사라지거나 바뀐 인터페이스와 마이그레이션 경로를 변경 요약에 명시합니다.

## 테스트
```bash
# 해당하는 검증만 남겨주세요
go test ./... && golangci-lint run   # Go (Session API · Validation Engine)
pnpm test && pnpm lint               # Next.js
pytest -q                            # Python (AI BFF)
pre-commit run -a                    # 포맷·린트·시크릿 스캔 전체
```
<!-- 결과·스크린샷·plan/diff 출력을 붙이거나 링크합니다. -->

## 체크리스트
- [ ] 제목이 Conventional Commits 형식입니다.
- [ ] 시크릿·크레덴셜 커밋이 없습니다 (gitleaks 통과).
- [ ] 영향 레이어 담당자를 Reviewers에 1명 이상 지정했습니다.
- [ ] 사용자 대면 변경 시 문서(README·docs·런북)를 같은 PR에 포함했습니다.

<details>
<summary>인프라 가드레일 — 보안 · 데이터 계약 · SLO · 비용/DR (해당 시 펼쳐서 작성)</summary>

### 영향 레이어 / 리뷰어
| 레이어 | 컴포넌트 | 담당 |
|---|---|---|
| 플랫폼·CI·DR | kubeadm · Cilium · MetalLB · Longhorn · KubeVirt · CDI · EC2 Orchestrator · ArgoCD · Velero · Crossplane · Istio | 김용균 |
| 보안 | Keycloak · Vault · Falco · Kyverno · WAF · GuardDuty · VM 격리(seccomp/AppArmor/SG) | 윤승호 |
| 관측성 | Prometheus · Grafana · Loki · Tempo · OTel · Hubble · SLO | 조승연 |
| Lab+데이터 | Lab DSL · Validation Engine · virtctl/SSM · Strimzi Kafka · Airflow · dbt · BigQuery · DataHub | 김찬영 |
| AI 튜터 | Gemini 3 Pro/Flash · RAG · ChromaDB · sentence-transformers · Guardrails | 양성호 |
| 서비스 | Go/Gin Session API · VM Orchestrator · Next.js · xterm.js · Kong · Redis · Lambda/SES · CloudFront | 한정현 |

### 보안
- [ ] 시크릿/토큰/API 키 하드코딩 없음 (Vault · GitHub Secrets 사용).
- [ ] 신규 IAM/RBAC/NetworkPolicy 최소권한 원칙을 지켰습니다.
- [ ] 멀티테넌트 VM 격리 4중 방어 유지 (namespace+ResourceQuota → Cilium NetPol → KubeVirt seccomp/AppArmor → Kyverno).
- [ ] Trivy · golangci-lint · ESLint · Ruff 게이트를 통과했습니다.

### 데이터 계약
- [ ] Kafka 토픽/스키마 변경 시 컨슈머 하위호환을 검토했습니다.
- [ ] Lab YAML DSL 변경 시 validator 통과 + 기존 Lab 회귀 테스트를 통과했습니다.
- [ ] OpenAPI 스키마 변경 시 프론트 타입 재생성을 포함했습니다.

### SLO
- [ ] 아래 SLO에 영향이 없거나 영향 분석을 첨부했습니다 — Lab 시작 <60s(온프렘)/<90s(EC2), VM 부팅 >99.5%, Validation <10s, AI 힌트 <5s, WebSocket 끊김 <1%.

### 비용 · DR
- 월 비용 변화: **+$0 (변화 없음)** 또는 `+$XX (AWS/GCP/온프렘 내역)`.
- [ ] 예산(AWS $710 / GCP $300 + Google AI Pro $30) 마진 내입니다.
- [ ] DR 경로(Velero 백업 → AWS 기반 복구)를 검토했고 RPO 1h / RTO 4h를 유지합니다. (GCP는 AI·학습 데이터 용도 전용 — DR 대상 아님)

</details>
