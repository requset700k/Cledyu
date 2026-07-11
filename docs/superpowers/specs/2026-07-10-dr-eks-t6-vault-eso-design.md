# DR Plan B — T6: Vault DR 오버레이 + ESO 배선 설계

> 상위 플랜: `docs/superpowers/plans/2026-07-09-dr-eks-overlay-plan-b.md` (Task 6)
> 선행: T1~T4(terraform), T5 완결(cert-manager·PKI·trust-manager Bundle·apps-eks)
> 브랜치: `feat/dr-eks-overlay`
> 작성: 2026-07-10

## 배경 / 목표

F8에서 확인: api의 `CLEDYU_KEYCLOAK_CLIENT_SECRET`(비-optional)은 `cledyu-api-oidc` 시크릿에서 오고,
그 시크릿은 **Vault → vault-backend ClusterSecretStore → ES → cledyu-api-oidc** 사슬로 만들어진다.
기존 Plan B Task 6은 **Vault 배포만** 다뤄 이 사슬의 ESO 배선을 빠뜨렸다. 이 스펙은 사슬 전체를 닫는다.

**목표:** 복원된 Vault가 IRSA로 auto-unseal되고, ESO가 그 Vault에 인증해 `cledyu-api-oidc`를 만들어
**api의 `CreateContainerConfigError`를 해소(→ api 파드 Running)** 한다.

## 런타임 검증된 사슬 (실측 근거)

```
vault 파드(SA=vault + IRSA 애노) → EKS pod-identity webhook이 web identity 주입
  → STS(T4 VPC 인터페이스 엔드포인트) AssumeRoleWithWebIdentity → 임시 크레드
  → seal "awskms" → KMS(T4 VPC 엔드포인트) Decrypt (T3 IRSA 정책이 e29e3ec2 허용)
  → 복원된 keyring unseal
vault-tls(cert-manager cledyu-ca 발급) → Vault TLS 리스너 (values.yaml이 볼륨 마운트)
ESO(SA=eso-controller) → k8s auth(EKS용 재설정) → Vault role external-secrets-operator
  → read cledyu/oidc/cledyu-web → cledyu-api-oidc(ns api) 생성 → api 기동
```

**실측 확정 사실:**
- **T3 IRSA**(`eks-dr-irsa.tf`): `kms:Encrypt/Decrypt/DescribeKey` on `alias/cledyu-vault-unseal`(=`arn:aws:kms:ap-northeast-2:504284203153:key/e29e3ec2-f5e0-4308-af6f-5b576cc99f52`), SA 바인딩 `vault:vault`. → awskms unseal에 충분.
- **T4 엔드포인트**: KMS·STS 인터페이스 엔드포인트가 private 클러스터의 unseal 경로를 NAT 없이 서빙.
- **vault-tls 볼륨/마운트는 `values.yaml`(base, DR 로드)에 존재** — DR로 자동 상속.
- **정적 AWS 크레드는 `values-awskms.yaml`에만 있고 DR은 이를 로드 안 함** → IRSA만 사용(env 덮어쓰기 없음).
- **ESO 컨트롤러 SA = `eso-controller`**(`external-secrets/values.yaml`, T5 앱이 로드) → store `serviceAccountRef`와 일치.
- **vault 차트 기본값** `authDelegator.enabled=true`(system:auth-delegator)·SA=`vault` → k8s auth TokenReview 동작.
- **ES `cledyu-web-oidc-client-secret`**: `namespace: api`, `SkipDryRunOnMissingResource`, target `cledyu-api-oidc`.
- **storageClass 키 경로 일치**: base `server.dataStorage.storageClass`·`server.auditStorage.storageClass: longhorn` →
  values-eks 오버라이드 키가 정확히 동일 → gp3로 교체(PVC Pending 위험 없음). `injector.enabled: false`(base).
- **PSA**: vault ns `enforce: restricted`, 파드 securityContext(runAsNonRoot·runAsUser 100·seccompProfile·
  allowPrivilegeEscalation false·capabilities drop) → restricted 준수 → EKS admission 통과. `disable_mlock=true`와 정합.
