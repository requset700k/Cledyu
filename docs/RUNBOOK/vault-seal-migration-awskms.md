# Vault seal 마이그레이션 — GCP KMS(gcpckms) → AWS KMS(awskms)

## 목적

Vault auto-unseal 을 GCP Cloud KMS 에서 AWS KMS 로 옮긴다. DR 을 AWS 기반으로 정했고
(`docs/architecture/phases.md` 클라우드 사용 범위), Vault unseal 이 만료형 GCP 크레딧에
의존하면 크레딧 만료/장애 시 재시작한 Vault 가 sealed 되어 복구가 막힌다. 또한 Vault raft
스냅샷은 **찍힐 때의 seal 로만 복원 unseal** 되므로, `#244` 의 Vault 백업이 AWS 자기완결
DR 소스로 유효하려면 이 마이그레이션이 **백업보다 먼저** 끝나야 한다.

> 위험도 높음(시크릿 레이어, raft HA 3노드). 반드시 유지보수 창에서, recovery key 를
> 손에 쥐고, raft 스냅샷을 먼저 뜬 뒤 진행한다. HashiCorp 공식 seal-migration 문서를
> 함께 참조: https://developer.hashicorp.com/vault/docs/concepts/seal#seal-migration

## 현재 상태 (2026-07 기준)

- seal_type = `gcpckms`, 3노드 unsealed, Vault 1.21.2. recovery keys 5 / threshold 3.
- recovery keys 보관: **GCP Secret Manager `cledyu-vault-bootstrap`** (`recovery_keys_b64`).
  GCP 를 벗어나기 전에 반드시 먼저 꺼내둔다(아이러니 포인트).
- ArgoCD `platform-vault` 앱은 **auto-sync(prune+selfHeal)** — 마이그레이션 중에는 반드시
  일시 비활성화한다(아래).

## 준비 완료(이 PR 시점) — 비파괴 부트스트랩

- AWS KMS 대칭키 `alias/cledyu-vault-unseal`
  (`arn:aws:kms:ap-northeast-2:504284203153:key/e29e3ec2-f5e0-4308-af6f-5b576cc99f52`) — **DR-durable, 삭제 금지**(복원 스냅샷 unseal 에 동일 키 필요).
- IAM user `cledyu-vault-unseal` + 최소권한 정책(`kms:Encrypt/Decrypt/DescribeKey` on the key).
- gitops `gitops/apps/vault/values-awskms.yaml` (마이그레이션 단계 values, 저장소에 스테이징만 됨 — 아직 앱에 미연결).

## 사전 조건 (실행 직전 체크)

- [ ] recovery key 5개 확보(threshold 3). `cledyu-vault-bootstrap` 에서 안전 채널로 취득, 화면/로그/PR 에 남기지 않는다.
- [ ] raft 스냅샷 백업 완료(아래 3단계).
- [ ] 유지보수 창 공지, 롤백 절차 숙지.
- [ ] AWS IAM access key 발급 준비(마이그레이션 시작 시 생성).

## 실행 절차 (유지보수 창)

모든 `vault` 명령은 pod 안에서:
`kubectl -n vault exec vault-0 -- sh -c 'VAULT_ADDR=https://127.0.0.1:8200 VAULT_SKIP_VERIFY=true vault <...>'`

### 1) AWS creds 시크릿 생성 (unseal 은 ESO 부트스트랩 못 씀 — 직접 생성)

```bash
export AWS_PROFILE=cledyu AWS_REGION=ap-northeast-2
# access key 발급 (출력을 안전 채널로만 취급)
aws iam create-access-key --user-name cledyu-vault-unseal
# 위 AccessKeyId/SecretAccessKey 로 k8s secret 생성
kubectl -n vault create secret generic vault-aws-kms-creds \
  --from-literal=AWS_ACCESS_KEY_ID='<AccessKeyId>' \
  --from-literal=AWS_SECRET_ACCESS_KEY='<SecretAccessKey>'
```

### 2) ArgoCD auto-sync 일시 비활성 (제어된 재시작)

```bash
kubectl -n argocd patch application platform-vault --type merge \
  -p '{"spec":{"syncPolicy":{"automated":null}}}'
```

### 3) raft 스냅샷 백업 (롤백 최후 보루)

```bash
kubectl -n vault exec vault-0 -- sh -c \
  'VAULT_ADDR=https://127.0.0.1:8200 VAULT_SKIP_VERIFY=true VAULT_TOKEN=<admin-token> \
   vault operator raft snapshot save /tmp/pre-awskms.snap'
kubectl -n vault cp vault-0:/tmp/pre-awskms.snap ./pre-awskms-$(date +%Y%m%d).snap
```
(admin 토큰은 vault-bootstrap.md 의 break-glass 경로로 단기 발급 후 폐기.)

### 4) 마이그레이션 config 적용 (values-awskms.yaml)

