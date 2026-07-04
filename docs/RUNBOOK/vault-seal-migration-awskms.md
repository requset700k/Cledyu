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

> **마이그레이션 완료(2026-07-04)**: 3노드 모두 seal_type = `awskms`, unsealed.
> 아래 "실행 절차"는 실제 수행된 이력 기록이다. 남은 GCP 이탈 정리는 "사후 정리" 참조.

- seal_type = `awskms`(완료), 3노드 unsealed, Vault 1.21.2. recovery keys 5 / threshold 3.
- recovery keys 보관: **AWS Secrets Manager `cledyu/vault/bootstrap`** (2026-07-04 이관 완료,
  break-glass AWS-네이티브). GCP SM `cledyu-vault-bootstrap` 원본은 이중 백업으로 유지.
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

   > 보안: recovery key 를 **명령 인자로 넘기지 않는다**. 인자로 주면 share 가 shell history,
   > 터미널 로그, `kubectl exec` args, k8s audit log 에 남는다. `-migrate` 를 key 인자 없이
   > 실행하면 `Unseal Key (will be hidden):` 프롬프트가 뜨므로 **TTY(`-it`)로 붙어 한 share 씩
   > 붙여넣는다**(입력은 에코되지 않는다). 노드마다 threshold(3)개를 넣는다.
   ```bash
   for p in vault-0 vault-1 vault-2; do
     # 프롬프트에 recovery key share 를 한 개씩 붙여넣는다 (3개 다 넣어야 그 노드가 unseal).
     # 인자로 주지 말 것 — history/audit 유출. TTY 프롬프트 입력은 에코되지 않는다.
     for i in 1 2 3; do
       kubectl -n vault exec -it $p -- sh -c \
         'VAULT_ADDR=https://127.0.0.1:8200 VAULT_SKIP_VERIFY=true vault operator unseal -migrate'
     done
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

> **중요(마이그레이션 완료 후 barrier 상태)**: 마이그레이션이 끝나면 root key(barrier)는 이미
> **awskms 로 재래핑**되어 있다. 따라서 `values-gcpckms.yaml`(gcpckms 단독)로 되돌려 재기동하면
> **unseal 되지 않는다**(gcpckms 는 더 이상 현재 barrier 를 풀 키가 아니다). gcpckms 단독 복귀는
> **마이그레이션이 아직 barrier 를 재래핑하기 전(= all-at-once 재기동 후 unseal-migrate 완료 전)**
> 에만 유효하다. 시점별로 롤백이 다르다.

**(가) 마이그레이션 진행 중(unseal-migrate 완료 전) 실패** — barrier 아직 gcpckms:
1. valueFiles 를 `values-gcpckms.yaml`(gcpckms 단독)로 되돌린다.
2. 파드 재시작 → gcpckms 로 다시 auto-unseal(기존 seal 그대로라 복구됨).

**(나) 마이그레이션 완료 후 awskms unseal 이 불안정** — barrier 는 awskms:
1. 대개 원인은 AWS creds/키(`vault-aws-kms-creds`, KMS 키 권한)다. **awskms 설정을 유지**한 채
   creds/권한을 고쳐 재기동한다(gcpckms 단독으로 되돌리지 말 것 — sealed 고착).
2. 굳이 GCP 로 되돌리려면 **역마이그레이션**(gcpckms 를 new active + awskms 를 `disabled` 로
   두고 노드별 `unseal-migrate`)을 5단계와 대칭으로 수행한다. 단순 config 교체가 아니다.

**(다) 최후 보루 — pre-migration raft 스냅샷 복원**(3단계 `pre-awskms-*.snap`):
- 이 스냅샷은 **gcpckms 로 래핑된 시점**이므로, `values-gcpckms.yaml`(gcpckms) 설정의 새 Vault 에
  restore 해야 gcpckms 로 unseal 된다. **데이터는 스냅샷 시점으로 롤백**(그 이후 쓰기 손실).
- GCP creds/키(gcpckms) 는 이 복원 경로 때문에 **유예 기간 동안 삭제 금지**(사후 정리 C 참조).

## 사후 정리 — GCP 이탈 완성 (마이그레이션 완료 후)

awskms 가 이미 active auto-unseal 이므로, 마이그레이션과 달리 **전면 다운타임이 필요 없다**.
파드를 1개씩 재시작하면 각 노드가 awskms 로 자동 unseal 되어 raft 에 재합류한다(quorum 유지).

### A. GitOps 변경 (이 PR)

- [x] `values-awskms.yaml` 에서 seal "gcpckms" { disabled } 블록 제거 (awskms 단독)
- [x] GCP creds(gcp-kms 볼륨/마운트/`GOOGLE_APPLICATION_CREDENTIALS`) 제거
- [x] 런북 recovery key 입력을 TTY 프롬프트(stdin)로 수정 — 인자 노출 제거

### B. 롤링 적용 (무중단) — 머지 후

ArgoCD 가 awskms-단독 ConfigMap 을 스테이징(OnDelete → 자동 재시작 안 됨). 파드를 **1개씩**
지우고 각 노드가 unsealed + Ready 로 복귀할 때까지 기다린 뒤 다음 노드로 넘어간다:

```bash
for p in vault-2 vault-1 vault-0; do   # follower 부터, active(vault-0) 마지막
  kubectl -n vault delete pod $p
  # delete 반환 직후엔 StatefulSet 이 동명 파드를 아직 안 만들었을 수 있다 —
  # Ready 대기 전에 재생성부터 기다린다(안 그러면 NotFound 로 즉시 실패).
  kubectl -n vault wait --for=create pod/$p --timeout=60s 2>/dev/null \
    || until kubectl -n vault get pod/$p >/dev/null 2>&1; do sleep 2; done
  kubectl -n vault wait --for=condition=Ready pod/$p --timeout=180s
  kubectl -n vault exec $p -- sh -c \
    'VAULT_ADDR=https://127.0.0.1:8200 VAULT_SKIP_VERIFY=true vault status -format=json' \
    | python3 -c "import json,sys;d=json.load(sys.stdin);print('$p',d['type'],'sealed=',d['sealed'])"
