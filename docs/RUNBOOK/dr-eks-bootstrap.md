# EKS DR 부트스트랩 런북 (Plan B — pilot-light)

> 온프렘 상실 시 EKS 에서 백업으로 과금최소경로(계정·수료·진도)를 재현하는 수동 부트스트랩 절차.
> **pilot-light**: 컨트롤플레인·VPC·IRSA·노드그룹(desired 0)은 Phase 0 로 상시 warm 유지(과금 ~$73/mo,
> 컨트롤플레인뿐). 앱은 seed 하지 않는다(node=0 에선 ArgoCD 가 뜰 컴퓨트가 없어 불가) — 매 failover(Phase 1)가
> hot 리소스·노드를 올린 뒤 기존 부트스트랩 본체로 앱을 처음부터 sync 한다.
> ⚠️ **실행 순서는 아래 체크리스트 기준** — 이 문서의 섹션 배치(Vault 복원이 앞에 옴)와 다르다. 실제 순서(재해 시,
> Phase 0 warm 은 사전 상시 유지 전제):
> ① Phase 1 terraform apply(hot)+노드 스케일(CLI) → ② **apps-eks 부트스트랩(Vault 를 여기서 배포)** → ③ Vault 스냅샷 복원 →
> ④ Vault k8s auth 재설정 → ⑤ CNPG 복원(재-failover 가드 포함) → ⑥ api/web Ready → ⑦ 공개 DNS 전환 → ⑧ api/web restart →
> ⑨ 검증 → ⑩ destroy(정상 복귀=failback[warm 유지] / destroy=완전 폐기). 드릴(T10)은 이 순서를 처음부터 끝까지 한 번 완주하는 것.

---

### Phase 0 — warm 스택 셋업 (최초 1회, seed 없음)

> **설계(B):** node=0 에선 ArgoCD 가 뜰 컴퓨트가 없어 "etcd seed"는 불가능하다(root-app 객체만 써질 뿐 자식 앱·
> 워크로드는 생성 안 됨). 그래서 **앱을 seed 하지 않는다.** warm = 컨트롤플레인·VPC·IRSA·SG·bastion 롤·노드그룹
> (desired 0) 뿐. 앱·bastion 인스턴스·NAT 는 Phase 1(재해)에서만 생긴다. 매 failover 가 기존 부트스트랩 본체
> (ArgoCD 설치→root-app→Vault→CNPG→DNS)로 앱을 처음부터 sync 한다. pilot-light 이득은 컨트롤플레인 상시
> warm(프로비저닝 ~10-15분 제거)뿐 — 2회차+ failover 는 warm etcd 에 남은 CR 로 더 빠를 수 있으나 부수효과일
> 뿐 절차는 매번 동일·멱등이다.

컨트롤플레인·VPC·IRSA·노드그룹(desired 0)만 상시 존재시킨다. NAT·엔드포인트·bastion 인스턴스·노드·앱은 이 단계서 만들지 않는다.

```bash
cd infra/terraform/aws && terraform init -reconfigure -input=false
terraform apply -var enable_eks_dr=true -var eks_dr_active=false -var eks_dr_node_desired=0 \
  -target=module.eks_dr_vpc -target=module.eks_dr -target=module.eks_dr_ebs_csi_irsa \
  -target=module.eks_dr_alb_irsa -target=aws_security_group.eks_dr_endpoints \
  -target=aws_security_group.eks_dr_bastion -target=module.eks_dr_endpoints \
  -target=module.eks_dr_vault_unseal_irsa -target=aws_iam_policy.eks_dr_vault_unseal \
  -target=aws_iam_policy.eks_dr_cnpg_restore -target=module.eks_dr_cnpg_restore_irsa \
  -target=aws_iam_role.eks_dr_bastion -target=aws_iam_role_policy_attachment.eks_dr_bastion_ssm \
  -target=aws_iam_role_policy.eks_dr_bastion_describe -target=aws_iam_role_policy.eks_dr_bastion_vault_restore \
  -target=aws_iam_instance_profile.eks_dr_bastion -target=aws_instance.eks_dr_bastion
#   → EKS 컨트롤플레인·VPC·IRSA·SG·bastion 롤/프로필/정책·노드그룹(0) 생성. hot(NAT·엔드포인트·bastion 인스턴스)·노드 없음.
#   → 평시 과금 = 컨트롤플레인만 ~$73/mo. 이 상태로 상시 유지(재해 대기). 검증: aws eks describe-cluster --name cledyu-dr → ACTIVE.
```

---

## 복원 방식 대비 — 헷갈리지 말 것

DR 대상 상태 저장소는 복원 방식이 **다르다**. 드릴 중 이 차이를 혼동하면 "왜 Vault 는
자동으로 안 채워지지?" 로 시간을 버린다.

| 대상 | 복원 방식 | 트리거 | 자격증명 |
|---|---|---|---|
| **CNPG (postgres·keycloak DB)** | **자동** — recovery `Cluster` 가 `externalClusters` barman `inheritFromIAMRole` 로 S3 원본을 읽어 부팅 시 PITR 복원 | 앱 sync(선언형) | **IRSA** `cledyu-dr-cnpg-restore-*` (S3 read) |
| **Vault (raft)** | **수동 break-glass** — 운영자가 스냅샷을 내려받아 `vault operator raft snapshot restore` 실행 | 사람(명령형) | **운영자/bastion** AWS 자격 (Vault SA IRSA 아님) |

**왜 Vault 는 수동인가:** `snapshot restore` 는 이미 기동·unseal 된 Vault 에 대한 일회성
명령형 작업이다. CNPG 처럼 "파드가 부팅하면서 S3 에서 알아서 복원"하는 선언형 훅이 없다.
그래서 `eks_dr_vault_unseal` IRSA 에는 **KMS seal 권한만** 주고 S3 read 는 주지 않는다
(S3 를 줘도 자동 복원되지 않으므로 불필요한 권한일 뿐). 스냅샷 취득은 운영자가 자신의
자격(또는 bastion instance profile 에 별도 부여)으로 수행한다.

---

## Vault 스냅샷 복원

### 취득 경로 (평시 — 온프렘에서 자동 적재)

온프렘 `vault-backup` CronJob(`gitops/apps/vault-backup`)이 6시간마다 raft 스냅샷을 떠서
S3 에 올린다. DR 시엔 이 적재분을 그대로 복원 소스로 쓴다.

- 스케줄: `0 */6 * * *` (RPO 목표 1~24h)
- 취득: `vault operator raft snapshot save`
- 적재 위치: `s3://cledyu-lab-dr-backups/vault/vault-raft-<UTC타임스탬프>.snap`

