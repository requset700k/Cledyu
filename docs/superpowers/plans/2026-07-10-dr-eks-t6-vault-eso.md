# DR Plan B — T6: Vault DR 오버레이 + ESO 배선 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

**Goal:** 복원된 Vault가 IRSA로 auto-unseal되고 ESO가 그 Vault에 인증해 `cledyu-api-oidc`를 만들어 api의 `CreateContainerConfigError`를 해소(→ api 파드 Running)한다.

**Architecture:** hashicorp vault 차트를 단일노드 raft + awskms(IRSA) + gp3로 values-eks 오버레이. cledyu-ca(cert-manager)가 vault-tls 발급. 온프렘 ClusterSecretStore·ES를 directory include로 재사용(드리프트 0). k8s auth는 복원 후 런북에서 EKS용으로 재설정.

**Tech Stack:** ArgoCD multi-source(원격 vault chart + $values + directory), Helm(hashicorp/vault 0.32.0), Vault(raft, awskms seal), External Secrets Operator, AWS KMS/STS(IRSA).

## Global Constraints

- **브랜치:** `feat/dr-eks-overlay`. **선행:** T5 완결(cert-manager·PKI·trust-manager·apps-eks) staged 상태.
- **커밋:** `git commit -m` 단일(heredoc 금지), 한국어 conventional, **Co-Authored-By 금지**. 수정은 Edit(주석 보존).
- **검증 오프라인:** vault는 원격 차트라 subagent는 **values-eks/Application 파일 내용 직접 검증**(yaml 유효성 + grep). 전체 `helm template`(차트 pull 필요)은 온라인/드릴에서. 실 AWS apply/unseal은 T10(사용자 직접).
- **리전/계정:** `ap-northeast-2` / `504284203153`. **unseal KMS 키:** `arn:aws:kms:ap-northeast-2:504284203153:key/e29e3ec2-f5e0-4308-af6f-5b576cc99f52`(verbatim, 삭제 금지).
- **vault chart:** `https://helm.releases.hashicorp.com` chart `vault` `0.32.0`(온프렘과 동일). **targetRevision** 모든 앱 `feat/dr-eks-overlay`(주석 `# 드릴 검증 후 main`).
- **정적 키 금지:** Vault creds는 IRSA. `<<VAULT_UNSEAL_ROLE_ARN>>`은 런북서 `terraform output eks_dr_vault_unseal_role_arn`으로 치환.

관련 스펙: `docs/superpowers/specs/2026-07-10-dr-eks-t6-vault-eso-design.md`.

---

## File Structure

**신규:**
- `gitops/apps/vault/values-eks.yaml` — Vault DR 델타(IRSA·gp3·단일노드 raft·seal awskms).
- `gitops/argocd/apps-eks/platform-vault.yaml` — Vault Application(온프렘 미러, 2곳 변경).
- `gitops/argocd/apps-eks/data-eso-store.yaml` — ClusterSecretStore+ES 재사용 Application.

**수정:**
- `docs/RUNBOOK/dr-eks-bootstrap.md` — k8s auth 재설정 스텝 추가 + stale self-signed vault-tls 스텝 삭제.

---

## Task 1: Vault DR 오버레이 (values-eks + platform-vault)

**Files:**
- Create: `gitops/apps/vault/values-eks.yaml`
- Create: `gitops/argocd/apps-eks/platform-vault.yaml`

**Interfaces:**
- Consumes: T3 `eks_dr_vault_unseal_role_arn`(IRSA), cledyu-ca(vault-tls 발급), gp3 SC(T5), base `gitops/apps/vault/values.yaml`.
- Produces: 단일노드 Vault(SA `vault`, IRSA awskms unseal, gp3). T10 스냅샷 복원 대상. `vault-active.vault.svc:8200`(ESO store 접속점).

- [ ] **Step 1: values-eks 작성**

Create `gitops/apps/vault/values-eks.yaml`:

