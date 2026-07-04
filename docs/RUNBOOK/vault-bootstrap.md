# Vault 초기 부트스트랩 런북

## 작업 범위

이 문서는 Cledyu 클러스터에 Vault를 처음 배포한 뒤 초기화, 루트 토큰 발급, 수동 언실까지 진행하는 절차를 정리함.

이번 단계는 Shamir 방식의 수동 언실을 사용함. GCP Cloud KMS 기반 auto-unseal, Kubernetes Auth, External Secrets Operator, 기존 시크릿 이관은 후속 작업으로 진행함.

## 선행 조건

- `platform-vault` ArgoCD Application 또는 Helm 배포가 적용되어 있어야 함.
- `vault` namespace가 존재해야 함.
- `vault-tls` Certificate가 `Ready=True` 상태여야 함.
- Vault Pod 3개가 Running 상태여야 함.

확인 명령어:

```bash
kubectl -n vault get pods
kubectl -n vault get pvc
kubectl -n vault get certificate vault-tls
kubectl -n vault get ingress vault
```

## 초기 상태 확인

초기화 전 Vault 상태 확인:

```bash
kubectl -n vault exec vault-0 -- env VAULT_SKIP_VERIFY=true vault status
```

초기화 전 기대 상태:

```text
Initialized: false
Sealed: true
```

## Vault 초기화

초기화는 `vault-0`에서 한 번만 실행함.

```bash
kubectl -n vault exec vault-0 -- \
  env VAULT_SKIP_VERIFY=true \
  vault operator init \
    -key-shares=5 \
    -key-threshold=3 \
    -format=json
```

명령 실행 결과에는 unseal key 5개와 root token이 포함됨. 이 값은 절대 GitHub, Discord, 공개 Notion, PR 코멘트, 쉘 히스토리에 남기지 않음.

권장 GCP Secret Manager 저장 항목:

```text
Secret: cledyu-vault-bootstrap
Vault URL: https://vault.cledyu.local
Unseal Key 1: <redacted>
Unseal Key 2: <redacted>
Unseal Key 3: <redacted>
Unseal Key 4: <redacted>
Unseal Key 5: <redacted>
Root Token: <redacted>
Key Shares: 5
Key Threshold: 3
Created by: 윤승호
Access: 김용균, 윤승호
```

## 수동 언실

각 Vault Pod에 unseal key 5개 중 3개를 입력함. 같은 key 3개 조합을 모든 Pod에 사용할 수 있음.

```bash
kubectl -n vault exec -it vault-0 -- env VAULT_SKIP_VERIFY=true vault operator unseal
kubectl -n vault exec -it vault-0 -- env VAULT_SKIP_VERIFY=true vault operator unseal
kubectl -n vault exec -it vault-0 -- env VAULT_SKIP_VERIFY=true vault operator unseal

kubectl -n vault exec -it vault-1 -- env VAULT_SKIP_VERIFY=true vault operator unseal
kubectl -n vault exec -it vault-1 -- env VAULT_SKIP_VERIFY=true vault operator unseal
kubectl -n vault exec -it vault-1 -- env VAULT_SKIP_VERIFY=true vault operator unseal

kubectl -n vault exec -it vault-2 -- env VAULT_SKIP_VERIFY=true vault operator unseal
kubectl -n vault exec -it vault-2 -- env VAULT_SKIP_VERIFY=true vault operator unseal
kubectl -n vault exec -it vault-2 -- env VAULT_SKIP_VERIFY=true vault operator unseal
```

## 상태 검증

각 Pod 상태 확인:

```bash
kubectl -n vault exec vault-0 -- env VAULT_SKIP_VERIFY=true vault status
kubectl -n vault exec vault-1 -- env VAULT_SKIP_VERIFY=true vault status
kubectl -n vault exec vault-2 -- env VAULT_SKIP_VERIFY=true vault status
```

기대 상태:

```text
Initialized: true
Sealed: false
Storage Type: raft
HA Enabled: true
HA Mode: active 또는 standby
```

외부 경로 확인:

```bash
curl --cacert infra/kubernetes/kubeconfig/cledyu-root-ca.pem \
  https://vault.cledyu.local/v1/sys/health
```

Windows에서 인증서 폐기 확인 문제로 실패하면 아래처럼 확인할 수 있음.

```powershell
curl.exe --ssl-no-revoke `
  --resolve vault.cledyu.local:443:10.10.0.101 `
  -i https://vault.cledyu.local/v1/sys/health
```