> 온프렘이 살아있는 동안에는 이 CronJob 이 최신 스냅샷을 계속 갱신한다. 온프렘 상실
> 시점의 **가장 최근 스냅샷**이 사실상 Vault 의 RPO 다.

### 복원 절차 (드릴 — EKS)

⚠️ **선행: 아래 "apps-eks 부트스트랩" 을 먼저 실행**(ArgoCD 설치 → root-app → Vault 앱 sync)해 vault-0/1/2 파드가
존재해야 이 복원이 가능하다. 문서 배치상 이 섹션이 앞에 있지만 **실행은 부트스트랩 후**다. Phase 1 terraform
apply 완료(bastion 존재) 전제. 모든 `kubectl` 명령은 **bastion 에서** 실행한다(private 엔드포인트).

```bash
# 0) bastion 진입 + kubeconfig
aws ssm start-session --target <eks_dr_bastion instance id>
aws eks update-kubeconfig --name cledyu-dr --region ap-northeast-2

# 1) Vault 는 KMS auto-unseal 로 "빈 raft" 로 떠 있다(T6). 최초 1회 init 으로 루트토큰 확보.
#    (awskms seal 이라 unseal 키 입력은 불필요 — init 만 하면 자동 unseal 된다.)
# in-pod vault CLI: VAULT_ADDR(https://127.0.0.1:8200)는 파드 env 에 있으나 VAULT_CACERT 는 없다.
# TLS 자체서명(cledyu-ca)이라 CA 를 명시 안 하면 x509 실패 → 모든 vault 명령에 VAULT_CACERT=/vault/tls/ca.crt.
# (cert 에 127.0.0.1 IP SAN 을 넣어뒀다 — certificate.yaml. 없으면 CA 를 줘도 127.0.0.1 은 IP-SAN 부재로
#  x509 실패한다. 드릴서 실측 후 SAN 추가 → 아래 127.0.0.1 명령들이 그대로 통한다.)
kubectl -n vault exec -it vault-0 -- sh -c 'VAULT_CACERT=/vault/tls/ca.crt vault operator init'
#    → 출력된 Initial Root Token 을 <INIT_ROOT> 로 보관(restore 실행에만 임시 사용).
#    vault-0 init·unseal 후 vault-1/2 가 retry_join 으로 자동 합류 → 3-node(스냅샷 3-peer 와 동일 토폴로지) 형성 대기:
kubectl -n vault exec -it vault-0 -- sh -c \
  'VAULT_CACERT=/vault/tls/ca.crt VAULT_TOKEN=<INIT_ROOT> vault operator raft list-peers'   # vault-0/1/2 3 peers 확인

# 2) 최신 스냅샷을 S3 에서 취득 — bastion instance profile 자격으로(정적 키 불필요).
#    이 롤에 vault/ 프리픽스 read + 백업키 Decrypt 가 붙어 있다(eks-dr-bastion.tf
#    aws_iam_role_policy.eks_dr_bastion_vault_restore). Vault SA IRSA(KMS seal 전용)와 별개.
aws s3 ls s3://cledyu-lab-dr-backups/vault/ | sort | tail -1     # 최신 파일명 확인
aws s3 cp s3://cledyu-lab-dr-backups/vault/vault-raft-<TS>.snap ./vault-raft.snap

# 3) 파드로 복사 후 restore. 스냅샷은 다른 클러스터(온프렘)에서 왔고 방금 init 한 EKS Vault 는
#    recovery/shamir 키가 달라, 일반 restore 는 seal 일관성 검사에서 거부된다 → -force 필수.
#    (HashiCorp API: /sys/storage/raft/snapshot-force = "Autounseal/shamir 키 일관성 검사를
#    우회, 다른 클러스터 스냅샷·다른 seal 설정 복원용". CLI 의 -force 가 이 엔드포인트.)
#    동일 KMS seal 키(e29e3ec2...)를 쓰는 것은 -force 를 건너뛰는 근거가 아니라, force 복원 후
#    복원된 barrier 키링이 같은 KMS 키로 auto-unseal 되게 하는 조건이다(키가 다르면 force 로
#    복원해도 unseal 불가 → Vault 가 봉인된 채 남는다. 그래서 이 키는 DR-durable, 삭제 금지).
kubectl -n vault cp ./vault-raft.snap vault-0:/tmp/vault-raft.snap
kubectl -n vault exec -it vault-0 -- sh -c \
  'VAULT_CACERT=/vault/tls/ca.crt VAULT_TOKEN=<INIT_ROOT> vault operator raft snapshot restore -force /tmp/vault-raft.snap'

# 4) force restore 후에는 스냅샷(원본 클러스터)의 recovery 키가 유효해지고 init(1단계) 때 받은
#    <INIT_ROOT> 는 무효화된다. restore 직후 Vault 는 복원된 keyring 을 KMS 로 재-unseal 하니
#    `vault status` 의 Sealed=false 를 먼저 확인(수 초). 이후 인증은 원본 자격으로 한다.
#
#    ⚠️ 드릴 실측: 부트스트랩 시크릿의 root_token 은 **온프렘이 초기 root 를 폐기(revoke)** 해
#    무효였다(login 403). 따라서 **recovery 키로 generate-root** 가 주경로다(root_token 직접 사용 X).
#    bastion instance profile 에 cledyu/vault/* GetSecretValue 있음(eks-dr-bastion.tf
#    aws_iam_role_policy.eks_dr_bastion_vault_restore) — 정적 키 불요.
#    (시크릿이 CMK 암호화면 롤에 해당 kms:Decrypt 추가 필요.)
SECRET=$(aws secretsmanager get-secret-value --secret-id cledyu/vault/bootstrap \
  --region ap-northeast-2 --query SecretString --output text)
THRESH=$(echo "$SECRET" | jq -r .recovery_keys_threshold)
GR=$(kubectl -n vault exec vault-0 -- sh -c \
  'VAULT_CACERT=/vault/tls/ca.crt vault operator generate-root -init -format=json')
NONCE=$(echo "$GR" | jq -r .nonce); OTP=$(echo "$GR" | jq -r .otp)
ENC=""; for k in $(echo "$SECRET" | jq -r '.recovery_keys_b64[]' | head -n "$THRESH"); do
  ENC=$(kubectl -n vault exec vault-0 -- sh -c \
    "VAULT_CACERT=/vault/tls/ca.crt vault operator generate-root -nonce=$NONCE -format=json '$k'" \
    | jq -r '.encoded_root_token // empty'); done
NEWROOT=$(kubectl -n vault exec vault-0 -- sh -c \
  "VAULT_CACERT=/vault/tls/ca.crt vault operator generate-root -decode=$ENC -otp=$OTP -format=json" | jq -r .token)
# 복원 확인 — cledyu/ kv 에 oidc·db 등 온프렘 시크릿이 있어야 정상(비어 있으면 복원 실패 판정).
kubectl -n vault exec vault-0 -- sh -c \
  "VAULT_CACERT=/vault/tls/ca.crt VAULT_TOKEN=$NEWROOT vault kv list cledyu"
# 복원 후 quorum 재확인 — 3 peers·leader 있어야 ESO 인증 진행. 없으면(leader 없음) quorum 실패 →
# HashiCorp lost-quorum peers.json 복구 필요(3-node 로 복원했으니 정상적으론 재선출됨).
kubectl -n vault exec vault-0 -- sh -c \
  "VAULT_CACERT=/vault/tls/ca.crt VAULT_TOKEN=$NEWROOT vault operator raft list-peers"   # vault-0 leader + vault-1/2 follower(Voter=true)
```

