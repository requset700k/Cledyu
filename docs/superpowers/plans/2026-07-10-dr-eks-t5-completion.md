# DR Plan B — T5 완결(B1·B2·B6·B7) + destroy 하드닝 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** helm template만 통과하고 런타임에 api가 못 뜨던 T5 오버레이를, EKS DR에서 apps-eks가 sync-clean하고 web이 Running하도록 실제로 닫는다(api Running 증명은 T6+T8).

**Architecture:** cert-manager+PKI+trust-manager Bundle을 apps-eks에 sync-wave로 얹어 CA 체인을 세우고(B1), api 차트의 Kafka(Strimzi) 커플링을 값 플래그로 게이팅하고(B2), 온프렘 전용 AWS 기능을 끄고(B7), Ingress 호스트를 `.com`으로 교정(B6)한다. 부트스트랩·destroy 절차를 런북에 명문화(T9). 전부 오프라인 검증(helm template + yaml 유효성 + grep), 실 AWS apply 없음.

**Tech Stack:** ArgoCD app-of-apps(sync-wave, multi-source, directory mode), Helm(first-party api/web 차트 + `values-eks.yaml` 오버레이), cert-manager v1.20.2, trust-manager v0.22.0, jetstack charts.

## Global Constraints

- **브랜치:** `feat/dr-eks-overlay` (terraform base `3ca5810` 위, T5 변경 staged). 이 브랜치에서 작업.
- **커밋 스타일:** `git commit -m "..."` 단일 -m(heredoc 금지), 메시지 한국어 conventional(`feat(dr):`/`fix(dr):`), **Co-Authored-By 줄 금지**.
- **기존 주석 보존:** 파일 수정은 반드시 Edit(부분 수술), Write로 전체 재작성 금지.
- **검증은 오프라인만:** `helm template`(first-party api/web), `python3` yaml 유효성, `grep`. 실 AWS `terraform plan/apply` 금지(T10 드릴에서 사용자 직접).
- **리전/계정:** `ap-northeast-2` / `504284203153` (verbatim).
- **cert-manager 버전 = `v1.20.2`**(온프렘 ansible과 동일 핀). 소스 primary=`https://charts.jetstack.io`, **fallback**=OCI `oci://quay.io/jetstack/charts`(온라인에서 `helm search repo jetstack/cert-manager --version v1.20.2` 미발견 시 전환) — F4.
- **targetRevision:** 모든 신규 Application은 `feat/dr-eks-overlay`(주석 `# 드릴 검증 후 main`), 기존 apps-eks 관례와 동일.
- **성공 기준:** apps-eks sync-clean + web Running + api는 secret 대기(ContainerCreating/CreateContainerConfigError)까지. **api Running은 범위 밖(T6+T8).**

관련 스펙: `docs/superpowers/specs/2026-07-10-dr-eks-t5-completion-design.md` (F1~F12 근거).

---

## File Structure

**신규:**
- `gitops/apps/cert-manager/values.yaml` — EKS DR용 lean cert-manager helm 값.
- `gitops/argocd/apps-eks/platform-cert-manager.yaml` — cert-manager Application(wave -10).
- `gitops/argocd/apps-eks/platform-pki.yaml` — PKI Application(wave -8, `pki-bootstrap.yaml` 재사용).

**수정:**
- `gitops/argocd/apps-eks/platform-trust-manager.yaml` — Bundle CR directory 소스 추가(F1).
- `gitops/apps/api/templates/kafka.yaml` — 전체 `{{ if .Values.kafka.enabled }}` 게이팅(F7).
- `gitops/apps/api/templates/deployment.yaml` — `kafka-certs` volume/mount 게이팅(F2).
- `gitops/apps/api/values.yaml` — `kafka.enabled: true` 기본값 추가(온프렘 보존).
- `gitops/apps/api/values-eks.yaml` — `kafka.enabled:false`·`aws.enabled:false`·`ingress.hosts`·ACM(B2/B6/B7).
- `gitops/apps/web/values-eks.yaml` — `ingress.hosts`·ACM(B6).
- `docs/RUNBOOK/dr-eks-bootstrap.md` — 부트스트랩 apply + destroy 고아방지 순서(T9).

