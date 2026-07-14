# DR Failback 런북 — 온프렘 복귀 (split-brain 방지·무손실)

> 온프렘 복구 후 서비스 권한을 EKS→온프렘으로 되돌린다. **자동 failback 없음** — 각 스텝 수동 승인.
> 데이터는 S3 barman 루프백으로 역복제(설계: docs/superpowers/specs/2026-07-13-dr-failback-reconciliation-design.md).
> 전제: failover 시 real-DR 캡처(dr-eks-bootstrap.md §real-DR)로 `-dr/` 에 base+WAL 축적됨. 진입 epoch=N.

## 값 표 (이번 사이클)
| 파일 | 이번 값 | failback 완료 후 |
|---|---|---|
| postgres-cnpg/values·keycloak-pg/values (운영) | drEpoch=N | drEpoch=N+1 |
| postgres-cnpg-dr/values·keycloak-pg-dr/values | drEpoch=N, backupEnabled=true | drEpoch=N+1, backupEnabled=false |
| postgres-cnpg-failback/values·keycloak-pg-failback/values | drEpoch=N | drEpoch=N+1 |

## 절차

### 0. 전제 확인
- 온프렘 인프라 정상(k3s·Vault unseal·ESO·cnpg-operator Running) + 하트비트 재개.
- EKS 여전히 서빙 중(api/app/auth → EKS ALB). 온프렘 앱은 미서빙(scale-0) 유지.

### 1. EKS 쓰기 quiesce (계획된 write-downtime 시작) — 【승인 게이트】
> 이후 새 쓰기를 막아 recovery 데이터셋을 고정한다. 없으면 flush~DNS전환 사이 EKS 쓰기가 소실.
```bash
# api(진도·과금 쓰기)와 keycloak(로그인·세션·계정 쓰기) 양쪽 쓰기경로를 모두 정지 — 둘 다 failback 대상이라
# 하나라도 열려 있으면 flush(step 2) 이후 그 DB 에 새 쓰기가 생겨 온프렘 recovery 데이터셋에 안 들어가고 소실된다(무손실 계약 위반).
#
# ⚠️ selfHeal 선정지(필수) — eks-service-api·eks-service-keycloak Application 은 automated.selfHeal=true 라
#    (service-api.yaml·service-keycloak.yaml), 아래 scale-0/instances-0 을 ArgoCD 가 즉시 desired 로 되돌려
#    quiesce 가 안 걸린다 → flush 후에도 쓰기가 다시 열려 recovery 데이터셋에 누락(무손실 계약 위반). 앱별
#    selfHeal 을 꺼도 root-app 이 되살리므로, application-controller 를 scale 0 으로 내려 selfHeal 엔진 자체를
#    멈춘다(dr-eks-bootstrap.md §destroy step0 과 동일 패턴). EKS 는 §8 에서 축소되므로 복원 불요.
kubectl --context eks-dr -n argocd scale statefulset argocd-application-controller --replicas=0
kubectl --context eks-dr -n argocd rollout status statefulset argocd-application-controller --timeout=60s
kubectl --context eks-dr -n api scale deploy/api --replicas=0   # 진도/과금 쓰기 정지
# Keycloak 은 operator CR(instances)로 SS 를 관리하므로 SS 직접 scale 은 되돌려진다 → CR instances=0 으로 정지.
kubectl --context eks-dr -n keycloak patch keycloak cledyu-keycloak --type merge -p '{"spec":{"instances":0}}'   # auth(로그인/계정) 쓰기 정지
kubectl --context eks-dr -n keycloak rollout status statefulset/cledyu-keycloak --timeout=120s 2>/dev/null || true
```