- **vault-tls SAN**: 인증서 dnsNames에 `vault-active.vault.svc` 포함 → ESO가 store의 `vault-active.vault.svc:8200`에
  TLS 접속 시 SAN 검증 통과. `ha.enabled=true`(replicas 1) 유지라 `vault-active` 서비스 생성됨.

## Part 1 — Vault 배포

### `gitops/apps/vault/values-eks.yaml` (신규)

base `values.yaml` 위 델타. **raft config 문자열은 base(멀티노드)를 통째로 대체**하므로 단일노드 버전을
자기완결로 쓴다(listener+tls + storage + **seal awskms** + service_registration 포함, retry_join 제거).

```yaml
---
# EKS DR 오버레이 — 단일노드 raft + IRSA awskms unseal + gp3. 정적 크레드 env 없음.
server:
  serviceAccount:
    # IRSA: SA(vault)에 role-arn. extraSecretEnvironmentVars(정적 크레드)는 넣지 않는다.
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

**못 박은 점:**
- `serviceAccount`는 **annotations만** 추가(name/create 미지정 → 차트 기본 SA `vault` 유지 → IRSA `vault:vault` 매칭).
- raft config에 **tls_cert_file 3줄 + seal awskms(KMS 키 verbatim) + service_registration 필수**(빠지면 TLS/unseal/등록 깨짐).
- `<<VAULT_UNSEAL_ROLE_ARN>>`은 부트스트랩(런북)에서 `terraform output eks_dr_vault_unseal_role_arn`으로 치환(계정 고정값).

### `gitops/argocd/apps-eks/platform-vault.yaml` (신규)

온프렘 `gitops/argocd/apps/platform-vault.yaml` 미러, **2곳 변경**:

```yaml
---
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
          - $values/gitops/apps/vault/values-eks.yaml   # ← values-awskms 대신 (정적 크레드 배제)
    - repoURL: https://github.com/requset700k/Cledyu.git
      targetRevision: feat/dr-eks-overlay   # 드릴 검증 후 main
      ref: values
    - repoURL: https://github.com/requset700k/Cledyu.git
      targetRevision: feat/dr-eks-overlay
      path: gitops/apps/vault
      directory:
        include: "{namespace.yaml,admin-serviceaccount.yaml,certificate.yaml}"   # ← ingress/serverstransport 제외(Traefik, B5)
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

**변경점:** (1) valueFiles `[values.yaml, values-eks.yaml]`(온프렘의 values-awskms 제외), (2) directory include에서
`ingress.yaml`·`serverstransport.yaml` 제외(Traefik CRD 없음 — Vault는 DR서 내부 접근/port-forward). `certificate.yaml`은
유지 → cert-manager(cledyu-ca)가 vault-tls 발급.

## Part 2 — ESO 배선 (최소: store + api-oidc)

### `gitops/argocd/apps-eks/data-eso-store.yaml` (신규)

PKI처럼 온프렘 매니페스트 재사용(directory include, 드리프트 0).

```yaml
---
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

**못 박은 점:**
- 재사용 매니페스트 **무변경**: `vault-backend` store(server `https://vault-active.vault.svc:8200`, k8s auth role
  `external-secrets-operator`, serviceAccountRef `eso-controller/external-secrets`, caProvider `vault-tls`/vault ns) — 이름이 EKS서도 동일.
- ES(`cledyu-web-oidc-client-secret`)는 `metadata.namespace: api`라 ClusterSecretStore(cluster-scoped)와 무관하게 **api ns에 생성**.
- ES가 api ns를 대상하므로 **api ns 선행 필요**(service-api가 생성). `SkipDryRunOnMissingResource` + retry(10)로 흡수 —
  api ns 생성 전 sync 시도는 실패→재시도→api ns 생성 후 성공.

## Part 3 — 런북 (명령형 게이트)

기존 Vault 복원 절차(`docs/RUNBOOK/dr-eks-bootstrap.md`, bastion에서 exec, `-force` + 원본 토큰)에 **k8s auth 재설정 스텝을 추가**하고, **stale한 "수동 self-signed vault-tls" 스텝을 삭제**(cert-manager가 발급).