---

## Task 1: cert-manager Application + lean values (B1a)

**Files:**
- Create: `gitops/apps/cert-manager/values.yaml`
- Create: `gitops/argocd/apps-eks/platform-cert-manager.yaml`

**Interfaces:**
- Consumes: 없음(플랫폼 진입점). jetstack `cert-manager` 차트 v1.20.2.
- Produces: cert-manager CRD(`cert-manager.io/v1`)+webhook. namespace `cert-manager`. Task 2(PKI)·Task 3(Bundle)·consumer Certificate가 이 CRD/webhook에 의존. sync-wave `-10`.

- [ ] **Step 1: lean values 작성**

Create `gitops/apps/cert-manager/values.yaml`:

```yaml
---
# EKS DR 전용 cert-manager 값 — 콜드 DR 1회 드릴이라 온프렘 HA 튜닝(replica 2/3·PDB·topology spread)
# 대신 lean(단일 replica). 노드 3개라 HA도 가능하지만 기동 단순·빠른 Ready 우선(YAGNI).
crds:
  enabled: true
replicaCount: 1
webhook:
  replicaCount: 1
cainjector:
  replicaCount: 1
```

- [ ] **Step 2: cert-manager Application 작성**

Create `gitops/argocd/apps-eks/platform-cert-manager.yaml`:

```yaml
---
# cert-manager — EKS DR 신설 앱. api/web/vault Certificate 가 cledyu-ca(ClusterIssuer, platform-pki)로
# 발급되려면 CRD+webhook 이 가장 먼저 떠야 한다 → sync-wave -10(전 앱 중 최선두).
# 소스 스타일은 apps-eks 관례(multi-source: upstream chart + $values git ref)와 동일.
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: eks-platform-cert-manager
  namespace: argocd
  annotations:
    argocd.argoproj.io/sync-wave: "-10"
  finalizers:
    - resources-finalizer.argocd.argoproj.io
spec:
  project: default
  sources:
    - repoURL: https://charts.jetstack.io
      chart: cert-manager
      targetRevision: v1.20.2
      helm:
        releaseName: cert-manager
        valueFiles:
          - $values/gitops/apps/cert-manager/values.yaml
    - repoURL: https://github.com/requset700k/Cledyu.git
      targetRevision: feat/dr-eks-overlay   # 드릴 검증 후 main
      ref: values
  destination:
    server: https://kubernetes.default.svc
    namespace: cert-manager
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
```

- [ ] **Step 3: YAML 유효성 + 필드 검증**

Run:
```bash
python3 -c "import yaml; yaml.safe_load(open('gitops/apps/cert-manager/values.yaml')); list(yaml.safe_load_all(open('gitops/argocd/apps-eks/platform-cert-manager.yaml'))); print('OK')"
grep -E 'chart: cert-manager|targetRevision: v1.20.2|sync-wave: \"-10\"|crds:|enabled: true' gitops/argocd/apps-eks/platform-cert-manager.yaml gitops/apps/cert-manager/values.yaml
```
Expected: `OK`, 그리고 `chart: cert-manager`·`targetRevision: v1.20.2`·`sync-wave: "-10"`·`crds:`/`enabled: true` 모두 매치.

> ⚠️ 온라인(드릴 착수) 시 1회 확인: `helm repo add jetstack https://charts.jetstack.io && helm search repo jetstack/cert-manager --version v1.20.2`. 미발견이면 Application의 `repoURL`을 `oci://quay.io/jetstack/charts`로 교체(chart/version 동일).

- [ ] **Step 4: Commit**

```bash
git add gitops/apps/cert-manager/values.yaml gitops/argocd/apps-eks/platform-cert-manager.yaml
git commit -m "feat(dr): EKS DR cert-manager 앱(v1.20.2, lean, wave -10) 추가 (B1)"
```

---

## Task 2: PKI Application — cledyu-ca 체인 (B1b)

**Files:**
- Create: `gitops/argocd/apps-eks/platform-pki.yaml`
- 재사용(수정 없음): `infra/kubernetes/cert-manager/pki-bootstrap.yaml`

