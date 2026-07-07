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

### 1. targetTime 결정 + (저트래픽 시) 마커 준비

**중요 — PITR이 성공하려면 `targetTime` 뒤에 커밋된 트랜잭션이 WAL에 최소 하나 있어야 한다.**
PostgreSQL은 "target을 넘어서는 커밋"을 만나야 그 지점에서 멈추고 복원을 "도달"로 인정한다.
target 이 마지막 쓰기보다 미래면(예: 새벽 idle 구간) 복원이
`recovery ended before configured recovery target was reached` FATAL 로 끝난다("문제 해결" 참고).

트래픽이 있는 시간대라면 최근 쓰기가 계속 WAL 을 채우므로 `targetTime = 5분 전`으로 바로 잡으면 된다:

```bash
# GNU date(리눅스). BSD date(macOS)는 `date -u -v-5M ...`.
# 형식 주의: 끝에 'Z'를 쓰지 말 것 — PostgreSQL recovery_target_time 이 'Z'를 거부한다.
# 반드시 '+00:00' 오프셋 형식으로.
TARGET_TIME=$(date -u -d '5 minutes ago' '+%Y-%m-%d %H:%M:%S+00:00')
echo "$TARGET_TIME"
```

**저트래픽/idle DB(예: 새벽)라면** 드릴 전에 target 앞뒤로 마커를 심어 PITR 정밀도까지 검증한다.
아래 3줄로 marker1 → (target은 이 둘 사이) → marker2 를 만든 뒤, `pg_switch_wal()` 로 즉시 S3 아카이빙한다:

```bash
# marker1: target 이전 시점 (복원 후 이 행은 살아있어야 함)
kubectl -n postgres exec cledyu-pg-1 -- psql -d cledyu -tAc \
  "create table if not exists _dr_drill(t timestamptz); insert into _dr_drill values(now()) returning t;"
# marker1 커밋 시각을 확인해 그보다 뒤(예: +20초)를 TARGET_TIME 으로 잡는다. 예:
# TARGET_TIME="2026-07-07 17:25:00+00:00"

# marker2: target 이후 시점 (복원 후 이 행은 없어야 함 = PITR 이 정확히 잘랐다는 증거)
kubectl -n postgres exec cledyu-pg-1 -- psql -d cledyu -tAc \
  "insert into _dr_drill values(now()) returning t;"
# 두 마커가 담긴 WAL 을 즉시 S3 로 강제 아카이빙
kubectl -n postgres exec cledyu-pg-1 -- psql -d cledyu -tAc "select pg_switch_wal()"
# 새 WAL 이 S3 에 올라왔는지 확인(목록 끝 번호가 늘어야 함)
aws s3 ls s3://cledyu-lab-dr-backups/postgres/cledyu-pg/wals/0000000100000000/ | tail -3
```

> `psql` 은 소켓 peer auth 라 컨테이너 OS 유저(`postgres`)로 붙는다 — `-U cledyu` 를 주면
> `Peer authentication failed` 로 막히므로 `-U` 를 생략(=postgres superuser)한다. 아래 검증 쿼리도 동일.

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
  # 원본과 동일 Postgres 이미지로 고정 — 복원은 같은 major/minor 바이너리에서만 안전하다.
  # (operator 기본 이미지가 원본과 다르면 복원이 실패할 수 있어 명시적으로 핀 고정)
  imageName: "ghcr.io/cloudnative-pg/postgresql:16.4@sha256:99be063781d171d3971089b49c992706bdab9ccbd2b57cdf126c7542773aedfe"
  storage: { size: 10Gi, storageClass: longhorn }
  bootstrap:
    recovery:
      source: cledyu-pg
      recoveryTarget:
        # 형식 주의: 'Z' 금지, '+00:00' 오프셋. (예: "2026-07-07 17:25:00+00:00")
        targetTime: "{{ TARGET_TIME (+00:00 형식, Z 금지) }}"
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
sed "s|{{ TARGET_TIME (+00:00 형식, Z 금지) }}|$TARGET_TIME|" /tmp/pitr-drill.yaml.template > /tmp/pitr-drill.yaml
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

### 4. 데이터 검증

**(A) 마커 방식으로 드릴한 경우(저트래픽) — PITR 정밀도 검증:**

```bash
# -U 생략 = postgres superuser(소켓 peer auth). marker1만 있고 marker2는 없어야 정상.
kubectl -n postgres exec cledyu-pg-drill-1 -- psql -d cledyu -tAc \
  "select count(*) as rows, max(t) as latest from _dr_drill"
```

예상: `rows=1`(marker1만), `latest=marker1 커밋 시각`. marker2(target 이후)는 잘려 나가야 한다 —
이것이 "target 시점으로 정확히 복원됐다"는 증거다. 정리 시 원본의 `_dr_drill` 테이블도 함께 삭제한다
(5단계).

