#!/bin/bash
# [8] RestoreData — 구 CNPG CR 제거 → ArgoCD 재생성 → bootstrap.recovery 가 최신 S3 로 복원.
#
# 🔀 **이식이 아니라 재배치다.** 런북 332-343 은 "root-app 적용 직후, CNPG 차트가 Cluster CR 을 만들기
# 전에" 지우라고 명시하지만, 우리는 [7] Vault 복원(~30분) **뒤**에 지운다 = ArgoCD 가 이미 만든 CR 을
# 지우고 재생성을 기다리는 **다른 동작**이다. 명령 2줄만 같다.
#
# **재배치가 오히려 옳다:** [7] 전엔 ESO 가 Vault 를 못 읽어 postgres-credentials-cnpg 를 못 만들고,
# 그 Secret 은 managed.roles[].passwordSecret 이라 [6] 시점 CR 은 어차피 제대로 뜨지 못한다.
# **전제 확인됨:** 재생성은 ArgoCD selfHeal 에 달렸고 data-postgres-cnpg-dr.yaml:31 ·
# data-keycloak-pg-dr.yaml:32 둘 다 selfHeal: true 다(런북 경로는 "첫 sync 가 만든다"라 selfHeal 이
# 필요 없었다 — 재배치가 **새 의존을 만들었고** 그게 마침 충족된 것이다).
set -euo pipefail
set -x

# ⚠️ SSM RunCommand 는 HOME 을 설정하지 않는다(스펙 §11.12).
export HOME=/root
aws eks update-kubeconfig --name cledyu-dr --region ap-northeast-2

# [P1b] 재-failover 시 잔존 CNPG CR 제거 (런북 338-342 이식). 단발(첫) failover 는 CR 이 없어 no-op.
kubectl -n postgres delete cluster cledyu-pg --ignore-not-found
kubectl -n keycloak delete cluster keycloak-pg --ignore-not-found

# ✅ **미확정 해소(2026-07-16) — delete 가 PVC 도 지운다. 여기에 PVC 삭제를 추가할 필요 없다.**
# H5 는 "PVC 가 남아 재생성된 Cluster 가 재사용 → S3 복원이 아니라 stale 데이터로 뜨는데
# `wait Ready` 도 [12] 의 count(*)>0 도 **통과**하는, 이 계획에서 가장 조용한 실패 경로"였다.
#
# **실측(kind + 레포와 동일한 CNPG 차트 0.26.1):** CNPG 가 PVC 에 `ownerReferences: Cluster/<name>`
# 를 붙이므로 Cluster 삭제 → 가비지 컬렉션이 PVC 를 연쇄 삭제한다(Terminating 거쳐 ~15초 내 소멸).
# 테스트 Cluster 는 레포와 **구조가 일치**한다(instances: 1 + storage: 만 — walStorage·tablespaces·
# pvcTemplate 없음, cluster.yaml:16-24 확인). storageClass 만 다른데(gp3 vs kind 기본) PVC 삭제는
# ownerRef GC 라 storageClass 와 무관하다.
#
# ⚠️ **그래도 T7 Step 4 의 표식(dr_drill_marker)은 유지한다.** 위 실측은 "지금 이 버전이 이렇게
#    동작한다"는 것이고, CNPG 가 동작을 바꾸거나 누가 retention 정책을 붙이면 조용히 되살아난다.
#    표식이 그 회귀를 잡는 **실환경 백스톱**이다 — 실측이 표식을 대체하지 않는다.
# 아래 get pvc 는 드릴 로그에 남기는 관찰일 뿐 게이트가 아니다.
kubectl -n postgres get pvc -l cnpg.io/cluster=cledyu-pg 2>&1 | head -3 || true
kubectl -n keycloak get pvc -l cnpg.io/cluster=keycloak-pg 2>&1 | head -3 || true

# ⚠️ 아래 대기는 **런북에 없다 — 신규다.** 런북 332-343 의 실제 내용은 delete 2줄이 전부고,
# 사람이 체크리스트(:363)로 "cledyu-pg-rw Ready" 를 눈으로 확인한다.
#
# **방금 지운 CR 을 바로 기다리면 안 된다** — ArgoCD 가 재생성하기 전이라 객체가 없어 `wait` 가
# "no matching resources found" 로 **즉시 에러**난다. ArgoCD sync 를 기다린 뒤 대기한다.
for i in $(seq 1 60); do
  if kubectl -n postgres get cluster cledyu-pg > /dev/null 2>&1 &&
    kubectl -n keycloak get cluster keycloak-pg > /dev/null 2>&1; then break; fi
  echo "ArgoCD 가 CNPG CR 을 재생성하기를 대기 $i/60"
  sleep 10
