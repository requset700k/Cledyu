# DR 실습 스택 A3 — api EC2 백엔드 활성 + 실습 fidelity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** api 의 EC2 실습 백엔드·Kafka 발행을 DR 에서 켜고(현행 fail-closed 게이트 뒤집기), 온프렘 KubeVirt 랩과 동등하게 EC2 세션이 채점되는지 드릴로 검증한다.

**Architecture:** api `values-eks.yaml` 의 `kafka.enabled`·`aws.enabled` 를 true 로 뒤집어 EC2 세션(launch template + tailnet) + Kafka 발행(validation-requests/results·lab-events)을 활성화한다. `kubevirt.enabled=false` 는 유지(DR 백엔드는 EC2 → sessions 는 EC2 overflow 로 non-nil). AWS/Tailscale 키(cledyu-api-aws)는 eso-store `directory.include` 에 ExternalSecret 을 추가해 공급한다. 마지막에 라이브 fidelity 드릴로 EC2 랩 채점 동등성을 확인한다.

**Tech Stack:** ArgoCD helm valueFiles, EC2 launch template(`lt-03243d5c74802ddfd`)+tailnet, Strimzi KafkaUser/cert(A1), ESO/Vault(cledyu/aws/api), validation-engine(A2).

## Global Constraints

- 선행: **A1(Kafka)** + **A2(validation-engine)** 완료. A1 은 lab-events 포함 전 토픽 배포(아래 교차의존 참조).
- repoURL `https://github.com/requset700k/Cledyu.git`, targetRevision 드릴 중 `feat/dr-eks-overlay`(머지 후 main).
- **server.mode=release 유지**(env.CLEDYU_SERVER_MODE, 이미 release). validator·sessions 가 non-nil 이 되므로 fail-closed 503(Codex P1/P2)은 발동 안 함 — 실습 정상 동작. fail-closed 코드는 안전망으로 유지.
- **교차의존(A1 에서 해소됨)**: api 는 Kafka mTLS 인증서(api-kafka-client-tls, kafka.enabled 시 발급)가 있으면 validator+eventsPub 를 함께 활성화해 **lab-events 토픽에 발행**한다(main.go). 따라서 A1 은 lab-events 토픽을 **포함**해야 한다(A1 exclude 는 servicemonitor·airflow-kafkauser 만).
- EC2 세션은 별도 리전 리소스 불필요(단일 리전 ap-northeast-2). SSM(API 기반)+tailnet 은 VPC 피어링 불요.
- 정적 검증(helm 렌더·yaml)은 자동. **fidelity(EC2 랩 채점 동등성)는 라이브 드릴**(Task 4) — 클러스터+EC2 필요.

---

### Task 1: api values-eks 게이트 뒤집기 (kafka·aws on)

fail-closed 로 꺼둔 kafka·aws 를 켠다. kubevirt 는 false 유지(EC2 백엔드). maxActiveSessions 는 DR 상한으로 상향.

**Files:**
- Modify: `gitops/apps/api/values-eks.yaml`(kafka·aws 블록 + kubevirt 주석)

**Interfaces:**
- Consumes: base `values.yaml` 의 aws 블록(launchTemplateId `lt-03243d5c74802ddfd`, region ap-northeast-2, instanceType t3.medium, secretName cledyu-api-aws) — 그대로 상속, delta 만 오버라이드.
- Produces: api Deployment 가 EC2 env(CLEDYU_AWS_*) + Kafka 인증서·KafkaUser 렌더. Task 2 의 cledyu-api-aws Secret 소비.

- [ ] **Step 1: kafka·aws 블록 뒤집기 + kubevirt 주석 갱신**

`gitops/apps/api/values-eks.yaml` 의 kafka·aws·kubevirt 블록을 다음으로 교체:
```yaml
# 실습 스택 활성(풀서비스 DR) — A1 Kafka + A2 validation-engine + EC2 백엔드.
kafka:
  enabled: true    # A1 Kafka 배포됨 → api-kafka-client cert·KafkaUser 렌더, validation 발행·results 소비·lab-events 발행 활성
aws:
  enabled: true    # EC2 실습 백엔드 활성 — 온프렘 부재 시 DR 유일 세션 백엔드(launch template + tailnet)
  maxActiveSessions: 10   # 온프렘 죽으니 DR 상한 상향. 실제 값은 노드 용량에 맞춰 조정(Plan B pilot-light 사이징).
kubevirt:
  # DR 백엔드는 EC2(aws overflow)라 KubeVirt 미사용 → kubevirt.enabled=false 유지.
  # sessions 는 EC2 provisioner 로 non-nil → 세션 API 정상. validator 는 A2 validation-engine 배포로 non-nil → 실검증.
  # (fail-closed 503: validator/sessions 가 non-nil 이므로 발동 안 함. 코드는 백엔드 진짜 부재 시 안전망으로 유지.)
  enabled: false
```

- [ ] **Step 2: 렌더 검증 — EC2 env + KafkaUser + Certificate 등장, kubevirt 503 미해당**

