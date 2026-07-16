#!/bin/bash
# [6] BootstrapApps — ArgoCD 설치 → 회귀 가드 → root-app 적용.
# 📋 런북 272-331(§apps-eks 부트스트랩)의 **3개 bash 블록을 이어붙였다** — 런북이 "동일한 bastion 쉘
# 세션에서 이어서 실행"을 요구하는데(가드의 상대경로 grep 이 앞 블록의 cd·REPO_ROOT 에 의존)
# SSM 은 호출마다 새 쉘이라 한 스크립트로 합쳐야 한다.
set -euo pipefail
set -x

# ⚠️ SSM RunCommand 는 HOME 을 설정하지 않는다(실측: HOME=[], PWD=/usr/bin) → kubectl·helm 이
# $HOME 밑 설정을 못 찾는다. 스펙 §11.12.
export HOME=/root

cloud-init status --wait

# ⚠️ 런북의 `command -v kubectl git helm aws >/dev/null && echo OK || echo "⚠"` 는 **게이트가 아니다**
# — (a) 다중 인자는 하나만 있어도 0 을 반환하고 (b) `&& echo || echo` 라 어차피 항상 성공한다.
# 게이트로 명시 변환한다(변환 규칙 4·5).
for b in kubectl git helm aws; do
  command -v "$b" > /dev/null || {
    echo "❌ $b 없음 — user_data 미완. /var/log/cloud-init-output.log 확인"
    exit 1
  }
done

# ⚠️ `git clone ... && cd ~/Cledyu` 를 그대로 쓰면 안 된다(H2, 실측):
#   (a) clone 은 **멱등이 아니다** — 두 번째 실행에서 "already exists and is not an empty directory" 로 실패
#   (b) `A && B` 는 AND-OR 리스트라 **set -e 가 A 의 실패를 안 잡는다** → cd 가 안 된 채 계속 가다가
#       뒤의 `git rev-parse` 에서 **엉뚱한 에러로** 죽는다(진짜 원인은 clone)
# T3 Step 10 이 "실패 → 고침 → 재실행"이라 증분 드릴에서 반드시 밟는다. 명시적으로 멱등화한다.
# 레포가 PUBLIC 이라 PAT 불요(런북의 조건부 무시).
if [ -d "$HOME/Cledyu/.git" ]; then
  git -C "$HOME/Cledyu" fetch --prune origin
  git -C "$HOME/Cledyu" reset --hard origin/main
else
  rm -rf "$HOME/Cledyu"
  git clone https://github.com/requset700k/Cledyu.git "$HOME/Cledyu"
fi
cd "$HOME/Cledyu" # ⚠️ && 로 잇지 않는다 — 실패가 묻힌다

aws eks update-kubeconfig --name cledyu-dr --region ap-northeast-2

# ── 가드 1: placeholder 잔존 (런북 :293-296) ─────────────────────────────────
# apps-eks 앱은 targetRevision=main 을 sync 하고 치환할 placeholder 가 없어야 한다.
REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT"
if grep -rn '<<' gitops/apps/*/values-eks.yaml gitops/apps/alb-controller/values.yaml; then
  echo "❌ placeholder 잔존 — 확인 필요"
  exit 1
fi
echo "placeholder 없음 OK ✅"

# ── ArgoCD 설치 (런북 :298-306) ──────────────────────────────────────────────
# 앱을 seed 하지 않으므로(design B) 노드가 올라온 직후 클러스터는 매번 비어 있다. ArgoCD 가 없으면
# root-app 을 적용·조정할 주체가 없어(치킨-에그) helm 으로 먼저 설치한다. 이후 platform-argocd 가
# 같은 릴리스를 ServerSideApply 로 adopt 한다. `helm upgrade --install` 은 멱등이라 warm etcd 에
# 이전 사이클 잔존이 있어도 안전하게 재실행된다 — **절대 스킵하지 않는다**([C4]).
helm repo add argo https://argoproj.github.io/argo-helm
helm repo update
helm upgrade --install argocd argo/argo-cd --version 7.7.10 \
  -f gitops/apps/argocd/values.yaml -f gitops/apps/argocd/values-eks.yaml \
  -n argocd --create-namespace --wait
kubectl -n argocd rollout status deploy/argocd-server --timeout=300s

# ── 가드 2: git-source 브랜치핀 (런북 :310-316, 그대로 유지 — 회귀 방어) ────────
# targetRevision 이 main 도 chart-version(vX.Y / X.Y)도 아니면 = 브랜치핀 → 중단.
# ^[[:space:]]*targetRevision: 로 앵커 — 주석줄은 제외(value-only revert 후 오탐 방지).
if grep -REn '^[[:space:]]*targetRevision:' gitops/argocd/root-app-eks.yaml gitops/argocd/apps-eks/ |
  grep -vE 'targetRevision:[[:space:]]*(main|v?[0-9]+\.[0-9])'; then
  echo "❌ git-source 가 main 아닌 revision(브랜치핀) — main 으로 되돌린 뒤 진행"
  exit 1
fi
echo "브랜치핀 없음 OK ✅"

# ── root-app 적용 (런북 :319-320) ────────────────────────────────────────────
# 이제 ArgoCD 가 존재하므로 wave 순서(cert-manager -10 → pki -8 → … → api/web 2)로 sync 된다.
kubectl apply -f gitops/argocd/root-app-eks.yaml

# ── 플랫폼 Ready 대기 (런북 :323) ────────────────────────────────────────────
kubectl -n cert-manager wait --for=condition=Available deploy/cert-manager --timeout=300s

# ⚠️ 런북 :324-325 의 `kubectl get clusterissuer cledyu-ca` · `kubectl -n api get configmap
# cledyu-root-ca-bundle` 은 **여기서 게이트하지 않는다**(H1).
# 사람용 "확인"이라 없으면 기다렸다 다시 보는 것이고, set -euo pipefail 아래선 하드 게이트가 된다:
#   · 첫 failover(깨끗한 warm): api ns 는 service-api(sync-wave 2) 가 만든다 → 이 시점엔 **없는 게 정상**
#     → NotFound → exit 1 → **건강한 DR 에서 오탐 실패**
#   · 반복 failover(warm etcd 잔존): 둘 다 **이전 사이클 잔존물로 존재**해 통과하지만, trust-manager 가
#     이번에 재분배했다는 걸 **전혀 증명하지 못한다**(2026-07-15 실측 — 이 클러스터가 그 상태였다)
# 어느 쪽이든 무의미하거나 해롭다. Global Constraints 의 "과대 게이트 → 건강한 DR 오탐" 그 자체다.
# → 실제 게이트는 [9] 가 한다: Kafka Ready 가 `cert-manager CA + trust-manager Bundle` 에 의존하므로
#   (런북 체크리스트 :359) 여기서 중복 검사할 이유가 없다.

echo "✅ ArgoCD 설치 + root-app 적용 완료 — 이후 sync 는 [9] 가 게이트한다"