**Interfaces:**
- Consumes: Task 1 cert-manager CRD+webhook. 기존 `infra/kubernetes/cert-manager/pki-bootstrap.yaml`(selfsigned-root→cledyu-root-ca→cledyu-ca).
- Produces: ClusterIssuer `cledyu-ca`(api/web/vault Certificate issuerRef), secret `cledyu-root-ca`(cert-manager ns; Task 3 Bundle의 source). sync-wave `-8`.

- [ ] **Step 1: 재사용 대상 존재 확인**

Run:
```bash
test -f infra/kubernetes/cert-manager/pki-bootstrap.yaml && grep -E 'name: cledyu-ca|name: cledyu-root-ca|name: selfsigned-root' infra/kubernetes/cert-manager/pki-bootstrap.yaml
```
Expected: 파일 존재 + `selfsigned-root`·`cledyu-root-ca`·`cledyu-ca` 매치(체인 확인).

- [ ] **Step 2: PKI Application 작성**

Create `gitops/argocd/apps-eks/platform-pki.yaml`:

```yaml
---
# PKI 부트스트랩 — 온프렘 pki-bootstrap.yaml 재사용(드리프트 0). DR 클러스터에 자체 CA 신규 발급.
# 체인: selfsigned-root(ClusterIssuer) → cledyu-root-ca(CA Cert/secret) → cledyu-root-ca(ClusterIssuer)
#      → cledyu-ca(CA Cert) → cledyu-ca(ClusterIssuer). api/web/vault Certificate 가 cledyu-ca 사용.
# cert-manager(-10) 이후, consumer(0) 이전 → wave -8.
# SkipDryRunOnMissingResource: 첫 sync 때 cert-manager CRD 등록 전이면 CR dry-run 을 skip(retry 수렴).
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: eks-platform-pki
  namespace: argocd
  annotations:
    argocd.argoproj.io/sync-wave: "-8"
  finalizers:
    - resources-finalizer.argocd.argoproj.io
spec:
  project: default
  source:
    repoURL: https://github.com/requset700k/Cledyu.git
    targetRevision: feat/dr-eks-overlay   # 드릴 검증 후 main
    path: infra/kubernetes/cert-manager
    directory:
      include: pki-bootstrap.yaml
  destination:
    server: https://kubernetes.default.svc
    namespace: cert-manager
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - ServerSideApply=true
      - SkipDryRunOnMissingResource=true
    retry:
      limit: 10
      backoff:
        duration: 10s
        factor: 2
        maxDuration: 3m
```

- [ ] **Step 3: YAML 유효성 + 필드 검증**

Run:
```bash
python3 -c "import yaml; list(yaml.safe_load_all(open('gitops/argocd/apps-eks/platform-pki.yaml'))); print('OK')"
grep -E 'path: infra/kubernetes/cert-manager|include: pki-bootstrap.yaml|sync-wave: \"-8\"|SkipDryRunOnMissingResource=true' gitops/argocd/apps-eks/platform-pki.yaml
```
Expected: `OK` + 4개 필드 모두 매치.

- [ ] **Step 4: Commit**

```bash
git add gitops/argocd/apps-eks/platform-pki.yaml
git commit -m "feat(dr): EKS DR PKI 앱(pki-bootstrap 재사용, cledyu-ca 체인, wave -8) 추가 (B1)"
```

---

## Task 3: trust-manager Bundle CR sync (B1c, F1)

**Files:**
- Modify: `gitops/argocd/apps-eks/platform-trust-manager.yaml`
- 참조(수정 없음): `gitops/apps/trust-manager/cledyu-root-ca-bundle.yaml`, `gitops/apps/trust-manager/namespace.yaml`

**Interfaces:**
- Consumes: Task 2 secret `cledyu-root-ca`(cert-manager ns). trust-manager 컨트롤러(이 앱의 chart 소스).
- Produces: Bundle CR `cledyu-root-ca-bundle` → 라벨 `cledyu.io/trust-bundle: root-ca` ns에 ConfigMap `cledyu-root-ca-bundle`(key `ca.crt`) 분배. api 파드의 `/etc/ssl/cledyu` 비-optional 마운트가 이걸 소비.

