# EKS Cold DR 부트스트랩 런북 (Plan B)

> **상태: 작성 중 (Plan B Task 9).** 온프렘 상실 시 EKS 에서 백업으로 과금최소경로를
> 재현하는 수동 부트스트랩 절차. 현재는 **Vault 스냅샷 복원** 섹션만 확정돼 있고,
> 나머지 스텝(terraform apply → 엔드포인트 치환 → CNPG 복원 → 앱 Ready → 검증 → destroy)은
> T9 에서 채운다. 드릴(T10)은 이 문서를 처음부터 끝까지 한 번 완주하는 것.

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

전제: Phase 1 terraform apply 완료(bastion 존재), Vault 앱(T6) sync 됨. 모든 `kubectl`
명령은 **bastion 에서** 실행한다(private 엔드포인트).

```bash
# 0) bastion 진입 + kubeconfig
aws ssm start-session --target <eks_dr_bastion instance id>
aws eks update-kubeconfig --name cledyu-dr --region ap-northeast-2

# 1) Vault 는 KMS auto-unseal 로 "빈 raft" 로 떠 있다(T6). 최초 1회 init 으로 루트토큰 확보.
#    (awskms seal 이라 unseal 키 입력은 불필요 — init 만 하면 자동 unseal 된다.)
kubectl -n vault exec -it vault-0 -- vault operator init
#    → 출력된 Initial Root Token 을 <INIT_ROOT> 로 보관(restore 실행에만 임시 사용).

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
  'VAULT_TOKEN=<INIT_ROOT> vault operator raft snapshot restore -force /tmp/vault-raft.snap'

# 4) force restore 후에는 스냅샷(원본 클러스터)의 recovery 키·루트토큰이 유효해지고
#    init(1단계) 때 받은 <INIT_ROOT> 는 무효화된다. 따라서 이후 인증은 원본 자격으로 한다:
#    원본 root token / recovery keys 는 DR 부트스트랩 시크릿(AWS Secrets Manager
#    `cledyu/vault/bootstrap`)에 보관 — 이걸로 인증하거나 recovery 키로 새 root 를 생성한다.
#      aws secretsmanager get-secret-value --secret-id cledyu/vault/bootstrap
#      vault operator generate-root  (원본 recovery 키 threshold 로)
#    bastion instance profile 에 cledyu/vault/* GetSecretValue 가 있다(eks-dr-bastion.tf
#    aws_iam_role_policy.eks_dr_bastion_vault_restore) — 정적 키 없이 취득 가능.
#    (그 시크릿이 CMK 로 암호화됐다면 롤에 해당 kms:Decrypt 추가 필요 — 코드 주석 참조.)
kubectl -n vault exec -it vault-0 -- sh -c \
  'VAULT_TOKEN=<원본 루트토큰> vault secrets list'   # 복원 확인
```

**복원 후 정합성 체크(다음 스텝의 선행조건):**
- Vault 안에 ESO 가 참조하는 경로(`cledyu/db/*`, `cledyu/oidc/*`, KMS/S3 자격 등)가
  존재해야 → EKS 의 external-secrets 가 api/keycloak 시크릿을 채운다.
- Vault 가 비어 있으면(복원 누락) api 는 in-memory 폴백으로 뜨고 keycloak-pg 자격이
  없어 Keycloak 이 기동 실패한다 → **드릴 실패로 판정**(자동 통과처럼 보이지 않게 주의).

### tmpfs / 잔존 주의

스냅샷은 Vault 전체 시크릿이다. 취득·복사한 로컬 파일(`./vault-raft.snap`,
`/tmp/vault-raft.snap`)은 복원 확인 후 즉시 삭제한다. (온프렘 CronJob 은 스냅샷을 tmpfs 로만
다루고 업로드 후 회수한다 — 수동 복원 경로도 동일 원칙을 지킨다.)

```bash
rm -f ./vault-raft.snap
kubectl -n vault exec -it vault-0 -- rm -f /tmp/vault-raft.snap
```

---

## (T9 잔여) 나머지 부트스트랩 스텝

- [ ] terraform apply (`enable_eks_dr=true`) + `<<...>>` 치환값(`terraform output`) 수집
- [ ] apps-eks root-app 적용 → 플랫폼(cert-manager·ALB·gp3·ESO·CNPG operator) Ready
- [ ] **Vault 스냅샷 복원**(위 섹션)
- [ ] CNPG 복원 차트 sync → `cledyu-pg-rw`·`keycloak-pg-rw` Ready(자동 S3 복원)
- [ ] Keycloak·api·web Ready + ALB/ACM 종단 확인
- [ ] 검증(로컬 테스트유저 로그인·복원 데이터 서빙) + RTO 실측
- [ ] destroy (`enable_eks_dr=false` apply) + 잔존 0 확인
