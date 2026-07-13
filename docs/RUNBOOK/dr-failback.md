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
kubectl --context eks-dr -n api scale deploy/api --replicas=0   # 쓰기 경로 정지(읽기도 멈춤 — 그 창만 다운)
```

### 2. EKS write frontier flush (EKS primary)
```bash
# postgres·keycloak 각각의 primary pod 에서:
kubectl --context eks-dr -n postgres exec -it cledyu-pg-1 -- psql -c "CHECKPOINT; SELECT pg_switch_wal();"
kubectl --context eks-dr -n keycloak exec -it keycloak-pg-1 -- psql -c "CHECKPOINT; SELECT pg_switch_wal();"
# (선택·최적화) 최신 base backup 으로 WAL 재생 단축. delete-first 로 반복 failback 멱등(AlreadyExists 방지).
kubectl --context eks-dr -n postgres delete backup failback-cutover --ignore-not-found
kubectl --context eks-dr -n postgres create -f - <<'YAML'
apiVersion: postgresql.cnpg.io/v1
kind: Backup
metadata: { name: failback-cutover, namespace: postgres }
spec: { cluster: { name: cledyu-pg } }
YAML
```

### 3. 온프렘 recovery — 【승인 게이트: 삭제 전 -dr 건전성 확인 필수】
```bash
N=<진입 epoch 정수>   # = 현재 운영/DR values 의 drEpoch(값 표 참조). 첫 failback 이면 0.
# (a) 선-확인: -dr 아카이브에 base+WAL 존재(불완전하면 stale 삭제 금지 — DB 소실).
aws s3 ls s3://cledyu-lab-dr-backups/postgres-dr/cledyu-pg-dr-e$((N+1))/ --recursive | grep -E 'base|wals' | head
# (b) path-swap: data-postgres-cnpg·data-keycloak-pg 앱 source.path 를 -failback 차트로 (git 커밋).
#     postgres-cnpg → postgres-cnpg-failback, keycloak-pg → keycloak-pg-failback. drEpoch=N 확인.
# (c) stale cluster 삭제(파괴적 — PVC 소멸) → ArgoCD 가 failback 차트로 fresh recovery.
kubectl -n postgres delete cluster cledyu-pg --ignore-not-found
kubectl -n keycloak delete cluster keycloak-pg --ignore-not-found
# (d) recovery 완료 대기.
kubectl -n postgres wait --for=condition=Ready cluster/cledyu-pg --timeout=900s
kubectl -n keycloak wait --for=condition=Ready cluster/keycloak-pg --timeout=900s
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
# (d) 새 epoch anchor 를 결정론적으로 확보 — 명시적 on-demand base backup.
#     ⚠️ 운영 ScheduledBackup(immediate:true)의 즉시백업은 CR '생성' 시에만 발화한다. adopt 시 path-swap
#        prune→recreate 로 발화하긴 하나 그 부수효과에 의존하지 않는다. 아래로 확실히 anchor 를 만든다.
#        (이게 없으면 새 epoch 에 WAL 만 있고 base 가 없어, 그 창에 재해 오면 f(N+1) recovery 가 anchor 없이 실패.)
# delete-first 로 멱등 — CR 이름 고정이라 반복 failback 시 AlreadyExists 방지.
# (Backup CR 삭제는 k8s 리소스만 지우고 S3 base backup 은 남긴다 — 이전 epoch anchor 보존.)
kubectl -n postgres delete backup failback-epoch-anchor --ignore-not-found
kubectl -n postgres create -f - <<'YAML'
apiVersion: postgresql.cnpg.io/v1
kind: Backup
metadata: { name: failback-epoch-anchor, namespace: postgres }
spec: { cluster: { name: cledyu-pg } }
YAML
kubectl -n keycloak delete backup failback-epoch-anchor --ignore-not-found
kubectl -n keycloak create -f - <<'YAML'
apiVersion: postgresql.cnpg.io/v1
kind: Backup
metadata: { name: failback-epoch-anchor, namespace: keycloak }
spec: { cluster: { name: keycloak-pg } }
YAML
# (e) anchor 도달 확인 — 새 epoch 경로에 completed base backup 존재.
kubectl -n postgres wait --for=jsonpath='{.status.phase}'=completed backup/failback-epoch-anchor --timeout=600s
aws s3 ls s3://cledyu-lab-dr-backups/postgres/cledyu-pg-e$((N+1))/base/ | head   # 비어 있으면 anchor 실패 → 다음 failover 불가
```

### 6. 수동 승인 → DNS 원복 — 【승인 게이트: split-brain 단일 권한 스위치】
> 여기서 서비스 권한이 온프렘으로 넘어감 = write-downtime 종료. **var 생략 시 레코드 destroy 주의**.
```bash
cd infra/terraform/aws && terraform apply -var enable_public_ingress=true -target=aws_route53_record.public
```

### 7. 온프렘 앱 재개
```bash
kubectl -n api rollout restart deploy/api && kubectl -n api rollout status deploy/api
kubectl -n web rollout restart deploy/web && kubectl -n web rollout status deploy/web
# Keycloak 은 operator CR(kind: Keycloak, cledyu-keycloak)이 StatefulSet 을 만든다 → deploy 아님.
# DB 재생성으로 커넥션 풀이 끊겼을 수 있어 재기동으로 keycloak-pg-rw 재연결 보장.
kubectl -n keycloak rollout restart statefulset/cledyu-keycloak && kubectl -n keycloak rollout status statefulset/cledyu-keycloak
kubectl -n api logs deploy/api | grep -E "db 연결|in-memory"   # in-memory 폴백 아님 확인
```

### 8. EKS 축소
```bash
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
