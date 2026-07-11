# DR 실습 스택 A1 — Kafka(Strimzi) EKS 오버레이 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 온프렘 Kafka(Strimzi) 스택을 EKS DR apps-eks 오버레이에 추가해, 재해 시 검증 파이프라인의 전송 계층(validation-requests/results/dlq)이 뜨게 한다.

**Architecture:** raw-매니페스트 디렉토리 앱(`kafka-cluster`)은 helm이 아니라 values-eks로 못 덮으므로, ① 공유 `kafka-nodepool.yaml`을 storage-class-agnostic(명시 `class` 제거 → 환경 기본 SC)으로 만들고 ② EKS용 ArgoCD Application 2개(strimzi-operator·kafka-cluster)를 `apps-eks/`에 추가하되 kafka-cluster 앱은 `directory.exclude`로 온프렘 전용 파일(ServiceMonitor·airflow KafkaUser·분석 토픽)을 뺀다. root-app-eks가 `apps-eks/`를 recurse하므로 파일 추가만으로 편입된다.

**Tech Stack:** ArgoCD(app-of-apps, multi-source, directory include/exclude), Strimzi Kafka Operator 0.43.0(helm), cert-manager 기반 Kafka CA(ca-secret-sync), gp3/longhorn 기본 SC.

## Global Constraints

- repoURL: `https://github.com/requset700k/Cledyu.git` (apps-eks 컨벤션 대문자 Cledyu — GitHub은 대소문자 무관하나 일관성 유지).
- targetRevision(git values/manifests): 드릴 중에는 `feat/dr-eks-overlay`, **머지 후 `main`으로 revert**(드릴 flip 커밋 2934d31과 동일 정책). 이 플랜의 새 파일도 이 규칙 따름.
- Strimzi 차트 버전 고정 `0.43.0`(온프렘과 동일 — CRD 서빙 버전 매칭).
- EKS는 VPC CNI(Cilium 아님) + prometheus-operator 없음 → **ServiceMonitor/Cilium CRD 렌더 금지**(sync 블로커).
- 네임스페이스 PSA: `strimzi-system`=baseline enforce, `kafka`=restricted enforce(매니페스트에 이미 securityContext 명시됨).
- ArgoCD syncOptions: `CreateNamespace=true`, `ServerSideApply=true`. CRD 전파 지연 흡수 위해 kafka-cluster는 retry limit 10.
- 정적 검증만 이 플랜 범위(yaml 유효성·앱 구조·exclude 정합). **실제 Kafka Ready·토픽 생성은 라이브 드릴**(A3 fidelity 또는 pilot-light 드릴)에서 확인 — 이 플랜에선 클러스터 미기동.

---

### Task 1: kafka-nodepool 을 storage-class-agnostic 으로

공유 매니페스트에서 명시 `class: longhorn` 을 제거해 각 환경 기본 SC(온프렘=longhorn, EKS=gp3)를 쓰게 한다. **온프렘 안전장치**: 온프렘 default SC 가 longhorn 이어야 한다(제거해도 동작 동일).

**Files:**
- Modify: `gitops/apps/kafka-cluster/kafka-nodepool.yaml`(spec.storage 블록)

**Interfaces:**
- Produces: `KafkaNodePool/kafka`(namespace kafka) — `spec.storage.class` 미지정. Task 3 의 kafka-cluster 앱이 이 파일을 그대로 include.

- [ ] **Step 1: 온프렘 default SC 가 longhorn 인지 확인(안전장치)**

온프렘 kubeconfig 로:
```bash
kubectl get storageclass -o custom-columns='NAME:.metadata.name,DEFAULT:.metadata.annotations.storageclass\.kubernetes\.io/is-default-class'
```
Expected: `longhorn` 행의 DEFAULT 가 `true`. 아니면 이 태스크 중단하고 사용자에게 보고(대안: DR 전용 nodepool 파일 분리). EKS 기본 SC 는 `gitops/apps/storage/storageclass-gp3.yaml`(is-default-class=true, ebs.csi.aws.com)로 이미 gp3.

- [ ] **Step 2: `class: longhorn` 제거**

