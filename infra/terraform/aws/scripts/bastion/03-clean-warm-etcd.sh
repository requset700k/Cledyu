#!/bin/bash
# [3] CleanWarmEtcd — 이전 사이클 잔존물 정리. 🆕 신규(런북에 원본 없음 — 7/14 드릴 *관찰*만 존재).
#
# warm etcd 는 failover 사이클 간 살아남는다. 7/14 드릴 발견(P1c): 고아 ALB webhook 이 남아 있으면
# coredns 애드온이 CREATE_FAILED 로 죽는다 → [5] InstallAddons 가 타임아웃.
# 노드 없이도 kubectl 로 지울 수 있다 — API 서버는 warm 에 상시 떠 있다.
set -euo pipefail
set -x

# ⚠️ SSM RunCommand 는 root 로 돌지만 HOME 을 설정하지 않는다(실측: HOME=[], PWD=/usr/bin).
# kubectl 은 $HOME/.kube/config 를 찾다 못 찾으면 기본값 localhost:8080 으로 폴백해
# "connection refused" 로 죽는다 — 에러가 원인을 안 가리킨다(스펙 §11.12).
export HOME=/root

cloud-init status --wait

# ⚠️ `command -v kubectl aws` 는 게이트가 아니다 — **하나만 있어도 0 을 반환**한다(실측).
# 초안이 그 형태였고, aws 가 없어도 조용히 통과했을 것이다. 하나씩 확인한다.
#
# [3] 은 페일오버의 **첫 bastion 단계**다 — 뒤 스크립트들이 쓰는 도구를 여기서 한 번에 막는다.
# 안 그러면 예컨대 jq 부재를 [7] 이 **~30분 뒤에야** 알려준다(07 은 jq 로 Vault 출력을 파싱).
# 2026-07-15 실측: bastion 에 7개 전부 설치돼 있다(user_data). 이 게이트는 그 회귀 방어다.
for b in kubectl aws jq helm git curl; do
  command -v "$b" > /dev/null || {
    echo "❌ $b 없음 — user_data 미완. /var/log/cloud-init-output.log 확인"
    exit 1
  }
done

aws eks update-kubeconfig --name cledyu-dr --region ap-northeast-2

# stale SSM 파라미터 삭제는 [2.4] ClearAlbParam(SDK)이 한다 — 여기가 아니다(설계 §5.1.2).
# bastion·SSM 에이전트에 의존하지 않는 게 안전하고 IAM 도 SFN 롤 하나로 끝난다.

# [P1c] 고아 ALB webhook 제거.
# ⚠️ 이름은 **2026-07-15 실측 확정**이다(warm 클러스터에서 직접 조회 — validating·mutating 양쪽 존재).
# 계획 초안은 이 이름을 추측으로 적었고 "틀리면 --ignore-not-found 로 조용히 통과한다"고 경고했으나,
# 실측 결과 추측이 맞았다. **이름 방어는 그 실측이 하는 것이지 아래 게이트가 하는 게 아니다** —
# 이름이 틀리면 delete 도 get 도 not-found 라 게이트는 조용히 통과한다(2026-07-16 확인). 아래 게이트가
# 실제로 잡는 것은 **고아 webhook**(= P1c 그 자체)이다.
# ⚠️ 살아있는 컨트롤러가 있으면 이 delete 는 2초 만에 되살아나는 **의미 없는 churn** 이다(실측).
#    그래도 지운다 — warm(노드 0)에서 고아를 치우는 게 이 스크립트의 목적이고, hot 에서 무해하기 때문이다.
#    "되살아났다"를 실패로 보지 않는 것이 아래 게이트의 핵심이다.
kubectl delete validatingwebhookconfiguration aws-load-balancer-webhook --ignore-not-found
kubectl delete mutatingwebhookconfiguration aws-load-balancer-webhook --ignore-not-found