Run:
```bash
helm template api gitops/apps/api -f gitops/apps/api/values.yaml -f gitops/apps/api/values-eks.yaml 2>&1 > /tmp/api-render.yaml
grep -c 'CLEDYU_AWS_LAUNCH_TEMPLATE_ID' /tmp/api-render.yaml      # 1 (aws on)
grep -c 'CLEDYU_KUBEVIRT_ENABLED' /tmp/api-render.yaml            # 1
grep -A1 'CLEDYU_KUBEVIRT_ENABLED' /tmp/api-render.yaml | grep -c '"false"'   # 1 (kubevirt off 유지)
grep -Ec 'kind: KafkaUser|kind: Certificate' /tmp/api-render.yaml            # >=2 (kafka on → cert+user)
grep -c 'CLEDYU_KAFKA_BROKERS' /tmp/api-render.yaml              # 1
```
Expected: 순서대로 `1`, `1`, `1`, `>=2`, `1`.

- [ ] **Step 3: Commit**

```bash
git add gitops/apps/api/values-eks.yaml
git commit -m "feat(dr): api values-eks 실습 활성(kafka·aws on, kubevirt off 유지) — 풀서비스 DR"
```

---

### Task 2: cledyu-api-aws ExternalSecret 를 eso-store 에 편입

EC2 수명주기 키 + Tailscale authkey(Vault `cledyu/aws/api` → Secret `cledyu-api-aws`, ns api)를 DR 에 공급.

**Files:**
- Modify: `gitops/argocd/apps-eks/data-eso-store.yaml`(directory.include 목록)

**Interfaces:**
- Consumes: `infra/kubernetes/external-secrets/cledyu-api-aws-externalsecret.yaml`(기존, ns api, vault-backend store), A2 에서 이미 추가된 validation-engine-aws.
- Produces: Secret `cledyu-api-aws`(ns api) — Task 1 api Deployment 가 AWS_ACCESS_KEY_ID/SECRET + CLEDYU_AWS_TAILSCALE_AUTH_KEY 로 소비.

- [ ] **Step 1: include 목록에 추가(A2 결과 위에)**

`gitops/argocd/apps-eks/data-eso-store.yaml` 의 `directory.include` 를 다음으로 변경(A2 에서 validation-engine-aws 추가된 상태 가정):
```yaml
      include: "{clustersecretstore.yaml,cledyu-web-oidc-externalsecret.yaml,cledyu-api-db-externalsecret.yaml,cledyu-validation-engine-aws-externalsecret.yaml,cledyu-api-aws-externalsecret.yaml}"
```

- [ ] **Step 2: 검증 (5개 항목 + api-aws 포함, ns/vault path)**

Run:
```bash
python3 -c "import yaml; d=yaml.safe_load(open('gitops/argocd/apps-eks/data-eso-store.yaml')); inc=d['spec']['source']['directory']['include']; assert 'cledyu-api-aws-externalsecret.yaml' in inc, inc; print('OK', inc.count(',')+1, 'items')"
python3 -c "import yaml; e=yaml.safe_load(open('infra/kubernetes/external-secrets/cledyu-api-aws-externalsecret.yaml')); assert e['metadata']['namespace']=='api'; keys=[d['secretKey'] for d in e['spec']['data']]; assert 'tailscale_authkey' in keys, keys; assert e['spec']['data'][0]['remoteRef']['key']=='aws/api'; print('OK', keys)"
```
Expected: `OK 5 items`, `OK ['access_key_id','secret_access_key','tailscale_authkey']`.

- [ ] **Step 3: Commit**

```bash
git add gitops/argocd/apps-eks/data-eso-store.yaml
git commit -m "feat(dr): eso-store 에 cledyu-api-aws ExternalSecret 편입(EC2 키+tailscale)"
```

---

### Task 3: 편입·렌더 스모크 검증

**Files:**
- Verify(수정 없음): `gitops/argocd/apps-eks/`, `gitops/apps/api/`

- [ ] **Step 1: apps-eks 전체 유효 + api Kafka 리소스가 kafka ns 대상인지**

Run:
```bash
for f in gitops/argocd/apps-eks/*.yaml; do python3 -c "import yaml,sys; list(yaml.safe_load_all(open('$f')))" || { echo "INVALID $f"; exit 1; }; done; echo "ALL VALID"
# api KafkaUser 가 kafka ns / cledyu-kafka 클러스터 대상인지(A1 브로커에 붙는지)
helm template api gitops/apps/api -f gitops/apps/api/values.yaml -f gitops/apps/api/values-eks.yaml 2>&1 | \
  python3 -c "import yaml,sys; [print('KafkaUser', d['metadata']['namespace'], d['metadata']['labels'].get('strimzi.io/cluster')) for d in yaml.safe_load_all(sys.stdin) if d and d.get('kind')=='KafkaUser']"
```
Expected: `ALL VALID`, 그리고 `KafkaUser kafka cledyu-kafka`.

- [ ] **Step 2: ServiceMonitor·Cilium 등 CRD-missing 미렌더 재확인(api)**