done
kubectl -n postgres get cluster cledyu-pg > /dev/null || {
  echo "❌ ArgoCD 가 cledyu-pg 를 재생성하지 않음 — selfHeal 확인"
  exit 1
}
kubectl -n keycloak get cluster keycloak-pg > /dev/null || {
  echo "❌ ArgoCD 가 keycloak-pg 를 재생성하지 않음 — selfHeal 확인"
  exit 1
}

# bootstrap.recovery 는 S3 에서 base+WAL 을 받아 재생하므로 오래 걸린다.
kubectl -n postgres wait --for=condition=Ready cluster/cledyu-pg --timeout=1200s
kubectl -n keycloak wait --for=condition=Ready cluster/keycloak-pg --timeout=1200s

echo "✅ cledyu-pg·keycloak-pg Ready (S3 복원 완료)"

# ── Keycloak 재기동 — **이 스크립트가 발밑에서 DB 를 갈아치웠기 때문이다** ────────────────────
# 🔴 **2026-07-16 T3 드릴 실측 — 이게 없으면 [9] 가 600s 타임아웃하고 페일오버가 거기서 멈춘다.**
#
# 위 delete/재생성으로 keycloak-pg 는 **새 파드·새 IP** 다. Keycloak 의 JDBC 커넥션 풀은 구 파드를
# 붙든 채이고 **스스로 복구하지 않는다** → 헬스가 DOWN 으로 굳는다:
#   {"name":"Keycloak Initialized","status":"UP"},                     ← 떠 있고 초기화도 됐는데
#   {"name":"Keycloak cluster health check","status":"DOWN",
#    "data":{"Failing since":"2026-07-15 20:44:34"}}                   ← 이 시각 = 위 wait 이 끝난 시각
# → readiness 503 → CR 이 `Ready=False :: Waiting for more replicas` 로 고착
# → [9] 의 `wait --for=condition=Ready keycloak/cledyu-keycloak` 이 타임아웃.
#
# **11-restart-apps.sh 가 api·web 에 대해 하는 것과 같은 병이다**(startup 1회 초기화 → 복구 없음).
# 그런데 **[11] 에 넣을 수 없다** — [9] 가 Keycloak Ready 를 게이트하므로 [11] 에 도달하기 전에 막힌다.
# 그리고 그 게이트는 옳다(조기 DNS 전환 시 ALB keycloak 타겟 unhealthy → 404/503, 런북 :406).
# → **원인을 만든 여기서 치운다.** [8] 은 이미 keycloak-pg Ready 를 알고 있으니 자리도 여기가 맞다.
#
# ⚠️ `rollout restart` 를 쓰지 않는다 — STS 를 keycloak-operator 가 소유해서(ownerReferences 확인)
#    파드 템플릿 annotation 을 되돌리면 **플립플롭**이 날 수 있다. `delete pod` 는 spec 을 안 건드려
#    오퍼레이터와 다투지 않는다(실측: 삭제 → 49초 만에 Ready=True).
# ⚠️ 이름(`cledyu-keycloak-0`)을 박지 않고 **라벨로** 지운다 — replicas 가 늘면 조용히 일부만 재기동된다.
#    셀렉터는 실측값이다(app=keycloak, app.kubernetes.io/instance=cledyu-keycloak).
# ⚠️ 대기는 **STS rollout** 으로 한다 — 삭제 직후 Keycloak CR 은 아직 Ready=True(구 상태)라
#    `wait --for=condition=Ready keycloak/...` 을 여기서 바로 쓰면 **stale 통과**한다.
kubectl -n keycloak delete pod -l app=keycloak,app.kubernetes.io/instance=cledyu-keycloak
kubectl -n keycloak rollout status statefulset/cledyu-keycloak --timeout=600s
echo "✅ Keycloak 재기동 완료 — 새 keycloak-pg 로 재연결됨"

# ⚠️ 런북 344 부터(§real-DR: DR-창 쓰기 캡처 — backupEnabled: false → true flip)는 **이식하지 않는다.**
# 설계 §8.1 이 "수동 PR"로 결정한 것이다 — 자동화하면 재해 중에 main 으로 push 할 GitHub 자격이
# 필요해지고, 온프렘 앱들도 같은 repoURL·main 을 sync 하므로 폭발 반경이 **살아 있는 운영 클러스터**까지
# 닿는다. [13] NotifyComplete 가 "지금 이 PR 을 올리세요"로 대신한다.