# 게이트 — **"존재하나"가 아니라 "고아인가"를 본다**(2026-07-16 T5 드릴 실측으로 교체).
#
# 🔴 **구 게이트는 재실행에서 오탐으로 죽었다.** "지운 뒤에도 남아 있으면 실패"였는데, 노드가 살아있는
#    상태(= 부분 실패 후 재실행)에선 **ALB 컨트롤러가 자기 webhook 을 2초 만에 되살린다** → 건강한
#    클러스터에서 [3] 이 죽었다. 실측 로그: `"aws-load-balancer-webhook" deleted` 직후 `get` 이 성공.
#    실재해는 [2] 직후 노드가 0이라 컨트롤러가 없어 안 터진다 — **재실행 경로에서만** 터졌다.
#
# 🔴 **그리고 구 게이트는 자기가 표방한 목적을 수행한 적이 없다.** 위 주석은 "이름이 바뀌면 조용히
#    no-op 이 되므로 게이트를 둔다"인데, 이름이 틀리면 `delete` 도 no-op 이고 `get` 도 not-found 라
#    **게이트가 그냥 통과한다.** 이름 방어는 게이트가 아니라 **실측**(위 2026-07-15)이 하고 있었다.
#
# **P1c 의 실체는 "webhook 이 존재한다"가 아니다** — *"컨트롤러가 죽어 endpoint 가 없는 webhook 이
# coredns 의 admission 을 막는다"* 이다. 그러니 판정도 그 조건으로 한다:
#   **webhook 이 남아 있다면 endpoint 가 있어야 한다.**
#   · 실재해(노드 0): 지우면 사라진다 → continue → 통과. **기존 방어 그대로다.**
#   · 재실행(노드 3): 재생성되나 endpoint 가 있다 → 통과. 살아있는 컨트롤러의 것이라 무해하다.
#   · **진짜 고아**: 남아있는데 endpoint 0 → ❌. **지금까지 한 번도 구분 못 하던 그 경우다.**
#
# ⚠️ service 이름을 박지 않는다 — **webhook 객체에서 읽는다**(A3·C-fix 가 이름 창작으로 물린 그 자리).
#    2026-07-16 실측값은 kube-system/aws-load-balancer-webhook-service:443 (webhook 9개 전부 동일)이고
#    endpoint 2개가 컨트롤러 파드 IP 와 일치했다. 그래도 **읽어서** 쓴다.
# ⚠️ `kubectl get endpoints` 를 쓰지 않는다 — **v1 Endpoints 는 v1.33+ 에서 deprecated** 라
#    경고를 뿌리고 언젠가 조용히 깨진다(실측 경고 확인). EndpointSlice 가 정식 경로다.
for k in validatingwebhookconfiguration mutatingwebhookconfiguration; do
  # 사라졌으면 정상(warm 의 기대 경로 · 첫 failover 로 애초에 없던 경우 포함).
  kubectl get "$k" aws-load-balancer-webhook > /dev/null 2>&1 || continue

  NS=$(kubectl get "$k" aws-load-balancer-webhook \
    -o jsonpath='{.webhooks[0].clientConfig.service.namespace}' 2> /dev/null)
  SVC=$(kubectl get "$k" aws-load-balancer-webhook \
    -o jsonpath='{.webhooks[0].clientConfig.service.name}' 2> /dev/null)
  # ⚠️ `[ -n "$NS" ] && [ -n "$SVC" ] || { ... }` 는 SC2015 다(A && B || C 는 if-then-else 가 아니다).
  #    여기선 동작이 우연히 맞지만 훅이 거부하고, if 가 읽기도 낫다.
  if [ -z "$NS" ] || [ -z "$SVC" ]; then
    echo "❌ $k 가 남았는데 clientConfig.service 를 못 읽었다 — webhook 구조가 바뀌었다"
    exit 1
  fi

  # 🔴 **address 가 아니라 conditions.ready 를 본다**(codex P2, 2026-07-16).
  # controller 파드가 terminating/NotReady 면 EndpointSlice 에 address 는 남아도 ready=false 다.
  # 그런데 admission webhook 은 **ready endpoint 로만** 호출되므로([5] coredns admission),
  # `.addresses[0]` 만 세면 ready=0 인데도 통과시켜 → [5] 가 "webhook no endpoints" 로 다시 죽는다.
  # §11.11 "존재만 보고 값을 안 봄" 의 재발이다 — address 존재 ≠ webhook 호출 가능.
  #
  # ⚠️ jsonpath `?()` 필터의 `==true` 지원은 kubectl 버전따라 다르다(bastion 은 v1.34) → 필터로
  #    지어내지 않고, `ready addr` 를 나란히 뽑아 grep 으로 센다. 출력 예: "true 10.90.3.85".
  # ⚠️ **재시도 필수** — controller 가 webhook 을 방금 재생성했으면 endpoint 가 아직 ready 전일 수
  #    있다(§11.18 (k): validating 2초·mutating 10초 재생성). 재시도 없이 즉시 실패시키면 건강한
  #    재실행을 죽인다(codex 도 "준비될 때까지 재시도" 를 지적). 2분 상한.
  # ([3] 내부합 여유 480s 라 이 2분은 SSM timeout 정합성을 안 깨뜨린다 — check-timeouts.py 로 확인.)
  READY=0
  for _ in $(seq 1 12); do
    READY=$(kubectl -n "$NS" get endpointslice -l "kubernetes.io/service-name=$SVC" \
      -o jsonpath='{range .items[*].endpoints[*]}{.conditions.ready}{" "}{.addresses[0]}{"\n"}{end}' 2> /dev/null |
      grep -c '^true ' || true)
    [ "${READY:-0}" -gt 0 ] && break
    echo "  $k: webhook 재생성됨, ${NS}/${SVC} 의 ready endpoint 대기 중"
    sleep 10
  done
  [ "${READY:-0}" -gt 0 ] || {
    echo "❌ $k 가 남았는데 ${NS}/${SVC} 의 **ready** endpoint 가 2분째 0 — 고아이거나 controller 고장(P1c)."
    echo "   이 상태로 두면 coredns 애드온이 admission 에 막혀 CREATE_FAILED 로 죽는다."
    exit 1
  }
  echo "$k: 재생성됨 + ready endpoint ${READY}개 (${NS}/${SVC}) — 살아있는 컨트롤러의 것이라 정상"
