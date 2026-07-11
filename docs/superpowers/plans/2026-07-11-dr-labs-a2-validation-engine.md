# DR 실습 스택 A2 — validation-engine EKS 오버레이 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** validation-engine 을 EKS DR apps-eks 오버레이에 추가해, A1 의 Kafka 위에서 EC2 세션을 SSM 으로 채점하게 한다. EKS 미지원 CiliumNetworkPolicy 는 게이트로 제거한다.

**Architecture:** validation-engine 은 로컬 helm 차트다(service-api 와 동일 패턴). CiliumNetworkPolicy 를 `.Values.networkPolicy.cilium.enabled` 로 게이트(기본 true=온프렘, EKS 는 values-eks 에서 false)해 CRD-missing sync 블로커를 없앤다. apps-eks Application 은 `helm.valueFiles: [values.yaml, values-eks.yaml]` 단일 소스로 붙이고, AWS SSM 키(cledyu-validation-engine-aws)는 기존 eso-store 앱의 `directory.include` 에 ExternalSecret 을 추가해 공급한다.

**Tech Stack:** ArgoCD(로컬 helm 차트 valueFiles, directory.include), cert-manager(kafka-clients-ca ClusterIssuer=A1), Strimzi KafkaUser(A1), ESO/Vault(cledyu/aws/validation-engine), EKS VPC CNI(NetworkPolicy 기본 미강제).

## Global Constraints

- 선행: **A1(Kafka)** 완료 — Strimzi CRD, `cledyu-kafka` 브로커, `kafka-clients-ca` ClusterIssuer, validation topics 존재 전제.
- repoURL `https://github.com/requset700k/Cledyu.git`, targetRevision 드릴 중 `feat/dr-eks-overlay`(머지 후 main).
- EKS 는 Cilium 없음(VPC CNI) → CiliumNetworkPolicy 렌더 금지. plain NetworkPolicy 는 VPC CNI 기본 미강제라 no-op(egress 무제한 → SSM 도달 가능) — 남겨도 무해.
- validation-engine 은 release 무관 항상 실검증(mock 없음). AWS secretKeyRef(cledyu-validation-engine-aws) 비-optional → **Vault 복원→ESO sync 후** 파드 기동(api 와 동일 DR 흐름).
- rbac.yaml 의 KubeVirt(`kubevirt.io`/`subresources.kubevirt.io`) ClusterRole 규칙은 CRD 부재여도 유효(RBAC 는 리소스 존재 불요) → 수정 불요, 블로커 아님. DR 은 EC2/SSM 경로만 사용.
- 정적 검증(helm 렌더·yaml·구조)만 이 플랜 범위. 라이브 기동은 드릴.

---

### Task 1: CiliumNetworkPolicy 게이트

`.Values.networkPolicy.cilium.enabled` 플래그로 CiliumNetworkPolicy 렌더를 감싼다(기본 true=온프렘). EKS 는 Task 2 values-eks 에서 false.

**Files:**
- Modify: `gitops/apps/validation-engine/templates/ciliumnetworkpolicy.yaml`(전체 문서를 `{{- if }}` 로 감쌈)
- Modify: `gitops/apps/validation-engine/values.yaml`(networkPolicy 블록 추가)

**Interfaces:**
- Produces: `.Values.networkPolicy.cilium.enabled`(bool, 기본 true). Task 2 values-eks 가 false 로 오버라이드.

- [ ] **Step 1: 템플릿을 게이트로 감싸기**

`gitops/apps/validation-engine/templates/ciliumnetworkpolicy.yaml` 최상단·최하단에 조건 추가(내용은 그대로):
```yaml
{{- if .Values.networkPolicy.cilium.enabled }}
---
apiVersion: cilium.io/v2
kind: CiliumNetworkPolicy
metadata:
  name: "{{ .Release.Name }}-kube-apiserver"
  namespace: {{ .Release.Namespace }}
spec:
  endpointSelector:
    matchLabels:
      app: {{ .Release.Name }}
  egress:
    - toEntities:
        - kube-apiserver
      toPorts:
        - ports:
            - port: "443"
              protocol: TCP
            - port: "6443"
              protocol: TCP
    {{- if .Values.aws.enabled }}
    - toEndpoints:
        - matchLabels:
            k8s:io.kubernetes.pod.namespace: kube-system
            k8s:k8s-app: kube-dns
      toPorts:
        - ports:
            - port: "53"
              protocol: UDP
            - port: "53"
              protocol: TCP
          rules:
            dns:
              - matchPattern: "*"
    - toFQDNs:
        - matchName: "ssm.ap-northeast-2.amazonaws.com"
      toPorts:
        - ports:
            - port: "443"
              protocol: TCP
    {{- end }}
{{- end }}
```
(주: 기존 주석은 보존해도 되나, 위는 최소 형태. 실제 편집 시 원본 주석 유지하고 `{{- if .Values.networkPolicy.cilium.enabled }}` 를 `---` 앞에, `{{- end }}` 를 파일 끝에 추가만 한다.)