Run:
```bash
helm template api gitops/apps/api -f gitops/apps/api/values.yaml -f gitops/apps/api/values-eks.yaml 2>&1 | grep -Ec 'kind: ServiceMonitor|cilium.io'
```
Expected: `0`(metrics.serviceMonitor.enabled=false 유지, Cilium 없음).

- [ ] **Step 3: Commit(문서 변경 없으면 skip)**

변경 없으면 커밋 없음. 있으면:
```bash
git add -A && git commit -m "chore(dr): A3 렌더 검증"
```

---

### Task 4: 실습 fidelity 라이브 드릴 (런북 절차 + 수용기준)

정적으로 증명 불가한 "온프렘 KubeVirt 랩 == DR EC2 랩 채점 동등성"을 드릴 절차로 문서화한다. 실제 실행은 pilot-light/드릴에서.

**Files:**
- Modify: `docs/RUNBOOK/dr-eks-bootstrap.md`(fidelity 드릴 섹션 추가)

- [ ] **Step 1: 런북에 fidelity 드릴 절차 추가**

`docs/RUNBOOK/dr-eks-bootstrap.md` 에 "실습 fidelity 검증(EC2 채점 동등성)" 섹션 추가:
```markdown
### 실습 fidelity 검증 (EC2 채점 == 온프렘 KubeVirt)

풀서비스 DR 의 실습이 온프렘과 동등한지 라이브로 확인한다. 대표 랩 6종(lab-linux-basics 등)에 대해:

```bash
# 1) 로컬 테스트유저로 세션 생성 → api 가 EC2 인스턴스를 띄우는지
#    (kubevirt=false, aws=true 이므로 sessions=EC2 provisioner)
kubectl -n api logs deploy/api | grep -E "EC2|launch|instance"        # EC2 세션 생성 로그
aws ec2 describe-instances --region ap-northeast-2 \
  --filters "Name=tag:cledyu-session,Values=*" "Name=instance-state-name,Values=running" \
  --query "Reservations[].Instances[].InstanceId" --output text        # 세션 인스턴스 존재

# 2) 사용자 터미널 도달(tailnet) — api 가 tailscale 로 인스턴스에 다이얼하는지
#    (CLEDYU_AWS_TAILSCALE_AUTH_KEY = cledyu-api-aws) : 세션 터미널 WebSocket 200

# 3) 검증엔진 채점 — 각 스텝을 통과 상태로 만들고 /validate 호출 → validation-engine 이
#    SSM SendCommand 로 EC2 를 채점 → validation-results → api 가 Postgres 반영
kubectl -n validation-engine logs deploy/validation-engine | grep -E "SSM|SendCommand|passed|failed"
# 수용기준: 온프렘에서 통과하는 정답 입력이 DR(EC2)에서도 passed, 오답은 failed.
#          6종 랩 각 최소 1스텝 정답/오답 대조가 온프렘과 일치.

# 4) mock-pass 미발생 확인(보안) — validator non-nil 이므로 "mock" 응답이 없어야 함
kubectl -n api logs deploy/api | grep -c "mock"                        # 0
```

수용기준 요약: (a) 세션=EC2 인스턴스 생성, (b) 터미널 tailnet 도달, (c) SSM 채점 결과가 온프렘과 동일 판정(정답 passed/오답 failed), (d) mock-pass 0건.
```

- [ ] **Step 2: Commit**

```bash
git add docs/RUNBOOK/dr-eks-bootstrap.md
git commit -m "docs(dr): 런북에 실습 fidelity 드릴(EC2 채점 동등성) 절차·수용기준 추가"
```

---

## Self-Review (작성자 체크)

**Spec 커버리지(§3.2·§3.3 api·§6.1 fidelity):** api 게이트 뒤집기(Task 1)·cledyu-api-aws ExternalSecret(Task 2)·fidelity 드릴(Task 4) 태스크 존재. kubevirt=false 유지 + fail-closed 정합(§5) 주석 반영.

**플레이스홀더 스캔:** 전 Step 실제 명령·매니페스트·수용기준. TBD/TODO 없음.

**타입/이름 일관성:** Secret `cledyu-api-aws`(Task2→Task1 소비), include 목록(A2 4개 → A3 5개), launchTemplateId `lt-03243d5c74802ddfd`(base values 상속), server.mode=release 전제 일관.

**교차의존 반영:** api 가 lab-events 에 발행 → A1 은 lab-events 토픽 포함해야 함(Global Constraints + A1 플랜 수정으로 반영). validator/sessions non-nil → fail-closed 미발동, 실습 정상.

**미해결(플랜 밖·후속):**
- maxActiveSessions=10 은 잠정 — Plan B pilot-light 노드 사이징서 확정.
- EC2 세션 인스턴스 태그·tailnet ACL(tag:lab-ec2)·SSM instance profile 은 launch template/terraform 측 전제 — 드릴서 도달성 실측(Task 4).
- fidelity 라이브 결과(6종 랩 대조)는 pilot-light 드릴에서 수집.
