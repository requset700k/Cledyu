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

> **⚠️ terraform 만으로는 노드가 안 내려간다 — CLI 로 먼저 축소한다**(2026-07-15 도출·2026-07-16 실측,
> 설계 스펙 `2026-07-15-dr-discord-approval-orchestration-design.md` §11.15).
> 아래 apply 의 `-var eks_dr_node_desired=0` 은 **모듈이 무시한다**(`.terraform/modules/eks_dr/modules/
> eks-managed-node-group/main.tf:476-481` → `ignore_changes = [scaling_config[0].desired_size]`).
> `eks_dr_active=false` 도 NAT·엔드포인트·bastion 만 게이트하고(eks-dr.tf:4) **노드그룹은 warm 소속이라
> 안 건드린다.** → terraform 은 "변경 없음"으로 **성공 보고**하는데 **m6i.xlarge 3대가 영영 계속 돈다
> (≈ $517/월).** 페일오버 `[4]` 가 노드를 **CLI 로** 올리는 바로 그 이유(ignore_changes)가 내릴 때도
> 똑같이 적용된다 — **올릴 때 CLI 면 내릴 때도 CLI 여야 대칭이 맞는다.** 그래서 아래 step 8.0 을 둔다.
>
> ⚠️ **이건 RTO 와 무관하다.** 노드 축소는 failback(재해 종료 후 복귀)이고, 그 시점엔 서비스가 이미
> DNS 로 온프렘에 넘어가 있다(§7). 축소 중 다운타임은 0 이며, 아래 ~15분은 아무도 기다리지 않는다.
> **속도가 아니라 "명령이 씹혀 영영 안 내려가는 것"을 고치는 것**이 이 step 의 목적이다.

**8.1) in-cluster 정리 (노드 살아있을 때 — 순서 절대 준수)**

> ⚠️ **노드보다 먼저 이걸 한다.** 노드를 먼저 0 으로 내리면 ALB 컨트롤러·EBS CSI 컨트롤러가 함께
> 죽어 **스스로 정리할 주체가 없어진다** → DR ALB·gp3 EBS 가 고아로 남는다(2026-07-16 실측: 노드부터
> 내렸다가 ALB 1·EBS 11 고아 발생, 노드를 도로 올려 정상 정리). **in-cluster 정리 → 8.0 축소 →
> 8.2 terraform** 순이다.

`dr-eks-bootstrap.md §destroy` 의 **0)~4.5) 스텝을 그대로 수행**한다(terraform destroy 는 아님 —
in-cluster 정리만). 요지·실측 함정만 옮기면:
- **0) ArgoCD selfHeal 정지** — `kubectl -n argocd scale statefulset argocd-application-controller --replicas=0`.
  안 끄면 지운 Ingress/PVC 를 즉시 되살려 ALB/EBS 삭제가 안 끝난다. (`patch --all` 은 미지원, 앱별
  loop 는 root-app 이 되살림 → **컨트롤러 scale 0** 만이 확실 — §destroy 실측.)
- **1) PVC 를 문 워크로드 종료** — vault StatefulSet · CNPG Cluster · **Kafka 는 `kafkas` + `kafkanodepool`
  둘 다**(NodePool 을 안 지우면 StrimziPodSet 브로커가 남아 PVC 가 Terminating 고착). 파드 빠질 때까지 대기.
- **2) `delete ingress -A --all`** → 컨트롤러가 ALB/TG/SG 정리. DR VPC 의 ALB 가 0 될 때까지 대기.
- **3) `delete pvc -A --all`** → 마운트 없어 즉시 → EBS CSI 가 gp3 볼륨 삭제. `get pv` 빌 때까지 대기.
- **4.5) 고아 ENI(`aws-K8S-*`, available) 삭제** — 8.0 으로 노드 빠진 **뒤** 생긴다. 안 지우면
  서브넷/VPC 삭제를 `DependencyViolation` 으로 막는다.

