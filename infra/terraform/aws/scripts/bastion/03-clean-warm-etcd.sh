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

echo "✅ warm etcd 정리 완료"
