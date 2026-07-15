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

# 🔴 **미확정 — delete 가 PVC 도 지우나. 판정처는 T7 Step 4 의 표식(dr_drill_marker)이다.**
# ⚠️ 원래 "T3 Step 10 에서 확인한다"였는데 **그 Step 이 실측 없이 지나갔다**(2026-07-15). 주석이 이미
#    지나간 관문을 가리키는 바람에 이 건이 미아가 됐다 → **살아있는 관문(T7)으로 다시 건다.**
# 남아서 재생성된 Cluster 가 그걸 재사용하면 **S3 복원이 아니라 stale 데이터로 뜬다**
# (PGDATA 가 이미 있으면 bootstrap.recovery 가 아예 안 돈다).
# 그리고 `wait --for=condition=Ready` 는 **통과하고**, [12] 의 count(*) > 0 도 **통과한다**(행은 있으니까)
# → 이 계획에서 가장 조용한 실패 경로다. **아래 get pvc 는 눈으로 보는 것일 뿐 게이트가 아니다.**
# T7 표식이 0/ERROR 로 나오면 여기에 PVC 삭제를 명시 추가하고 이 주석을 실측 결과로 대체한다.
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

# ⚠️ 런북 344 부터(§real-DR: DR-창 쓰기 캡처 — backupEnabled: false → true flip)는 **이식하지 않는다.**
# 설계 §8.1 이 "수동 PR"로 결정한 것이다 — 자동화하면 재해 중에 main 으로 push 할 GitHub 자격이
# 필요해지고, 온프렘 앱들도 같은 repoURL·main 을 sync 하므로 폭발 반경이 **살아 있는 운영 클러스터**까지
# 닿는다. [13] NotifyComplete 가 "지금 이 PR 을 올리세요"로 대신한다.