done

# [P1d] stale hostAlias — 같은 성격의 warm etcd 잔존물(로테이션된 ALB IP 를 가리켜 api oidc 가 10s hang).
# git 에 hostAliases 가 없으므로(git log -S 확인) 이건 런타임 주입분이다.
# api Deployment 가 없거나(첫 failover) hostAliases 가 없으면 remove 가 실패하는데, 둘 다 정상이라 삼킨다.
if kubectl -n api get deploy api > /dev/null 2>&1; then
  kubectl -n api patch deploy api --type json \
    -p '[{"op":"remove","path":"/spec/template/spec/hostAliases"}]' 2> /dev/null &&
    echo "P1d: stale hostAliases 제거" ||
    echo "P1d: hostAliases 없음 — no-op(정상)"
else
  echo "P1d: api Deployment 없음 — no-op(첫 failover 정상)"
fi

# ── [P1e] Vault raft PVC 정리 (2026-07-16 신규) ─────────────────────────────
# **왜 여기인가:** [7] 의 `vault operator init` 은 **이미 초기화된 Vault 에서 에러로 죽는다**
# (`Vault is already initialized`). 그런데 [7] 은 init 산물(root/recovery)을 **일부러 안 남긴다**
# (감사 로그·stdout 유출 방어) → 재실행 시 토큰을 얻을 길이 없어 **DR 이 그 자리에서 멈춘다.**
# warm etcd 처럼 **PVC 도 failover 사이클 간 살아남으므로**(gp3, values-eks.yaml:12) 이건
# "이전 사이클 잔존물"이고 [3] 의 소관이다 — P1c·P1d 와 같은 성격이다.
# DR EKS 의 raft 에 보존할 상태는 없다. 매번 온프렘 스냅샷으로 덮어쓰므로 **비우는 게 정답**이다.
#
# **순서가 핵심이다(2026-07-16 kind 실측).** `delete pvc` 를 먼저, `delete pod` 를 나중에 한다:
#   · 파드가 **Running** 이면(= failback 이 노드를 안 내린 상태, 스펙 §11.15) PVC 는 pvc-protection
#     파이널라이저에 걸려 **deletionTimestamp 만 받고 Bound 에 머문다**. 기본 `--wait=true` 는
#     여기서 timeout 에러 → set -e 로 [3] 이 죽고, PVC 엔 "파드가 죽는 순간 지워지는" 지뢰가 남는다.
#     → `--wait=false` 로 **표시만** 하고, 파드를 지워 붙들던 손을 떼게 한다.
#   · 파드가 **Pending** 이면(= 정상 warm, 노드 0) 애초에 안 붙든다(미스케줄 파드는 in-use 가 아니다)
#     → PVC 가 즉시 지워진다. 같은 코드가 두 상태 모두에서 동작한다.
#   · **STS 는 건드리지 않는다** — 그래야 ArgoCD selfHeal(platform-vault.yaml:37)과 싸우지 않는다.
#     STS 컨트롤러가 알아서 새 PVC(다른 uid)를 만들고 파드를 붙인다(실측: 10초 내 수렴).
#
# ⚠️ 라벨 셀렉터로 **PVC 를 지우면 안 된다** — 차트의 volumeClaimTemplates 엔 라벨이 없다
#    (helm template 확인: metadata 가 name: data / name: audit 뿐). -l 을 쓰면 0개 매칭으로
#    **조용히 통과**한다. ns 통째 --all 이 유일하게 셀렉터 오타가 불가능한 형태다.
#    vault ns 엔 이 차트 말고 PVC 를 만드는 것이 없다(앱의 directory include 는 ns·SA·cert 뿐).
# ⚠️ 파드는 반대로 **라벨로** 지운다(이름 vault-0..2 를 박으면 replicas 변경 시 조용히 샌다).
#    셀렉터는 helm template 로 확인한 실제 값이다. vault-agent-injector 는 name 이 달라 안 걸린다.
# ⚠️ ns 가 없어도(첫 failover) delete 는 exit 0 + "No resources found" 다(실측) → 존재 게이트 불요.
kubectl -n vault delete pvc --all --wait=false
kubectl -n vault delete pod -l app.kubernetes.io/name=vault,component=server --ignore-not-found

