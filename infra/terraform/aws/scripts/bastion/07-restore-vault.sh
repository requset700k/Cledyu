#!/bin/bash
# [7] RestoreVault — Vault init → S3 스냅샷 force restore → generate-root → k8s auth 재설정 → ESO 재기동.
# 📋 런북 75-160(§복원 절차) + 161-194(§Vault k8s auth 재설정) + **195-206(§tmpfs / 잔존 주의)** 이식.
#
# 🔒 **이 스크립트만 `set -x` 를 쓰지 않는다(리뷰 지적 C6).** set -x 는 실행 명령을 **인자까지** 추적하므로
# Vault init 출력·$INIT_ROOT·$NEWROOT·recovery 키가 stdout 에 찍힌다. 그 출력은 자식 SM 이
# **CloudWatch 로그그룹**에 보내고 `stdoutTail` 로 **Discord 실패 알림**에도 싣는다 → **루트 토큰 평문 노출.**
# echo 로도 흘리지 않는다 — 확인 게이트는 값을 출력하지 않고 판정한다.
set -euo pipefail

# ⚠️ SSM RunCommand 는 HOME 을 설정하지 않는다(실측: HOME=[], PWD=/usr/bin). 스펙 §11.12.
export HOME=/root

# ⚠️ 스냅샷은 **Vault 전체 시크릿**이다(런북 §tmpfs / 잔존 주의). 런북의 `./vault-raft.snap` 을 그대로
# 쓰면 PWD=/usr/bin 이라 시스템 디렉터리에 시크릿을 떨군다. 전용 임시 디렉터리 + trap 으로
# **실패 경로에서도 반드시 삭제**한다(런북은 사람이 마지막에 rm 하지만 자동화는 죽으면 안 지운다).
WORK=$(mktemp -d /tmp/vault-restore.XXXXXX)
cleanup() {
  rm -rf "$WORK"
  # 파드 안 사본도 지운다(런북 :204 — `-it` 제거). 파드가 없거나 이미 지워졌으면 무시.
  kubectl -n vault exec vault-0 -- rm -f /tmp/vault-raft.snap > /dev/null 2>&1 || true
}
trap cleanup EXIT
cd "$WORK"

: "${SNAPSHOT_KEY:?SNAPSHOT_KEY 미지정 — 승인 output 의 snapshot 을 주입해야 한다}"

aws eks update-kubeconfig --name cledyu-dr --region ap-northeast-2

VC="VAULT_CACERT=/vault/tls/ca.crt" # 모든 vault 명령에 필수 — TLS 자체서명(cledyu-ca)

# ── 1) init (런북 :86-88) ────────────────────────────────────────────────────
# KMS auto-unseal 이라 unseal 키 입력 불요 — init 만 하면 자동 unseal 된다.
# 런북의 <INIT_ROOT> 플레이스홀더(사람이 눈으로 보고 넣는 값)를 변수 캡처로 대체(변환 규칙 3).
kubectl -n vault wait --for=condition=Ready pod/vault-0 --timeout=300s
INIT=$(kubectl -n vault exec vault-0 -- sh -c "$VC vault operator init -format=json")
INIT_ROOT=$(echo "$INIT" | jq -r .root_token)
if [ -z "$INIT_ROOT" ] || [ "$INIT_ROOT" = "null" ]; then
  echo "❌ init 실패 — root_token 없음"
  exit 1
fi
unset INIT # 출력 전체를 메모리에 붙들지 않는다

# 3-node(스냅샷 3-peer 와 동일 토폴로지) 형성 대기 (런북 :90-92)
for i in $(seq 1 30); do
  PEERS=$(kubectl -n vault exec vault-0 -- sh -c \
    "$VC VAULT_TOKEN=$INIT_ROOT vault operator raft list-peers -format=json" 2> /dev/null |
    jq -r '.data.config.servers | length' 2> /dev/null || echo 0)
  [ "${PEERS:-0}" -ge 3 ] && break
  echo "raft peers 대기 ${PEERS:-0}/3 ($i/30)"
  sleep 10
done
[ "${PEERS:-0}" -ge 3 ] || {
  echo "❌ raft 3-peer 미형성(현재 ${PEERS:-0}) — 스냅샷과 토폴로지 불일치"
  exit 1
}

# ── 2) 스냅샷 취득 (런북 :97-98) ─────────────────────────────────────────────
# ⚠️ 런북은 `s3 ls | tail -1`(최신 고정)이지만 우리는 **승인 시 고른 값**을 쓴다 — 드롭다운의 존재 이유다.
aws s3 cp "s3://cledyu-lab-dr-backups/${SNAPSHOT_KEY}" "$WORK/vault-raft.snap"
[ -s "$WORK/vault-raft.snap" ] || {
  echo "❌ 스냅샷이 비었거나 없음: $SNAPSHOT_KEY"
  exit 1
}

# ── 3) force restore (런북 :107-109) ─────────────────────────────────────────
# 스냅샷은 다른 클러스터(온프렘)에서 왔고 방금 init 한 EKS Vault 는 recovery/shamir 키가 달라
# 일반 restore 는 seal 일관성 검사에서 거부된다 → **-force 필수**.
kubectl -n vault cp "$WORK/vault-raft.snap" vault-0:/tmp/vault-raft.snap
kubectl -n vault exec vault-0 -- sh -c \
  "$VC VAULT_TOKEN=$INIT_ROOT vault operator raft snapshot restore -force /tmp/vault-raft.snap"
unset INIT_ROOT # force restore 후 무효화된다(런북 :111) — 더 들고 있지 않는다

