# DR Failback 격리 스코프 드릴 (프로덕션 무영향, 100% 커버)

> 목적: **failback 복원 부품**(정적키 CNPG recovery · 데이터 무손실 복원 · `-dr/` read 경로)이 실제로 동작하는지
> **진짜 운영 DB를 건드리지 않고** 임시 클러스터로 실증한다. failover 상태 불필요 · DNS 무변경 · 다운타임 없음.
>
> 격리 드릴 **밖**(실 failover→failback 필요): write-downtime 창 · drEpoch 2사이클 · DNS전환/split-brain · adopt · anchor 도달
> → 예정된 DR 창에서 팀 조율로 실측(스펙 R1~R6, PR 명시).

## 검증 매트릭스 (이 4개로 복원 부품 100%)

| # | 테스트 | 소스 | 증명 |
|---|---|---|---|
| A | postgres 복원 | `postgres/cledyu-pg` (운영) | 정적키 recovery + **라이브 count 비교 = 무손실** |
| B | keycloak 복원 | `keycloak/keycloak-pg` (운영) | 정적키 recovery + **라이브 count 비교 = 무손실** |
| C | keycloak 복원 | `keycloak-dr/keycloak-pg-dr` (`-dr`) | **`-dr` read 공유 메커니즘**(KMS 복호화·barman 읽기·정적키) 실동작 |
| D | postgres `-dr` 직접 read | `postgres-dr/…/wals/*` 객체 | **postgres 키의 `-dr` GetObject+KMS 복호화**(A·B·C가 안 건드리는 postgres 고유 IAM 경로) |

- **A·B** = 무손실(둘 다 라이브 비교). **C** = -dr 공유 메커니즘. **D** = postgres 고유 -dr read.
- 왜 이 소스 배치: postgres `-dr`(cledyu-pg-dr)엔 base backup이 없어(과거 불완전 드릴, WAL만) 복원 소스로 못 씀
  → postgres 복원은 운영 아카이브로(대신 라이브 비교 가능), postgres의 `-dr` read 자체는 D(직접 read)로 실증.

## 전제조건

- [ ] **Task 1 IAM 적용됨** — 온프렘 postgres·keycloak writer 키에 `-dr/` GetObject read. (2026-07-14 apply·`aws iam get-user-policy` 확인 완료.)
- [ ] **온프렘 클러스터 도달 가능** — 아래 전부 `--context onprem` 으로, 온프렘 접근 머신에서 실행.
- [ ] **S3 소스 base 존재 확인**:
  ```bash
  aws s3 ls s3://cledyu-lab-dr-backups/postgres/cledyu-pg/base/         | tail -1   # A 소스
  aws s3 ls s3://cledyu-lab-dr-backups/keycloak/keycloak-pg/base/       | tail -1   # B 소스
  aws s3 ls s3://cledyu-lab-dr-backups/keycloak-dr/keycloak-pg-dr/base/ | tail -1   # C 소스
  aws s3 ls s3://cledyu-lab-dr-backups/postgres-dr/cledyu-pg-dr/wals/   | tail -1   # D 대상(객체 존재)
  ```
- 임시 이름(진짜와 분리): `cledyu-pg-fbdrill`·`keycloak-pg-fbdrill-op`·`keycloak-pg-fbdrill-dr`. 소비자 svc 무영향.

---

## 절차

### 1. 드릴 매니페스트 3개 (온프렘 머신에 저장)

**A. `fbdrill-pg-op.yaml`** (postgres, 운영 소스):
```yaml
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata: { name: cledyu-pg-fbdrill, namespace: postgres }
spec:
  instances: 1
  imageName: "ghcr.io/cloudnative-pg/postgresql:16.4@sha256:99be063781d171d3971089b49c992706bdab9ccbd2b57cdf126c7542773aedfe"
  storage: { size: "10Gi", storageClass: longhorn }
  bootstrap: { recovery: { source: src } }
  externalClusters:
    - name: src
      barmanObjectStore:
        destinationPath: "s3://cledyu-lab-dr-backups/postgres"
        serverName: "cledyu-pg"
        endpointURL: "https://s3.ap-northeast-2.amazonaws.com"
        s3Credentials:
          accessKeyId: { name: cledyu-backup-s3, key: ACCESS_KEY_ID }
          secretAccessKey: { name: cledyu-backup-s3, key: ACCESS_SECRET_KEY }
        wal: { compression: gzip }
  monitoring: { enablePodMonitor: false }
```

**B. `fbdrill-kc-op.yaml`** (keycloak, 운영 소스): A와 동일하되 —
`name: keycloak-pg-fbdrill-op`, `namespace: keycloak`,
`imageName: "ghcr.io/cloudnative-pg/postgresql:18.2-system-trixie@sha256:3f44daf4c2ddea3481b018b3b004f91a439b93fc995a387f9aff69058bef19ac"`,
`storage.size: "20Gi"`, `destinationPath: "s3://cledyu-lab-dr-backups/keycloak"`, `serverName: "keycloak-pg"`.

**C. `fbdrill-kc-dr.yaml`** (keycloak, `-dr` 소스): B와 동일하되 —
`name: keycloak-pg-fbdrill-dr`, `destinationPath: "s3://cledyu-lab-dr-backups/keycloak-dr"`, `serverName: "keycloak-pg-dr"`.

