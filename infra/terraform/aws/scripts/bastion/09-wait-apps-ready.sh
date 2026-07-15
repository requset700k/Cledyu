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

# Kafka — 의존: cert-manager CA + trust-manager Bundle + gp3(런북 :359). **Vault 와 무관**이라
# [7] 뒤에 와도 안전하다(드릴이 검증한 건 "의존 순서"이지 "줄 순서"가 아니다).
kubectl -n kafka wait --for=condition=Ready kafka/cledyu-kafka --timeout=900s

# 토픽도 CR 이고 오퍼레이터가 Ready 를 관리한다 → 이름 하드코딩 없이 전부 대기.
# 이름을 박지 않는 이유: 랩이 늘면 토픽이 느는데 스크립트를 안 고치면 **새 토픽을 안 기다리고 통과**한다.
# 🔴 **미확정 — KafkaTopic 에 Ready 조건이 있는지 Step 10 에서 확인한다**(Kafka CR 은 Strimzi 문서로
# 확인했으나 KafkaTopic 은 미확인). 없으면 이 줄을 **삭제**하고 Kafka CR Ready 만 게이트한다.
kubectl -n kafka wait --for=condition=Ready kafkatopic --all --timeout=300s

# VE 선행: Kafka(KafkaUser·client cert) + **Vault 복원→ESO 로 cledyu-validation-engine-aws Secret**
# (AWS 키 non-optional) — [7]·[8] 이 끝난 뒤라 충족돼 있다(:365).
kubectl -n validation-engine rollout status deploy/validation-engine --timeout=600s

# auth 는 Keycloak Ready 이후에만 넘긴다 — 조기 전환 시 ALB keycloak 타겟 unhealthy → 404/503(:406).
kubectl -n keycloak wait --for=condition=Ready keycloak/cledyu-keycloak --timeout=600s

# ⚠️ bootstrap svc 응답 확인(:359 의 `cledyu-kafka-kafka-bootstrap.kafka.svc:9093`)은 **제외한다.**
# Kafka CR 이 Ready 면 리스너가 준비된 것이고(Strimzi 가 status.listeners 를 채움) 9093 은 TLS 라
# curl 로 검사가 안 된다. **어설프게 만들면 G1 처럼 틀린다** — 중복 검사를 빼는 게 낫다.

# ── [10] SwitchDNS 가 읽을 ALB 호스트명 기록 ────────────────────────────────
# non-VPC Lambda 는 private EKS 에 못 닿고 자식 SM 은 stdout 을 CloudWatch 로 보낼 뿐이라
# SSM 파라미터가 유일한 전달 경로다(설계 §5.1.2).
ALB=$(kubectl -n api get ingress api -o jsonpath='{.status.loadBalancer.ingress[0].hostname}')
[ -n "$ALB" ] || {
  echo "❌ ALB 호스트명 비어있음 — Ingress 미프로비저닝"
  exit 1
}

# ⚠️ 이 호출엔 bastion 롤의 ssm:PutParameter 가 필요하다(Step 8 이 만든다). 없으면 **~40분 복구를
# 다 끝내고 마지막 줄에서** AccessDenied 로 죽고, [10] 은 설계대로 fail-closed 라 DNS 를 안 넘긴다
# → 전부 복구됐는데 서비스가 안 돌아온다(F3).
aws ssm put-parameter --region ap-northeast-2 --name /cledyu-dr/failover/alb-hostname \
  --type String --overwrite --value "$ALB"

echo "✅ Kafka·VE·Keycloak Ready · ALB=$ALB"