`gitops/apps/kafka-cluster/kafka-nodepool.yaml` 의 storage 블록을 다음으로 변경(그 외 필드 유지):
```yaml
  storage:
    type: persistent-claim
    size: 30Gi
    # class 미지정 → 각 클러스터 기본 StorageClass 사용(온프렘=longhorn, EKS DR=gp3).
    # 환경별 SC 차이를 raw-디렉토리 앱에서 흡수(values 오버레이 불가하므로 default SC 에 위임).
    deleteClaim: false
```

- [ ] **Step 3: YAML 유효성 검증**

Run:
```bash
python3 -c "import yaml; list(yaml.safe_load_all(open('gitops/apps/kafka-cluster/kafka-nodepool.yaml')))" && echo OK
grep -q 'class: longhorn' gitops/apps/kafka-cluster/kafka-nodepool.yaml && echo "FAIL: class 잔존" || echo "PASS: class 제거됨"
```
Expected: `OK` + `PASS: class 제거됨`.

- [ ] **Step 4: Commit**

```bash
git add gitops/apps/kafka-cluster/kafka-nodepool.yaml
git commit -m "refactor(kafka): nodepool storageClass 를 환경 기본 SC 로 위임(EKS DR gp3 대응)"
```

---

### Task 2: EKS strimzi-operator ArgoCD Application

온프렘 `data-strimzi-operator.yaml` 패턴을 apps-eks 로 이식(Strimzi CRD·오퍼레이터 설치, wave 0).

**Files:**
- Create: `gitops/argocd/apps-eks/data-strimzi-operator.yaml`

**Interfaces:**
- Produces: Application `eks-data-strimzi-operator`(ns argocd) → Strimzi 오퍼레이터를 `strimzi-system` 에 설치, `kafka` ns watch. Task 3(kafka-cluster)이 이 오퍼레이터가 설치한 Kafka/KafkaNodePool/KafkaTopic/KafkaUser CRD 에 의존(wave 1).

- [ ] **Step 1: apps-eks Application 작성**

`gitops/argocd/apps-eks/data-strimzi-operator.yaml`:
```yaml
---
# EKS DR — Strimzi Kafka Operator. onprem data-strimzi-operator.yaml 과 동일 패턴(multi-source).
# wave 0 — kafka-cluster(Task 3, wave 1)보다 먼저 CRD·오퍼레이터가 서야 한다.
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: eks-data-strimzi-operator
  namespace: argocd
  annotations:
    argocd.argoproj.io/sync-wave: "0"
  finalizers:
    - resources-finalizer.argocd.argoproj.io
spec:
  project: default
  sources:
    - repoURL: https://strimzi.io/charts/
      chart: strimzi-kafka-operator
      targetRevision: 0.43.0
      helm:
        releaseName: strimzi
        valueFiles:
          - $values/gitops/apps/strimzi-operator/values.yaml
    - repoURL: https://github.com/requset700k/Cledyu.git
      targetRevision: feat/dr-eks-overlay
      ref: values
    - repoURL: https://github.com/requset700k/Cledyu.git
      targetRevision: feat/dr-eks-overlay
      path: gitops/apps/strimzi-operator
      directory:
        include: namespace.yaml
  destination:
    server: https://kubernetes.default.svc
    namespace: strimzi-system
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
      - ServerSideApply=true
    retry:
      limit: 5
      backoff:
        duration: 10s
        factor: 2
        maxDuration: 3m
  # Strimzi CRD 는 apply 시 openAPIV3Schema/required 빈배열·status diff 가 영구 발생 → 비교 제외.
  ignoreDifferences:
    - group: apiextensions.k8s.io
      kind: CustomResourceDefinition
      jsonPointers:
        - /spec/preserveUnknownFields
        - /status
      jqPathExpressions:
        - .spec.versions[].schema.openAPIV3Schema
```

- [ ] **Step 2: YAML 유효성 + 구조 검증**