- [ ] **Step 2: values.yaml 에 게이트 플래그 추가**

`gitops/apps/validation-engine/values.yaml` 끝에 추가:
```yaml
# 네트워크 정책 — 온프렘은 Cilium(CiliumNetworkPolicy 로 apiserver·SSM egress 통제).
# EKS DR 은 VPC CNI 라 Cilium CRD 가 없어 values-eks 에서 false → CiliumNetworkPolicy 미렌더(sync 블로커 제거).
# (plain NetworkPolicy 는 VPC CNI 기본 미강제라 no-op — egress 무제한, SSM 도달 가능.)
networkPolicy:
  cilium:
    enabled: true
```

- [ ] **Step 3: 렌더 검증 (기본 true → 1개, false → 0개)**

Run:
```bash
helm template ve gitops/apps/validation-engine -f gitops/apps/validation-engine/values.yaml 2>&1 | grep -c 'kind: CiliumNetworkPolicy'
helm template ve gitops/apps/validation-engine -f gitops/apps/validation-engine/values.yaml --set networkPolicy.cilium.enabled=false 2>&1 | grep -c 'kind: CiliumNetworkPolicy'
```
Expected: 첫째 `1`, 둘째 `0`.

- [ ] **Step 4: Commit**

```bash
git add gitops/apps/validation-engine/templates/ciliumnetworkpolicy.yaml gitops/apps/validation-engine/values.yaml
git commit -m "feat(validation-engine): CiliumNetworkPolicy 를 networkPolicy.cilium.enabled 로 게이트(EKS DR 대응)"
```

---

### Task 2: validation-engine values-eks.yaml

EKS DR 델타만 담는 오버레이(Cilium off). aws.enabled·KAFKA_BROKERS 는 기본값(true·kafka.svc) 그대로 유효하므로 재선언 불요.

**Files:**
- Create: `gitops/apps/validation-engine/values-eks.yaml`

**Interfaces:**
- Consumes: `.Values.networkPolicy.cilium.enabled`(Task 1).
- Produces: (helm valueFiles 로 Task 3 앱이 로드).

- [ ] **Step 1: values-eks 작성**

`gitops/apps/validation-engine/values-eks.yaml`:
```yaml
---
# EKS DR 오버레이 — deltas only, values.yaml 위에 레이어.
# Cilium 미사용(VPC CNI) → CiliumNetworkPolicy 미렌더(cilium.io/v2 CRD 없어 sync 블로커).
networkPolicy:
  cilium:
    enabled: false
# 참고(재선언 불요, 확인용):
#   - aws.enabled: true (values.yaml 기본) → EC2 세션 SSM 채점 활성, cledyu-validation-engine-aws 주입(Task 4 ESO).
#   - env.KAFKA_BROKERS: cledyu-kafka-kafka-bootstrap.kafka.svc:9093 (A1 Kafka) — 그대로 유효.
#   - env.OTEL_ENDPOINT: alloy.loki(...) — DR 에 관측 미배포지만 otlptracegrpc 지연연결이라 non-fatal(노이즈만).
#     노이즈 제거는 비-DR 후속(코드가 빈 endpoint 시 tracing skip 하도록).
```

- [ ] **Step 2: 렌더 검증 (values.yaml + values-eks → Cilium 0개)**

Run:
```bash
helm template ve gitops/apps/validation-engine -f gitops/apps/validation-engine/values.yaml -f gitops/apps/validation-engine/values-eks.yaml 2>&1 | grep -c 'kind: CiliumNetworkPolicy'
helm template ve gitops/apps/validation-engine -f gitops/apps/validation-engine/values.yaml -f gitops/apps/validation-engine/values-eks.yaml 2>&1 | grep -E 'kind: (Deployment|KafkaUser|Certificate|NetworkPolicy)' | sort -u
```
Expected: 첫째 `0`(Cilium 제거). 둘째: Deployment·KafkaUser·Certificate·NetworkPolicy 는 여전히 렌더(핵심 리소스 유지).

- [ ] **Step 3: Commit**

```bash
git add gitops/apps/validation-engine/values-eks.yaml
git commit -m "feat(dr): validation-engine values-eks (Cilium off)"
```

---

### Task 3: EKS validation-engine ArgoCD Application

service-api 패턴(단일 소스 로컬 차트 + valueFiles)으로 apps-eks 에 붙인다. wave 는 Kafka(A1, wave 1) 이후.