초기화와 언실이 완료된 active node 기대 응답:

```text
HTTP/1.1 200 OK
initialized: true
sealed: false
standby: false
```

## 후속 작업

### 완료됨

- Kubernetes Auth backend 활성화.
- `cledyu/` KV v2 secrets engine 생성.
- Vault policy 5종 생성.
- Kubernetes ServiceAccount role mapping 4종 생성.
- Vault OIDC auth backend 활성화.
- `team-platform` 그룹 기반 `cledyu-operator` policy 매핑.
- `team-security` 그룹 기반 `cledyu-operator`, `cledyu-admin` policy 매핑.
- 초기 시크릿 이관.
- Keycloak admin credential 이관.
- Keycloak Postgres credential 이관.
- ArgoCD OIDC client secret 이관.
- `web`, `api`, `tutor` OIDC client metadata 이관.
- Vault OIDC client secret 이관.
- `grafana` OIDC client는 secret 미생성 상태라 pending metadata로 기록.
- File audit device 활성화.
- Vault OIDC admin 로그인 및 `cledyu/*` smoke read/write/delete 검증.
- 기존 root token revoke 완료. 평시 운영은 Keycloak OIDC + `cledyu-operator`/`cledyu-admin` policy 사용.
- `vault-admin` Kubernetes auth break-glass role 추가. root token 없이 `vault-admin` ServiceAccount 토큰으로 단기 admin token 발급 가능.

### 남음

- recovery key GCP Secret Manager 최종 등록 확인. 로컬 bootstrap 기준 recovery key 5개, threshold 3 확인 완료.
- 기존 root token 보관 항목은 GCP Secret Manager에서 revoked / break-glass 상태로 정리.
- GCP Cloud KMS auto-unseal 구성.
- 수동 언실 의존성 제거.
- Grafana OIDC client secret 생성 후 Vault 값 갱신.
- Keycloak `vault` OIDC client Terraform import 필요. 현재 로컬 `infra/terraform/keycloak`에는 state가 없어 import 미수행. 운영 state 위치와 Keycloak client UUID 확인 후 `terraform import 'keycloak_openid_client.clients["vault"]' 'cledyu/<uuid>'` 실행.
- Google AI API key 이관.
- Strimzi 준비 후 audit log를 `security-logs` 파이프라인으로 연동.

## Kubernetes Auth / Secret Migration

아래 스크립트는 root token을 로컬 bootstrap JSON에서 읽고, 값을 화면에 출력하지 않은 채 Vault에 초기 설정을 적용함.

```powershell
PowerShell -ExecutionPolicy Bypass -File scripts/vault-bootstrap-configure.ps1
```

Vault OIDC backend까지 구성하려면 Keycloak `vault` confidential client secret을 안전한 채널로 주입함.
스크립트는 `cert-manager/cledyu-root-ca`의 공개 인증서를 읽어 `oidc_discovery_ca_pem`에 함께 등록함.
이는 Vault Pod가 `https://keycloak.cledyu.local` discovery endpoint의 내부 CA 서명 인증서를 검증하기 위한 설정임.

```powershell
$env:VAULT_OIDC_CLIENT_SECRET = "<secure secret value>"
PowerShell -ExecutionPolicy Bypass -File scripts/vault-bootstrap-configure.ps1
Remove-Item Env:\VAULT_OIDC_CLIENT_SECRET
```

적용 항목:

```text
Secrets engine:
- cledyu/                 kv-v2

Auth backend:
- kubernetes/
- oidc/

Audit device:
- file/                    /vault/audit/audit.log

Policies:
- cledyu-argocd
- cledyu-grafana
- cledyu-keycloak-admin
- cledyu-keycloak-db
- cledyu-admin
- cledyu-operator
- cledyu-service-oidc
- vault-admin

Kubernetes auth roles:
- cledyu-argocd           argocd/argocd-server
- cledyu-grafana          monitoring/grafana
- cledyu-keycloak         keycloak/cledyu-keycloak
- cledyu-services         web/api/tutor service accounts
- vault-admin             vault/vault-admin -> vault-admin

OIDC auth roles:
- cledyu-operator         team-platform -> cledyu-operator
- cledyu-admin            team-security -> cledyu-operator, cledyu-admin

Migrated paths:
- cledyu/keycloak/admin
- cledyu/keycloak/postgres
- cledyu/oidc/argocd
- cledyu/oidc/web
- cledyu/oidc/api
- cledyu/oidc/tutor
- cledyu/oidc/vault
- cledyu/oidc/grafana
```

