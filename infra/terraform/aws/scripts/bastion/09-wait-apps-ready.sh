#!/bin/bash
# [9] WaitAppsReady — 앱이 다 뜰 때까지 대기 + [10] 이 쓸 ALB 호스트명 기록.
#
# 🆕 **이식이 아니라 신규 조립이다.** 런북 체크리스트(:357-370)는 `- [ ]` 항목에 명령 조각이 백틱으로
# 박힌 **사람용 확인표**이고, [6][7][8][10][11][12] 까지 전부 다루는 **마스터 체크리스트**다.
# 그리고 **기다리는 명령이 없다** — 사람이 `get` 으로 보고 판단한다. 조각을 모아 기계 게이트로 만든다.
#   Kafka    : :359 `get kafka`(READY=True)  → wait --for=condition=Ready (Strimzi 공식 문서 방식)
#   VE       : :364 `get deploy`(Available)  → rollout status
#   Keycloak : :409 의 wait 를 **여기로 재배치** (원래 §DNS 전환 안에 있다 — [10] 은 non-VPC Lambda 라
#              kubectl 을 못 쓰므로 게이트가 여기로 와야 한다)
#
# ⚠️ **G1 함정 자리다.** 런북이 "미배포는 정상"이라 적어둔 것들(ServiceMonitor 2종·CiliumNetworkPolicy·
# plain NetworkPolicy·lab-ssh-key)은 **게이트하지 않는다** — EKS 에선 없는 게 정상이라 검사하면
# **건강한 DR 에서 오탐**한다. 오퍼레이터가 관리하는 condition=Ready 에만 의존하고 상태를 직접 파싱하지 않는다.
set -euo pipefail
set -x

# ⚠️ SSM RunCommand 는 HOME 을 설정하지 않는다(스펙 §11.12).
export HOME=/root
aws eks update-kubeconfig --name cledyu-dr --region ap-northeast-2

# ── 존재 게이트 (2026-07-16 실측 — 초안에 없던 함정) ─────────────────────────
# ⚠️ **`kubectl wait` 는 대상이 없으면 기다리지 않고 즉시 에러다.** 아래 `--timeout=900s` 등은
# "CR 이 **이미** 있을 때"만 의미가 있다. kubectl v1.34.0(bastion 실제 버전) 실측:
#   · 이름 지정    : `Error from server (NotFound): pods "x" not found` → exit 1, 즉시
#   · --all 로 0개 : `error: no matching resources found`               → exit 1, 즉시
# `rollout status` 도 같다. 즉 ArgoCD sync 가 조금만 늦으면 **건강한 DR 이 오탐 실패**한다.
#
# 그리고 [9] 의 실패는 뒤가 없다: ALB 파라미터가 안 써지고 → [10] 이 설계대로 fail-closed →
# **DNS 미전환 = 전부 복구됐는데 서비스가 안 돌아온다**(아래 put-parameter 주석의 F3 와 같은 결말).
#
# 08-restore-data.sh:38-53 이 **같은 함정을 같은 방식으로** 막는다("방금 지운 CR 을 바로 기다리면
# 안 된다 — 객체가 없어 wait 가 즉시 에러난다"). 그 선례를 따른다. 09 만 빠져 있었다.
#
# ⚠️ CRD 자체가 아직 없는 경우(오퍼레이터 sync 가 늦음)도 이 루프가 함께 흡수한다 —
#    `get` 이 "server doesn't have a resource type" 으로 실패하는 것도 재시도 대상이다.
wait_exists() { # $1=설명, $2.. = kubectl 인자
  local desc=$1
  shift
  local i
  # 10분 — 08 의 ArgoCD 재생성 대기와 동일. (주석을 for 줄 안에 두면 shfmt 가 줄을 쪼갠다)
  for i in $(seq 1 60); do
    kubectl "$@" > /dev/null 2>&1 && return 0
    echo "ArgoCD sync 대기: $desc ($i/60)"
    sleep 10
  done
  echo "❌ $desc 가 10분 안에 안 나타남 — ArgoCD sync 상태 확인"
  return 1 # set -e 가 여기서 스크립트를 끝낸다
}