**복원 후 정합성 체크(다음 스텝의 선행조건):**
- Vault 안에 ESO 가 참조하는 경로(`cledyu/db/*`, `cledyu/oidc/*`, KMS/S3 자격 등)가
  존재해야 → EKS 의 external-secrets 가 api/keycloak 시크릿을 채운다.
- Vault 가 비어 있으면(복원 누락) api 는 in-memory 폴백으로 뜨고 keycloak-pg 자격이
  없어 Keycloak 이 기동 실패한다 → **드릴 실패로 판정**(자동 통과처럼 보이지 않게 주의).
- **라이브 터미널 필수 시드(2개):** `cledyu/aws/api` 에 Tailscale authkey **두 개**가 모두 있어야
  `cledyu-api-tailscale` ExternalSecret 이 Healthy 하고 api 가 terminal_url 을 광고한다(session.go 는
  두 조건 AND — 세션 키 설정 && api tsnet 가입).
  - `tailscale_authkey` — 세션 EC2 인스턴스가 cloud-init 으로 tailnet 가입(ephemeral, tag:lab-ec2).
  - `api_tailscale_authkey` — api 파드 자신이 tsnet 으로 tailnet 가입(tag:cledyu-api).
  온프렘 Vault 에 시드돼 있으면 스냅샷으로 함께 복원된다. 어느 키든 없으면 그 ExternalSecret 만 Degraded
  (필수 AWS 키 cledyu-api-aws·SSM 채점·api 기동은 정상 — 터미널 키는 별도 ExternalSecret 로 분리돼 있어
  필수 키 동기화를 막지 않는다) — 터미널이 안 뜨면 실습 진행 불가이므로 **온프렘에서 시드 상태를 유지**한다.

### Vault k8s auth 를 EKS 용으로 재설정 (복원·unseal 후, ESO 인증 직전) — T6

복원 스냅샷의 `auth/kubernetes/config` 는 온프렘 `kubernetes_ca_cert`·`token_reviewer_jwt` 라 EKS API 검증이
실패한다. **vault 파드 안에서 재실행**하면 `@`경로가 EKS 파드의 SA CA·토큰을 읽어 교정된다(role
`external-secrets-operator` 는 스냅샷에 이미 있어 재설정 불요).

```bash
# bastion. VAULT_ADDR(127.0.0.1) 는 파드 env, VAULT_CACERT 만 명시(CA=/vault/tls/ca.crt).
# disable_local_ca_jwt=true — 제공한 reviewer JWT/CA 로만 TokenReview(로컬 파드 CA/JWT 미사용) → EKS 서 안정적.
kubectl -n vault exec vault-0 -- sh -c \
  "VAULT_CACERT=/vault/tls/ca.crt VAULT_TOKEN=$NEWROOT vault write auth/kubernetes/config \
     kubernetes_host=https://kubernetes.default.svc:443 \
     kubernetes_ca_cert=@/var/run/secrets/kubernetes.io/serviceaccount/ca.crt \
     token_reviewer_jwt=@/var/run/secrets/kubernetes.io/serviceaccount/token \
     disable_local_ca_jwt=true"

# ⚠️ 드릴 실측: ESO 컨트롤러는 Vault 가 늦게(복원 후) 살아나면 **실패한 provider client 를 캐시**해
# store 를 InvalidProviderConfig 로 붙잡는다 → k8s auth 재설정 직후 **ESO 재기동으로 캐시를 버린다**.
kubectl -n external-secrets rollout restart deploy/external-secrets
kubectl -n external-secrets rollout status deploy/external-secrets --timeout=120s

# 검증: store Ready → ExternalSecret SecretSynced → 시크릿 생성 → api 기동
kubectl get clustersecretstore vault-backend \
  -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}'   # True
kubectl get externalsecret -A                                     # 전부 STATUS SecretSynced
kubectl -n api get secret cledyu-api-oidc cledyu-api-db            # 생성 확인
```

주: `token_reviewer_jwt` 는 파드 projected 토큰(~1h 만료). 재설정 직후 1회 sync 로 `cledyu-api-oidc`(Retain)
생성되므로 드릴엔 무해(이후 만료로 재-sync 가 막혀도 시크릿은 잔존). 장기 운영이면 비만료 Secret 기반
reviewer 토큰 필요(`vault-bootstrap.md` 2026-07-04 인시던트).

> vault-tls 는 cert-manager(cledyu-ca)가 `certificate.yaml` 로 자동 발급 — 수동 self-signed 불요.

### tmpfs / 잔존 주의

스냅샷은 Vault 전체 시크릿이다. 취득·복사한 로컬 파일(`./vault-raft.snap`,
`/tmp/vault-raft.snap`)은 복원 확인 후 즉시 삭제한다. (온프렘 CronJob 은 스냅샷을 tmpfs 로만
다루고 업로드 후 회수한다 — 수동 복원 경로도 동일 원칙을 지킨다.)

```bash
rm -f ./vault-raft.snap
kubectl -n vault exec -it vault-0 -- rm -f /tmp/vault-raft.snap
```

---

## 부트스트랩 스텝 (실행 순서 = 체크리스트)

- [ ] Phase 0 warm 사전 셋업 확인(최초 1회 — 위 참고, 이미 상시 유지 중이면 skip)
- [ ] Phase 1 — terraform apply(hot)+노드 스케일(CLI) — DR hot 리소스(NAT·엔드포인트·bastion 인스턴스)·노드 생성

### Phase 1 — 재해 페일오버 트리거 (pilot-light, 운영자 머신)

warm(Phase 0)은 이미 존재하므로 여기서는 hot·노드만 올린다. 노드 desired 는 모듈이 `ignore_changes`
하므로 terraform apply 로는 오르지 않는다 — CLI(§Global P1) 필수.