검증 명령어:

```bash
vault secrets list
vault auth list
vault audit list
vault policy list
vault list auth/kubernetes/role
vault read auth/oidc/role/cledyu-operator
vault read auth/oidc/role/cledyu-admin
vault kv metadata get cledyu/keycloak/admin
vault kv metadata get cledyu/keycloak/postgres
vault kv metadata get cledyu/oidc/argocd
```

## Vault OIDC Admin Login

운영자는 root token을 평시 운영에 사용하지 않고 Keycloak 계정으로 Vault에 로그인함.

구성 기준:

```text
Auth backend: oidc/
OIDC discovery URL: https://keycloak.cledyu.local/realms/cledyu
Client ID: vault
Operator role: cledyu-operator
Operator group: team-platform
Operator policy: cledyu-operator
Admin role: cledyu-admin
Admin group: team-security
Admin policies: cledyu-operator, cledyu-admin
CLI callback: http://localhost:8250/oidc/callback
UI callback: https://vault.cledyu.local/ui/vault/auth/oidc/oidc/callback
```

CLI 로그인:

```bash
export VAULT_ADDR=https://vault.cledyu.local

# team-platform
vault login -method=oidc role=cledyu-operator

# team-security
vault login -method=oidc role=cledyu-admin
```

검증:

```bash
vault kv put cledyu/smoke/oidc-admin checked_by=yunseungho
vault kv get cledyu/smoke/oidc-admin
vault kv delete cledyu/smoke/oidc-admin
```

기대 결과:

```text
team-platform 사용자는 cledyu/* secret read/write 가능
team-security 사용자는 cledyu/* secret read/write와 Vault control-plane 변경 가능
그 외 그룹 사용자는 cledyu-operator/cledyu-admin role 로그인 실패 또는 policy 미부여
```

## Kubernetes Auth Admin Break-Glass

Keycloak OIDC admin 로그인이 불가능하지만 Kubernetes API 접근은 가능한 경우, `vault-admin` ServiceAccount 토큰으로 Vault admin token을 발급함.
이 경로는 root token을 다시 보관하지 않기 위한 보조 운영 경로이며, 사용 시 감사 로그 확인과 작업 후 토큰 폐기를 전제로 함.

구성 기준:

```text
Kubernetes ServiceAccount: vault/vault-admin
Vault auth role: auth/kubernetes/role/vault-admin
Mapped policy: vault-admin
TTL: 30m
Max TTL: 1h
```

PowerShell:

```powershell
$saJwt = kubectl -n vault create token vault-admin --duration=30m
vault write auth/kubernetes/login role=vault-admin jwt="$saJwt"
```

Linux / macOS:

```bash
SA_JWT="$(kubectl -n vault create token vault-admin --duration=30m)"
vault write auth/kubernetes/login role=vault-admin jwt="$SA_JWT"
```

응답의 `token`은 root token이 아니라 `vault-admin` policy가 붙은 단기 client token임.
작업 후 현재 token을 즉시 폐기함.

```bash
vault token revoke -self
```

운영 원칙:

```text
평시 1순위: Keycloak OIDC + cledyu-operator/cledyu-admin policy
비상 2순위: Kubernetes vault-admin ServiceAccount token + vault-admin policy
최후 3순위: recovery key threshold 3으로 generate-root 후 즉시 revoke
```

> **중요 실패 모드(2026-07-04 실증)**: 1순위·2순위가 **동시에** 막힐 수 있다.
> - 2순위(k8s `vault-admin` 로그인)는 **Kubernetes auth 자체가 고장나면 같이 죽는다** — 예:
>   `token_reviewer_jwt` 만료로 `auth/kubernetes/login` 이 전역 403 이면 vault-admin 도 403.
> - 1순위(OIDC)는 실서버 OIDC role 이 `cledyu-platform` 하나뿐으로 드리프트되어 있어
>   `cledyu-admin` role 로그인이 "role not found" 로 실패한다(런북의 cledyu-operator/
>   cledyu-admin 명칭과 실제가 불일치 — 아래 인시던트 참고).
>
> 이 조합이면 **3순위 generate-root 만 남는다**. 즉 recovery key 접근성이 진짜 최후 보루다.

## Kubernetes Auth token_reviewer_jwt 만료 인시던트 (2026-07-04)