- [ ] **Step 1: 현재 상태 확인(3번째 소스 부재 + Bundle CR 존재)**

Run:
```bash
grep -c 'repoURL' gitops/argocd/apps-eks/platform-trust-manager.yaml
grep -E 'kind: Bundle|cledyu.io/trust-bundle' gitops/apps/trust-manager/cledyu-root-ca-bundle.yaml
```
Expected: `repoURL` 2개(= chart + $values, directory 소스 없음), Bundle CR 파일에 `kind: Bundle`·selector 라벨 존재.

- [ ] **Step 2: 3번째 directory 소스 추가**

Edit `gitops/argocd/apps-eks/platform-trust-manager.yaml` — `sources:` 배열의 마지막 `$values` 소스 블록 바로 뒤에 directory 소스를 추가한다. 아래 old→new:

old_string:
```yaml
    - repoURL: https://github.com/requset700k/Cledyu.git
      targetRevision: feat/dr-eks-overlay   # 드릴 검증 후 main
      ref: values
  destination:
```
new_string:
```yaml
    - repoURL: https://github.com/requset700k/Cledyu.git
      targetRevision: feat/dr-eks-overlay   # 드릴 검증 후 main
      ref: values
    # Bundle CR + namespace — directory mode(온프렘 platform-trust-manager.yaml 과 동일).
    # cledyu-root-ca-bundle 이 라벨 ns 에 ConfigMap 분배 → api /etc/ssl/cledyu 비-optional 마운트 충족(F1).
    - repoURL: https://github.com/requset700k/Cledyu.git
      targetRevision: feat/dr-eks-overlay   # 드릴 검증 후 main
      path: gitops/apps/trust-manager
      directory:
        include: "{namespace.yaml,cledyu-root-ca-bundle.yaml}"
  destination:
```

- [ ] **Step 3: YAML 유효성 + 소스 3개 검증**

Run:
```bash
python3 -c "import yaml; d=list(yaml.safe_load_all(open('gitops/argocd/apps-eks/platform-trust-manager.yaml')))[0]; assert len(d['spec']['sources'])==3, d['spec']['sources']; print('sources=3 OK')"
grep -E 'cledyu-root-ca-bundle.yaml|SkipDryRunOnMissingResource=true' gitops/argocd/apps-eks/platform-trust-manager.yaml
```
Expected: `sources=3 OK` + directory include·SkipDryRun 매치(SkipDryRun은 기존에 이미 있음 — Bundle CRD 첫 sync 대응).

- [ ] **Step 4: Commit**

```bash
git add gitops/argocd/apps-eks/platform-trust-manager.yaml
git commit -m "fix(dr): EKS trust-manager 에 Bundle CR 소스 추가 — api CA 번들 마운트 충족 (B1/F1)"
```

---

## Task 4: api Kafka 커플링 게이팅 — 차트 수술 (B2, F2·F7)

**Files:**
- Modify: `gitops/apps/api/templates/kafka.yaml`
- Modify: `gitops/apps/api/templates/deployment.yaml` (kafka-certs volume/mount)
- Modify: `gitops/apps/api/values.yaml` (kafka.enabled 기본값)

**Interfaces:**
- Consumes: 없음(값 플래그 도입).
- Produces: 값 `kafka.enabled`(기본 true). false 시 `KafkaUser`·`Certificate api-kafka-client`·`kafka-certs` 볼륨/마운트 미렌더. Task 5(values-eks)가 EKS에서 false로 설정.

- [ ] **Step 1: 현재 문제 재현(kafka.enabled 미도입 → 렌더에 KafkaUser 존재)**

Run:
```bash
helm template api gitops/apps/api -f gitops/apps/api/values.yaml --set kafka.enabled=false | grep -E 'kind: KafkaUser|api-kafka-client|name: kafka-certs' || echo "NONE"
```
Expected: **KafkaUser/api-kafka-client/kafka-certs 가 여전히 렌더됨**(아직 게이트 없음) — 즉 `NONE`이 안 나오고 매치가 뜬다. 이것이 F2/F7 블로커.