**Files:**
- Create: `gitops/argocd/apps-eks/service-validation-engine.yaml`

**Interfaces:**
- Consumes: A1 Kafka(브로커·KafkaUser·kafka-clients-ca issuer), Task 1·2(게이트+values-eks), Task 4(cledyu-validation-engine-aws secret).
- Produces: Application `eks-service-validation-engine`(ns validation-engine). Deployment 는 cledyu-validation-engine-aws(ESO) + validation-engine-kafka-client-tls(cert-manager) 준비 후 기동.

- [ ] **Step 1: apps-eks Application 작성**

`gitops/argocd/apps-eks/service-validation-engine.yaml`:
```yaml
---
# EKS DR — validation-engine. service-api 패턴(로컬 helm 차트 + values-eks).
# wave 2 — A1 Kafka(strimzi wave 0, kafka wave 1) 뒤. KafkaUser·client cert(kafka-clients-ca)·브로커 선재.
# AWS SSM 키(cledyu-validation-engine-aws)는 Vault 복원→ESO(Task 4) 후 주입되어 파드 기동.
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: eks-service-validation-engine
  namespace: argocd
  annotations:
    argocd.argoproj.io/sync-wave: "2"
  finalizers:
    - resources-finalizer.argocd.argoproj.io
spec:
  project: default
  source:
    repoURL: https://github.com/requset700k/Cledyu.git
    targetRevision: feat/dr-eks-overlay
    path: gitops/apps/validation-engine
    helm:
      releaseName: validation-engine
      valueFiles:
        - values.yaml
        - values-eks.yaml
  destination:
    server: https://kubernetes.default.svc
    namespace: validation-engine
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
      - ServerSideApply=true
    retry:
      limit: 10
      backoff:
        duration: 30s
        factor: 2
        maxDuration: 5m
  ignoreDifferences:
    - group: kafka.strimzi.io
      kind: KafkaUser
      jsonPointers:
        - /status
```

- [ ] **Step 2: YAML·구조 검증 + CRD-missing 미포함 확인**

Run:
```bash
python3 -c "import yaml; d=list(yaml.safe_load_all(open('gitops/argocd/apps-eks/service-validation-engine.yaml')))[0]; assert d['metadata']['name']=='eks-service-validation-engine'; assert d['metadata']['annotations']['argocd.argoproj.io/sync-wave']=='2'; vf=d['spec']['source']['helm']['valueFiles']; assert vf==['values.yaml','values-eks.yaml'], vf; print('OK')"
# 이 앱이 렌더할 매니페스트에 CiliumNetworkPolicy 가 없어야(게이트 확인)
helm template validation-engine gitops/apps/validation-engine -f gitops/apps/validation-engine/values.yaml -f gitops/apps/validation-engine/values-eks.yaml 2>&1 | grep -c 'cilium.io'
```
Expected: `OK`, 그리고 마지막 `0`(cilium 참조 없음).

- [ ] **Step 3: Commit**

```bash
git add gitops/argocd/apps-eks/service-validation-engine.yaml
git commit -m "feat(dr): apps-eks validation-engine (wave 2, Kafka 뒤)"
```

---

### Task 4: cledyu-validation-engine-aws ExternalSecret 를 eso-store 에 편입

SSM 채점 키(Vault `cledyu/aws/validation-engine` → Secret `cledyu-validation-engine-aws`, ns validation-engine)를 DR 에 공급. 기존 eso-store 앱의 `directory.include` 에 매니페스트 추가.

**Files:**
- Modify: `gitops/argocd/apps-eks/data-eso-store.yaml`(directory.include 목록)

**Interfaces:**
- Consumes: `infra/kubernetes/external-secrets/cledyu-validation-engine-aws-externalsecret.yaml`(기존, ns validation-engine, vault-backend store), vault-backend ClusterSecretStore(eso-store 기존).
- Produces: Secret `cledyu-validation-engine-aws`(ns validation-engine) — Task 3 Deployment 가 소비.

- [ ] **Step 1: include 목록에 추가**

`gitops/argocd/apps-eks/data-eso-store.yaml` 의 `directory.include` 를 다음으로 변경:
```yaml
      include: "{clustersecretstore.yaml,cledyu-web-oidc-externalsecret.yaml,cledyu-api-db-externalsecret.yaml,cledyu-validation-engine-aws-externalsecret.yaml}"
```

- [ ] **Step 2: 검증 (include 에 4개 항목 + validation-engine 포함)**

