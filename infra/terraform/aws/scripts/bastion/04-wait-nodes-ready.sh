#!/bin/bash
# [4.5] WaitNodesReady — 노드가 **k8s 에 Ready** 될 때까지 기다린다. 🆕 신규(런북에 원본 없음).
#
# 왜 있나 — 초안엔 없었고, T4 Step 5 실측이 만들어냈다(스펙 §11.14):
#   [4] 가 쓰던 게이트(`describeNodegroup` → `Status == ACTIVE`)는 **아무것도 안 거른다.**
#   Nodegroup.Status 는 스케일 전·중·후 전부 ACTIVE 다 — 대답이 안 바뀌므로 질문이 무의미했다.
#   실측: 16:25:30 scale → 16:26:04 status=ACTIVE → 16:26:05 InstallAddons / 노드 Ready 도 16:26:05.
#   **여유 0초.** 노드가 35s 만에 떠준 덕에 우연히 살았다(EKS 노드 부팅은 통상 60~120s).
#
# 진짜 목적은 "기다리기"가 아니라 **DEGRADED 의 뜻을 하나로 만드는 것**이다:
#   [5] 의 check 는 DEGRADED 를 치명으로 본다. 그런데 DEGRADED 는 두 뜻이다 —
#     (a) 노드가 아직 없어서 못 돈다  → 기다리면 낫는다
#     (b) 진짜 고장(고아 ALB webhook = P1c) → 기다려도 안 낫는다
#   구분이 불가능하면 어느 쪽으로 짜도 반은 틀린다. **여기서 노드를 확실히 세우면 이후 DEGRADED 는
#   (b) 뿐**이므로 그 치명 판정이 비로소 옳아진다.
#
# SFN 이 직접 못 하는 이유: non-VPC 인 SFN 은 private EKS 에 못 닿는다(설계 §5.1.2 와 같은 제약).
# "노드가 k8s 에 Ready 인가"를 아는 주체는 클러스터 안의 bastion 뿐이다.
# ASG 의 InService 도 답이 아니다 — **EC2 헬스체크지 kubelet 조인이 아니다**(실측: 인스턴스 running
# 시각 < 노드 Ready 시각). 같은 함정의 다른 층이다.
set -euo pipefail
set -x

# ⚠️ SSM RunCommand 는 HOME 을 설정하지 않는다(실측: HOME=[], PWD=/usr/bin). 스펙 §11.12.
export HOME=/root

cloud-init status --wait

aws eks update-kubeconfig --name cledyu-dr --region ap-northeast-2

# ⚠️ **대수 검증이 먼저다 — `kubectl wait --all` 만 쓰면 이 게이트도 초안과 똑같이 무효다.**
# `kubectl wait --for=condition=Ready node --all` 은 **노드가 0대면 즉시 통과한다**("모든 노드가 Ready"가
# 노드가 없으면 공허참). [4] 직후엔 노드가 아직 0대일 수 있으므로 그 한 줄만으론 아무것도 안 거른다.
# → **먼저 3대가 보일 때까지** 기다린 뒤에 Ready 를 본다(스펙 §11.14 (f) — 새 게이트를 만들며 같은
#   과소 게이트를 또 밟을 뻔했다).
WANT=3
# 최대 10분 — 노드 등장(부팅 + kubelet 등록)까지. 통상 35~120s.
for i in $(seq 1 60); do
  N=$(kubectl get nodes --no-headers 2> /dev/null | wc -l)
  [ "$N" -ge "$WANT" ] && break
  echo "노드 등장 대기 ${N}/${WANT} ($i/60)"
  sleep 10
done
[ "$N" -ge "$WANT" ] || {
  echo "❌ 노드가 ${WANT}대에 못 미친다(현재 ${N}) — [4] ScaleNodes 가 실패했거나 용량 부족"
  exit 1
}

# 등장했으니 이제 Ready 를 기다린다. 여기선 --all 이 공허참일 수 없다(위에서 N>=3 확인).
kubectl wait --for=condition=Ready node --all --timeout=600s

# ⚠️ 판정을 출력이 아니라 **개수**로 한다(스펙 §11.11 — "존재만 보고 값을 안 봄" 회피).
READY=$(kubectl get nodes --no-headers | grep -cw Ready)
[ "$READY" -ge "$WANT" ] || {
  echo "❌ Ready 노드 ${READY}/${WANT} — wait 는 통과했는데 실측이 안 맞는다"
  exit 1
}

echo "✅ 노드 ${READY}/${WANT} Ready — 이후 애드온 DEGRADED 는 '노드 없음'이 아니다"