# Kafka — 의존: cert-manager CA + trust-manager Bundle + gp3(런북 :359). **Vault 와 무관**이라
# [7] 뒤에 와도 안전하다(드릴이 검증한 건 "의존 순서"이지 "줄 순서"가 아니다).
wait_exists "kafka/cledyu-kafka" -n kafka get kafka cledyu-kafka
kubectl -n kafka wait --for=condition=Ready kafka/cledyu-kafka --timeout=900s

# 토픽도 CR 이고 오퍼레이터가 Ready 를 관리한다 → 이름 하드코딩 없이 전부 대기.
# 이름을 박지 않는 이유: 랩이 늘면 토픽이 느는데 스크립트를 안 고치면 **새 토픽을 안 기다리고 통과**한다.
#
# ⚠️ **`--all` 은 0개일 때 공허참으로 통과하는 게 아니라 에러난다**(위 실측). 이름을 안 박는 대신
# **대수 검증**을 먼저 한다 — 04-wait-nodes-ready.sh:36-47 과 같은 형태다(거기선 노드 3대,
# 여기선 "1개 이상"). 개수를 박으면 랩이 늘 때 위 주석의 문제가 그대로 돌아오므로 `>0` 만 본다.
#
# ⚠️ **`|| true` 필수 — 이게 없으면 이 루프가 CRD 미등록을 못 흡수하고 죽는다**(2026-07-16 실측, codex P2).
#   kafkatopic CRD 가 아직 없으면 `get` 이 non-zero 를 내고, `set -o pipefail` 아래서 그게 `wc -l` 파이프의
#   종료코드가 되어 `NT=$(...)` 대입이 실패 → **set -e 로 스크립트가 여기서 죽는다**(재시도 못 함).
#   `cmd | wc -l || true` 는 실패해도 wc 가 빈 입력에 "0" 을 내므로 NT=0 → 루프가 정상 재시도한다.
#   (실제 흐름에선 위 `wait kafka/cledyu-kafka` 가 Strimzi CRD 존재를 이미 보장하지만 — Kafka·KafkaTopic
#    CRD 는 한 오퍼레이터가 함께 깐다 — 그 방어에 기대지 않고 이 줄 자체를 자기완결로 둔다.)
for i in $(seq 1 60); do
  NT=$(kubectl -n kafka get kafkatopic --no-headers 2> /dev/null | wc -l || true)
  [ "${NT:-0}" -gt 0 ] && break
  echo "ArgoCD sync 대기: kafkatopic ($i/60)"
  sleep 10
done
[ "${NT:-0}" -gt 0 ] || {
  echo "❌ KafkaTopic 이 하나도 없다 — ArgoCD sync 상태 확인"
  exit 1
}
# ✅ **미확정 해소(2026-07-16)** — KafkaTopic 에 Ready 조건이 **있다.** 쓰는 버전(Strimzi 0.45.2)의
# CRD 를 직접 확인했다(`helm template --include-crds`): kafkatopics.kafka.strimzi.io v1beta2 가
# additionalPrinterColumns 에 `Ready ← .status.conditions[?(@.type=="Ready")].status` 를 선언하고
# status.properties 에 conditions 가 있다. 오퍼레이터가 안 세팅하는 조건에 printer column 을 달지
# 않으므로 이 줄은 유효하다 — **삭제하지 않는다.**
# 그리고 이 wait 은 위의 `kafka/cledyu-kafka` Ready **뒤**라 Topic Operator(Entity Operator 의 일부)가
# 이미 떠 있다 → 조건이 채워질 주체가 존재한다.
kubectl -n kafka wait --for=condition=Ready kafkatopic --all --timeout=300s