### 2. EKS write frontier flush + S3 도달 대기 (EKS primary) — 【승인 게이트】
> ⚠️ `pg_switch_wal()` 은 세그먼트 **전환만** 트리거하고 barman 의 S3 업로드 완료를 보장하지 않는다. flush 직후
> recovery(step3)를 시작하면 마지막 WAL 이 아직 `postgres-dr/`·`keycloak-dr/` 에 없어 PostgreSQL 이 그 이전을
> end-of-archive 로 보고 승격 → quiesce 직전 쓰기가 누락된다. 따라서 **on-demand Backup(quiesce 상태 base)을
> 양쪽 다 만들고 `completed` 까지 대기**해, flush 데이터가 S3(-dr)에 확실히 도달한 뒤에만 step3 로 넘어간다.
```bash
# (a) postgres·keycloak primary 에서 flush.
kubectl --context eks-dr -n postgres exec -it cledyu-pg-1  -- psql -c "CHECKPOINT; SELECT pg_switch_wal();"
kubectl --context eks-dr -n keycloak exec -it keycloak-pg-1 -- psql -c "CHECKPOINT; SELECT pg_switch_wal();"
# (b) quiesce 상태 최종 base backup 생성(양쪽 필수·대칭). delete-first 로 반복 failback 멱등(AlreadyExists 방지).
kubectl --context eks-dr -n postgres delete backup failback-cutover --ignore-not-found
kubectl --context eks-dr -n postgres create -f - <<'YAML'
apiVersion: postgresql.cnpg.io/v1
kind: Backup
metadata: { name: failback-cutover, namespace: postgres }
spec: { cluster: { name: cledyu-pg } }
YAML
kubectl --context eks-dr -n keycloak delete backup failback-cutover --ignore-not-found
kubectl --context eks-dr -n keycloak create -f - <<'YAML'
apiVersion: postgresql.cnpg.io/v1
kind: Backup
metadata: { name: failback-cutover, namespace: keycloak }
spec: { cluster: { name: keycloak-pg } }
YAML
# (c) 두 backup 모두 completed = flush 데이터가 -dr S3 에 도달. 이 대기 통과 전엔 step3 recovery 시작 금지.
kubectl --context eks-dr -n postgres  wait --for=jsonpath='{.status.phase}'=completed backup/failback-cutover --timeout=900s
kubectl --context eks-dr -n keycloak wait --for=jsonpath='{.status.phase}'=completed backup/failback-cutover --timeout=900s
```

### 3. 온프렘 recovery — 【승인 게이트: 삭제 전 -dr 건전성 확인 필수】
```bash
N=<진입 epoch 정수>   # = 현재 운영/DR values 의 drEpoch(값 표 참조). 첫 failback 이면 0.
# (a) 선-확인: postgres·keycloak **둘 다** -dr 아카이브에 base+WAL 존재해야 진행. 하나라도 비면 그 DB 는
#     (c) stale 삭제 금지 — 해당 -dr 복원 소스가 없어 삭제 시 DB 영구 소실(대칭 필수, keycloak 도 삭제 대상이므로).
aws s3 ls s3://cledyu-lab-dr-backups/postgres-dr/cledyu-pg-dr-e$((N+1))/   --recursive | grep -E 'base|wals' | head
aws s3 ls s3://cledyu-lab-dr-backups/keycloak-dr/keycloak-pg-dr-e$((N+1))/ --recursive | grep -E 'base|wals' | head
# (b) path-swap: data-postgres-cnpg·data-keycloak-pg 앱 source.path 를 -failback 차트로 (git 커밋).
#     postgres-cnpg → postgres-cnpg-failback, keycloak-pg → keycloak-pg-failback. drEpoch=N 확인.
# (c) stale cluster 삭제(파괴적 — PVC 소멸) → ArgoCD 가 failback 차트로 fresh recovery.
#     ⚠️ --context onprem 필수 — step 1·2 가 --context eks-dr 를 썼으므로 current-context 가 eks-dr 이면
#        이 delete 가 서빙 중인 EKS primary(cledyu-pg/keycloak-pg)를 지워 DR 서비스 DB 를 내린다. 반드시 온프렘 고정.
kubectl --context onprem -n postgres delete cluster cledyu-pg --ignore-not-found
kubectl --context onprem -n keycloak delete cluster keycloak-pg --ignore-not-found
# (d) recovery 완료 대기.
kubectl --context onprem -n postgres wait --for=condition=Ready cluster/cledyu-pg --timeout=900s
kubectl --context onprem -n keycloak wait --for=condition=Ready cluster/keycloak-pg --timeout=900s
```

### 4. 데이터 정합 체크 — 【승인 게이트: 불일치 시 cutover 중단】
```bash
# EKS vs 온프렘 핵심 테이블 대조(무손실 실증). 불일치면 quiesce 유지·원인 규명.
# ⚠️ -d cledyu 필수 — api 테이블은 cledyu DB(기본 postgres DB 아님).
for ctx in eks-dr onprem; do
  echo "== $ctx =="
  kubectl --context $ctx -n postgres exec cledyu-pg-1 -- psql -d cledyu -tAc \
    "SELECT count(*) FROM lab_completions; SELECT count(*) FROM session_progress; SELECT max(updated_at) FROM session_progress;"
done
# keycloak user count 대조(-d keycloak).
for ctx in eks-dr onprem; do
  echo "== $ctx =="
  kubectl --context $ctx -n keycloak exec keycloak-pg-1 -- psql -d keycloak -tAc "SELECT count(*) FROM user_entity;"
done
```