- [ ] **Step 2: base values.yaml 에 kafka.enabled 기본값 추가**

Edit `gitops/apps/api/values.yaml` — `aws:` 섹션 주석 앞에 kafka 블록 삽입:

old_string:
```yaml
# AWS EC2 오버플로우 — 온프렘 KubeVirt 풀이 가득 차면 세션을 EC2로 버스트한다(Phase 13).
```
new_string:
```yaml
# Kafka(Strimzi) 커플링 — 온프렘 기본 활성. EKS DR 는 values-eks 에서 false(Strimzi/KafkaUser CRD 미배포 →
# kafka.yaml·kafka-certs 볼륨 미렌더). false 여야 EKS 에서 api Application sync 가 통과한다(F2/F7).
kafka:
  enabled: true

# AWS EC2 오버플로우 — 온프렘 KubeVirt 풀이 가득 차면 세션을 EC2로 버스트한다(Phase 13).
```

- [ ] **Step 3: kafka.yaml 전체 게이팅**

Edit `gitops/apps/api/templates/kafka.yaml` — 파일 맨 앞에 `{{- if .Values.kafka.enabled }}`, 맨 끝에 `{{- end }}` 추가.

맨 앞(old→new):

old_string:
```yaml
---
# Session API 가 Kafka(validation-requests) 에 mTLS 로 발행하기 위한 클라이언트 인증서.
```
new_string:
```yaml
{{- if .Values.kafka.enabled }}
---
# Session API 가 Kafka(validation-requests) 에 mTLS 로 발행하기 위한 클라이언트 인증서.
```

맨 끝(old→new) — 파일의 마지막 ACL 블록 뒤에 `{{- end }}`:

old_string:
```yaml
      - resource:
          type: topic
          name: lab-events
          patternType: literal
        operations: [Write, Describe]
```
new_string:
```yaml
      - resource:
          type: topic
          name: lab-events
          patternType: literal
        operations: [Write, Describe]
{{- end }}
```

- [ ] **Step 4: deployment.yaml 의 kafka-certs volume/mount 게이팅**

Edit `gitops/apps/api/templates/deployment.yaml` — volumeMount:

old_string:
```yaml
          volumeMounts:
            - name: kafka-certs
              mountPath: /etc/kafka-certs
              readOnly: true
            - name: cledyu-ca
```
new_string:
```yaml
          volumeMounts:
            {{- if .Values.kafka.enabled }}
            - name: kafka-certs
              mountPath: /etc/kafka-certs
              readOnly: true
            {{- end }}
            - name: cledyu-ca
```

그리고 volume:

old_string:
```yaml
      volumes:
        - name: kafka-certs
          secret:
            secretName: api-kafka-client-tls
        - name: cledyu-ca
```
new_string:
```yaml
      volumes:
        {{- if .Values.kafka.enabled }}
        - name: kafka-certs
          secret:
            secretName: api-kafka-client-tls
        {{- end }}
        - name: cledyu-ca
```

- [ ] **Step 5: 게이트 동작 검증(EKS=false 는 사라지고, 온프렘=true 는 유지)**

Run:
```bash
echo "== kafka.enabled=false (EKS) =="
helm template api gitops/apps/api -f gitops/apps/api/values.yaml --set kafka.enabled=false | grep -E 'kind: KafkaUser|api-kafka-client|name: kafka-certs' || echo "NONE(기대)"
echo "== 기본(온프렘, kafka.enabled=true) =="
helm template api gitops/apps/api -f gitops/apps/api/values.yaml | grep -Ec 'kind: KafkaUser|api-kafka-client|name: kafka-certs'
```
Expected: 첫 블록 `NONE(기대)`(EKS 렌더에서 전부 제거), 둘째 블록 숫자 ≥ 3(온프렘 회귀 없음).

- [ ] **Step 6: Commit**

```bash
git add gitops/apps/api/templates/kafka.yaml gitops/apps/api/templates/deployment.yaml gitops/apps/api/values.yaml
git commit -m "fix(dr): api Kafka 커플링을 kafka.enabled 로 게이팅 — EKS sync 블로커 제거 (B2/F7)"
```

