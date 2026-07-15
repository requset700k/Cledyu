#!/bin/bash
# [12] VerifyServing — 복원본이 실제로 서빙되는지. 🆕 신규(런북 :370 은 한 줄짜리 체크 항목이라
# 명령이 없다 — 설계 §5.1.4 가 무인증으로 재설계한 것을 구현).
#
# ⚠️ 로그인 계정을 쓰지 않는 이유: [7] 의 generate-root 산물($NEWROOT)은 그 스크립트의 셸 변수이고,
# 이 스크립트는 별도 SSM RunCommand = **새 셸**이며, 자식 SM 은 stdout 을 CloudWatch 로 보낼 뿐이라
# Vault 토큰을 전달할 경로가 없다. password grant 활성 여부도 미확인 가정이었다(설계 §5.1.4 H1).
set -euo pipefail
set -x

# ⚠️ SSM RunCommand 는 HOME 을 설정하지 않는다(스펙 §11.12).
export HOME=/root
aws eks update-kubeconfig --name cledyu-dr --region ap-northeast-2

# ── (1) Keycloak + DNS + 복원된 realm ────────────────────────────────────────
# ⚠️ 여기서는 **DNS 를 일부러 탄다** — [10] SwitchDNS 가 실제로 먹었는지가 이 검증의 일부다.
# ([11] 의 게이트는 api 내부 상태를 보는 것이라 ALB 직결이었지만, 여기는 "사용자가 도달하나"를 본다.)
# Route53 헬스체크(onprem_pull)와 동일 신호라 검증된 재사용이다.
curl -sf https://auth.cledyu.com/realms/cledyu-learn | grep -q cledyu-learn ||
  {
    echo "❌ realm 미응답 — Keycloak/DNS/복원 중 하나 실패"
    exit 1
  }
echo "(1) realm 응답 OK ✅"

# ── (2) 복원 데이터가 실제로 들어왔는가 — DB 직접 ────────────────────────────
# ⚠️ `-d cledyu` 필수. 없으면 psql 이 접속 유저와 같은 이름의 DB 에 붙어 users 테이블을 못 찾는다
# → **정상 복원인데 항상 실패**한다(postgres-cnpg-dr/templates/cluster.yaml:53 `database: cledyu`).
# ⚠️ `-U cledyu` 를 **쓰지 않는다**(H3) — 레포의 psql 선례 5건이 전부 `-U` 를 안 쓴다
# (dr-failback.md:85 · dr-failback-isolated-drill.md:85·87·90·91·97 모두 `psql -d <db> -tAc`).
# CNPG 파드의 OS 유저는 postgres 라 local 소켓 peer 인증에서 `-U cledyu` 는 OS유저≠롤로 어긋난다.
# **[12] 는 페일오버 전체의 마지막 게이트**다 — 여기서 죽으면 **완벽히 복구된 DR 이 ❌ 실패 알림**을 보낸다.
ROWS=$(kubectl -n postgres exec cledyu-pg-1 -- psql -d cledyu -tAc "SELECT count(*) FROM users" | tr -d '[:space:]')
[ "${ROWS:-0}" -gt 0 ] || {
  echo "❌ 복원본에 데이터 없음(users=$ROWS)"
  exit 1
}
echo "(2) users=$ROWS ✅"

echo "✅ realm 응답 + users=$ROWS — 복원본이 서빙 중"

# **트레이드오프:** 전체 OIDC 로그인 플로우(브라우저 리다이렉트)는 검증하지 않는다 — 헤드리스 브라우저가
# 필요해 재해 경로에 둘 만한 것이 아니다. [11] 의 `/ready` keycloak=connected 게이트와 위 (1) 의 realm
# 응답으로 "Keycloak 이 서빙 가능한 상태"까지는 덮인다.