```bash
# ⚠️ tfvars 가 없어 -target 없이 apply 하면 프로덕션을 오-destroy 한다 → DR 리소스만 -target(destroy 와 동일 목록).
# (1) hot 리소스(NAT·엔드포인트·bastion) — terraform. 노드 desired 는 모듈이 ignore 하므로 여기선 안 오른다.
cd infra/terraform/aws
terraform apply -var enable_eks_dr=true -var eks_dr_active=true -var eks_dr_node_desired=0 \
  -target=module.eks_dr_vpc -target=module.eks_dr -target=module.eks_dr_ebs_csi_irsa \
  -target=module.eks_dr_alb_irsa -target=aws_security_group.eks_dr_endpoints \
  -target=aws_security_group.eks_dr_bastion -target=module.eks_dr_endpoints \
  -target=module.eks_dr_vault_unseal_irsa -target=aws_iam_policy.eks_dr_vault_unseal \
  -target=aws_iam_policy.eks_dr_cnpg_restore -target=module.eks_dr_cnpg_restore_irsa \
  -target=aws_iam_role.eks_dr_bastion -target=aws_iam_role_policy_attachment.eks_dr_bastion_ssm \
  -target=aws_iam_role_policy.eks_dr_bastion_describe -target=aws_iam_role_policy.eks_dr_bastion_vault_restore \
  -target=aws_iam_instance_profile.eks_dr_bastion -target=aws_instance.eks_dr_bastion
#   → NAT·엔드포인트·bastion 생성(~2-3분). NAT 를 노드보다 먼저 세워 이미지 pull 경로 확보.
#   → bastion instance id: terraform output / aws ec2 describe-instances Name=cledyu-dr-bastion
# (2) [P1] 노드 스케일 0→N — CLI(terraform desired_size ignore_changes 회피, §Global).
NG=$(aws eks list-nodegroups --cluster-name cledyu-dr --region ap-northeast-2 --query 'nodegroups[0]' --output text)
aws eks update-nodegroup-config --cluster-name cledyu-dr --region ap-northeast-2 \
  --nodegroup-name "$NG" --scaling-config minSize=0,maxSize=6,desiredSize=3
#   → 노드 3 생성(~3-5분).
```

이후 **아래 "apps-eks 부트스트랩"부터 기존 절차를 매 failover 동일하게 수행**한다(seed 안 했으므로 여기서
처음부터 설치·sync): bastion 진입 → `helm upgrade --install argocd`(멱등) → root-app apply → Vault 복원 →
CNPG → api/web restart → DNS 전환.

> **[C4]** 매 failover 가 `helm upgrade --install argocd` 를 재실행하므로 `argocd-application-controller`
> 는 항상 replicas=1 로 (재)생성된다 — failback(아래)이 0 으로 내렸든 무관. **"재해 시 helm/root-app 재실행
> 불요" 같은 스킵 문구는 넣지 않는다**(그건 교차사이클 dead-end 를 만든다 — seed 를 안 하므로 매번 처음부터
> sync 해야 한다).

### apps-eks 부트스트랩 (bastion 에서)

> 아래 3개 bash 블록(ArgoCD 설치 → C2 가드 → root-app apply)은 **동일한 bastion 쉘 세션**에서 이어서 실행한다 —
> 가드 블록의 상대경로 grep 이 앞 블록에서 `cd`한 작업 디렉터리(`REPO_ROOT`)에 의존하므로, 세션을 끊거나 새
> 터미널로 옮기면 cwd·변수(`REPO_ROOT` 등)가 이어지지 않아 가드가 엉뚱한 경로를 본다.

```bash
# -1) bastion 준비 — user_data 가 kubectl/awscli/git/helm 을 모두 설치한다(egress 대기+curl --retry 포함).
#     ⚠️ 부팅 직후 NAT 준비 전이면 예전엔 kubectl 설치가 깨졌다(드릴 실측) → user_data 에 대기/retry 를 넣어 해소.
#     여기선 cloud-init 완료를 기다린 뒤 도구 존재만 검증한다(없으면 user_data 실패 → cloud-init 로그 확인).
cloud-init status --wait                                             # user_data 완료까지 대기
command -v kubectl git helm aws >/dev/null && echo "tools OK" \
  || echo "⚠ user_data 미완/실패 → sudo cat /var/log/cloud-init-output.log 로 원인 확인"
# repo 가 private 면 인증 필요: git clone https://<GITHUB_PAT>@github.com/requset700k/Cledyu.git ~/Cledyu
git clone https://github.com/requset700k/Cledyu.git ~/Cledyu && cd ~/Cledyu
aws eks update-kubeconfig --name cledyu-dr --region ap-northeast-2   # (Vault 복원 때 이미 했으면 생략)

# 0) 사전 확인 — apps-eks 앱은 targetRevision=main 을 sync. 치환할 placeholder 는 없다:
#    IRSA 롤 ARN(vault/alb)은 role_name 고정→결정적이라 하드코딩, vpcId·ACM cert 는 ALB 컨트롤러가
#    auto-discover(vpc=IMDS, cert=Ingress host 기반 *.cledyu.com 매칭). 혹시 남은 placeholder 가 있으면 잡힌다.
REPO_ROOT=$(git rev-parse --show-toplevel); cd "$REPO_ROOT"
grep -rn '<<' gitops/apps/*/values-eks.yaml gitops/apps/alb-controller/values.yaml \
  && echo "⚠ placeholder 잔존 — 확인 필요" || echo "placeholder 없음 OK ✅"

# 0.5) ArgoCD 설치(멱등 — 매 failover 반복, §Phase1 [C4]) — 앱을 seed 하지 않으므로(design B) 노드가
#      올라온 직후 클러스터는 매번 비어 있다. ArgoCD(Application CRD·컨트롤러)가 없으면 root-app 을
#      적용·조정할 주체가 없어(치킨-에그) helm 으로 먼저 설치한다. 이후 platform-argocd 가 같은 릴리스
#      (argocd, ns argocd)를 ServerSideApply 로 adopt 한다. helm upgrade --install 은 멱등이라 warm etcd
#      에 이전 사이클 잔존이 있어도 안전하게 재실행된다 — 절대 스킵하지 않는다.
helm repo add argo https://argoproj.github.io/argo-helm && helm repo update
helm upgrade --install argocd argo/argo-cd --version 7.7.10 \
  -f gitops/apps/argocd/values.yaml -f gitops/apps/argocd/values-eks.yaml \
  -n argocd --create-namespace --wait
kubectl -n argocd rollout status deploy/argocd-server --timeout=300s
```