---

## Task 5: api/web values-eks 델타 — kafka off·aws off·host·ACM (B2·B6·B7)

**Files:**
- Modify: `gitops/apps/api/values-eks.yaml`
- Modify: `gitops/apps/web/values-eks.yaml`

**Interfaces:**
- Consumes: Task 4 값 `kafka.enabled`. base `aws.enabled`(기존 게이트). ACM 와일드카드 `*.cledyu.com`(`data.aws_acm_certificate.wildcard`).
- Produces: EKS 렌더에서 api — KafkaUser/kafka-certs 없음, AWS 크레드 env 없음, ingress host `api.cledyu.com` 단일. web — host `app.cledyu.com`.

- [ ] **Step 1: api values-eks 에 kafka/aws off + host 추가, ACM 주석 정정**

Edit `gitops/apps/api/values-eks.yaml` — 파일 끝 `ingress:` 블록의 certificate-arn 부분을 교체하고, 최상위 플래그를 추가한다.

먼저 최상위 플래그 추가(old→new):

old_string:
```yaml
replicaCount: 1

ingress:
  className: alb
```
new_string:
```yaml
replicaCount: 1

# 온프렘 전용 기능 off — 이 secret/CR 들이 EKS 엔 없어서 게이팅 안 하면 api 가 못 뜬다.
kafka:
  enabled: false   # Strimzi/KafkaUser 미배포 (B2/F7)
aws:
  enabled: false   # EC2 오버플로우 미사용 → 비-optional AWS 크레드(cledyu-api-aws) 주입 차단 (B7/F10)

ingress:
  className: alb
  # Ingress host 를 .com 단일로 교정 — .local 은 internet-facing ALB 에서 죽은 규칙(B6).
  # api.cledyu.com 은 와일드카드 *.cledyu.com ACM 과 매칭.
  hosts:
    - api.cledyu.com
```

그리고 ACM 주석/치환원 정정(old→new):

old_string:
```yaml
    # 부트스트랩(T9 런북)에서 terraform output(aws_acm_certificate_validation.auth)으로 치환.
    alb.ingress.kubernetes.io/certificate-arn: "<<ACM auth cert ARN>>"
```
new_string:
```yaml
    # 와일드카드 *.cledyu.com ACM(data.aws_acm_certificate.wildcard)의 ARN. 자동발견 대신 명시 지정(드릴 결정성, F3).
    # 부트스트랩(T9 런북)에서 실제 ARN 으로 치환. 계정 고정값이라 확정 후 하드코딩 가능.
    alb.ingress.kubernetes.io/certificate-arn: "<<WILDCARD_ACM_ARN>>"
```

- [ ] **Step 2: web values-eks 에 host 추가 + ACM 주석 정정**

Edit `gitops/apps/web/values-eks.yaml`:

old_string:
```yaml
ingress:
  className: alb
  annotations:
    alb.ingress.kubernetes.io/scheme: internet-facing
    alb.ingress.kubernetes.io/target-type: ip
    alb.ingress.kubernetes.io/group.name: cledyu-dr
    # 부트스트랩(T9 런북)에서 terraform output(aws_acm_certificate_validation.auth)으로 치환.
    alb.ingress.kubernetes.io/certificate-arn: "<<ACM auth cert ARN>>"
```
new_string:
```yaml
ingress:
  className: alb
  # Ingress host 를 .com 단일로 교정(B6). app.cledyu.com 은 와일드카드 *.cledyu.com ACM 과 매칭.
  hosts:
    - app.cledyu.com
  annotations:
    alb.ingress.kubernetes.io/scheme: internet-facing
    alb.ingress.kubernetes.io/target-type: ip
    alb.ingress.kubernetes.io/group.name: cledyu-dr
    # 와일드카드 *.cledyu.com ACM(data.aws_acm_certificate.wildcard)의 ARN. 부트스트랩(T9 런북)에서 치환.
    alb.ingress.kubernetes.io/certificate-arn: "<<WILDCARD_ACM_ARN>>"
```