### 2. apply → 복원 대기
```bash
kubectl --context onprem apply -f fbdrill-pg-op.yaml -f fbdrill-kc-op.yaml -f fbdrill-kc-dr.yaml
kubectl --context onprem -n postgres wait --for=condition=Ready cluster/cledyu-pg-fbdrill      --timeout=900s
kubectl --context onprem -n keycloak wait --for=condition=Ready cluster/keycloak-pg-fbdrill-op --timeout=900s
kubectl --context onprem -n keycloak wait --for=condition=Ready cluster/keycloak-pg-fbdrill-dr --timeout=900s
# 안 뜨면: kubectl --context onprem -n <ns> describe cluster <name> | tail -30
#          kubectl --context onprem -n <ns> logs <name>-1 | grep -iE 'error|denied|barman|recovery' | tail -20
```

### 3-AB. 무손실 실증 (drill vs live count 일치)
```bash
# A: postgres
echo "[pg drill]"; kubectl --context onprem -n postgres exec cledyu-pg-fbdrill-1 -- psql -d cledyu -tAc \
  "SELECT count(*) FROM lab_completions; SELECT count(*) FROM session_progress;"
echo "[pg live ]"; kubectl --context onprem -n postgres exec cledyu-pg-1 -- psql -d cledyu -tAc \
  "SELECT count(*) FROM lab_completions; SELECT count(*) FROM session_progress;"
# B: keycloak
echo "[kc drill]"; kubectl --context onprem -n keycloak exec keycloak-pg-fbdrill-op-1 -- psql -d keycloak -tAc "SELECT count(*) FROM user_entity;"
echo "[kc live ]"; kubectl --context onprem -n keycloak exec keycloak-pg-1 -- psql -d keycloak -tAc "SELECT count(*) FROM user_entity;"
```
> 판정: drill ≈ live (마지막 WAL 아카이브 지연분만 차이 허용) → **무손실 복원 실증**.

### 3-C. `-dr` read 경로 실증 (keycloak-dr 에서 복원됨 = KMS·barman·정적키 동작)
```bash
kubectl --context onprem -n keycloak exec keycloak-pg-fbdrill-dr-1 -- psql -d keycloak -tAc "SELECT count(*) FROM user_entity;"
```
> 판정: Ready + count>0 → `-dr` 공유 메커니즘(암복호화·읽기) 실동작. (데이터는 7/12 -dr 백업분이라 라이브 비교 아님.)

### 3-D. postgres 고유 `-dr` read 직접 실증 (복원 없이 GetObject+KMS)
```bash
AK=$(kubectl --context onprem -n postgres get secret cledyu-backup-s3 -o jsonpath='{.data.ACCESS_KEY_ID}' | base64 -d)
SK=$(kubectl --context onprem -n postgres get secret cledyu-backup-s3 -o jsonpath='{.data.ACCESS_SECRET_KEY}' | base64 -d)
OBJ=$(AWS_ACCESS_KEY_ID=$AK AWS_SECRET_ACCESS_KEY=$SK \
      aws s3 ls --recursive s3://cledyu-lab-dr-backups/postgres-dr/cledyu-pg-dr/wals/ | head -1 | awk '{print $NF}')
AWS_ACCESS_KEY_ID=$AK AWS_SECRET_ACCESS_KEY=$SK \
      aws s3 cp "s3://cledyu-lab-dr-backups/$OBJ" /tmp/fbdrill.wal \
  && echo "✅ postgres 키 → postgres-dr/ GetObject+KMS 복호화 성공" && rm -f /tmp/fbdrill.wal \
  || echo "❌ postgres -dr read 실패 → IAM(-dr GetObject) 또는 KMS Decrypt 확인"
```
> 판정: `✅` 출력 → postgres 키가 `postgres-dr/` 를 실제로 read+복호화 = postgres 고유 IAM 경로 실증.

### 4. 폐기 (반드시 — 드릴 클러스터는 운영 데이터 사본/PII 포함)
```bash
kubectl --context onprem -n postgres delete cluster cledyu-pg-fbdrill --ignore-not-found
kubectl --context onprem -n keycloak delete cluster keycloak-pg-fbdrill-op keycloak-pg-fbdrill-dr --ignore-not-found
kubectl --context onprem -n postgres get pvc | grep fbdrill || echo "postgres pvc 정리됨"
kubectl --context onprem -n keycloak get pvc | grep fbdrill || echo "keycloak pvc 정리됨"
```

---

## 통과 기준 (전부 만족해야 격리 드릴 PASS)

- [ ] A·B·C 세 클러스터 모두 `Ready`.
- [ ] **A·B**: drill count ≈ live count (무손실).
- [ ] **C**: keycloak-dr 복원 count>0 (`-dr` 공유 메커니즘 동작).
- [ ] **D**: `✅` (postgres 키의 `-dr` GetObject+KMS 실동작).
- [ ] step4 폐기 완료(잔여 PVC 없음).

→ 이 5개면 **복원 부품(정적키 recovery·무손실·양쪽 `-dr` read)은 실환경 실증 100%**. 남은 5개(write-downtime·drEpoch 2사이클·DNS/split-brain·adopt·anchor)는 실 failover→failback 드릴 몫.

## 주의
- 실패 신호: `AccessDenied`→IAM 미반영 / `Expected empty archive`→backup 블록 오포함(드릴엔 없어야) / `no backup found`→serverName·prefix 불일치.
- 드릴 클러스터는 backup 블록 없음(read 전용) → S3 오염·Object Lock 충돌 없음.
- 진짜 `cledyu-pg`·`keycloak-pg` 와 이름·PVC 완전 분리 → 운영 무영향. 같은 노드 자원 잠깐 사용하니 한가한 시간대 권장.
- 반복 실행 안전: 재실행 전 step4 로 이전 드릴 클러스터 삭제.