```bash
# [P2] git-source targetRevision 이 main 도 chart-version(vX.Y / X.Y)도 아니면 = 브랜치핀 → 중단.
# chart-version targetRevision(예: 0.32.0·v1.20.2·7.7.10)은 오탐 대상이 아니므로 무시 — git-source 브랜치핀만 잡는다.
# ^[[:space:]]*targetRevision: 로 앵커 — 주석줄(# targetRevision: …)은 제외(value-only revert 후 오탐 방지).
if grep -REn '^[[:space:]]*targetRevision:' gitops/argocd/root-app-eks.yaml gitops/argocd/apps-eks/ \
   | grep -vE 'targetRevision:[[:space:]]*(main|v?[0-9]+\.[0-9])'; then
  echo "❌ git-source 가 main 아닌 revision(브랜치핀) — main 으로 되돌린 뒤 진행"; exit 1
fi
```

> 참고: 현재 main 의 `root-app-eks.yaml`·일부 apps-eks 에 `feat/dr-eks-overlay-cnpg-keycloak` 핀이 잔존
> (#290 머지 전 revert 누락)한다 — 이 가드가 첫 드릴에서 그걸 잡을 것이므로, 사전에 main 을 정리해 둘 것
> (별도 프로덕션 이슈).

```bash
# 1) root-app 적용 — 이제 ArgoCD 가 존재하므로 wave 순서(cert-manager -10 → pki -8 → ... → api/web 0)로 sync
kubectl apply -f gitops/argocd/root-app-eks.yaml

# 2) 플랫폼 Ready 대기: cert-manager·cledyu-ca(ClusterIssuer)·Bundle(ConfigMap) 확인
kubectl -n cert-manager wait --for=condition=Available deploy/cert-manager --timeout=300s
kubectl get clusterissuer cledyu-ca
kubectl -n api get configmap cledyu-root-ca-bundle   # trust-manager Bundle 분배 확인
```

### CNPG 재-failover 가드 (구 CR 삭제 — 복원 전, bastion·kubectl 컨텍스트 확보 후)

[P1b] failback 후 온프렘이 다시 primary 로 전진하므로, warm etcd 에 잔존하는 이전 사이클 CNPG CR 은 stale
데이터를 가리킨다. root-app 적용(위) 직후, CNPG 차트가 `Cluster` CR 을 만들기 전에 구 CR 을 지워 ArgoCD 가
새로 만들게 하면 `bootstrap.recovery` 가 최신 S3 로 재실행된다. 단발(첫) failover 는 CR 이 없어 아래는 no-op.

```bash
# [P1b] 재-failover 시 잔존 CNPG CR 제거 → ArgoCD 재생성 → bootstrap.recovery 최신 S3. 단발 failover 는 CR 이 없어 no-op.
kubectl -n postgres delete cluster cledyu-pg --ignore-not-found
kubectl -n keycloak delete cluster keycloak-pg --ignore-not-found
```

- [ ] apps-eks root-app 적용 → 플랫폼(cert-manager·ALB·gp3·ESO·CNPG operator) Ready
- [ ] Kafka Ready(실습 스택 — A1) — strimzi-operator(wave 0) Running 후 kafka-cluster(wave 1) sync.
      `kubectl -n kafka get kafka cledyu-kafka`(READY=True), `kubectl -n kafka get kafkatopic`(validation-requests·-dlq·-results·lab-events·security-logs 존재),
      bootstrap svc `cledyu-kafka-kafka-bootstrap.kafka.svc:9093` 응답. 의존: cert-manager CA + trust-manager Bundle + gp3(nodepool SC-agnostic).
      (ServiceMonitor 2종 미배포는 정상 — EKS 관측 스택 없음, directory.exclude 로 제거.)
- [ ] **Vault 스냅샷 복원**(위 섹션)
- [ ] **CNPG 재-failover 가드**(위 섹션) 적용 후 CNPG 복원 차트 sync → `cledyu-pg-rw`·`keycloak-pg-rw` Ready(자동 S3 복원)
- [ ] validation-engine Ready(실습 스택 — A2) — `kubectl -n validation-engine get deploy validation-engine` Available.
      선행: A1 Kafka(KafkaUser `validation-engine` Ready · kafka-clients-ca client cert) + **Vault 복원→ESO 로 `cledyu-validation-engine-aws` Secret 생성 후** 기동(AWS 키 non-optional).
      (CiliumNetworkPolicy·plain NetworkPolicy 둘 다 미렌더 정상 — EKS values-eks 게이트. lab-ssh-key 없음도 정상 — EC2/SSM 채점.)
- [ ] Keycloak·api·web Ready + ALB/ACM 종단 확인
- [ ] **공개 DNS 전환**(아래 섹션) — DNS 안 바꾸면 죽은 온프렘 프록시로 계속 감. WAF(/metrics 차단)는 api·web values-eks 의 wafv2-acl-arn 로 ALB 생성과 동시에 자동 연결(수동 불요) → 여기선 붙었는지 확인만
- [ ] **api·web rollout restart**(아래 섹션) — CNPG·Keycloak·DNS Ready 후 필수. api 는 startup 1회만 DB/auth 초기화·실패 시 degraded 유지라, 의존성이 늦게 살아나면 restart 해야 DB모드·로그인 활성
- [ ] 검증(로컬 테스트유저 로그인·복원 데이터 서빙) + RTO 실측
- [ ] 온프렘 복구 후: **정상 복귀 = failback**(warm 유지, 아래) / **완전 폐기 = destroy**(고아 방지 순서 필수, 아래)

### 공개 DNS 전환 (+ WAF 연결 확인) (검증·서빙 전 필수)

> ⚠️ **실행 위치 = bastion 아님, 운영자 작업 머신**. 아래 `aws wafv2 get-web-acl-for-resource`·`elbv2 describe-load-balancers`·
> `route53 change-resource-record-sets` 는 bastion instance profile 권한 밖이다(bastion 롤은 eks:Describe +
> Vault 복원용 S3/KMS/Secrets 만 — eks-dr-bastion.tf). bastion 에서 실행하면 AccessDenied. route53/wafv2/elbv2
> 권한이 있는 운영자 자격의 머신에서 실행한다(kubectl 로 ALB DNS 취득만 bastion, AWS API 전환은 운영자 머신).

공개 DNS(`aws_route53_record.public`)는 온프렘 프록시 ALB 를 alias 로 가리킨다. 온프렘 장애 시에도 그대로면
EKS ALB target 이 Healthy 여도 사용자가 도달하지 못한다. api/app 은 즉시 EKS ALB 로 돌린다.
**`auth.cledyu.com` 은 T8(Keycloak) Ingress 가 이 오버레이에 포함**되어 같은 `cledyu-dr` ALB 로 노출되므로 함께 전환한다.
단 auth 는 **Keycloak CR Ready(issuer 응답 가능) 이후**에만 넘긴다 — 그 전에 넘기면 ALB keycloak 타겟 unhealthy 로
404/503 이 되고, 반대로 미전환 시 auth 레코드가 죽은 온프렘 ALB 에 남아 API OIDC discovery(issuer `https://auth.cledyu.com`)
·브라우저 로그인/토큰 갱신이 계속 실패한다.

```bash
# EKS ALB DNS 이름·zone 취득 (ALB Controller 가 api/web Ingress 로 생성)
ALB=$(kubectl -n api get ingress api -o jsonpath='{.status.loadBalancer.ingress[0].hostname}'); echo "$ALB"
ALB_ZONE=$(aws elbv2 describe-load-balancers --region ap-northeast-2 \
  --query "LoadBalancers[?DNSName=='$ALB'].CanonicalHostedZoneId" --output text)
ZONE=$(aws route53 list-hosted-zones-by-name --dns-name cledyu.com --query "HostedZones[0].Id" --output text)

# /metrics 차단 WAF(cledyu-lab-public, block-public-metrics 룰)는 api·web values-eks 의 wafv2-acl-arn
# annotation 으로 ALB 생성과 동시에 자동 연결된다 → 수동 associate-web-acl 불요(프로비저닝~연결 노출 창 제거).
# 여기서는 실제로 붙었는지 + /metrics 차단만 확인한다.
ALB_ARN=$(aws elbv2 describe-load-balancers --region ap-northeast-2 \
  --query "LoadBalancers[?DNSName=='$ALB'].LoadBalancerArn" --output text)
aws wafv2 get-web-acl-for-resource --resource-arn "$ALB_ARN" --region ap-northeast-2 \
  --query "WebACL.Name" --output text          # → cledyu-lab-public (비어 있으면 values-eks ARN stale → 갱신 후 재sync)
# 확인: curl https://api.cledyu.com/metrics → 403(WAF block)

# (A) 실제 페일오버 — api/app 은 즉시, auth 는 Keycloak Ready 후 같은 cledyu-dr ALB 로 UPSERT
for h in api app; do
  aws route53 change-resource-record-sets --hosted-zone-id "$ZONE" --change-batch \
    "{\"Changes\":[{\"Action\":\"UPSERT\",\"ResourceRecordSet\":{\"Name\":\"$h.cledyu.com\",\"Type\":\"A\",\"AliasTarget\":{\"HostedZoneId\":\"$ALB_ZONE\",\"DNSName\":\"$ALB\",\"EvaluateTargetHealth\":false}}}]}"
done
# auth: Keycloak CR 이 Ready(issuer https://auth.cledyu.com 응답 가능)된 뒤에만 전환 — 조기 전환 시 keycloak 타겟 unhealthy → 503.
kubectl -n keycloak wait --for=condition=Ready keycloak/cledyu-keycloak --timeout=600s
aws route53 change-resource-record-sets --hosted-zone-id "$ZONE" --change-batch \
  "{\"Changes\":[{\"Action\":\"UPSERT\",\"ResourceRecordSet\":{\"Name\":\"auth.cledyu.com\",\"Type\":\"A\",\"AliasTarget\":{\"HostedZoneId\":\"$ALB_ZONE\",\"DNSName\":\"$ALB\",\"EvaluateTargetHealth\":false}}}]}"
# ⚠️ 이 레코드(api·app·auth)는 terraform aws_route53_record.public 관리분 — 온프렘 복구 후 terraform apply 로 원복(failback).

# (B) 라이브 DNS 미전환 로컬 드릴 검증(F3) — 운영자 머신에서만 (auth 는 Keycloak Ready 후 함께 검증)
for h in api app auth; do
  curl -sk --resolve $h.cledyu.com:443:$(dig +short $ALB|head -1) https://$h.cledyu.com/ -o /dev/null -w "%{http_code} $h\n"
done
```

### api·web 재기동 (복원 데이터·로그인 활성화 — CNPG·Keycloak·DNS Ready 후)

api 는 startup 에 `store.Open`(DB)·auth provider 를 **1회만** 초기화하고, 실패하면 in-memory/nil 로 **계속 실행**한다
(재연결 루프 없음 — `apps/api/cmd/server/main.go`). 그래서 Vault 복원으로 `cledyu-api-oidc` 가 생겨 api 파드가 **일찍**
뜨면, 그 시점에 CNPG(`cledyu-pg-rw`)·Keycloak·`auth.cledyu.com` 이 아직이면 **영구 degraded**(진도/계정 in-memory,
로그인 불가)로 남는다. → 의존성이 모두 Ready 된 뒤 **반드시 rollout restart** 로 재초기화한다.

```bash
kubectl -n api rollout restart deploy/api && kubectl -n api rollout status deploy/api
kubectl -n web rollout restart deploy/web && kubectl -n web rollout status deploy/web
# 재기동 후 api 로그에 "db 연결 — 유저/진행 상태 영속화 활성"(in-memory 폴백 아님)·/ready checks 의 keycloak=connected 확인.
kubectl -n api logs deploy/api | grep -E "db 연결|in-memory"
```

### 실습 fidelity 검증 (EC2 채점 == 온프렘 KubeVirt) — A3

풀서비스 DR 의 실습이 온프렘과 동등한지 라이브로 확인한다. 대표 랩 6종(lab-linux-basics 등)에 대해:

```bash
# 1) 로컬 테스트유저로 세션 생성 → api 가 EC2 인스턴스를 띄우는지
#    (kubevirt=false·aws=true 이므로 sessions=EC2 provisioner, validator=validation-engine(A2) 로 non-nil → 503 안 뜸)
kubectl -n api logs deploy/api | grep -E "EC2|launch|instance"
aws ec2 describe-instances --region ap-northeast-2 \
  --filters "Name=tag:cledyu.io/managed-by,Values=cledyu-session" "Name=instance-state-name,Values=running" \
  --query "Reservations[].Instances[].InstanceId" --output text          # 세션 인스턴스 존재(provisioner.go 태그: cledyu.io/managed-by=cledyu-session)

# 2) 사용자 터미널 도달(tailnet) — api 가 tsnet 으로 인스턴스에 다이얼(라이브 터미널 WebSocket 200).
#    CLEDYU_AWS_TAILSCALE_AUTH_KEY(세션 tailnet 가입) + CLEDYU_AWS_API_TAILSCALE_AUTH_KEY(api tsnet) 둘 다
#    필요(둘 다 cledyu-api-tailscale Secret) — 라이브 터미널은 DR 실습에 필요하므로 tailscale_authkey·
#    api_tailscale_authkey 는 부트스트랩 시드 필수(위 '복원 후 정합성 체크' 참고). 둘 다 시드되면
#    cledyu-api-tailscale ExternalSecret Healthy. 어느 키든 미시드면 이 시크릿만 Degraded(필수 AWS 키·SSM 채점·api 는 정상).

# 3) 검증엔진 채점 — 각 스텝을 통과 상태로 만들고 /validate 호출 → validation-engine 이 SSM SendCommand 로
#    EC2 를 채점 → validation-results → api 가 Postgres(session_steps/progress/completions)에 반영.
kubectl -n validation-engine logs deploy/validation-engine | grep -E "SSM|SendCommand|passed|failed"
# 수용기준: 온프렘에서 통과하는 정답 입력이 DR(EC2)에서도 passed, 오답은 failed. 6종 랩 각 최소 1스텝 정답/오답 대조가 온프렘과 일치.

# 4) mock-pass 미발생 확인(보안) — validator non-nil 이므로 "mock" 응답이 없어야 한다.
kubectl -n api logs deploy/api | grep -c "mock"                          # 0
```

수용기준 요약: (a) 세션=EC2 인스턴스 생성, (b) 터미널 tailnet 도달, (c) SSM 채점 결과가 온프렘과 동일 판정(정답 passed/오답 failed), (d) mock-pass 0건.

### failback (온프렘 복구 후 — 클러스터는 warm 유지, 노드만 회수)

warm(컨트롤플레인·VPC·IRSA·노드그룹0·etcd)은 유지하고 hot·노드만 회수한다. 완전 폐기는 아래 destroy.

```bash
# 1) [Imp1] 공개 DNS 를 온프렘으로 먼저 원복 — 서빙 파괴 전에 트래픽부터 돌린다(운영자 머신, route53 권한).
#    failover 가 route53 CLI UPSERT 로 EKS ALB 를 덮었으므로 terraform 관리값(온프렘 프록시 ALB alias)으로 되돌린다.
#    ⚠️ 반드시 -var enable_public_ingress=true + -target — 안 그러면 count=0(기본 false)이라 terraform 이 레코드를
#       DELETE 하거나 공개 스택을 오-destroy 한다. 온프렘 Healthy 확인 후 실행.
cd infra/terraform/aws && terraform apply -var enable_public_ingress=true -target=aws_route53_record.public
# 2) 고아 방지 in-cluster 정리(노드·컨트롤러 살아있을 때): 아래 destroy 의 0)~4.5) 스텝 전체를 수행
#    (⚠️ terraform destroy 는 아님 — in-cluster 정리만). step 0) selfHeal 정지 → step 1) statefulset/CNPG/kafka
#    CR unmount → step 2) Ingress → step 3) PVC → step 4.5) ENI. ⚠️ step 1) 을 빼면(PVC 를 아직 물고 있는 워크로드
#    를 안 지우고 바로 PVC 를 지우면) step 3) PVC 삭제가 pvc-protection finalizer 로 Terminating 에 걸린다.
#    Ingress→ALB/TG, PVC→EBS 가 정리되도록 완료까지 대기.
# 3) [P1] 노드 N→0 (CLI). 위 in-cluster 정리 끝난 뒤 실행.
NG=$(aws eks list-nodegroups --cluster-name cledyu-dr --region ap-northeast-2 --query 'nodegroups[0]' --output text)
aws eks update-nodegroup-config --cluster-name cledyu-dr --region ap-northeast-2 --nodegroup-name "$NG" --scaling-config minSize=0,maxSize=6,desiredSize=0
# 4) hot 회수: eks_dr_active=false → NAT·엔드포인트·bastion 소멸. 컨트롤플레인·VPC·IRSA·노드그룹(0)·etcd 유지.
cd infra/terraform/aws && terraform apply -var enable_eks_dr=true -var eks_dr_active=false -var eks_dr_node_desired=0 \
  -target=module.eks_dr_vpc -target=module.eks_dr -target=module.eks_dr_ebs_csi_irsa \
  -target=module.eks_dr_alb_irsa -target=aws_security_group.eks_dr_endpoints \
  -target=aws_security_group.eks_dr_bastion -target=module.eks_dr_endpoints \
  -target=module.eks_dr_vault_unseal_irsa -target=aws_iam_policy.eks_dr_vault_unseal \
  -target=aws_iam_policy.eks_dr_cnpg_restore -target=module.eks_dr_cnpg_restore_irsa \
  -target=aws_iam_role.eks_dr_bastion -target=aws_iam_role_policy_attachment.eks_dr_bastion_ssm \
  -target=aws_iam_role_policy.eks_dr_bastion_describe -target=aws_iam_role_policy.eks_dr_bastion_vault_restore \
  -target=aws_iam_instance_profile.eks_dr_bastion -target=aws_instance.eks_dr_bastion
```

> 재-failover 시 CNPG 최신 재복원은 **Phase 1 의 CNPG 재-failover 가드**가 보장한다(failback 이 CR 을 남겨도
> 무방). 정상 복귀는 이 failback(warm 유지), **완전 폐기는 아래 destroy + `enable_eks_dr=false`**(과금 0,
> 단 다음 재해는 Phase 0 부터 다시).

### destroy (고아 방지)

DR ALB(aws-load-balancer-controller 가 Ingress 보고 out-of-band 생성)와 gp3 EBS(reclaim=Delete)는
terraform 밖이다. 클러스터를 먼저 부수면 ALB·target group·`k8s-*` SG·ENI·EBS 가 고아로 남고,
남은 ENI 가 서브넷/VPC 삭제를 `DependencyViolation` 으로 막는다. 반드시 in-cluster 부터 정리한다.

```bash
# VPC id·클러스터명을 destroy 前에 1회 취득 — 아래 전 스텝에서 $VPCID 로 재사용(고아 검증까지). 클러스터가
# 지워지면 describe 가 실패하므로 반드시 여기서 먼저 잡는다.
VPCID=$(aws eks describe-cluster --name cledyu-dr --region ap-northeast-2 --query 'cluster.resourcesVpcConfig.vpcId' --output text); CLUSTER=cledyu-dr

# 0) ArgoCD selfHeal 중지 — root-app 은 automated.selfHeal 로 apps-eks 를 계속 조정한다. 안 끄면 아래에서 지운
#    Ingress/PVC 를 즉시 다시 만들어 ALB/EBS 삭제가 안 끝나고 ENI/볼륨이 고아로 남는다(드릴 실측: vault 가
#    재생성돼 gp3 EBS 6개가 부활했다).
#    ⚠️ 드릴 실측: `kubectl patch applications --all` 은 **동작 안 한다**(patch 는 --all 미지원 → "unknown flag: --all").
#    앱별 loop 로 automated 를 지워도 root-app 이 자식에 automated 를 되살린다(race). → **application-controller 를
#    스케일 0** 으로 내려 selfHeal 엔진 자체를 정지시키는 게 확실하다(self-managed 삭제-cascade 데드락도 회피 —
#    삭제가 아니라 스케일다운이라 컨트롤러가 자기 자신을 prune 하지 않는다). ALB controller·EBS CSI 는 계속 떠서 정리 담당.
kubectl -n argocd scale statefulset argocd-application-controller --replicas=0
kubectl -n argocd rollout status statefulset argocd-application-controller --timeout=60s

# 1) PVC 를 물고 있는 워크로드 먼저 종료 — vault StatefulSet + CNPG Cluster(T7) + Kafka 브로커(실습 스택 A1).
#    파드가 PVC 를 마운트한 채 delete pvc 하면 pvc-protection 으로 PVC 가 Terminating 에 묶여 EBS CSI 가 볼륨을
#    못 지우고 고아가 된다(gp3 EBS 잔존 → 고아 볼륨 검증·서브넷/VPC 삭제까지 막힘).
kubectl -n vault delete statefulset vault --ignore-not-found
kubectl delete clusters.postgresql.cnpg.io -A --all --ignore-not-found   # 오퍼레이터가 파드+PVC 정리
# Kafka 브로커도 gp3 PVC 3개(kafka-nodepool-eks)를 마운트 → 삭제해야 마운트가 풀린다.
# ⚠️ 드릴 실측(2026-07-12): node-pool 기반 Kafka 는 브로커 파드를 KafkaNodePool→StrimziPodSet 이 소유한다.
#    Kafka CR(kafkas)만 지우면 StrimziPodSet/브로커 파드가 남아 PVC 가 Terminating 고착(EBS 고아)한다 →
#    KafkaNodePool 도 반드시 지워야 파드가 빠진다. (KafkaNodePool deleteClaim:false 라 PVC 자체는 남고,
#    아래 3) delete pvc 에서 마운트 없어진 뒤 삭제된다.)
# 전제: strimzi-cluster-operator(strimzi-system)는 아직 떠 있어야 삭제를 처리한다 — 위 0)은 argocd
#    application-controller 만 scale0 했다.
kubectl delete kafkas.kafka.strimzi.io -A --all --ignore-not-found
kubectl delete kafkanodepool.kafka.strimzi.io -A --all --ignore-not-found   # ← 브로커 파드 실소유자(StrimziPodSet). 없으면 파드 안 빠져 PVC 고착
kubectl wait --for=delete pod -n vault -l app.kubernetes.io/name=vault --timeout=300s 2>/dev/null || true
kubectl wait --for=delete pod -n kafka -l strimzi.io/cluster=cledyu-kafka --timeout=300s 2>/dev/null || true

# 2) Ingress 삭제 → 컨트롤러가 ALB/TG/SG 정리 (selfHeal 꺼서 재생성 안 됨, 완료까지 대기)
kubectl delete ingress -A --all
aws elbv2 describe-load-balancers --region ap-northeast-2 \
  --query "LoadBalancers[?VpcId=='$VPCID'].LoadBalancerArn" --output text   # 빈 값 될 때까지 확인

# 3) PVC 삭제 → 이제 마운트 없어 즉시 삭제 → EBS CSI 가 gp3 볼륨 삭제. PV/EBS 삭제 완료까지 대기.
kubectl delete pvc -A --all
for i in $(seq 1 30); do [ -z "$(kubectl get pv -o name 2>/dev/null)" ] && break; echo "PV/EBS 삭제 대기($i/30)..."; sleep 10; done
kubectl get pv 2>/dev/null   # 남아있으면(Retain PV 등) 수동 확인 — gp3(Delete)면 비어야 정상

# 4) LoadBalancer 타입 Service 없음(traefik 은 DR 앱셋 미포함) — skip

# 4.5) ⚠️ 드릴 실측: 노드 종료 시 VPC CNI 보조 ENI(설명 aws-K8S-*)가 detach 만 되고 'available' 로 남아
#      node SG·subnet 삭제를 DependencyViolation 으로 막는다(terraform 이 aws_subnet/aws_security_group.node
#      에서 10분+ "Still destroying" 로 멈춤). terraform destroy 실행 후 거기서 멈추면 **다른 터미널**에서
#      available 상태의 CNI ENI 를 지운다 → terraform 재시도가 subnet/SG 삭제를 이어간다.
# VPCID 는 위 destroy 시작부에서 이미 취득됨(destroy 前).
for eni in $(aws ec2 describe-network-interfaces --region ap-northeast-2 \
    --filters "Name=vpc-id,Values=$VPCID" "Name=status,Values=available" \
    --query "NetworkInterfaces[?starts_with(Description,'aws-K8S-')].NetworkInterfaceId" --output text); do
  aws ec2 delete-network-interface --region ap-northeast-2 --network-interface-id "$eni"
done

# 5) terraform destroy — in-cluster EBS/ALB 정리 완료 후에만.
#    ⚠️ 이 디렉토리는 tfvars 가 없어 -target 없이 apply 하면 프로덕션 게이트 리소스(public-ingress·waf 등)를
#    오-destroy 한다 → apply 때와 동일한 DR -target 목록만. enable_eks_dr=false 로 DR count=0 → 파괴.
cd "$(git rev-parse --show-toplevel)/infra/terraform/aws"
terraform apply -var enable_eks_dr=false \
  -target=module.eks_dr_vpc -target=module.eks_dr -target=module.eks_dr_ebs_csi_irsa \
  -target=module.eks_dr_alb_irsa -target=aws_security_group.eks_dr_endpoints \
  -target=aws_security_group.eks_dr_bastion -target=module.eks_dr_endpoints \
  -target=module.eks_dr_vault_unseal_irsa -target=aws_iam_policy.eks_dr_vault_unseal \
  -target=aws_iam_policy.eks_dr_cnpg_restore -target=module.eks_dr_cnpg_restore_irsa \
  -target=aws_iam_role.eks_dr_bastion -target=aws_iam_role_policy_attachment.eks_dr_bastion_ssm \
  -target=aws_iam_role_policy.eks_dr_bastion_describe -target=aws_iam_role_policy.eks_dr_bastion_vault_restore \
  -target=aws_iam_instance_profile.eks_dr_bastion -target=aws_instance.eks_dr_bastion

# 6) 고아 검증(전부 0/비어야 함)
aws elbv2 describe-load-balancers --region ap-northeast-2 --query "LoadBalancers[?VpcId=='$VPCID']" --output text
aws ec2 describe-volumes --region ap-northeast-2 --filters "Name=tag:kubernetes.io/cluster/$CLUSTER,Values=owned" --query "Volumes[].VolumeId" --output text
aws ec2 describe-network-interfaces --region ap-northeast-2 --filters "Name=vpc-id,Values=$VPCID" --query "NetworkInterfaces[].NetworkInterfaceId" --output text
aws ec2 describe-security-groups --region ap-northeast-2 --query "SecurityGroups[?starts_with(GroupName,'k8s-')].GroupId" --output text
```