# 게이트: **실제로 사라졌나.** Terminating 에 걸린 채 넘어가면 [6] 이 그 PVC 를 재사용하고
# [7] 이 already-initialized 로 죽는다 — 이 블록이 막으려던 바로 그 실패다.
# 새로 만들어진 PVC 는 deletionTimestamp 가 없으므로 "하나라도 있으면 아직 안 끝난 것"으로 판정한다.
# ⚠️ 개수로는 못 본다 — STS 컨트롤러가 즉시 새 PVC 를 만들어 count 가 0 이 되는 순간이 없다(실측).
# 2분 (주석을 for 줄 안에 두면 shfmt 가 줄을 쪼갠다)
for i in $(seq 1 24); do
  STUCK=$(kubectl -n vault get pvc \
    -o jsonpath='{range .items[*]}{.metadata.deletionTimestamp}{"\n"}{end}' 2> /dev/null |
    grep -c . || true)
  [ "${STUCK:-0}" -eq 0 ] && break
  echo "Vault PVC Terminating 대기 ${STUCK}개 ($i/24)"
  sleep 5
done
[ "${STUCK:-0}" -eq 0 ] || {
  echo "❌ Vault PVC 가 2분째 Terminating — 파드가 아직 붙들고 있다."
  echo "   노드가 살아있는 채로 [3] 이 돌았을 가능성(스펙 §11.15: failback 이 노드를 안 내림)."
  echo "   kubectl -n vault get pod -o wide 로 확인 후 노드를 0 으로 내리고 재실행하라."
  exit 1
}
echo "P1e: Vault raft PVC 정리 완료 — [7] 의 init 이 fresh Vault 를 만난다"

# [P1f] stale Kafka PVC — Vault(P1e)와 같은 warm-etcd 잔존물(2026-07-18 드릴 실측).
# Kafka 는 deleteClaim:false 라 teardown 이 EBS 만 지우고(우리 failback CleanupOrphans 는 AWS 레벨로
# EBS 만 삭제) PVC/PV 객체는 warm etcd 에 남는다 → 다음 failover 에 stale PVC 가 이미 삭제된 EBS(vol-...)
# 를 물어 FailedAttachVolume → 브로커가 영영 ContainerCreating → [9] WaitAppsReady 가
# kafka/cledyu-kafka Ready 를 못 봐 타임아웃(실측: 브로커 3개 전부 vol-... NotFound).
# DR Kafka 는 매 failover 새로 뜨는 일회성이라 PVC 를 지워 ebs-csi 가 fresh EBS 를 프로비저닝하게 한다.
# ⚠️ kafka ns 엔 이 PVC(data-cledyu-kafka-*) 말고 PVC 를 만드는 게 없다 → --all 이 셀렉터 오타 불가.
# ⚠️ 브로커가 PVC 를 붙들면(재실행·노드 살아있음) pvc-protection 으로 Terminating 고착 → 브로커도 지운다.
#    실재해(노드 0)엔 파드가 없어 pod delete 는 no-op 이다.
if kubectl get ns kafka > /dev/null 2>&1; then
  kubectl -n kafka delete pvc --all --wait=false
  kubectl -n kafka delete pod -l strimzi.io/name=cledyu-kafka-kafka --ignore-not-found

  # 게이트: 실제로 사라졌나. Terminating 고착이면 Kafka 가 그 PVC 를 재사용 → 위 FailedAttachVolume 재현.
  # 새 PVC 는 deletionTimestamp 가 없으므로 "timestamp 있는 게 하나라도 있으면 아직"으로 판정(P1e 와 동일).
  for i in $(seq 1 24); do
    STUCK=$(kubectl -n kafka get pvc \
      -o jsonpath='{range .items[*]}{.metadata.deletionTimestamp}{"\n"}{end}' 2> /dev/null |
      grep -c . || true)
    [ "${STUCK:-0}" -eq 0 ] && break
    echo "Kafka PVC Terminating 대기 ${STUCK}개 ($i/24)"
    sleep 5
  done
  [ "${STUCK:-0}" -eq 0 ] || {
    echo "❌ Kafka PVC 가 2분째 Terminating — 브로커가 아직 붙들고 있다(노드 살아있는 채 [3] 실행?)."
    echo "   kubectl -n kafka get pod -o wide 확인 후 노드 0 으로 내리고 재실행하라."
    exit 1
  }
  echo "P1f: Kafka PVC 정리 완료 — 브로커가 fresh EBS 를 프로비저닝한다"