`gitops/argocd/apps/platform-vault.yaml` 의 valueFiles 를 `values-gcpckms.yaml` →
`values-awskms.yaml` 로 교체하고 ArgoCD 로 **config 리소스만** 동기화한다(ConfigMap `vault-config` 에
seal "awskms"(active) + seal "gcpckms"{disabled}, StatefulSet 에 AWS creds env 반영). Vault 의
StatefulSet 은 **updateStrategy=OnDelete** 라 config 만 바뀌고 파드는 자동 재시작되지 않는다 —
다음 단계에서 직접 지운다. **`-migrate` 서버 플래그는 존재하지 않는다**(마이그레이션은 config-driven).

### 5) all-at-once 재기동 → 노드별 unseal-migrate (로컬 3노드 raft 재현으로 검증됨)

> **검증 결과(1.21.2 OSS, raft)**: 새 config 로 재기동하면 각 노드가 "seal migration mode"로
> **sealed** 대기하고(자동 unseal 안 됨), **각 노드마다** recovery key threshold(3)로
> `vault operator unseal -migrate` 를 넣어야 한다(leader 만으로는 follower 가 auto-unseal 안 됨).
> raft 특성상 전체를 잠깐 함께 내렸다 올리는 게 안전 — **짧은 Vault 전면 다운타임 발생**(ESO 캐시된
> 시크릿은 유지되나 Vault API·신규 시크릿은 그동안 불가).

1. **3노드 파드를 함께 재시작**(전면 다운타임 시작). StatefulSet(OnDelete)이 새 config 로 재생성:
   ```bash
   kubectl -n vault delete pod vault-0 vault-1 vault-2
   ```
   세 파드 모두 seal migration mode = **sealed** 로 뜬다(`vault status` 로 확인).
2. **각 노드에 recovery key threshold(3)로 unseal-migrate**(recovery key 는 GCP SM
   `cledyu-vault-bootstrap` 에서 취득, 값을 로그/히스토리에 남기지 않는다):
   ```bash
   for p in vault-0 vault-1 vault-2; do
     # <share-1..3> 을 각각 넣는다 (3개 다 넣어야 그 노드가 unseal)
     kubectl -n vault exec $p -- sh -c 'VAULT_ADDR=https://127.0.0.1:8200 VAULT_SKIP_VERIFY=true vault operator unseal -migrate <recovery-key-share-1>'
     kubectl -n vault exec $p -- sh -c 'VAULT_ADDR=https://127.0.0.1:8200 VAULT_SKIP_VERIFY=true vault operator unseal -migrate <recovery-key-share-2>'
     kubectl -n vault exec $p -- sh -c 'VAULT_ADDR=https://127.0.0.1:8200 VAULT_SKIP_VERIFY=true vault operator unseal -migrate <recovery-key-share-3>'
   done
   ```
3. 재기동 직후 raft leader 재선출로 잠깐 "no active node" 가 보일 수 있다 — 세 노드가 모두
   unseal-migrate 되면 클러스터가 재형성되고 leader 가 선출된다(6단계로 확인).

### 6) 검증

```bash
for p in vault-0 vault-1 vault-2; do
  kubectl -n vault exec $p -- sh -c 'VAULT_ADDR=https://127.0.0.1:8200 VAULT_SKIP_VERIFY=true vault status -format=json' \
    | python3 -c "import json,sys;d=json.load(sys.stdin);print('$p', d['type'], 'sealed=',d['sealed'])"
done
# 기대: 3노드 모두 type=awskms, sealed=False
```

### 7) auto-sync 재활성

```bash
kubectl -n argocd patch application platform-vault --type merge \
  -p '{"spec":{"syncPolicy":{"automated":{"prune":true,"selfHeal":true}}}}'
```

## 롤백

마이그레이션이 실패/불안정하면:
1. valueFiles 를 `values-gcpckms.yaml`(gcpckms 단독)로 되돌린다.
2. 파드 재시작 → gcpckms 로 다시 auto-unseal(기존 seal 그대로라 복구됨).
3. 그래도 안 되면 3단계 raft 스냅샷으로 복원(신규 Vault 에 snapshot restore, gcpckms 로 unseal).
4. GCP creds/키는 롤백 완료 전까지 삭제하지 않는다.

## 사후 정리 (마이그레이션 안정 후 별도 PR)

- [ ] `values-awskms.yaml` 에서 seal "gcpckms" { disabled } 블록 제거 (awskms 단독)
- [ ] GCP creds(gcp-kms 볼륨/마운트/`GOOGLE_APPLICATION_CREDENTIALS`) 제거
- [ ] k8s Secret `vault-gcp-kms-creds` 삭제
- [ ] GCP KMS key(`cledyu-vault-keyring/vault-unseal-key`) + SA `vault-unseal-sa` 삭제(스케줄)
- [ ] **recovery key 백업을 GCP Secret Manager 밖(AWS Secrets Manager 등)으로 이관** — 그러지 않으면 break-glass 가 여전히 GCP 에 종속
- [ ] `values-gcpckms.yaml`/`.example` 은 이력용으로 두거나 삭제

정리 완료 시점에 비로소 "Vault unseal 이 AWS 자기완결"이 성립한다.