Run:
```bash
python3 -c "import yaml; d=yaml.safe_load(open('gitops/argocd/apps-eks/data-eso-store.yaml')); inc=d['spec']['source']['directory']['include']; assert 'cledyu-validation-engine-aws-externalsecret.yaml' in inc, inc; print('OK', inc.count(',')+1, 'items')"
# 대상 ExternalSecret 이 실제 존재·유효 + ns/vault path 확인
python3 -c "import yaml; e=yaml.safe_load(open('infra/kubernetes/external-secrets/cledyu-validation-engine-aws-externalsecret.yaml')); assert e['metadata']['namespace']=='validation-engine'; assert e['spec']['data'][0]['remoteRef']['key']=='aws/validation-engine'; print('OK ns/vaultpath')"
```
Expected: `OK 4 items`, `OK ns/vaultpath`.

- [ ] **Step 3: Commit**

```bash
git add gitops/argocd/apps-eks/data-eso-store.yaml
git commit -m "feat(dr): eso-store 에 cledyu-validation-engine-aws ExternalSecret 편입"
```

---

### Task 5: 편입 검증 + 런북 체크

**Files:**
- Verify(수정 없음): `gitops/argocd/apps-eks/`
- Modify: `docs/RUNBOOK/dr-eks-bootstrap.md`(부트스트랩 체크리스트에 validation-engine 항목)

**Interfaces:**
- Consumes: Task 3 앱.
- Produces: (검증 태스크).

- [ ] **Step 1: apps-eks 전체 유효 + wave 순서(Kafka < validation-engine)**

Run:
```bash
for f in gitops/argocd/apps-eks/*.yaml; do python3 -c "import yaml,sys; list(yaml.safe_load_all(open('$f')))" || { echo "INVALID $f"; exit 1; }; done; echo "ALL VALID"
python3 -c "
import yaml,glob
for f in sorted(glob.glob('gitops/argocd/apps-eks/*.yaml')):
    d=yaml.safe_load(open(f)); a=d.get('metadata',{}).get('annotations',{})
    n=d['metadata']['name']
    if 'strimzi' in n or 'kafka' in n or 'validation' in n:
        print(a.get('argocd.argoproj.io/sync-wave','(none)'), n)
"
```
Expected: `ALL VALID`. wave 출력에서 strimzi=0, kafka=1, validation-engine=2 순.

- [ ] **Step 2: 런북에 validation-engine 체크 추가**

`docs/RUNBOOK/dr-eks-bootstrap.md` 체크리스트에 Kafka 항목 다음, api 앞에 추가:
```markdown
- [ ] validation-engine Ready — `kubectl -n validation-engine get deploy validation-engine` Available.
      cledyu-validation-engine-aws Secret(ESO, Vault 복원 후) + validation-engine-kafka-client-tls(cert-manager,
      kafka-clients-ca) 준비돼야 기동. KafkaUser `validation-engine`(ns kafka) Ready. (CiliumNetworkPolicy 없음은
      정상 — EKS 게이트. lab-ssh-key 없음도 정상 — DR 은 EC2/SSM 채점.)
```

- [ ] **Step 3: Commit**

```bash
git add docs/RUNBOOK/dr-eks-bootstrap.md
git commit -m "docs(dr): 런북 체크리스트에 validation-engine Ready 검증 추가"
```

---

## Self-Review (작성자 체크)

**Spec 커버리지(§3.1 validation-engine·§3.3·§3.4):** validation-engine 앱(Task 3)·CiliumNetworkPolicy 게이트(Task 1·2)·cledyu-validation-engine-aws ExternalSecret(Task 4)·KafkaUser/cert 의존(A1 선행 명시) 모두 태스크 존재.

**플레이스홀더 스캔:** 전 Step 실제 명령·매니페스트. TBD/TODO 없음.

**타입/이름 일관성:** `.Values.networkPolicy.cilium.enabled`(Task1 정의 → Task2 오버라이드), Application `eks-service-validation-engine`(Task3), Secret `cledyu-validation-engine-aws`(Task4→Task3 소비), valueFiles `[values.yaml, values-eks.yaml]`(Task3, service-api 패턴 일치).

**미해결(플랜 밖·후속):**
- OTEL_ENDPOINT(alloy.loki) DR 미배포 — non-fatal(지연연결), 노이즈 제거는 코드가 빈 endpoint 시 tracing skip 하도록(비-DR 후속).
- validation-engine 이미지 태그(sha-f01d232)가 EC2Executor 포함 최신인지 — 드릴 전 확인(현행 코드에 ec2.go 존재).
- 실습 fidelity(온프렘 KubeVirt 랩 vs DR EC2 채점 동등성)는 A3 드릴 검증.
- 라이브 기동(validation-engine Ready·Kafka 연결·SSM 채점)은 드릴 이월.