```yaml
---
# EKS DR 오버레이 — 단일노드 raft + IRSA awskms unseal + gp3. 정적 크레드 env 없음.
# raft.config 는 base(멀티노드 retry_join)를 통째로 대체 — tls/seal/service_registration 자기완결.
server:
  serviceAccount:
    # IRSA: SA(vault)에 role-arn 만 추가(name/create 미지정 → 차트 기본 SA vault 유지).
    annotations:
      eks.amazonaws.com/role-arn: "<<VAULT_UNSEAL_ROLE_ARN>>"   # terraform output eks_dr_vault_unseal_role_arn
  dataStorage:
    storageClass: gp3
  auditStorage:
    storageClass: gp3
  ha:
    replicas: 1
    raft:
      config: |
        ui = true
        disable_mlock = true

        listener "tcp" {
          address = "[::]:8200"
          cluster_address = "[::]:8201"
          tls_disable = 0
          tls_cert_file = "/vault/tls/tls.crt"
          tls_key_file = "/vault/tls/tls.key"
          tls_client_ca_file = "/vault/tls/ca.crt"
          telemetry {
            unauthenticated_metrics_access = true
          }
        }

        storage "raft" {
          path = "/vault/data"
        }

        seal "awskms" {
          region     = "ap-northeast-2"
          kms_key_id = "arn:aws:kms:ap-northeast-2:504284203153:key/e29e3ec2-f5e0-4308-af6f-5b576cc99f52"
        }

        service_registration "kubernetes" {}

        telemetry {
          prometheus_retention_time = "30s"
          disable_hostname = true
        }
```

- [ ] **Step 2: platform-vault Application 작성**

Create `gitops/argocd/apps-eks/platform-vault.yaml`:

```yaml
---
# Vault — 온프렘 platform-vault.yaml 미러, 2곳 변경:
#  (1) valueFiles 를 values-awskms.yaml → values-eks.yaml(정적 크레드 배제, IRSA)
#  (2) directory include 에서 ingress.yaml/serverstransport.yaml 제외(Traefik CRD 없음, B5)
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: eks-platform-vault
  namespace: argocd
  finalizers:
    - resources-finalizer.argocd.argoproj.io
spec:
  project: default
  sources:
    - repoURL: https://helm.releases.hashicorp.com
      chart: vault
      targetRevision: 0.32.0
      helm:
        releaseName: vault
        valueFiles:
          - $values/gitops/apps/vault/values.yaml
          - $values/gitops/apps/vault/values-eks.yaml
    - repoURL: https://github.com/requset700k/Cledyu.git
      targetRevision: feat/dr-eks-overlay   # 드릴 검증 후 main
      ref: values
    - repoURL: https://github.com/requset700k/Cledyu.git
      targetRevision: feat/dr-eks-overlay   # 드릴 검증 후 main
      path: gitops/apps/vault
      directory:
        include: "{namespace.yaml,admin-serviceaccount.yaml,certificate.yaml}"
  destination:
    server: https://kubernetes.default.svc
    namespace: vault
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

- [ ] **Step 3: 오프라인 검증 (파일 내용 직접)**

Run:
```bash
python3 -c "import yaml; yaml.safe_load(open('gitops/apps/vault/values-eks.yaml')); list(yaml.safe_load_all(open('gitops/argocd/apps-eks/platform-vault.yaml'))); print('yaml OK')"
echo '-- values-eks 필수 요소 --'
grep -E 'eks.amazonaws.com/role-arn|storageClass: gp3|seal "awskms"|tls_cert_file|service_registration|replicas: 1' gitops/apps/vault/values-eks.yaml
echo '-- retry_join 미존재(단일노드) --'; grep -c 'retry_join' gitops/apps/vault/values-eks.yaml
echo '-- 정적 크레드 미존재(values.yaml+values-eks) --'; grep -c 'extraSecretEnvironmentVars\|AWS_ACCESS_KEY_ID' gitops/apps/vault/values.yaml gitops/apps/vault/values-eks.yaml
echo '-- platform-vault: values-eks 로드 + Traefik 제외 --'
grep -E 'values-eks.yaml|values-awskms.yaml' gitops/argocd/apps-eks/platform-vault.yaml
grep -E 'include:' gitops/argocd/apps-eks/platform-vault.yaml
```
Expected: `yaml OK`; role-arn·gp3(2회)·seal awskms·tls_cert_file·service_registration·`replicas: 1` 매치; `retry_join` = **0**; extraSecretEnvironmentVars/AWS_ACCESS_KEY_ID = **0 0**; valueFiles에 values-eks 있고 values-awskms **없음**; include에 `namespace.yaml,admin-serviceaccount.yaml,certificate.yaml`만(ingress/serverstransport 없음).

> **온라인/드릴 전체 렌더(선택):** `helm repo add hashicorp https://helm.releases.hashicorp.com && helm pull hashicorp/vault --version 0.32.0 --untar && helm template vault ./vault -f gitops/apps/vault/values.yaml -f gitops/apps/vault/values-eks.yaml | grep -E 'role-arn|gp3|awskms|AWS_ACCESS_KEY_ID'`.