**(B) 트래픽 있는 시간대에 `session_progress` 로 RPO 실측한 경우:**

```bash
kubectl -n postgres exec cledyu-pg-drill-1 -- psql -d cledyu -tAc \
  "select max(updated_at) from session_progress"
```

`targetTime` 과 실측 `max(updated_at)` 의 차이가 곧 이번 드릴에서 실측한 RPO다(목표 5~15분 이내).

**실측값 기록:** `<쿼리 출력값>`
**실측 RPO:** `<targetTime - 위 값, 분 단위>` (목표 5~15분 이내인지 판정: `<판정 결과>`)

### 5. 드릴 정리

```bash
kubectl -n postgres delete cluster cledyu-pg-drill
# 마커 방식으로 드릴했다면 원본의 스캐폴드 테이블도 제거
kubectl -n postgres exec cledyu-pg-1 -- psql -d cledyu -tAc "drop table if exists _dr_drill"
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
- **복원 Job 이 `invalid value for parameter "recovery_target_time"` FATAL 로 죽는다**:
  `targetTime` 을 `...Z`(예: `2026-07-07T17:25:00Z`) 로 줬을 때 발생한다 — PostgreSQL 의
  `recovery_target_time` 파서는 'Z'(Zulu) 표기를 받지 않는다. `+00:00` 오프셋 형식
  (예: `"2026-07-07 17:25:00+00:00"`) 으로 바꾼다. (CNPG 1.25 실측)
- **복원 Job 이 `recovery ended before configured recovery target was reached` FATAL 로 죽는다**:
  `targetTime` 이 WAL 에 기록된 **마지막 커밋보다 미래**일 때 발생한다. PostgreSQL 은 target 을
  넘어서는 커밋을 만나야 복원을 "도달"로 인정하는데, target 뒤에 트랜잭션이 없으면 WAL 을 다 재생하고도
  target 에 못 닿아 실패한다. 저트래픽/idle 구간에서 흔하다. 해결: 1단계처럼 target **이후** 시점에
  마커(marker2)를 하나 심고 `pg_switch_wal()` 로 아카이빙한 뒤 재시도하거나, `recoveryTarget` 를
  아예 빼서 **최신(WAL 끝)까지 복원**한다(이 경우 시점 지정은 못 하지만 복원 가능성은 검증됨).
- **`Peer authentication failed for user "cledyu"`**: `psql -U cledyu` 로 소켓 접속 시 발생 — 컨테이너
  OS 유저는 `postgres` 라 peer auth 가 어긋난다. `-U` 를 생략(=postgres superuser)하면 통과한다.
  아카이빙 지연 확인:
  `kubectl -n postgres exec cledyu-pg-1 -- psql -tAxc "select last_archived_time, last_failed_time, now()-last_archived_time as age from pg_stat_archiver"`
  (`last_failed_time` 이 최근이면 아카이빙 자체가 깨진 것, `age` 만 크면 단순 idle).
- **`cledyu-pg-drill` 이 Ready 로 전이되지 않고 대기**: `recoveryTarget.targetTime` 시점까지
  WAL이 S3에 아카이빙되어 있지 않으면 복원이 그 지점에서 멈춘다. 위 `pg_stat_archiver` 로 아카이빙
  지연을 확인하거나, `targetTime` 을 아카이빙된 범위 안(더 과거)으로 조정해 재시도한다.
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
| 2026-07-08 02:48 KST | 2026-07-07 17:25:00+00 | ~60초 | N/A | N/A (idle) | 최초 드릴. 마커 방식(marker1 17:24:39 복원·marker2 17:47:47 정확히 제외)으로 **복원 가능성+PITR 정밀도** 검증. 새벽 idle 구간이라 honest RPO 는 미측정(트래픽 시간대 정기 드릴에서 측정 예정). 실행 중 `Z` 형식 거부 + idle target FATAL 두 버그 발견해 매니페스트·절차 수정. |

> 이 문서는 실측치가 쌓이는 살아있는 기록이다. RTO 는 실측이 유의미하나, 위 최초 드릴의 RPO 는
> 트래픽 없는 새벽에 마커로 돌린 파이프라인 검증이라 N/A 로 둔다 — honest RPO 는 트래픽 있는
> 시간대의 정기 드릴에서 `session_progress` 로 측정한다(4단계 B).

## 참고

- 백업 소스: `gitops/apps/postgres-cnpg/templates/cluster.yaml` (Task 4, barman S3 설정)
- 자격증명: `gitops/apps/backup-secrets` (Task 2, `cledyu-backup-s3` Secret)
- 계획 원문: `docs/superpowers/plans/2026-07-01-dr-backup-plan-a-backup-layer.md` Task 7
- CNPG 복구 문서: https://cloudnative-pg.io/documentation/current/recovery/
