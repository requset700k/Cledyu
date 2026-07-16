#!/bin/bash
# [7] RestoreVault — Vault init → S3 스냅샷 force restore → generate-root → k8s auth 재설정 → ESO 재기동.
# 📋 런북 75-160(§복원 절차) + 161-194(§Vault k8s auth 재설정) + **195-206(§tmpfs / 잔존 주의)** 이식.
#
# 🔒 **이 스크립트만 `set -x` 를 쓰지 않는다(리뷰 지적 C6).** set -x 는 실행 명령을 **인자까지** 추적하므로
# Vault init 출력·$INIT_ROOT·$NEWROOT·recovery 키가 stdout 에 찍힌다. 그 출력은 자식 SM 이
# **CloudWatch 로그그룹**에 보내고 `stdoutTail` 로 **Discord 실패 알림**에도 싣는다 → **루트 토큰 평문 노출.**
# echo 로도 흘리지 않는다 — 확인 게이트는 값을 출력하지 않고 판정한다.
#
# 🔒 **그리고 시크릿을 `kubectl exec` 의 명령 인자로 넘기지 않는다 — stdin 으로 넘긴다.**
# 위의 set -x 방어는 **stdout 한 층만** 막았고 이 층이 뚫려 있었다(2026-07-16 실측):
#   exec 의 command 인자는 **requestURI 쿼리스트링**에 실려 API 서버 감사 로그로 나간다.
#     /api/v1/namespaces/vault/pods/vault-0/exec?command=sh&command=-c&command=<명령 전문>
#   eks-dr.tf:71 이 `cluster_enabled_log_types = ["audit", ...]` 라 그게 CloudWatch 로 간다.
#   실측(07-13~14 드릴 4회분, /aws/eks/cledyu-dr/cluster, 보존 90일):
#     · VAULT_TOKEN=       24건  ($INIT_ROOT · $NEWROOT)
#     · generate-root -nonce 12건 (= 4회 x threshold 3) → **recovery 키 전량**
#     · generate-root -decode 4건 ($ENC + $OTP → 루트 토큰 복호 가능)
#   그 recovery 키는 DR 것이 아니라 cledyu/vault/bootstrap 의 **온프렘 운영 Vault** 키다(아래 §4).
#   `logs:FilterLogEvents` 만 있으면 읽힌다 — 망 격리 말고는 방어가 없었다.
#
# → **규칙: 토큰·키는 `printf '%s' "$X" | kubectl exec -i ... -- sh -c "... \$(cat) ..."`.**
#   stdin 은 requestURI 에 안 실린다. `\$(cat)` 은 bastion 셸이 아니라 **파드 안 sh** 가 평가하므로
#   명령 문자열 자체엔 시크릿이 없다. 값이 2개면 `read a; read b` 로 줄 단위로 받는다.
#   ⚠️ 새 exec 를 추가할 때 이 규칙을 어기면 감사 로그로 조용히 샌다 — 스크립트는 정상 동작하므로
#      테스트로는 절대 안 잡힌다.
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