- [ ] **Step 3: EKS 렌더 종합 검증(api/web)**

Run:
```bash
echo "== api EKS 렌더 =="
helm template api gitops/apps/api -f gitops/apps/api/values.yaml -f gitops/apps/api/values-eks.yaml > /tmp/api-eks.yaml
grep -E 'kind: KafkaUser|api-kafka-client|name: kafka-certs' /tmp/api-eks.yaml || echo "kafka 제거 OK"
grep -E 'AWS_ACCESS_KEY_ID|AWS_SECRET_ACCESS_KEY' /tmp/api-eks.yaml || echo "AWS 크레드 제거 OK"
grep -E 'host: api.cledyu.com' /tmp/api-eks.yaml && ! grep -E 'api.cledyu.local' /tmp/api-eks.yaml && echo "host .com 단일 OK"
echo "== web EKS 렌더 =="
helm template web gitops/apps/web -f gitops/apps/web/values.yaml -f gitops/apps/web/values-eks.yaml | grep -E 'host: app.cledyu.com' && echo "web host OK"
echo "== api 온프렘 회귀(AWS 유지) =="
helm template api gitops/apps/api -f gitops/apps/api/values.yaml | grep -Eq 'AWS_ACCESS_KEY_ID' && echo "온프렘 AWS 유지 OK"
```
Expected: `kafka 제거 OK`·`AWS 크레드 제거 OK`·`host .com 단일 OK`·`web host OK`·`온프렘 AWS 유지 OK` 모두 출력.

- [ ] **Step 4: Commit**

```bash
git add gitops/apps/api/values-eks.yaml gitops/apps/web/values-eks.yaml
git commit -m "fix(dr): api/web values-eks — kafka/aws off·host .com·ACM 와일드카드 (B2/B6/B7)"
```

---

## Task 6: T9 런북 — 부트스트랩 apply + destroy 고아방지 (T9)

**Files:**
- Modify: `docs/RUNBOOK/dr-eks-bootstrap.md`

**Interfaces:**
- Consumes: bastion(SSM), apps-eks root-app, aws-load-balancer-controller가 만든 out-of-band ALB/EBS.
- Produces: 운영자용 명령 절차(부트스트랩·destroy). 코드 아님.

- [ ] **Step 1: 부트스트랩 apply 명령 명시 + ACM 치환 안내 추가**

Edit `docs/RUNBOOK/dr-eks-bootstrap.md` — 체크리스트의 `apps-eks root-app 적용` 항목 근처에, 아래 명시 절차 블록을 추가한다(기존 줄 보존, 삽입). 삽입 위치: `- [ ] apps-eks root-app 적용 → 플랫폼(...) Ready` 줄 **앞**에.

추가 블록:
```markdown
### apps-eks 부트스트랩 (bastion 에서)

```bash
# 0) values-eks 의 <<WILDCARD_ACM_ARN>> 치환 (계정 고정값)
ARN=$(aws acm list-certificates --region ap-northeast-2 \
  --query "CertificateSummaryList[?DomainName=='*.cledyu.com'].CertificateArn" --output text)
# gitops/apps/{api,web}/values-eks.yaml 의 <<WILDCARD_ACM_ARN>> 를 $ARN 으로 치환해 커밋(드릴 브랜치)

# 1) root-app 적용 — 이후 ArgoCD 가 wave 순서(cert-manager -10 → pki -8 → ... → api/web 0)로 sync
kubectl apply -f gitops/argocd/root-app-eks.yaml

# 2) 플랫폼 Ready 대기: cert-manager·cledyu-ca(ClusterIssuer)·Bundle(ConfigMap) 확인
kubectl -n cert-manager wait --for=condition=Available deploy/cert-manager --timeout=300s
kubectl get clusterissuer cledyu-ca
kubectl -n api get configmap cledyu-root-ca-bundle   # trust-manager Bundle 분배 확인
```
```

- [ ] **Step 2: destroy 고아방지 순서 추가**

Edit `docs/RUNBOOK/dr-eks-bootstrap.md` — 체크리스트의 `- [ ] destroy (...) + 잔존 0 확인` 줄을 아래 상세 절차로 **교체**한다.