### 5. drEpoch bump + adopt — 【승인 게이트】
```bash
# (a) lockstep bump: 운영·DR·failback 6개 values drEpoch N→N+1 + DR backupEnabled true→false 를 한 커밋.
#     사후 가드: grep -rn 'drEpoch:' gitops/apps/postgres-cnpg*/ gitops/apps/keycloak-pg*/ → 전부 N+1 동일 확인.
# (b) path-swap 원복: data-postgres-cnpg·data-keycloak-pg source.path 를 운영 차트로. (git 커밋)
# (c) adopt: 운영 차트 재-sync. cledyu-pg 는 이미 존재 → bootstrap 재실행 없음. backup serverName=cledyu-pg-e{N+1}
#     로 전진 아카이빙(WAL) 개시. ArgoCD OutOfSync(bootstrap diff) 나면 ignoreDifferences 검토(R1).
# (d) 새 epoch anchor 를 결정론적으로 확보 — 명시적 on-demand base backup. **(c) adopt sync 완료 후 실행** —
#     그래야 cluster backup 이 새 epoch serverName(cledyu-pg-e{N+1})을 가리켜 base backup 이 올바른 경로로 간다.
#     ⚠️ 운영 ScheduledBackup(immediate:true)의 즉시백업은 CR '생성' 시에만 발화한다. adopt 시 path-swap
#        prune→recreate 로 발화하긴 하나 그 부수효과에 의존하지 않는다. 아래로 확실히 anchor 를 만든다.
#        (이게 없으면 새 epoch 에 WAL 만 있고 base 가 없어, 그 창에 재해 오면 f(N+1) recovery 가 anchor 없이 실패.)
# delete-first 로 멱등 — CR 이름 고정이라 반복 failback 시 AlreadyExists 방지.
# (Backup CR 삭제는 k8s 리소스만 지우고 S3 base backup 은 남긴다 — 이전 epoch anchor 보존.)
# ⚠️ 아래 anchor 생성/대기는 전부 온프렘 대상 → --context onprem 명시(step3 와 동일 사유).
kubectl --context onprem -n postgres delete backup failback-epoch-anchor --ignore-not-found
kubectl --context onprem -n postgres create -f - <<'YAML'
apiVersion: postgresql.cnpg.io/v1
kind: Backup
metadata: { name: failback-epoch-anchor, namespace: postgres }
spec: { cluster: { name: cledyu-pg } }
YAML
kubectl --context onprem -n keycloak delete backup failback-epoch-anchor --ignore-not-found
kubectl --context onprem -n keycloak create -f - <<'YAML'
apiVersion: postgresql.cnpg.io/v1
kind: Backup
metadata: { name: failback-epoch-anchor, namespace: keycloak }
spec: { cluster: { name: keycloak-pg } }
YAML
# (e) anchor 도달 확인 — 새 epoch 경로에 completed base backup 존재. postgres·keycloak 대칭 필수:
#     한쪽이라도 base 없이 진행하면 drEpoch bump 후 그 DB 의 새 epoch 는 WAL 만 있어 다음 failover recovery 가 실패한다.
kubectl --context onprem -n postgres  wait --for=jsonpath='{.status.phase}'=completed backup/failback-epoch-anchor --timeout=600s
kubectl --context onprem -n keycloak wait --for=jsonpath='{.status.phase}'=completed backup/failback-epoch-anchor --timeout=600s
aws s3 ls s3://cledyu-lab-dr-backups/postgres/cledyu-pg-e$((N+1))/base/   | head   # 비어 있으면 postgres anchor 실패 → 다음 failover 불가
aws s3 ls s3://cledyu-lab-dr-backups/keycloak/keycloak-pg-e$((N+1))/base/ | head   # 비어 있으면 keycloak anchor 실패 → 다음 failover 불가
```

### 6. 수동 승인 → DNS 원복 — 【승인 게이트: split-brain 단일 권한 스위치】
> 여기서 서비스 권한이 온프렘으로 넘어감 = write-downtime 종료. **var 생략 시 레코드 destroy 주의**.
```bash
cd infra/terraform/aws && terraform apply -var enable_public_ingress=true -target=aws_route53_record.public
```