- [ ] **Step 4: Commit**
```bash
git add gitops/apps/vault/values-eks.yaml gitops/argocd/apps-eks/platform-vault.yaml
git commit -m "feat(dr): Vault DR 오버레이 — IRSA awskms unseal + gp3 + 단일노드 raft (T6)"
```

---

## Task 2: ESO 배선 (ClusterSecretStore + api-oidc ES)

**Files:**
- Create: `gitops/argocd/apps-eks/data-eso-store.yaml`
- 재사용(수정 없음): `infra/kubernetes/external-secrets/{clustersecretstore.yaml,cledyu-web-oidc-externalsecret.yaml}`

**Interfaces:**
- Consumes: ESO 오퍼레이터(T5), 복원+재설정된 Vault(Task 1 + 런북), vault-tls(cledyu-ca).
- Produces: `vault-backend` ClusterSecretStore + `cledyu-api-oidc` 시크릿(api ns) → api `CLEDYU_KEYCLOAK_CLIENT_SECRET` 충족.

- [ ] **Step 1: 재사용 대상 존재 + 이름 정합 확인**

Run:
```bash
test -f infra/kubernetes/external-secrets/clustersecretstore.yaml && test -f infra/kubernetes/external-secrets/cledyu-web-oidc-externalsecret.yaml && echo "files OK"
grep -E 'name: vault-backend|role: external-secrets-operator|name: eso-controller|namespace: api|name: cledyu-api-oidc' infra/kubernetes/external-secrets/clustersecretstore.yaml infra/kubernetes/external-secrets/cledyu-web-oidc-externalsecret.yaml
```
Expected: `files OK` + store(vault-backend·role external-secrets-operator·eso-controller)·ES(namespace api·target cledyu-api-oidc) 매치.

- [ ] **Step 2: data-eso-store Application 작성**

Create `gitops/argocd/apps-eks/data-eso-store.yaml`:

```yaml
---
# ESO 배선 — 온프렘 매니페스트 재사용(directory include). vault-backend store 는 k8s auth 로 Vault 인증
# (런북에서 EKS 용 재설정). cledyu-web-oidc ES 는 namespace api 라 api ns(service-api) 선행 필요 —
# SkipDryRunOnMissingResource + retry 로 흡수.
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: eks-data-eso-store
  namespace: argocd
  finalizers:
    - resources-finalizer.argocd.argoproj.io
spec:
  project: default
  source:
    repoURL: https://github.com/requset700k/Cledyu.git
    targetRevision: feat/dr-eks-overlay   # 드릴 검증 후 main
    path: infra/kubernetes/external-secrets
    directory:
      include: "{clustersecretstore.yaml,cledyu-web-oidc-externalsecret.yaml}"
  destination:
    server: https://kubernetes.default.svc
    namespace: external-secrets
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

- [ ] **Step 3: 오프라인 검증**

Run:
```bash
python3 -c "import yaml; list(yaml.safe_load_all(open('gitops/argocd/apps-eks/data-eso-store.yaml'))); print('yaml OK')"
grep -E 'path: infra/kubernetes/external-secrets|clustersecretstore.yaml|cledyu-web-oidc-externalsecret.yaml|SkipDryRunOnMissingResource=true' gitops/argocd/apps-eks/data-eso-store.yaml
```
Expected: `yaml OK` + path·include 2파일·SkipDryRun 매치.

- [ ] **Step 4: Commit**
```bash
git add gitops/argocd/apps-eks/data-eso-store.yaml
git commit -m "feat(dr): ESO 배선 — vault-backend store + cledyu-api-oidc ES 재사용 (T6)"
```

---

## Task 3: 런북 — k8s auth 재설정 + stale 스텝 삭제

**Files:**
- Modify: `docs/RUNBOOK/dr-eks-bootstrap.md`

**Interfaces:**
- Consumes: 복원+unseal된 Vault, vault 파드 SA, 스냅샷 root/admin 토큰.
- Produces: 운영자용 k8s auth 재설정 절차 → ESO 인증 성립.

- [ ] **Step 1: k8s auth 재설정 스텝 추가**

Edit `docs/RUNBOOK/dr-eks-bootstrap.md` — Vault 복원/unseal 절차 **뒤**(ESO가 붙기 직전)에 아래 블록을 삽입한다. 삽입 위치 앵커: 체크리스트의 `- [ ] apps-eks root-app 적용` 줄 **뒤**(또는 Vault 복원 절 말미). 아래 마크다운 블록을 추가:

```markdown
### Vault k8s auth 를 EKS 용으로 재설정 (복원·unseal 후, ESO 인증 직전)