# ── 0) vault-0 의 **API 가 응답할 때까지** 대기 ─────────────────────────────
# 🔴 **파드 Ready 를 기다리면 안 된다 — 영원히 안 온다**(2026-07-16 T3 드릴 실측).
# 초안은 `kubectl -n vault wait --for=condition=Ready pod/vault-0 --timeout=300s` 였고
# **구조적으로 성공이 불가능**했다:
#   1. 차트의 readinessProbe 가 `vault status -tls-skip-verify` 다(실측으로 확인한 helm 기본값)
#   2. **초기화 안 된 Vault 는 `vault status` 가 exit 2**(sealed) — 아래 §2 게이트가 쓰는 그 성질
#   3. → probe 실패 → 파드가 **영원히 0/1**
#   4. → `wait --for=condition=Ready` 가 300s 타임아웃 → **`vault operator init` 에 도달조차 못 함**
#   **init 만이 Ready 를 만들 수 있는데 init 이 이 wait 뒤에 있다 — 닭-달걀이다.**
#   실측: vault-0/1/2 전부 `0/1 Running` · `Ready=False` · `{"initialized":false,"sealed":true}`
#         → `error: timed out waiting for the condition on pods/vault-0`
#
# **왜 지금까지 안 드러났나:** 07 은 오늘 **처음 스크립트로** 돌았다(T3 Step 10 이 실측 없이 지나갔다 —
# 08-restore-data.sh 의 H5 주석이 같은 사실을 기록). 7/13·14 드릴은 **런북(사람)** 경로였고
# 사람은 파드가 0/1 이어도 그냥 init 을 친다 — **스크립트만 Ready 를 기다린다.**
# (§11.12 와 같은 뿌리: "명령은 맞는데 실행 주체가 다르다".)
#
# → **Ready 가 아니라 "API 가 응답하는가"를 본다.** sealed 여도 status 는 JSON 을 준다(exit 2 지만
#   파싱 가능). `kubectl exec` 는 readiness 와 무관하게 Running 컨테이너에 붙는다(실측).
# ⚠️ 여기서도 **출력을 먼저 캡처**한다 — `exec ... | jq` 직결은 exit 2 가 pipefail 로 jq 판정을 덮는다(§2).
# 여기서 얻은 $ST 를 아래 §2 게이트가 그대로 쓴다 — 사이에 initialized 를 바꾸는 것이 없다.
# 5분 (주석을 for 줄 안에 두면 shfmt 가 줄을 쪼갠다)
for i in $(seq 1 60); do
  ST=$(kubectl -n vault exec vault-0 -- sh -c "$VC vault status -format=json" 2> /dev/null || true)
  echo "$ST" | jq -e 'has("initialized")' > /dev/null 2>&1 && break
  echo "vault-0 API 대기 ($i/60)"
  sleep 5
done
echo "$ST" | jq -e 'has("initialized")' > /dev/null 2>&1 || {
  echo "❌ vault-0 API 가 5분째 무응답 — kubectl -n vault logs vault-0 확인"
  exit 1
}
echo "vault-0 API 응답 OK ✅ (Ready 는 init 뒤에 온다 — 여기서 기다리지 않는다)"

# ── 1) 스냅샷을 **init 보다 먼저** 파드 안까지 넣어둔다 (런북 :97-98 을 앞으로 재배치) ──────
# 🔀 **런북과 순서가 다르다 — 의도된 재배치다(2026-07-16).**
# init 과 restore 사이의 모든 줄은 **되돌릴 수 없는 구간**이다: init 은 성공했는데 뒤가 실패하면
# Vault 는 "초기화됨 + fresh keyring" 인데 그 keyring 의 recovery 키를 이 스크립트가 **안 남기므로**
# (감사 로그·stdout 유출 방어) 아무도 root 를 못 얻는다 → **복구 불능**. [3] 의 PVC 정리로 재실행은
# 가능해졌지만, 그건 **파이프라인을 [3] 부터 다시 태우는 것**이라 재해 중 20~30분이 날아간다.
# → init 에 의존하지 않는 것(S3 다운로드·파드로 복사)을 **전부 앞으로 빼서** 그 구간을 최소화한다.
#   남는 구간은 "3-peer 대기 + restore 한 줄"뿐이다(peer 대기는 토큰이 필요해 뒤로 못 뺀다).
# ⚠️ 런북은 `s3 ls | tail -1`(최신 고정)이지만 우리는 **승인 시 고른 값**을 쓴다 — 드롭다운의 존재 이유다.
aws s3 cp "s3://cledyu-lab-dr-backups/${SNAPSHOT_KEY}" "$WORK/vault-raft.snap"
[ -s "$WORK/vault-raft.snap" ] || {
  echo "❌ 스냅샷이 비었거나 없음: $SNAPSHOT_KEY"
  exit 1
}
kubectl -n vault cp "$WORK/vault-raft.snap" vault-0:/tmp/vault-raft.snap