**8.0) 노드 N→0 (CLI — in-cluster 정리 후, terraform 앞)**
```bash
NG=$(aws eks list-nodegroups --cluster-name cledyu-dr --region ap-northeast-2 --query 'nodegroups[0]' --output text)
aws eks update-nodegroup-config --cluster-name cledyu-dr --region ap-northeast-2 \
  --nodegroup-name "$NG" --scaling-config minSize=0,maxSize=6,desiredSize=0

# ── 15분 꼬리 제거: 가드 둘 뒤에만 마지막 노드를 강제 종료 ──────────────────────────────
# ⚠️ 마지막 1대는 kube-system PDB(coredns·ebs-csi `maxUnavailable=1`)가 막아 드레인 타임아웃
#    (~15분, 2026-07-16 실측 15분25초)까지 안 빠진다. failback 은 서비스가 이미 온프렘(§7)이라
#    **그 coredns 를 의존하는 것이 없으므로** 강제 종료해도 안전하다. 단 **가드 없이 강제하면 위험**하다
#    (워크로드가 살아있는 상황에서 돌면 실사용 중인 coredns 를 죽인다) → 아래 2가드 뒤에만 강제한다.
export HOME=/root; aws eks update-kubeconfig --name cledyu-dr --region ap-northeast-2

# 가드 A: 8.1 이 실제로 끝났나 — **앱 네임스페이스에 파드가 하나도 없어야** 한다.
#   kube-system·amazon-guardduty(daemonset)만 남는 게 정상. 하나라도 앱 파드가 있으면 8.1 미완이므로
#   강제하지 않고 멈춘다(순서 어김·8.1 스킵 방어). NS 목록은 앱 소속만 — 필수 시스템 ns 는 제외.
APPNS="vault postgres keycloak kafka api web validation-engine"
LEFT=$(kubectl get pod --all-namespaces --field-selector=status.phase=Running -o json 2>/dev/null \
  | jq --argjson ns "$(printf '%s\n' $APPNS | jq -R . | jq -s .)" \
      '[.items[] | select(.metadata.namespace as $n | $ns | index($n))] | length')
if [ "${LEFT:-1}" -ne 0 ]; then
  echo "❌ 앱 파드가 아직 ${LEFT}개 남았다 — 8.1 in-cluster 정리가 안 끝났다. 강제 종료 중단."
  echo "   8.1 을 완료한 뒤 재실행하라(순서: 8.1 → 8.0)."
  exit 1
fi

# 가드 B: 노드가 딱 kube-system PDB 꼬리만 남았나 — desired 는 이미 0 이어야 한다(위 CLI).
DESIRED=$(aws eks describe-nodegroup --cluster-name cledyu-dr --region ap-northeast-2 \
  --nodegroup-name "$NG" --query 'nodegroup.scalingConfig.desiredSize' --output text)
[ "$DESIRED" = "0" ] || { echo "❌ desired=$DESIRED (0 아님) — 위 update-nodegroup-config 확인"; exit 1; }

# 두 가드 통과 → 남은 노드의 EC2 를 강제 종료. desired 가 0 이라 ASG 가 대체하지 않는다.
for id in $(aws ec2 describe-instances --region ap-northeast-2 \
    --filters "Name=tag:eks:nodegroup-name,Values=$NG" "Name=instance-state-name,Values=running" \
    --query 'Reservations[].Instances[].InstanceId' --output text); do
  echo "가드 통과 — 마지막 노드 강제 종료: $id"
  aws ec2 terminate-instances --region ap-northeast-2 --instance-ids "$id" >/dev/null
done
```
> **왜 강제해도 되나(그리고 언제 안 되나):** failback 축소는 **RTO 밖**이다 — 서비스는 이미 DNS 로
> 온프렘에 넘어가 있어(§7) 이 15분 동안 다운타임이 0 이고, DR 클러스터의 coredns 를 의존하는 것도 없다.
> **위험은 오직 "워크로드가 살아있는데 coredns 를 죽이는 것"** 하나인데, 가드 A 가 앱 파드 0 을 확인하고서야
> 강제하므로 그 시나리오를 막는다. 순서가 어긋나거나(8.1 스킵) 워크로드가 남아있으면 강제하지 않고 멈춘다.
> **⚠️ 이 두 가드를 빼고 무조건 강제하지 말 것** — 그건 실사용 중인 필수 서비스를 끊을 수 있다.
> 순번 8.0 이 8.1 뒤인 것은 오타가 아니다 — **정리(8.1)가 강제 종료의 전제**다.

**8.2) hot 회수 (terraform)**
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