복원 스냅샷의 `auth/kubernetes/config` 는 온프렘 `kubernetes_ca_cert`·`token_reviewer_jwt` 라 EKS API 검증이
실패한다. **vault 파드 안에서 재실행**하면 `@`경로가 EKS 파드의 SA CA·토큰을 읽어 교정된다(role
`external-secrets-operator` 는 스냅샷에 이미 있어 재설정 불요).

```bash
# bastion. VAULT_TOKEN = 복원된 스냅샷의 root/admin 토큰. (VAULT_ADDR/VAULT_CACERT 는 파드 env 에 있음)
kubectl -n vault exec vault-0 -- sh -c '
  export VAULT_TOKEN='"$VAULT_TOKEN"'
  vault write auth/kubernetes/config \
    kubernetes_host=https://kubernetes.default.svc:443 \
    kubernetes_ca_cert=@/var/run/secrets/kubernetes.io/serviceaccount/ca.crt \
    token_reviewer_jwt=@/var/run/secrets/kubernetes.io/serviceaccount/token'

# 검증: ESO 가 store 인증 → 시크릿 생성
kubectl -n api get externalsecret cledyu-web-oidc-client-secret   # STATUS: SecretSynced
kubectl -n api get secret cledyu-api-oidc                          # 생성 확인 → api 기동
```

주: `token_reviewer_jwt` 는 파드 projected 토큰(~1h 만료). 재설정 직후 1회 sync 로 `cledyu-api-oidc`(Retain)
생성되므로 드릴엔 무해. 장기 운영이면 비만료 Secret 기반 reviewer 토큰 필요(vault-bootstrap.md 2026-07-04 인시던트).
```

- [ ] **Step 2: stale self-signed vault-tls 스텝 삭제(있으면)**

Run(먼저 존재 확인):
```bash
grep -nE "self-signed|openssl|kubectl create secret tls vault-tls" docs/RUNBOOK/dr-eks-bootstrap.md
```
매치가 있으면 해당 "수동 self-signed vault-tls 생성" 문단을 Edit 로 삭제하고, 대신 한 줄 주석 추가:
`> vault-tls 는 cert-manager(cledyu-ca)가 certificate.yaml 로 자동 발급(수동 생성 불요).`
매치가 없으면(원 런북에 해당 스텝이 없던 경우) Step 2 는 skip.

- [ ] **Step 3: 문서 검증**

Run:
```bash
grep -E 'auth/kubernetes/config|token_reviewer_jwt|cledyu-api-oidc|kubernetes_ca_cert' docs/RUNBOOK/dr-eks-bootstrap.md
```
Expected: 4개 문자열 모두 매치.

- [ ] **Step 4: Commit**
```bash
git add docs/RUNBOOK/dr-eks-bootstrap.md
git commit -m "docs(dr): 런북 Vault k8s auth EKS 재설정 스텝 + cert-manager vault-tls 정정 (T6)"
```

---

## Self-Review (완료)

- **스펙 커버리지:** Part 1(Task 1)·Part 2(Task 2)·Part 3(Task 3) 전부 매핑. 성공기준(api Running)·범위밖(DB=T7·Keycloak=T8) 반영.
- **플레이스홀더:** `<<VAULT_UNSEAL_ROLE_ARN>>`은 의도적 런타임 치환값(런북 명시). 코드 플레이스홀더 아님.
- **타입/이름 정합:** SA `vault`(IRSA `vault:vault`), store `vault-backend`·`eso-controller`·`external-secrets-operator`, secret `cledyu-api-oidc`(ns api), KMS 키 verbatim — 스펙·terraform과 일치.
- **커밋 정책:** 단일 -m·Co-Authored-By 없음·Edit 수술 준수. (실행 시 커밋은 사용자 — 서브에이전트 no-commit.)