old_string:
```markdown
- [ ] destroy (`enable_eks_dr=false` apply) + 잔존 0 확인
```
new_string:
```markdown
- [ ] destroy — **고아 방지 순서 필수** (아래)

### destroy (고아 방지)

DR ALB(aws-load-balancer-controller 가 Ingress 보고 out-of-band 생성)와 gp3 EBS(reclaim=Delete)는
terraform 밖이다. 클러스터를 먼저 부수면 ALB·target group·`k8s-*` SG·ENI·EBS 가 고아로 남고,
남은 ENI 가 서브넷/VPC 삭제를 `DependencyViolation` 으로 막는다. 반드시 in-cluster 부터 정리한다.

```bash
# 1) Ingress 삭제 → 컨트롤러가 ALB/TG/SG 정리 (완료까지 대기)
kubectl delete ingress -A --all
aws elbv2 describe-load-balancers --region ap-northeast-2 \
  --query "LoadBalancers[?VpcId=='<dr-vpc-id>'].LoadBalancerArn" --output text   # 빈 값 될 때까지 확인

# 2) PVC 삭제 → EBS CSI 가 gp3 볼륨 삭제 (드릴 데이터는 S3 백업에 있으니 폐기 가능)
kubectl delete pvc -A --all

# 3) LoadBalancer 타입 Service 없음(traefik 은 DR 앱셋 미포함) — skip

# 4) terraform destroy
cd infra/terraform/aws && terraform apply -var enable_eks_dr=false

# 5) 고아 검증(전부 0/비어야 함)
aws elbv2 describe-load-balancers --region ap-northeast-2 --query "LoadBalancers[?VpcId=='<dr-vpc-id>']" --output text
aws ec2 describe-volumes --region ap-northeast-2 --filters "Name=tag:kubernetes.io/cluster/<dr-cluster-name>,Values=owned" --query "Volumes[].VolumeId" --output text
aws ec2 describe-network-interfaces --region ap-northeast-2 --filters "Name=vpc-id,Values=<dr-vpc-id>" --query "NetworkInterfaces[].NetworkInterfaceId" --output text
aws ec2 describe-security-groups --region ap-northeast-2 --query "SecurityGroups[?starts_with(GroupName,'k8s-')].GroupId" --output text
```
```

- [ ] **Step 3: 문서 검증(필수 블록 존재)**

Run:
```bash
grep -E 'kubectl apply -f gitops/argocd/root-app-eks.yaml|kubectl delete ingress -A|kubectl delete pvc -A|DependencyViolation|WILDCARD_ACM_ARN|cledyu-root-ca-bundle' docs/RUNBOOK/dr-eks-bootstrap.md
```
Expected: 6개 문자열 모두 매치.

- [ ] **Step 4: Commit**

```bash
git add docs/RUNBOOK/dr-eks-bootstrap.md
git commit -m "docs(dr): 런북 apps-eks 부트스트랩 명령 + destroy 고아방지 순서 (T9)"
```

---

## Self-Review (완료)

- **스펙 커버리지:** B1(Task 1·2·3), B2(Task 4·5), B6(Task 5), B7(Task 5), T9(Task 6). F1=Task3, F2/F7=Task4, F8=범위 밖(성공기준에 명시), F10=Task5(aws off), F9/F11/F12=클리어(조치 불요). 전 항목 태스크 매핑됨.
- **플레이스홀더:** `<<WILDCARD_ACM_ARN>>`는 의도적 런타임 치환값(T9 런북에 치환 절차 명시) — 코드 플레이스홀더 아님.
- **타입/이름 정합:** 값 키 `kafka.enabled`(Task4 도입 → Task5 소비), `aws.enabled`(기존), ClusterIssuer `cledyu-ca`(Task2 산출 → consumer 참조), ConfigMap `cledyu-root-ca-bundle`(Task3 분배 → deployment 소비) 전부 일치.
- **커밋 정책:** 단일 -m·Co-Authored-By 없음·Edit 수술(주석 보존) 준수.
