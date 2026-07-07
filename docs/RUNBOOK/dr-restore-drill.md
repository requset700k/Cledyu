# CNPG PITR 복원 드릴 (Postgres RPO/RTO 실측)

## 목적

`cledyu-pg`(CNPG, `gitops/apps/postgres-cnpg`) 의 S3 백업이 **실제로 복원 가능한지** 증명하고,
그 과정에서 RPO(데이터 손실 허용 범위)와 RTO(복원 소요 시간)를 실측한다. 백업이 쌓이고 있다는
사실만으로는 복원 가능성이 보장되지 않는다 — barman 설정 오류, WAL 아카이빙 누락, 자격증명 만료
등은 실제 `recovery` 부트스트랩을 시도해봐야 드러난다. 이 드릴은 원본 `cledyu-pg` 를 건드리지
않고 별도 임시 클러스터(`cledyu-pg-drill`)로 복원해 검증한 뒤 폐기한다.

> 이 런북은 `docs/superpowers/plans/2026-07-01-dr-backup-plan-a-backup-layer.md` Task 4
> (cledyu Postgres → CNPG 이관)가 **배포되어 `cledyu-pg` 클러스터가 라이브 상태로 S3에 실제
> 백업이 쌓이고 있어야** 실행 가능하다. Task 4 미배포 상태에서는 아래 절차를 실행할 대상
> (`externalClusters.cledyu-pg` 의 S3 백업)이 존재하지 않는다.

## 트리거

- Task 4 배포 직후 최초 1회 — 백업 파이프라인이 실제로 복원 가능함을 검증(필수).
- 정기 DR 드릴(분기 1회 권장) — 백업 설정 변경(오퍼레이터 업그레이드, barman → barman-cloud
  플러그인 이관 등) 이후 회귀 확인.
- RPO 목표(5~15분) 미달 의심 시 — WAL 아카이빙 지연 여부 실측.

## 사전 조건

- `kubectl` 이 Cledyu 클러스터의 `postgres` 네임스페이스에 접근 가능해야 한다.
- `cledyu-pg` CNPG 클러스터가 Ready 상태이고, S3에 최소 1개 이상의 base backup + WAL이 쌓여
  있어야 한다:

```bash
kubectl -n postgres get cluster cledyu-pg
aws s3 ls s3://cledyu-lab-dr-backups/postgres/ --recursive | tail -20
```

예상 출력: `cledyu-pg` 가 `Cluster in healthy state`, S3 목록에 `base/` 및 WAL 객체 존재.

- 드릴 클러스터도 `postgres` 네임스페이스에 뜨므로 여유 스토리지(`longhorn`, 10Gi)가 있어야 한다.
- 실측 RPO가 의미를 가지려면 드릴 직전 시점까지 `session_progress` 등 실 데이터에 쓰기가
  발생하고 있어야 한다(트래픽이 있는 시간대에 실행 권장). 완전히 정적인 DB로 드릴을 돌리면
  RPO=0이 나오는 게 당연하므로 실측 의미가 없다.

## 절차

### 1. targetTime 결정 (드릴 실행 시점 기준 5분 전 UTC)

```bash
TARGET_TIME=$(date -u -d '5 minutes ago' +%Y-%m-%dT%H:%M:%SZ)
echo "$TARGET_TIME"
```

> `date -u -d` 는 GNU date(리눅스) 문법이다. macOS 등 BSD date 환경에서는
> `date -u -v-5M +%Y-%m-%dT%H:%M:%SZ` 를 쓴다.

**실행 시각 기록:** `<실행 시각(로컬), TARGET_TIME 값을 여기 기입>`

### 2. 드릴용 Cluster 매니페스트 작성

`/tmp/pitr-drill.yaml` (아래 `targetTime` 값을 1단계의 `$TARGET_TIME` 으로 치환한다 — `sed` 또는
직접 편집):

```yaml
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: cledyu-pg-drill
  namespace: postgres
spec:
  instances: 1
  storage: { size: 10Gi, storageClass: longhorn }
  bootstrap:
    recovery:
      source: cledyu-pg
      recoveryTarget:
        targetTime: "{{ 5분 전 UTC 타임스탬프 }}"
  externalClusters:
    - name: cledyu-pg
      barmanObjectStore:
        destinationPath: "s3://cledyu-lab-dr-backups/postgres"
        endpointURL: "https://s3.ap-northeast-2.amazonaws.com"
        s3Credentials:
          accessKeyId: { name: cledyu-backup-s3, key: ACCESS_KEY_ID }
          secretAccessKey: { name: cledyu-backup-s3, key: ACCESS_SECRET_KEY }
```

치환 예:

```bash
sed "s|{{ 5분 전 UTC 타임스탬프 }}|$TARGET_TIME|" /tmp/pitr-drill.yaml.template > /tmp/pitr-drill.yaml
```

(위 YAML 블록을 그대로 `/tmp/pitr-drill.yaml.template` 로 저장해두면 재사용하기 쉽다.)

`cledyu-backup-s3` Secret 은 Task 2(`gitops/apps/backup-secrets`)에서 이미 `postgres` 네임스페이스에
동기화되어 있으므로 별도 생성이 필요 없다 — 드릴 클러스터는 원본 `cledyu-pg` 와 동일한 자격증명을
재사용해 S3에서 읽기만 한다(원본 클러스터에는 쓰기 없음).

### 3. 복원 실행 + Ready 대기

**RTO 측정 시작 시각 기록:** `<kubectl apply 실행 시각>`

```bash
kubectl apply -f /tmp/pitr-drill.yaml
kubectl -n postgres wait --for=condition=Ready cluster/cledyu-pg-drill --timeout=600s
```