# VE 선행: Kafka(KafkaUser·client cert) + **Vault 복원→ESO 로 cledyu-validation-engine-aws Secret**
# (AWS 키 non-optional) — [7]·[8] 이 끝난 뒤라 충족돼 있다(:365).
# `rollout status` 도 대상이 없으면 즉시 에러다 → 존재부터 확인한다.
wait_exists "deploy/validation-engine" -n validation-engine get deploy validation-engine
kubectl -n validation-engine rollout status deploy/validation-engine --timeout=600s

# auth 는 Keycloak Ready 이후에만 넘긴다 — 조기 전환 시 ALB keycloak 타겟 unhealthy → 404/503(:406).
wait_exists "keycloak/cledyu-keycloak" -n keycloak get keycloak cledyu-keycloak
kubectl -n keycloak wait --for=condition=Ready keycloak/cledyu-keycloak --timeout=600s

# ⚠️ bootstrap svc 응답 확인(:359 의 `cledyu-kafka-kafka-bootstrap.kafka.svc:9093`)은 **제외한다.**
# Kafka CR 이 Ready 면 리스너가 준비된 것이고(Strimzi 가 status.listeners 를 채움) 9093 은 TLS 라
# curl 로 검사가 안 된다. **어설프게 만들면 G1 처럼 틀린다** — 중복 검사를 빼는 게 낫다.

# ── [10] SwitchDNS 가 읽을 ALB 호스트명 기록 ────────────────────────────────
# non-VPC Lambda 는 private EKS 에 못 닿고 자식 SM 은 stdout 을 CloudWatch 로 보낼 뿐이라
# SSM 파라미터가 유일한 전달 경로다(설계 §5.1.2).
# ⚠️ 실패 원인이 **두 가지**고 둘 다 대기가 필요한데 초안은 둘 다 안 기다렸다:
#   (a) Ingress 객체 자체가 없다       → ArgoCD sync 대기(위 존재 게이트와 같은 클래스).
#       초안은 `ALB=$(kubectl get ...)` 라 **set -e 가 여기서 죽여** 아래 친절한 메시지에 도달조차
#       못 했다 — 운영자는 원인 대신 raw kubectl 에러를 본다.
#   (b) 객체는 있는데 hostname 이 빈다 → alb-controller 가 프로비저닝 중(통상 2~3분).
#       초안의 `[ -n "$ALB" ]` 는 이 경우를 **재시도 없이 즉시 실패**로 처리했다.
# [9] 는 마지막 줄이 put-parameter 다 → 여기서 죽으면 복구를 다 해놓고 DNS 만 안 넘어간다.
wait_exists "ingress/api" -n api get ingress api
# 5분 — ALB 프로비저닝(통상 2~3분)
for i in $(seq 1 30); do
  ALB=$(kubectl -n api get ingress api \
    -o jsonpath='{.status.loadBalancer.ingress[0].hostname}' 2> /dev/null || echo "")
  [ -n "$ALB" ] && break
  echo "ALB 프로비저닝 대기 ($i/30)"
  sleep 10
done
[ -n "${ALB:-}" ] || {
  echo "❌ ALB 호스트명이 5분 안에 안 나옴 — alb-controller 로그 확인"
  exit 1
}

# ⚠️ 이 호출엔 bastion 롤의 ssm:PutParameter 가 필요하다(Step 8 이 만든다). 없으면 **~40분 복구를
# 다 끝내고 마지막 줄에서** AccessDenied 로 죽고, [10] 은 설계대로 fail-closed 라 DNS 를 안 넘긴다
# → 전부 복구됐는데 서비스가 안 돌아온다(F3).
aws ssm put-parameter --region ap-northeast-2 --name /cledyu-dr/failover/alb-hostname \
  --type String --overwrite --value "$ALB"

echo "✅ Kafka·VE·Keycloak Ready · ALB=$ALB"