### 추가: k8s auth 재설정 (복원·unseal 후, ESO 인증 직전)

온프렘 config는 `kubernetes_host=https://kubernetes.default.svc:443`(무관 URL)이지만 스냅샷에 저장된
`kubernetes_ca_cert`·`token_reviewer_jwt`가 **온프렘 값**이라 EKS API 검증 실패. **vault 파드 안에서 재실행**하면
`@`경로가 EKS 파드의 SA CA·토큰을 읽어 교정된다(role `external-secrets-operator`는 스냅샷에 이미 존재 — 재설정 불요).

```bash
# bastion, VAULT_TOKEN=복원된 스냅샷의 root/admin 토큰
kubectl -n vault exec vault-0 -- sh -c '
  export VAULT_TOKEN='"$VAULT_TOKEN"'
  vault write auth/kubernetes/config \
    kubernetes_host=https://kubernetes.default.svc:443 \
    kubernetes_ca_cert=@/var/run/secrets/kubernetes.io/serviceaccount/ca.crt \
    token_reviewer_jwt=@/var/run/secrets/kubernetes.io/serviceaccount/token'

# 검증: ESO가 store 인증 → 시크릿 생성 확인
kubectl -n api get externalsecret cledyu-web-oidc-client-secret   # STATUS SecretSynced
kubectl -n api get secret cledyu-api-oidc                          # 생성 확인
```

**못 박은 점:**
- `token_reviewer_jwt`는 vault 파드의 projected SA 토큰(Vault에 **저장된 값은 재설정 시점 스냅샷**이라 ~1h 후 만료).
  **드릴엔 무해**: 재설정 직후(초 단위) ESO가 1회 login→TokenReview(이때 reviewer 유효)→`cledyu-api-oidc` 생성,
  `creationPolicy: Owner`+`deletionPolicy: Retain`이라 이후 reviewer 만료로 재-sync가 막혀도 **시크릿은 잔존**→api 무관.
  (지속 재-sync가 필요한 장기 운영이면 온프렘 2026-07-04 인시던트처럼 비만료 Secret 기반 reviewer 토큰 — 현 범위 밖.)
- vault SA의 `system:auth-delegator`(TokenReview 권한)는 차트 기본값(`authDelegator.enabled=true`)이 생성 → 별도 조치 불요.
- 삭제: 기존 계획의 "openssl로 self-signed vault-tls 수동 생성" 스텝 — cert-manager(cledyu-ca)가 certificate.yaml로 자동 발급하므로 obsolete.

## 성공 기준

Vault 복원+unseal → k8s auth 재설정 → ESO가 `cledyu-api-oidc` 생성 → **api의 `CreateContainerConfigError` 해소 →
api 파드 Running**(container started). api **Ready**·실데이터 서빙은 Keycloak(T8)·DB(T7) 필요 — 범위 밖.

## 범위 밖

- **DB DSN(`cledyu-api-db`)**: 온프렘 DB를 가리키는 클러스터 종속 값 → DR CNPG로 재배선은 **T7**.
- **Keycloak**(T8), **CNPG 복원**(T7), **검증 드릴**(T10).
- ArgoCD/admin/validation/aws 계열 ES — 과금경로 최소에 불필요.

## 검증 (오프라인)

- `helm template vault gitops/apps/vault -f gitops/apps/vault/values.yaml -f gitops/apps/vault/values-eks.yaml`:
  `role-arn` 애노 존재, `storageClass: gp3`(data+audit), `seal "awskms"` 렌더, `tls_cert_file` 존재,
  **`AWS_ACCESS_KEY_ID` env 미존재**, retry_join 미존재(단일노드).
- Application 매니페스트 yaml 유효성 + directory include에 ingress/serverstransport 미포함 확인.
- 재사용 매니페스트(store/ES)는 무변경이라 별도 검증 불요.
- 실 AWS apply/unseal은 T10 드릴(사용자 직접).