### 7. 온프렘 앱 재개
> DNS 가 온프렘을 가리킨 뒤(step6) 재기동해야 api 의 startup-1회 OIDC discovery(issuer https://auth.cledyu.com)가
> 온프렘 Keycloak 으로 해석된다(그 전 재기동 시 죽은/quiesce 된 EKS auth 로 붙어 영구 degraded).
```bash
# 전제: 온프렘 앱이 step0 에서 scale-0/ArgoCD suspend 로 눌려 있었으면 먼저 desired 로 복구해야 한다
#       (rollout restart 는 replicas=0 을 올리지 않는다). ArgoCD 자동 sync 상태면 이미 desired 로 떠 있다.
#   예) kubectl --context onprem -n api scale deploy/api --replicas=<desired>  (web·keycloak instances 도 동일)
# 전부 온프렘 대상 → --context onprem 고정(EKS 는 아직 살아있으므로 context 혼동 시 엉뚱한 사이트 재기동).
kubectl --context onprem -n api rollout restart deploy/api && kubectl --context onprem -n api rollout status deploy/api
kubectl --context onprem -n web rollout restart deploy/web && kubectl --context onprem -n web rollout status deploy/web
# Keycloak 은 operator CR(kind: Keycloak, cledyu-keycloak)이 StatefulSet 을 만든다 → deploy 아님.
# DB 재생성으로 커넥션 풀이 끊겼을 수 있어 재기동으로 keycloak-pg-rw 재연결 보장.
kubectl --context onprem -n keycloak rollout restart statefulset/cledyu-keycloak && kubectl --context onprem -n keycloak rollout status statefulset/cledyu-keycloak
kubectl --context onprem -n api logs deploy/api | grep -E "db 연결|in-memory"   # in-memory 폴백 아님 확인
```

### 8. EKS 축소
```bash
# [P1] 노드그룹은 EKS 모듈이 desired_size 를 최초 생성값으로만 쓰고 이후 변경을 ignore_changes 한다
#   (eks-dr.tf:72). 따라서 terraform 의 eks_dr_node_desired=0 로는 기존 노드가 안 줄어든다 → DNS 원복돼도
#   DR 노드가 desired=3/running 으로 남아 과금·stale workload 유지. CLI 로 먼저 0 으로 내린다.
# ⚠️ 노드 0 전에 in-cluster 정리(ArgoCD selfHeal 정지 → Ingress→ALB, PVC→EBS, ENI) 필수 — 안 하면
#   ALB/gp3 EBS 가 고아로 남아 과금·삭제 블록. 상세는 dr-eks-bootstrap.md §failback / §destroy(step 0~4.5).
NG=$(aws eks list-nodegroups --cluster-name cledyu-dr --region ap-northeast-2 --query 'nodegroups[0]' --output text)
aws eks update-nodegroup-config --cluster-name cledyu-dr --region ap-northeast-2 \
  --nodegroup-name "$NG" --scaling-config minSize=0,maxSize=6,desiredSize=0
aws eks wait nodegroup-active --cluster-name cledyu-dr --region ap-northeast-2 --nodegroup-name "$NG"

cd infra/terraform/aws && terraform apply \
  -var enable_eks_dr=true -var eks_dr_active=false -var eks_dr_node_desired=0 \
  -target=module.eks_dr_vpc -target=module.eks_dr -target=module.eks_dr_ebs_csi_irsa \
  -target=module.eks_dr_alb_irsa -target=aws_security_group.eks_dr_endpoints \
  -target=aws_security_group.eks_dr_bastion -target=module.eks_dr_endpoints \
  -target=module.eks_dr_vault_unseal_irsa -target=aws_iam_policy.eks_dr_vault_unseal \
  -target=aws_iam_policy.eks_dr_cnpg_restore -target=module.eks_dr_cnpg_restore_irsa \
  -target=aws_iam_role.eks_dr_bastion -target=aws_iam_role_policy_attachment.eks_dr_bastion_ssm \
  -target=aws_iam_role_policy.eks_dr_bastion_describe -target=aws_iam_role_policy.eks_dr_bastion_vault_restore \
  -target=aws_iam_instance_profile.eks_dr_bastion -target=aws_instance.eks_dr_bastion
# ⚠️ enable_eks_dr=true 필수 — 생략 시 warm 컨트롤플레인까지 destroy. (목록은 dr-eks-bootstrap.md §Phase1 과 동일)
```

### 9. 사후 확인
- 온프렘 연속 아카이빙(새 epoch `postgres/cledyu-pg-e{N+1}`) 재개·RPO 정상.
- 로그인·진도 서빙 정상. `[P1b]` 용 warm etcd stale CNPG CR 은 다음 failover 가 처리.