Run:
```bash
python3 -c "import yaml; d=list(yaml.safe_load_all(open('gitops/argocd/apps-eks/data-strimzi-operator.yaml')))[0]; assert d['metadata']['name']=='eks-data-strimzi-operator'; assert d['metadata']['annotations']['argocd.argoproj.io/sync-wave']=='0'; assert d['spec']['sources'][0]['targetRevision']=='0.43.0'; print('OK')"
grep -c 'targetRevision: feat/dr-eks-overlay' gitops/argocd/apps-eks/data-strimzi-operator.yaml   # 2 (git values + namespace)
```
Expected: `OK`, 그리고 `2`.

- [ ] **Step 3: Strimzi 차트가 values 로 렌더되는지(로컬 helm)**

Run:
```bash
helm repo add strimzi https://strimzi.io/charts/ >/dev/null 2>&1; helm repo update >/dev/null 2>&1
helm template strimzi strimzi/strimzi-kafka-operator --version 0.43.0 \
  -f gitops/apps/strimzi-operator/values.yaml -n strimzi-system 2>&1 | grep -c 'kind: CustomResourceDefinition'
```
Expected: CRD 수 > 0(Kafka/KafkaNodePool/KafkaTopic/KafkaUser 등 렌더됨). 에러 없이 렌더되면 통과.

- [ ] **Step 4: Commit**

```bash
git add gitops/argocd/apps-eks/data-strimzi-operator.yaml
git commit -m "feat(dr): apps-eks strimzi-operator (Kafka CRD·오퍼레이터, wave 0)"
```

---

### Task 3: EKS kafka-cluster ArgoCD Application (directory.exclude)

온프렘 `data-kafka-cluster.yaml` 을 이식하되 EKS 미지원/불필요 파일을 `directory.exclude` 로 제거(ServiceMonitor·airflow KafkaUser·분석 토픽). SC-agnostic 노드풀(Task 1)로 gp3 자동 사용.

**Files:**
- Create: `gitops/argocd/apps-eks/data-kafka-cluster.yaml`

**Interfaces:**
- Consumes: `eks-data-strimzi-operator`(Task 2)가 설치한 Strimzi CRD; Task 1 의 SC-agnostic nodepool; cert-manager `cledyu-ca`(platform-pki) + trust-manager Bundle(kafka ns 라벨 `cledyu.io/trust-bundle: root-ca`).
- Produces: `Kafka/cledyu-kafka`(ns kafka, bootstrap `cledyu-kafka-kafka-bootstrap.kafka.svc:9093` mTLS) + 토픽 validation-requests/results/dlq + KafkaUser(entity operator). A2(validation-engine)·A3(api)가 이 브로커·토픽에 의존.

- [ ] **Step 1: apps-eks Application 작성(exclude 포함)**

`gitops/argocd/apps-eks/data-kafka-cluster.yaml`:
```yaml
---
# EKS DR — Kafka 클러스터(Strimzi). onprem data-kafka-cluster.yaml 이식.
# EKS 미지원/불필요 파일만 directory.exclude 로 제거:
#   - *servicemonitor* : prometheus-operator CRD 없음(sync 블로커)
#   - kafkauser-airflow-analytics : airflow DR 미배포
# ⚠️ 토픽은 전부 포함(lab-events·security-logs 도) — api 가 kafka.enabled 시 lab-events 에 발행하므로(A3 교차의존)
#   토픽을 빼면 발행 에러. 분석 소비자 없어도 KafkaTopic CR 은 무해(메시지 미소비 시 보존기간 후 만료).
# nodepool 은 SC-agnostic(class 제거)이라 EKS 기본 SC(gp3) 자동 사용.
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: eks-data-kafka-cluster
  namespace: argocd
  annotations:
    # strimzi-operator(wave 0)가 CRD 설치 후 sync.
    argocd.argoproj.io/sync-wave: "1"
  finalizers:
    - resources-finalizer.argocd.argoproj.io
spec:
  project: default
  source:
    repoURL: https://github.com/requset700k/Cledyu.git
    targetRevision: feat/dr-eks-overlay
    path: gitops/apps/kafka-cluster
    directory:
      recurse: true
      exclude: "{kafka-servicemonitor.yaml,kafka-exporter-servicemonitor.yaml,kafkauser-airflow-analytics.yaml}"
  destination:
    server: https://kubernetes.default.svc
    namespace: kafka
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
      kind: Kafka
      jsonPointers:
        - /status
    - group: kafka.strimzi.io
      kind: KafkaTopic
      jsonPointers:
        - /status
    - group: ""
      kind: PersistentVolumeClaim
      jsonPointers:
        - /status
```