**증상**: ExternalSecrets Operator 의 `ClusterSecretStore vault-backend` 가 "unable to create
client", 18개 ExternalSecret 전부 `SecretSyncedError`. 앱은 캐시된 k8s Secret 으로 계속 가동
(즉시 장애 아님). ESO 로그: `PUT auth/kubernetes/login` → **403 permission denied**.

**근본원인**: `auth/kubernetes/config` 의 `token_reviewer_jwt` 가 짧은 수명 projected 토큰으로
설정돼 **만료**. Vault 가 이 JWT 로 Kubernetes TokenReview API 를 호출하는데 만료되어 실패 →
**모든 k8s auth 로그인이 전역 403**. Vault 파드 재시작으로 안 고쳐진다(런타임이 아니라 저장된
config 문제). ExternalSecret refresh 주기(1h)라 실제 auth 붕괴 시점보다 최대 1h 늦게 표면화되어
직전의 파드 재시작/seal 마이그레이션과 혼동하기 쉽다 — **store `lastTransitionTime` 과 파드
재시작 시각을 대조**해 인과를 분리할 것.

**진단(비밀 아님)**:
```bash
vault read auth/kubernetes/config
# token_reviewer_jwt_set = true 이면 명시적 reviewer JWT 사용 중(만료 후보)
```

**수정(정석 — 비만료 reviewer 토큰)**: vault SA(`vault`, 이미 `system:auth-delegator` 보유)용
**Secret 기반 비만료 토큰**을 만들어 `token_reviewer_jwt` 로 설정한다. projected 토큰과 달리
`kubernetes.io/service-account-token` Secret 의 토큰은 만료되지 않는다.
```bash
kubectl -n vault apply -f - <<'EOF'
apiVersion: v1
kind: Secret
metadata:
  name: vault-k8s-auth-reviewer
  namespace: vault
  annotations:
    kubernetes.io/service-account.name: vault
type: kubernetes.io/service-account-token
EOF
# 토큰이 채워지길 기다린 뒤(값 미출력):
REVJWT="$(kubectl -n vault get secret vault-k8s-auth-reviewer -o jsonpath='{.data.token}' | base64 -d)"
vault write auth/kubernetes/config \
  kubernetes_host="https://kubernetes.default.svc:443" \
  disable_iss_validation=true \
  token_reviewer_jwt="$REVJWT"
```
적용 즉시 `ClusterSecretStore vault-backend` Ready=True, 18/18 SecretSynced 회복(ESO 컨트롤러
재시작으로 즉시 재검증 가능). 이 변경은 GitOps 미관리(수동 config) — Secret `vault-k8s-auth-reviewer`
는 vault ns 의 라이브 리소스로 유지된다(ArgoCD platform-vault include 목록 밖이라 prune 되지 않음).

**후속(OIDC break-glass 복원)**: `auth/oidc/role` 에 `cledyu-platform` 만 존재하고
`cledyu-admin` 이 없어 이번에 OIDC admin 진입이 불가했다. 향후 대비로 `cledyu-platform` 구조를
미러링해 `cledyu-admin` OIDC role(정책 `cledyu-admin`, team-security 그룹)을 재생성 권장.

## Root Token Break-Glass 전환

2026-05-11 기준 기존 root token은 revoke 완료됨. 이후 평시 Vault 운영은 Keycloak OIDC 로그인과
`cledyu-operator` 또는 `cledyu-admin` policy로 수행함.

완료된 전환 조건:

```text
1. team-platform 사용자의 cledyu-operator OIDC 로그인 성공
2. team-security 사용자의 cledyu-admin OIDC 로그인 성공
3. cledyu-operator policy로 secret read/write/delete 테스트 성공
4. cledyu-admin policy로 Vault control-plane read/write 테스트 성공
5. recovery key 5개 / threshold 3 확인
6. 기존 root token revoke 완료
```

폐기 검증 명령:

```bash
vault token revoke <root-token>
vault token lookup
# revoked token이면 lookup 실패가 정상
```

GCP Secret Manager 항목 정리:

```text
Secret: cledyu-vault-root-token
Labels: archive, break-glass, revoked
Status: revoked / archived
Note:
  - 기존 root token은 2026-05-11 revoke 완료
  - 평시 운영 금지
  - 평시 Vault 운영은 Keycloak OIDC + cledyu-operator/cledyu-admin policy 사용
  - Kubernetes API 접근 가능 시 vault-admin ServiceAccount 기반 단기 admin token 사용
  - 비상 시 recovery key 5개 중 3개를 사용해 generate-root 절차로 새 root token 재발급
  - root 권한 작업은 김용균, 윤승호 승인 후 진행
Access: 김용균, 윤승호
```