fi

# ── [P1g] stale TargetGroupBinding CR (2026-07-18 드릴 실측) ─────────────────
# P1e·P1f 와 **같은 계열의 warm-etcd 잔존물**이다. failback teardown(approach B)은 ALB·타겟그룹을
# AWS 레벨로 지우지만(CleanupOrphans), 그 타겟그룹을 참조하던 **TargetGroupBinding CR 은 warm etcd 에
# 남는다** — Kafka 가 EBS 만 지우고 PVC 를 남기는 것과 정확히 같은 구조(deleteClaim:false 격).
#
# TGB 이름(k8s-<ns>-<svc>-<hash>)은 ns/service/port 로부터 **결정적**이라, 다음 failover 에 LB 컨트롤러가
# 만들려는 이름과 그대로 충돌한다: `targetgroupbindings "k8s-api-api-*" already exists` → **Ingress
# deploy model 이 실패** → 컨트롤러가 그 사이클의 fresh 타겟그룹 생성·**WAF 연결·Ingress status 갱신을
# 전부 완료하지 못한다.** 그 최종 증상이 [10] SwitchDNS 의 `WAF 미연결`(2026-07-18 exec b2f12033 실측).
# 게다가 stale TGB 는 이미 삭제된 TG(TargetGroupNotFound)를 계속 물어 reconcile 에러를 무한 반복한다.
#
# ⚠️ **왜 07-16 최초 failover 는 통과했나:** 그땐 이 CR 이 애초에 없었다(첫 사이클). teardown 이 이 CR 을
#    남긴 것이 회귀의 근원 — 즉 이 정리 블록의 부재가 곧 버그다.
# [3] 시점엔 앱이 아직 sync 전([9] 이 나중)이라 **여기 존재하는 TGB 는 전부 이전 사이클 stale** 이다.
# 지우면 컨트롤러가 앱 sync 때 fresh TG ARN 으로 재생성한다.
# ⚠️ stale TGB 는 이미 없는 TG 를 물어 finalizer(elbv2.k8s.aws/resources)의 cleanup(DeregisterTargets)이
#    할 일이 없다 → 그냥 두면 finalizer 가 Terminating 에 고착할 수 있다. cleanup 이 무의미하므로
#    **finalizer 를 떼고** 지운다. 남은 Terminating(같은 이름)도 fresh 생성엔 "already exists" 라 똑같이 막는다.
kubectl get targetgroupbinding -A \
  -o jsonpath='{range .items[*]}{.metadata.namespace}{" "}{.metadata.name}{"\n"}{end}' 2> /dev/null |
  while read -r ns name; do
    [ -z "$ns" ] && continue
    kubectl -n "$ns" patch targetgroupbinding "$name" --type merge \
      -p '{"metadata":{"finalizers":[]}}' 2> /dev/null || true
    kubectl -n "$ns" delete targetgroupbinding "$name" --ignore-not-found --wait=false
    echo "P1g: stale TGB 제거 ${ns}/${name}"
  done

# 게이트: 실제로 다 사라졌나. 하나라도 남으면 fresh 생성이 "already exists" 로 막혀 WAF 가 안 붙는다.
for i in $(seq 1 24); do
  REMAIN=$(kubectl get targetgroupbinding -A -o name 2> /dev/null | grep -c . || true)
  [ "${REMAIN:-0}" -eq 0 ] && break
  echo "TGB 삭제 대기 ${REMAIN}개 ($i/24)"
  sleep 5
done
[ "${REMAIN:-0}" -eq 0 ] || {
  echo "❌ TargetGroupBinding 이 2분째 ${REMAIN}개 남음 — finalizer 고착 의심."
  echo "   kubectl get targetgroupbinding -A 로 확인 후 finalizer 를 수동으로 떼라."
  exit 1
}
echo "P1g: stale TargetGroupBinding 정리 완료 — 컨트롤러가 fresh TG 로 재생성 → WAF 자동 연결"

echo "✅ warm etcd 정리 완료"
