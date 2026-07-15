#!/bin/bash
# [11] RestartApps — api·web 재기동으로 복원 데이터·로그인 활성화. 📋 런북 420-433 이식.
#
# 왜 필요한가(런북 :422-425): api 는 startup 에 store.Open(DB)·auth provider 를 **1회만** 초기화하고,
# 실패하면 in-memory/nil 로 **계속 실행**한다(재연결 루프 없음 — apps/api/cmd/server/main.go).
# Vault 복원으로 cledyu-api-oidc 가 생겨 api 파드가 **일찍** 뜨면, 그 시점에 CNPG·Keycloak·
# auth.cledyu.com 이 아직이면 **영구 degraded**(진도/계정 in-memory, 로그인 불가)로 남는다.
# → 의존성이 모두 Ready 된 뒤 **반드시 rollout restart** 로 재초기화한다.
set -euo pipefail
set -x

# ⚠️ SSM RunCommand 는 HOME 을 설정하지 않는다(스펙 §11.12).
export HOME=/root
aws eks update-kubeconfig --name cledyu-dr --region ap-northeast-2

# ⚠️ 런북의 `rollout restart && rollout status` 를 그대로 쓰지 않는다 — `A && B` 는 AND-OR 리스트라
# **set -e 가 A(restart)의 실패를 안 잡는다**(변환 규칙 5, H2 와 같은 클래스). 줄을 나눈다.
kubectl -n api rollout restart deploy/api
kubectl -n api rollout status deploy/api --timeout=300s
kubectl -n web rollout restart deploy/web
kubectl -n web rollout status deploy/web --timeout=300s

# ── 게이트 1: /ready 의 keycloak (설계 §5.1.3) ───────────────────────────────
# ⚠️ `curl -sf` 만으로는 게이트가 아니다 — health.go 의 `ready := len(h.labs) > 0` 이라
# **keycloak 이 degraded 여도 200** 을 뱉는다. checks 를 직접 봐야 한다.
# ⚠️ `.status` 를 보면 안 된다 — readinessCheck 는 debug 모드(`Server.Mode != "release"`)에서
# auth 가 nil 이어도 `{"status":"ok","detail":"optional_in_debug"}` 를 준다.
# **`.detail == "connected"` 만이 h.auth != nil 을 뜻한다**(과소 게이트 회피).
# ⚠️ 설계 §5.1.3 의 `grep keycloak=connected` 는 약칭이다 — 실제 응답은 JSON 이라 그 문자열이 없다.
#
# DNS 를 타지 않고 ALB 에 직접 붙는다 — [10] 이 방금 전환했지만 리졸버 캐시에 의존할 이유가 없고,
# 이 게이트는 api 내부 상태(auth 초기화)를 보는 것이지 DNS 를 보는 게 아니다(런북 :415 의 --resolve 와 같은 취지).
ALB=$(kubectl -n api get ingress api -o jsonpath='{.status.loadBalancer.ingress[0].hostname}')
[ -n "$ALB" ] || {
  echo "❌ ALB 호스트명 없음"
  exit 1
}
READY=$(curl -sfk -H "Host: api.cledyu.com" "https://${ALB}/ready")
echo "$READY" | jq -e '.checks.keycloak.detail == "connected"' > /dev/null ||
  {
    echo "❌ /ready 의 keycloak 이 connected 아님 — auth provider 초기화 실패(영구 degraded)"
    exit 1
  }
echo "게이트 1 OK ✅ (keycloak=connected)"

# ── 게이트 2: DB 모드 (설계 §5.1.3) ──────────────────────────────────────────
# ⚠️ **부분매칭 함정**(G1) — 실제 로그(소스 확인, 2026-07-15):
#     main.go:195  "db 연결 실패 — 진행 상태 영속화 비활성(in-memory 전용)"   ← 실패
#     main.go:199  "db 연결 — 유저/진행 상태 영속화 활성"                     ← 성공
# **실패 문자열이 "db 연결" 을 포함**하므로 `grep -q "db 연결"` 은 degraded 를 통과시킨다.
# 런북 원본이 `grep -E "db 연결|in-memory"` 인 것은 **사람에게 둘 다 보여 판단시키려는 용도**이며
# 그대로 기계 게이트로 옮기면 안 된다 → **실패 명시 거부 → 성공 전문 매치** 순서.
# (`"in-memory 전용"` 은 main.go:105 의 redis 경고 `"...세션 락 in-memory 폴백..."` 과 안 겹친다 — 확인함)
LOG=$(kubectl -n api logs deploy/api --tail=200)
echo "$LOG" | grep -q "in-memory 전용" && {
  echo "❌ in-memory 폴백 — DB 미연결(영구 degraded)"
  exit 1
}
echo "$LOG" | grep -q "db 연결 — 유저/진행 상태 영속화 활성" || {
  echo "❌ db 연결 성공 로그 없음"
  exit 1
}
echo "게이트 2 OK ✅ (db 연결)"

echo "✅ api·web 재기동 완료 — keycloak 연결 + DB 모드 확인"