- [ ] **Step 2: YAML 유효성 + exclude 정합 검증**

Run:
```bash
python3 -c "import yaml; d=list(yaml.safe_load_all(open('gitops/argocd/apps-eks/data-kafka-cluster.yaml')))[0]; assert d['metadata']['name']=='eks-data-kafka-cluster'; ex=d['spec']['source']['directory']['exclude']; [ (s in ex) or (_ for _ in ()).throw(AssertionError(s)) for s in ['kafka-servicemonitor.yaml','kafka-exporter-servicemonitor.yaml','kafkauser-airflow-analytics.yaml'] ]; assert 'lab-events' not in ex and 'security-logs' not in ex, 'topic 은 제외하면 안 됨'; print('OK')"
```
Expected: `OK`(3개 exclude 항목 존재, 토픽은 미제외). 참고: onprem `ignoreDifferences` 의 `cilium.io/CiliumIdentity` 항목은 EKS엔 Cilium 없어 제거했다(불필요).

- [ ] **Step 3: 실제 include 되는 파일 목록 확인(exclude 시뮬레이션)**

Run:
```bash
cd gitops/apps/kafka-cluster
EXCL="kafka-servicemonitor.yaml kafka-exporter-servicemonitor.yaml kafkauser-airflow-analytics.yaml"
echo "=== EKS 에 include 되는 파일 ==="; \
for f in $(find . -name '*.yaml' | sed 's|^\./||' | sort); do echo "$EXCL" | grep -qw "$f" || echo "  + $f"; done
cd - >/dev/null
```
Expected(include 되어야): namespace, kafka-nodepool, kafka.yaml, ca-certificates, ca-secret-sync, ca-sync-watcher, kafka-clients-clusterissuer, kafka-metrics-configmap, kafka-metrics-service, kafka-exporter-service, **topics/ 전부**(validation-requests/results/dlq + lab-events + security-logs). **제외 확인**: `kafka-servicemonitor.yaml`·`kafka-exporter-servicemonitor.yaml`·`kafkauser-airflow-analytics.yaml` 3개만 목록에 없어야 함.

- [ ] **Step 4: include 되는 매니페스트 전부 YAML 유효성**

Run:
```bash
cd gitops/apps/kafka-cluster
EXCL="kafka-servicemonitor.yaml kafka-exporter-servicemonitor.yaml kafkauser-airflow-analytics.yaml"
for f in $(find . -name '*.yaml' | sed 's|^\./||'); do echo "$EXCL" | grep -qw "$f" && continue; python3 -c "import yaml,sys; list(yaml.safe_load_all(open('$f')))" || { echo "INVALID: $f"; exit 1; }; done
echo "ALL VALID"; cd - >/dev/null
```
Expected: `ALL VALID`.

- [ ] **Step 5: Commit**

```bash
git add gitops/argocd/apps-eks/data-kafka-cluster.yaml
git commit -m "feat(dr): apps-eks kafka-cluster (directory.exclude 로 ServiceMonitor·airflow·분석토픽 제외, wave 1)"
```

---

### Task 4: root-app-eks 편입 + 전체 렌더 스모크

root-app-eks 가 `apps-eks/` 를 recurse 하므로 Task 2·3 파일은 자동 편입된다. 편입·wave·의존을 정적으로 확인한다.

**Files:**
- Verify(수정 없음): `gitops/argocd/root-app-eks.yaml`, `gitops/argocd/apps-eks/`

**Interfaces:**
- Consumes: Task 2·3 산출 Application 2개.
- Produces: (없음 — 검증 태스크). 라이브 sync 는 드릴에서.

- [ ] **Step 1: root-app 이 apps-eks recurse 하는지 확인**