# ── 2) init (런북 :86-88) ────────────────────────────────────────────────────
# KMS auto-unseal 이라 unseal 키 입력 불요 — init 만 하면 자동 unseal 된다.
# 런북의 <INIT_ROOT> 플레이스홀더(사람이 눈으로 보고 넣는 값)를 변수 캡처로 대체(변환 규칙 3).
#
# ⚠️ **이미 초기화돼 있으면 여기서 죽는다** — 그리고 그 에러(`Vault is already initialized`)는
# 원인을 안 가리킨다. 운영자는 재해 중에 "왜 Vault 가 이미 초기화돼 있지?"부터 조사하게 된다.
# 진짜 원인은 [3] 의 P1e(raft PVC 정리)가 안 돌았다는 것이다 — [3] 을 건너뛰고 [7] 만 재실행했거나,
# [3] 의 PVC 정리가 실패했는데 넘어갔거나. **먼저 판정해서 원인을 말해준다.**
# (여기서 복구를 시도하지 않는 이유: 이미 초기화된 Vault 의 keyring 이 fresh 인지 복원본인지
#  스크립트가 알 방법이 없고, 그 둘은 root 획득 경로가 서로 다르다. 추측 대신 [3] 부터 재실행이 정답.)
# ⚠️ **§0 이 캡처한 $ST 를 그대로 쓴다 — 다시 조회하지 않는다.**
#    `vault status | jq` 로 직결하면 안 되기 때문이다(2026-07-16 실측): `vault status` 는 **sealed 면
#    exit 2** 이고, `set -o pipefail` 아래서 그 2 가 jq 의 판정을 덮어써 파이프라인이 non-zero →
#    if 가 false → **"초기화됨 + sealed" 를 그냥 통과시킨다**(= 게이트가 막으려던 바로 그 상태).
#    3상태(fresh / 초기화+sealed / 초기화+unsealed) 실측으로 확인했다. 캡처 형태만이 안전하고,
#    §0 이 이미 그 형태로 잡아뒀으므로 재조회는 함정을 한 번 더 만들 뿐이다.
if echo "$ST" | jq -e '.initialized == true' > /dev/null 2>&1; then
  echo "❌ Vault 가 이미 초기화돼 있다 — [7] 은 fresh Vault 를 전제한다."
  echo "   원인: [3] CleanWarmEtcd 의 P1e(Vault raft PVC 정리)가 안 돌았다."
  echo "   → [7] 만 재실행하지 말고 **[3] 부터** 파이프라인을 다시 태울 것."
  exit 1
fi

INIT=$(kubectl -n vault exec vault-0 -- sh -c "$VC vault operator init -format=json")
INIT_ROOT=$(echo "$INIT" | jq -r .root_token)
if [ -z "$INIT_ROOT" ] || [ "$INIT_ROOT" = "null" ]; then
  echo "❌ init 실패 — root_token 없음"
  exit 1
fi
unset INIT # 출력 전체를 메모리에 붙들지 않는다

# 3-node(스냅샷 3-peer 와 동일 토폴로지) 형성 대기 (런북 :90-92)
for i in $(seq 1 30); do
  PEERS=$(printf '%s' "$INIT_ROOT" | kubectl -n vault exec -i vault-0 -- sh -c \
    "$VC VAULT_TOKEN=\$(cat) vault operator raft list-peers -format=json" 2> /dev/null |
    jq -r '.data.config.servers | length' 2> /dev/null || echo 0)
  [ "${PEERS:-0}" -ge 3 ] && break
  echo "raft peers 대기 ${PEERS:-0}/3 ($i/30)"
  sleep 10
done
[ "${PEERS:-0}" -ge 3 ] || {
  echo "❌ raft 3-peer 미형성(현재 ${PEERS:-0}) — 스냅샷과 토폴로지 불일치"
  exit 1
}

