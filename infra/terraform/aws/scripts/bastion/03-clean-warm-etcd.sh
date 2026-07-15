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
# 실측 결과 추측이 맞았다. 이름이 바뀌면 여기가 조용히 no-op 이 되므로 아래 검증 게이트를 둔다.
kubectl delete validatingwebhookconfiguration aws-load-balancer-webhook --ignore-not-found
kubectl delete mutatingwebhookconfiguration aws-load-balancer-webhook --ignore-not-found

# 조용한 실패 방어 — 지운 뒤에도 남아 있으면 이름이 틀렸거나 즉시 재생성된 것이다.
# (첫 failover 로 애초에 없었으면 아래도 통과한다 — "없음"이 정상 상태다.)
for k in validatingwebhookconfiguration mutatingwebhookconfiguration; do
  if kubectl get "$k" aws-load-balancer-webhook > /dev/null 2>&1; then
    echo "❌ $k/aws-load-balancer-webhook 이 삭제 후에도 존재 — P1c 가 안 고쳐졌다"
    exit 1
  fi
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

echo "✅ warm etcd 정리 완료"