Run:
```bash
python3 -c "import yaml; d=yaml.safe_load(open('gitops/argocd/root-app-eks.yaml')); s=d['spec']['source']; print('path=',s['path'],'recurse=',s.get('directory',{}).get('recurse')); assert s['path']=='gitops/argocd/apps-eks'"
ls gitops/argocd/apps-eks/ | grep -E 'strimzi|kafka'
```
Expected: path=`gitops/argocd/apps-eks` (recurse 또는 디렉토리 앱), 그리고 `data-strimzi-operator.yaml`·`data-kafka-cluster.yaml` 둘 다 목록에 존재.

- [ ] **Step 2: apps-eks 전체 Application YAML 유효 + wave 순서 확인**

Run:
```bash
for f in gitops/argocd/apps-eks/*.yaml; do python3 -c "import yaml,sys; list(yaml.safe_load_all(open('$f')))" || { echo "INVALID $f"; exit 1; }; done; echo "ALL VALID"
python3 -c "
import yaml,glob
for f in sorted(glob.glob('gitops/argocd/apps-eks/*.yaml')):
    d=yaml.safe_load(open(f)); a=d.get('metadata',{}).get('annotations',{})
    print(a.get('argocd.argoproj.io/sync-wave','(none)'), d['metadata']['name'])
"
```
Expected: `ALL VALID`. wave 출력에서 `eks-data-strimzi-operator=0`, `eks-data-kafka-cluster=1`(strimzi 이 kafka 보다 먼저). cert-manager(-10)·pki(-8)·external-secrets(-3) 등 CA/시크릿 선행 앱이 더 앞 wave 인지 육안 확인.

- [ ] **Step 3: 드릴 검증 항목 문서화(라이브 미기동 명시)**

`docs/RUNBOOK/dr-eks-bootstrap.md` 의 부트스트랩 체크리스트에 Kafka 항목 추가(플랫폼 Ready 이후, validation-engine·api 이전):
```markdown
- [ ] Kafka Ready — strimzi-system 오퍼레이터 Running, `kubectl -n kafka get kafka cledyu-kafka` READY=True,
      `kubectl -n kafka get kafkatopic` 에 validation-requests/results/dlq 존재, bootstrap svc
      `cledyu-kafka-kafka-bootstrap.kafka.svc:9093` 응답. (ServiceMonitor 없음은 정상 — EKS 관측 미배포)
```

- [ ] **Step 4: Commit**

```bash
git add docs/RUNBOOK/dr-eks-bootstrap.md
git commit -m "docs(dr): 런북 부트스트랩 체크리스트에 Kafka Ready 검증 추가"
```

---

## Self-Review (작성자 체크)

**Spec 커버리지(§3.1 Kafka 부분):** strimzi-operator(Task 2)·kafka-cluster+토픽3종(Task 3)·구조배포/메시지미복원(directory.exclude 로 분석토픽만 제외, 검증토픽 유지)·CRD-missing 게이트(ServiceMonitor exclude, Task 3) 모두 태스크 존재. validation-engine·api 는 A2·A3(별도 플랜)로 명시 분리 — 이 플랜 범위 밖.

**플레이스홀더 스캔:** 모든 Step 에 실제 명령·매니페스트 존재. TBD/TODO 없음.

**타입/이름 일관성:** Application 이름 `eks-data-strimzi-operator`(Task2)·`eks-data-kafka-cluster`(Task3) 일관. exclude 파일명 5종 Task3 Step1/2/3 에서 동일. nodepool `class` 제거는 Task1 산출 → Task3 가 include 로 소비, 일치.

**미해결(플랜 밖·후속):**
- 온프렘 default SC=longhorn 전제(Task1 Step1 가드). 아니면 DR 전용 nodepool 분리 필요.
- (해소됨) api 가 `lab-events` 토픽에 발행하므로 **토픽은 전부 include**(exclude 는 servicemonitor·airflow-kafkauser 만). 분석 소비자 없어도 KafkaTopic 은 무해.
- 리소스 사이징: 3-broker Kafka(각 2Gi req/30Gi PVC) + 나머지 스택 → DR 노드 용량은 Plan B(pilot-light)·드릴서 확인.
- 라이브 Kafka Ready 검증은 드릴(A3 fidelity 또는 pilot-light 드릴)로 이월 — 이 플랜은 정적 검증만.