## Generate-Root 비상 재발급 절차

아래 절차는 OIDC admin 경로가 동작하지 않거나 Vault root 권한이 반드시 필요한 비상 상황에서만 사용함.
recovery key 값과 새 root token은 Discord, PR, 공개 Notion, 쉘 히스토리에 남기지 않음.

전제 조건:

```text
Recovery key shares: 5
Recovery key threshold: 3
필요 승인: 김용균, 윤승호
```

절차:

```bash
# 1. Vault endpoint 설정
export VAULT_ADDR=https://vault.cledyu.local

# 2. generate-root 세션 시작
vault operator generate-root -init -format=json

# 출력값에서 nonce, otp를 안전한 임시 메모장에만 보관
# 예시 필드: nonce, otp, started, progress, required

# 3. recovery key 보유자 3명이 순차 승인
vault operator generate-root -nonce=<nonce> <recovery-key-1>
vault operator generate-root -nonce=<nonce> <recovery-key-2>
vault operator generate-root -nonce=<nonce> <recovery-key-3>

# 4. threshold 충족 후 반환된 encoded_token을 otp로 decode
vault operator generate-root -decode=<encoded_token> -otp=<otp>

# 5. 새 root token으로 비상 작업 수행
# 6. 비상 작업 종료 즉시 새 root token revoke
vault token revoke <new-root-token>
```

운영 원칙:

```text
새 root token은 장기 보관하지 않음.
비상 작업 종료 후 즉시 revoke.
GCP Secret Manager에는 root token 원문이 아니라 generate-root 절차와 recovery key 보관 위치만 유지.
```

## Audit Log 위치와 보존 정책

Vault file audit device는 다음 위치에 기록함.

```text
Audit device: file/
File path: /vault/audit/audit.log
Storage: Vault auditStorage PVC
Size: 5Gi
```

운영 확인 명령:

```bash
vault audit list
vault token lookup
tail -n 5 /vault/audit/audit.log
```

보존 정책:

- 감사 로그는 Vault `auditStorage` PVC에 보존함.
- `vault token lookup`, secret read/write, auth backend 호출 등 Vault API 요청이 HMAC 처리된 형태로 기록됨.
- 원본 audit log에는 민감한 접근 패턴이 포함될 수 있으므로 GitHub, Discord, 공개 Notion에 원문 공유 금지.
- 단일 파일 무한 append로 PVC가 가득 차면 Vault 요청 처리가 중단될 수 있으므로 운영 전 logrotate 또는 sidecar 기반 로테이션을 적용함.
- 장기 보존은 Strimzi Kafka `security-logs` 토픽과 S3 Glacier 파이프라인이 준비된 뒤 연동함.
- 장기 파이프라인 전까지는 `vault-0`의 `/vault/audit/audit.log`를 1차 포렌식 근거로 사용함.

로테이션 예시:

```text
/vault/audit/audit.log {
    rotate 7
    daily
    compress
    missingok
    postrotate
        kill -HUP $(pidof vault)
    endpostrotate
}
```

## GCP KMS Auto-Unseal 전환

> **이력(2026-07-04 기준 supersede)**: 아래는 최초 gcpckms 도입 절차의 기록이다. 이후 DR 을
> AWS 기반으로 정하면서 auto-unseal 을 **gcpckms → awskms 로 마이그레이션 완료**했다(3노드
> awskms 단독 auto-unseal). 현재 seal 절차·롤백·사후정리는
> `docs/RUNBOOK/vault-seal-migration-awskms.md` 를 따른다.

현재 PR에서는 `values-gcpckms.example.yaml`만 추가함. 실제 전환은 GCP KMS 키 정보와 권한이 준비된 뒤 진행함.

전환 전 필요한 값:

```text
GCP project id
KMS region
KMS key ring
KMS crypto key
Vault Pod가 사용할 GCP credential 또는 Workload Identity
```

전환 순서:

```text
1. GCP KMS key 생성
2. Vault Pod 권한 부여
3. vault-gcp-kms Secret 또는 Workload Identity 구성
4. values.yaml에 seal "gcpckms" 블록 반영
5. Helm/ArgoCD sync
6. Vault 재시작 후 sealed=false 확인
7. 수동 unseal 의존성 제거
```