# ── 3) force restore (런북 :107-109) ─────────────────────────────────────────
# 스냅샷은 다른 클러스터(온프렘)에서 왔고 방금 init 한 EKS Vault 는 recovery/shamir 키가 달라
# 일반 restore 는 seal 일관성 검사에서 거부된다 → **-force 필수**.
# (스냅샷은 §1 에서 이미 파드 안 /tmp 에 들어와 있다 — init 앞으로 뺐다.)
printf '%s' "$INIT_ROOT" | kubectl -n vault exec -i vault-0 -- sh -c \
  "$VC VAULT_TOKEN=\$(cat) vault operator raft snapshot restore -force /tmp/vault-raft.snap"
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
# ⚠️ 키는 stdin 으로만 넘긴다 — 인자로 넘기면 requestURI 로 감사 로그에 박힌다(헤더 §stdin 규칙).
# 이 줄이 실측에서 12건(4회 x 3키) 새던 자리다. $NONCE 는 시크릿이 아니라(키 없이는 무용) 인자로 둔다.
for k in $(echo "$SECRET" | jq -r '.recovery_keys_b64[]' | head -n "$THRESH"); do
  ENC=$(printf '%s' "$k" | kubectl -n vault exec -i vault-0 -- sh -c \
    "$VC vault operator generate-root -nonce=$NONCE -format=json \"\$(cat)\"" |
    jq -r '.encoded_root_token // empty')
done
unset SECRET # recovery 키를 더 들고 있지 않는다
[ -n "$ENC" ] || {
  echo "❌ generate-root 실패 — encoded_root_token 없음(recovery 키/threshold 확인)"
  exit 1
}

# $ENC·$OTP 는 **둘 다** 시크릿이다(합치면 루트 토큰) → 값이 2개라 줄 단위로 받는다.
# read -r: 백슬래시 해석 금지(base64 안전). vault 이미지는 alpine=busybox ash 라 -r 지원.
NEWROOT=$(printf '%s\n%s\n' "$ENC" "$OTP" | kubectl -n vault exec -i vault-0 -- sh -c \
  "read -r e; read -r o; $VC vault operator generate-root -decode=\"\$e\" -otp=\"\$o\" -format=json" |
  jq -r .token)
if [ -z "$NEWROOT" ] || [ "$NEWROOT" = "null" ]; then
  echo "❌ generate-root decode 실패"
  exit 1
fi

# ── 복원 확인 게이트 (런북 :135-137 — "확인한다"를 게이트로) ──────────────────
# ⚠️ 값을 **출력하지 않고** 판정한다. 비어 있으면 복원 실패.
printf '%s' "$NEWROOT" | kubectl -n vault exec -i vault-0 -- sh -c \
  "$VC VAULT_TOKEN=\$(cat) vault kv list cledyu" | grep -q . ||
  {
    echo "❌ cledyu kv 가 비었다 — 복원 실패"
    exit 1
  }
echo "복원 확인 OK ✅"

# 복원 후 quorum 재확인 (런북 :140-142) — leader 없으면 ESO 인증이 진행되면 안 된다.
printf '%s' "$NEWROOT" | kubectl -n vault exec -i vault-0 -- sh -c \
  "$VC VAULT_TOKEN=\$(cat) vault operator raft list-peers -format=json" |
  jq -e '.data.config.servers | map(select(.leader == true)) | length == 1' > /dev/null ||
  {
    echo "❌ raft leader 부재 — quorum 실패"
    exit 1
  }
echo "raft quorum OK ✅"

# ── 5) k8s auth 를 EKS 용으로 재설정 (런북 :167-173) ──────────────────────────
# 복원 스냅샷의 auth/kubernetes/config 는 **온프렘** CA·reviewer JWT 라 EKS API 검증이 실패한다.
# vault 파드 안에서 재실행하면 @경로가 **EKS 파드의** SA CA·토큰을 읽어 교정된다.
printf '%s' "$NEWROOT" | kubectl -n vault exec -i vault-0 -- sh -c \
  "$VC VAULT_TOKEN=\$(cat) vault write auth/kubernetes/config \
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