done
# 기대: 각 노드 type=awskms, sealed=False (recovery key 입력 없이 auto-unseal)
```
acid test: GCP creds 가 config 에서 사라졌는데도 파드가 unsealed 로 복귀 = **GCP 독립 실증**.

### C. 자원 회수 (B 검증 후) — 2026-07-04 진행

- [x] k8s Secret `vault-gcp-kms-creds` 삭제(`kubectl -n vault delete secret vault-gcp-kms-creds`).
      StatefulSet·파드·ESO 어디에도 미참조 확인 후 삭제, Vault awskms/unsealed 정상.
- [x] **recovery key 백업을 AWS Secrets Manager 로 이관 완료** —
      `cledyu/vault/bootstrap`(arn `...:secret:cledyu/vault/bootstrap`, 504284203153/ap-northeast-2).
      이로써 generate-root break-glass 가 AWS-네이티브. GCP SM `cledyu-vault-bootstrap` 원본은
      이중 백업으로 유지(제거는 선택).
- [ ] GCP KMS key(`cledyu-vault-keyring/vault-unseal-key`) + SA `vault-unseal-sa` — **보류(유지)**.
      pre-migration raft 스냅샷(gcpckms 래핑) 복원의 유일 수단이라 dormant 로 두고 **프로젝트
      종료(2026-07-22) teardown 때 함께 정리**. 지금 삭제 이득 없음.
- [x] `values-gcpckms.yaml`/`.example` 제거(죽은 파일, 어떤 ArgoCD 앱도 미참조).

C 완료(KMS 키 보류 제외)로 **"Vault unseal + break-glass 가 AWS 자기완결"** 성립. 잔여 GCP
종속은 (1) GCP KMS 키(구 스냅샷 복원용, 의도적 보류) (2) GCP SM recovery key 원본(이중 백업).