예상 출력: `cluster.postgresql.cnpg.io/cledyu-pg-drill condition met` (600초 내). 시간이 오래
걸리거나 타임아웃되면 "문제 해결" 절을 참고한다.

**RTO 측정 종료 시각 기록:** `<Ready 확인 시각>` → **실측 RTO:** `<종료 - 시작, 분 단위>`

### 4. 데이터 검증 (RPO 실측)

```bash
kubectl -n postgres exec cledyu-pg-drill-1 -- psql -U cledyu -d cledyu -tAc \
  "select max(updated_at) from session_progress"
```

예상: 복원된 DB의 최신 `updated_at` 이 `targetTime` 근방(목표 RPO 5~15분 이내)이어야 한다.
`targetTime` 과 실측 `max(updated_at)` 의 차이가 곧 이번 드릴에서 실측한 RPO다.

**실측값 기록:** `<위 쿼리 출력값(최신 updated_at)>`
**실측 RPO:** `<targetTime - 위 값, 분 단위>` (목표 5~15분 이내인지 판정: `<판정 결과>`)

### 5. 드릴 정리

```bash
kubectl -n postgres delete cluster cledyu-pg-drill
```

예상 출력: `cluster.postgresql.cnpg.io "cledyu-pg-drill" deleted`. 연결된 PVC도 함께 정리되는지
확인한다(`kubectl -n postgres get pvc | grep cledyu-pg-drill` 가 빈 결과여야 한다 — 일부 CNPG
버전은 클러스터 삭제 후 PVC가 잠시 `Terminating` 으로 남을 수 있으니 재확인).

## 검증

- 4단계에서 실측 RPO가 목표(5~15분) 이내였다.
- 3단계 Ready 대기가 타임아웃 없이 완료됐다(실측 RTO를 기록해 추세를 추적한다).
- 5단계 정리 후 `cledyu-pg-drill` Cluster/Pod/PVC 가 남아있지 않다.
- 원본 `cledyu-pg` 클러스터 상태(Ready, 서비스 연결)가 드릴 전후로 변화 없다(드릴은 원본을
  전혀 건드리지 않으므로 당연히 그래야 한다 — 변화가 있었다면 별도 조사 필요).

## 롤백 / 문제 해결

- **롤백 대상 없음**: 이 드릴은 원본 `cledyu-pg` 클러스터를 전혀 건드리지 않는다(4단계까지 읽기
  전용, 5단계에서 정리하는 것도 별도 임시 클러스터 `cledyu-pg-drill` 뿐이다). 따라서 원본
  클러스터에 대해 롤백할 변경 사항 자체가 없다. 드릴이 실패하거나 중단된 경우의 조치는 아래
  "정리"만 하면 된다 — `kubectl -n postgres delete cluster cledyu-pg-drill` 로 드릴 클러스터를
  제거하면 드릴 시작 전 상태로 완전히 복귀한다.
- **`cledyu-pg-drill` 이 Ready 로 전이되지 않고 대기**: `recoveryTarget.targetTime` 시점까지
  WAL이 S3에 아카이빙되어 있지 않으면 복원이 그 지점에서 멈춘다. 원본 클러스터의 WAL 아카이빙
  지연(`kubectl -n postgres exec cledyu-pg-1 -- psql -U cledyu -tAc "select * from pg_stat_archiver"`)을
  확인하거나, `targetTime` 을 더 과거 시점(예: 15~30분 전)으로 늦춰 재시도한다.
- **드릴 클러스터는 반드시 폐기한다**: 이름이 `cledyu-pg-drill` 로 고정돼 있어 정리하지 않고
  재실행하면 `AlreadyExists` 로 실패한다. 또한 방치하면 스토리지를 계속 점유한다.
- **원본 `cledyu-pg` 는 이 드릴 동안 영향을 받지 않는다**: 드릴 클러스터는 `externalClusters`
  로 S3 백업만 읽으며 원본에 스트리밍 복제 연결을 맺지 않는다(`bootstrap.recovery.source` 는
  barman 백업 임포트를 가리키는 CNPG 용어이지 라이브 연결이 아니다).
- **오퍼레이터가 barman-cloud 플러그인으로 이관된 이후**에는 `externalClusters[].barmanObjectStore`
  in-tree 필드가 deprecated 될 수 있다(`gitops/apps/postgres-cnpg/templates/cluster.yaml` 상단
  주석 참고) — 그 경우 이 런북의 매니페스트도 플러그인 방식으로 갱신해야 한다.

## 결과 기록 (실행할 때마다 아래에 이어서 기입)

| 실행일시 | targetTime(UTC) | 실측 RTO | 실측 RPO | 목표(5~15분) 충족 | 비고 |
|---|---|---|---|---|---|
| `<예: 2026-08-01 14:30 KST>` | `<TARGET_TIME>` | `<분>` | `<분>` | `<Y/N>` | `<특이사항>` |

> 최초 실행 결과는 위 표의 첫 행에 채워 넣는다. 이 문서는 실측치가 쌓이는 살아있는 기록이며,
> 아직 한 번도 실행된 적이 없으므로 표는 비어 있다(가상의 수치를 채우지 않는다).

## 참고

- 백업 소스: `gitops/apps/postgres-cnpg/templates/cluster.yaml` (Task 4, barman S3 설정)
- 자격증명: `gitops/apps/backup-secrets` (Task 2, `cledyu-backup-s3` Secret)
- 계획 원문: `docs/superpowers/plans/2026-07-01-dr-backup-plan-a-backup-layer.md` Task 7
- CNPG 복구 문서: https://cloudnative-pg.io/documentation/current/recovery/