# restore 직후 복원된 keyring 을 KMS 로 재-unseal 한다 → Sealed=false 확인(런북 :113)
for i in $(seq 1 30); do
  SEALED=$(kubectl -n vault exec vault-0 -- sh -c "$VC vault status -format=json" 2> /dev/null |
    jq -r .sealed 2> /dev/null || echo true)
  [ "$SEALED" = "false" ] && break
  echo "unseal 대기 ($i/30)"
  sleep 5
done
[ "$SEALED" = "false" ] || {
  echo "❌ 복원 후 Vault 가 봉인된 채 — KMS 키 불일치 의심"
  exit 1
}

# ── 4) generate-root (런북 :121-133) ─────────────────────────────────────────
# ⚠️ 드릴 실측: 부트스트랩 시크릿의 root_token 은 온프렘이 초기 root 를 폐기해 무효(login 403).
# → **recovery 키로 generate-root 가 주경로**다.
SECRET=$(aws secretsmanager get-secret-value --secret-id cledyu/vault/bootstrap \
  --region ap-northeast-2 --query SecretString --output text)
THRESH=$(echo "$SECRET" | jq -r .recovery_keys_threshold)
GR=$(kubectl -n vault exec vault-0 -- sh -c "$VC vault operator generate-root -init -format=json")
NONCE=$(echo "$GR" | jq -r .nonce)
OTP=$(echo "$GR" | jq -r .otp)

# 마지막 키만 encoded_root_token 을 낸다 — 런북 그대로.
ENC=""
for k in $(echo "$SECRET" | jq -r '.recovery_keys_b64[]' | head -n "$THRESH"); do
  ENC=$(kubectl -n vault exec vault-0 -- sh -c \
    "$VC vault operator generate-root -nonce=$NONCE -format=json '$k'" |
    jq -r '.encoded_root_token // empty')
done
unset SECRET # recovery 키를 더 들고 있지 않는다
[ -n "$ENC" ] || {
  echo "❌ generate-root 실패 — encoded_root_token 없음(recovery 키/threshold 확인)"
  exit 1
}

NEWROOT=$(kubectl -n vault exec vault-0 -- sh -c \
  "$VC vault operator generate-root -decode=$ENC -otp=$OTP -format=json" | jq -r .token)
if [ -z "$NEWROOT" ] || [ "$NEWROOT" = "null" ]; then
  echo "❌ generate-root decode 실패"
  exit 1
fi

# ── 복원 확인 게이트 (런북 :135-137 — "확인한다"를 게이트로) ──────────────────
# ⚠️ 값을 **출력하지 않고** 판정한다. 비어 있으면 복원 실패.
kubectl -n vault exec vault-0 -- sh -c \
  "$VC VAULT_TOKEN=$NEWROOT vault kv list cledyu" | grep -q . ||
  {
    echo "❌ cledyu kv 가 비었다 — 복원 실패"
    exit 1
  }
echo "복원 확인 OK ✅"

# 복원 후 quorum 재확인 (런북 :140-142) — leader 없으면 ESO 인증이 진행되면 안 된다.
kubectl -n vault exec vault-0 -- sh -c \
  "$VC VAULT_TOKEN=$NEWROOT vault operator raft list-peers -format=json" |
  jq -e '.data.config.servers | map(select(.leader == true)) | length == 1' > /dev/null ||
  {
    echo "❌ raft leader 부재 — quorum 실패"
    exit 1
  }
echo "raft quorum OK ✅"

# ── 5) k8s auth 를 EKS 용으로 재설정 (런북 :167-173) ──────────────────────────
# 복원 스냅샷의 auth/kubernetes/config 는 **온프렘** CA·reviewer JWT 라 EKS API 검증이 실패한다.
# vault 파드 안에서 재실행하면 @경로가 **EKS 파드의** SA CA·토큰을 읽어 교정된다.
kubectl -n vault exec vault-0 -- sh -c \
  "$VC VAULT_TOKEN=$NEWROOT vault write auth/kubernetes/config \
     kubernetes_host=https://kubernetes.default.svc:443 \
     kubernetes_ca_cert=@/var/run/secrets/kubernetes.io/serviceaccount/ca.crt \
     token_reviewer_jwt=@/var/run/secrets/kubernetes.io/serviceaccount/token \
     disable_local_ca_jwt=true"
unset NEWROOT # 시크릿 구간 끝

# ── 6) ESO 재기동 (런북 :176-178 — 드릴 실측) ────────────────────────────────
# ESO 컨트롤러는 Vault 가 늦게(복원 후) 살아나면 **실패한 provider client 를 캐시**해 store 를
# InvalidProviderConfig 로 붙잡는다 → k8s auth 재설정 직후 재기동으로 캐시를 버린다.
# ⚠️ 이게 빠지면 이후 **모든 Secret 이 안 생긴다**.
set -x # ← 여기부터는 시크릿을 만지지 않는다
kubectl -n external-secrets rollout restart deploy/external-secrets
kubectl -n external-secrets rollout status deploy/external-secrets --timeout=120s

# 검증 (런북 :181) — store 가 Ready 여야 [8] 의 CNPG 자격·[9] 의 VE AWS 키가 생긴다.
for i in $(seq 1 24); do
  READY=$(kubectl get clustersecretstore vault-backend \
    -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2> /dev/null || echo "")
  [ "$READY" = "True" ] && break
  echo "vault-backend Ready 대기 ($i/24)"
  sleep 5
done
[ "$READY" = "True" ] || {
  echo "❌ clustersecretstore vault-backend 가 Ready 아님 — ESO→Vault 인증 실패"
  exit 1
}

echo "✅ Vault 복원 + k8s auth 재설정 + ESO 재기동 완료"
