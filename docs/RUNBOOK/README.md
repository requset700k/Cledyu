# Runbook

운영 절차 모음. 모든 런북은 **복붙으로 실행 가능한 커맨드 + 검증 + 롤백**을 포함합니다.

## 작성 규칙

파일명: `<영역>-<동작>.md` (예: `kafka-add-topic.md`, `k8s-drain-node.md`)

각 런북의 섹션 구조:

```markdown
# <제목>

## 트리거

- 언제 이 런북을 실행하는가 (알림, 증상, 스케줄)

## 사전 조건

- 권한, 리소스, 사전 체크 커맨드

## 절차

1. 단계별 커맨드 (복사 가능한 코드블럭)
2. 각 단계의 **예상 출력**

## 검증

- 성공 판정 커맨드 / 메트릭 / 대시보드 링크

## 롤백

- 실패 시 되돌리는 절차

## 참고

- 관련 ADR, 이슈, Grafana 패널
```

## 런북 인덱스 (추가 시 업데이트)

### 플랫폼 / DR — 김용균

- [x] `ec2-overflow.md`
- [x] `dr-restore-drill.md`
- [ ] `k8s-drain-node.md`
- [ ] `velero-restore.md`
- [ ] `dr-drill-gke-autopilot.md`
- [ ] `argocd-rollback.md`

### 보안 — 윤승호

- [ ] `vault-unseal.md`
- [ ] `secret-rotation.md`
- [ ] `siem-alert-triage.md`

### 관측성 — 조승연

- [ ] `slo-investigation.md`
- [ ] `chaos-drill.md`

### 데이터 — 김찬영

- [ ] `kafka-add-topic.md`
- [ ] `kafka-reset-consumer-offset.md`
- [ ] `airflow-backfill.md`

### AI — 양성호

- [ ] `gemini-quota-exhausted.md`
- [ ] `nlp-batch-reprocess.md`

### 서비스 — 한정현

- [ ] `api-probe-triage.md`
- [ ] `local-vm-session-qa.md`
- [ ] `lambda-alert-fail.md`
- [ ] `cloudfront-cache-invalidation.md`
