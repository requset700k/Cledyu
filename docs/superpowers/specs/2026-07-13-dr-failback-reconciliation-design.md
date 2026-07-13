# DR Failback · 아카이브 계보 reconciliation 설계

- 날짜: 2026-07-13
- 기준 baseline: `origin/main` (로컬 `main`·`docs/dr-failback-reconciliation` 브랜치는 stale — 아래 §0)
- 관련: [[project_dr_pilot_light]] · [[project_dr_plan_b_eks_overlay]] · [[project_cnpg_dr_recovery_paths]] · [[project_cnpg_bootstrap_failsafe]] · [[project_dr_scope_delegation]] · [[project_postgres_cnpg_cutover]]
- 선행 문서: `docs/superpowers/specs/2026-07-11-full-service-dr-redesign-design.md`(pilot-light) · `docs/superpowers/plans/2026-07-03-dr-backup-plan-c-orchestration.md`(Task 6 failback 스켈레톤) · `docs/RUNBOOK/dr-eks-bootstrap.md`(failover 런북)

---

## 0. 먼저 바로잡을 사실 (scope 정정)

- `docs/dr-failback-reconciliation` 브랜치엔 **failback 계획이 없다.** `origin/main`보다 16커밋 **뒤처진** stale 브랜치이고(0 ahead / 16 behind), `docs/RUNBOOK/dr-failback.md`는 **아직 존재하지 않는다.**
- 지금까지의 "계획"은 두 조각의 스켈레톤뿐이다:
  - Plan C Task 6 — `dr-failback.md` 작성 + "역복제 후 수동 전환" 6줄 순서.
  - full-service-dr-redesign §2.2 — `eks_dr_active=false` + DNS 원복 한 줄.
- 그 사이 아키텍처가 **pilot-light + CNPG**로 진화해, 위 스켈레톤만으로는 failback을 구현할 수 없다. 이 스펙은 **`origin/main` 위에서 새로 작성**하며, 구현 브랜치는 `origin/main`에서 딴다.

---

## 1. 배경 · 문제

### 1.1 failback이란

full-service DR(pilot-light)은 온프렘 마비 시 EKS를 hot 스케일해 **풀서비스를 AWS에서 read-write로 제공**한다(failover). **failback**은 온프렘이 복구된 뒤 서비스 권한을 온프렘으로 되돌리는 역방향 절차다. 원칙(Plan C·backup 설계 §온프렘 복귀):

- **자동 failback 없음.** 온프렘이 살아나도 트래픽을 자동으로 되돌리지 않는다. EKS가 read-write를 계속 쥐고, 온프렘은 수동 승인 전까지 트래픽을 받지 않는다.
- **DNS 단일 권한.** "지금 누가 서비스하나"를 Route 53 한 곳이 결정 → 두 사이트 동시 write(split-brain) 자체가 성립 불가.
- **역복제 필수.** 재해 중 최신 데이터는 EKS에 쌓이고 온프렘은 재해 시점에 얼어붙어 있다. 그대로 온프렘을 열면 옛 데이터로 갈라진다 → 되돌리기 **전에** EKS→온프렘으로 맞춰야 한다.

### 1.2 현행 DR 모델의 데이터 갭 (이 스펙이 닫는 것)

현행 failover 런북(`dr-eks-bootstrap.md`)의 실제 동작을 실측하면 다음이 드러난다:

1. **EKS DR 차트는 자체 아카이빙을 끈 채(`backupEnabled=false`) 운영된다.** (`postgres-cnpg-dr`·`keycloak-pg-dr` values 기본값.) → 재해 중 EKS에 쌓인 새 쓰기(진도·수료·DR-창 가입계정)가 **S3 어디에도 durable하게 남지 않는다.** 현 모델은 사실상 **DR-창 데이터 손실을 수용**한다.
2. **재-failover 가드 `[P1b]`**: 재-failover 시 warm etcd의 stale CNPG CR을 지워 ArgoCD가 재생성 → `bootstrap.recovery`가 **`postgres/`(serverName=`cledyu-pg`, 온프렘 원본 아카이브)** 에서 재실행된다. 즉 매 failover가 **온프렘 원본 아카이브 하나**만 계보로 삼는다.

failback에서 EKS의 DR-창 데이터를 온프렘으로 되돌리려면 (a) failover 시 **EKS 아카이빙을 켜서** DR-창 쓰기를 S3에 남기고, (b) 온프렘이 그걸 읽어 recovery해야 한다. 그런데 (a)를 켜는 순간 **아카이브 계보 충돌**이라는 두 번째 문제가 터진다.

### 1.3 아카이브 계보 충돌 = "reconciliation"의 실체

barman 아카이브가 저장되는 S3 버킷은 **Object Lock GOVERNANCE 30일**이 걸려 있어(`backup.tf`) 만료 전 삭제가 불가능하다. CNPG는 새 primary가 아카이빙을 시작할 때 대상 경로가 비어 있어야 하며, 이미 WAL이 있으면 **`"WAL archive check: Expected empty archive"`로 Setting up primary에서 멈춘다**(2026-07-13 실측, `postgres-cnpg-dr` values 주석). 따라서:

- 온프렘이 원본 아카이브(`postgres/` serverName=`cledyu-pg`)를 그대로 재사용해 failback 후 다시 아카이빙하면 → 30일 내엔 충돌.
- EKS가 `postgres-dr/` serverName=`cledyu-pg-dr`로 반복 재해마다 다시 쓰면 → 30일 내엔 충돌.

**반복 failover/failback이 성립하려면 매 사이트-전환이 "빈 새 아카이브 경로"에 써야 한다.** 이것이 메모의 미해결 항목 **"failback 반복재해 재-recovery 미해결"**([[project_dr_pilot_light]])과 **"인플레이스 복구=별도 매니페스트 TBD"**([[project_cnpg_dr_recovery_paths]])의 근본 원인이며, 브랜치 이름 *reconciliation*이 가리키는 대상이다. 해법은 §3의 **drEpoch 계보 모델**이다.

---

## 2. 검증된 baseline (설계 근거, 2026-07-13 origin/main 실측)

이 스펙의 모든 결정은 아래 실측 위에 선다. (구현 시 재확인 대상.)

| # | 사실 | 근거 파일 | 설계 영향 |
|---|---|---|---|
| B1 | 온프렘 정적 S3 키는 **프리픽스별 완전 격리** — `backup_writers=[postgres,vault,velero,keycloak]`, 각 정책 `PrefixObjects(Put/Get on ${prefix}/*)`·`ListOwnPrefix`·`DenyForeignPrefixListing`. `postgres` 키는 `postgres/`만, **`postgres-dr/`는 못 읽음.** 단 KMS는 `UseBackupKmsKey`로 `Decrypt`+`GenerateDataKey` **이미 보유**(read 복호화 가능). | `infra/terraform/aws/backup.tf` | **S3 권한만 확장**(§5, KMS 무변경) — 없으면 온프렘 recovery가 `postgres-dr/` GetObject/ListBucket에서 AccessDenied |
| B2 | EKS DR 복원 롤은 이미 `postgres/*` read + `postgres-dr/*` read·write + ListBucket + KMS Decrypt/GenerateDataKey 완비. DB별 롤 분리(postgres/keycloak). | `infra/terraform/aws/eks-dr-irsa.tf` | EKS 쪽 IAM **무변경**. failover 아카이빙 즉시 가능 |
| B3 | 공개 DNS = `aws_route53_record.public`(terraform 관리, alias→온프렘 프록시 ALB). failover 런북이 `route53 change-resource-record-sets UPSERT`로 EKS ALB로 돌림. | `infra/terraform/aws/public-ingress.tf` · `dr-eks-bootstrap.md` §공개 DNS 전환 | failback DNS 원복 = `terraform apply`(§7)로 record를 온프렘 프록시로 되돌림. **최후 단계** |
| B4 | 운영·DR CNPG 이미지 digest **동일** — postgres `16.4@sha256:99be06…`, keycloak `18.2-system-trixie@sha256:3f44da…`. | 각 `cluster.yaml` | recovery major 호환(target≥source) 충족. failback 차트도 동일 digest 고정 |
| B5 | keycloak 운영 backup도 secret `cledyu-backup-s3`(keycloak ns의 keycloak-writer 키) 사용. postgres와 **같은 이름·다른 ns·다른 키**. | `keycloak-pg/templates/cluster.yaml`·`scheduledbackup.yaml` | failback 차트도 ns별 `cledyu-backup-s3` 재사용 |
| B6 | on-prem ArgoCD 자식 앱은 root-app **selfHeal 지배** — 런타임 `argocd app set` 토글은 되돌려짐, **git 편집이 유일한 조작 경로**. 운영 앱 `syncPolicy.automated{prune,selfHeal}`. | `gitops/argocd/apps/data-postgres-cnpg.yaml` | failback recovery 오케스트레이션은 git-driven path-swap + 수동 CR 삭제(§4) |
| B7 | 현행 failover는 `backupEnabled=false` + `[P1b]` 가드로 **원본 아카이브 하나**만 계보로 사용 → DR-창 쓰기 미보존. | `dr-eks-bootstrap.md` §CNPG 재-failover 가드 | §3 drEpoch가 이 모델을 확장(드릴 기본값은 불변, real-DR 경로만 flip) |
| B8 | **base-backup anchor 비대칭**: `keycloak-pg-dr`엔 `ScheduledBackup` 있음, **`postgres-cnpg-dr`엔 없음.** CNPG recovery는 base backup을 anchor로 WAL을 재생하므로, `-dr/`에 base backup이 없으면(WAL만) recovery 불가. 운영 `ScheduledBackup`은 `immediate:true`(cledyu-pg-daily). | `postgres-cnpg-dr/`(파일 부재)·`keycloak-pg-dr/templates/scheduledbackup.yaml`·`postgres-cnpg/templates/scheduledbackup.yaml` | §6: `postgres-cnpg-dr`에 `ScheduledBackup(immediate:true, backupEnabled 게이트)` 추가 → DR primary 기동 시 `-dr/`에 base backup 확보 |
| B9 | `postgres-credentials-cnpg`(ESO←Vault `db/postgres`)는 각 CNPG 차트가 자체 정의(`deletionPolicy:Retain`, `cnpg.io/reload:"true"` 라벨). `cledyu-backup-s3`는 **별도 앱 `backup-secrets/`**가 뿌림. `postgres-cnpg-dr`은 resources 블록 없음(BestEffort). | `postgres-cnpg-dr/templates/externalsecret.yaml`·`backup-secrets/`·`postgres-cnpg-dr/templates/cluster.yaml` | failback 차트는 ExternalSecret **미러 필수**(path-swap prune 대비) + 운영 **resources 미러**(온프렘 QoS). `cledyu-backup-s3`는 미러 불요(별도 앱 존속) |

---

## 3. drEpoch 아카이브 계보 모델 (핵심)

### 3.1 정의

**`drEpoch`** = 완료된 failback 횟수를 세는 단조증가 정수. 기본값 **0**(재해 이력 없는 정상 상태 = 현행). 각 사이트-primary는 자기 아카이브 serverName에 epoch를 접미해, **매 사이트-전환이 빈 새 경로**에 쓰게 한다. (barman 내부 backup-generation "G1/G3" 용어와 구분하려 `epoch`/`-e{N}` 명명.)

serverName 규칙 (postgres 예; keycloak은 `cledyu-pg`→`keycloak-pg`, prefix `postgres`→`keycloak` 대칭):

| 역할 | prefix | serverName | 사용처 |
|---|---|---|---|
| 온프렘 운영 아카이브(전진) | `postgres/` | `N==0 → cledyu-pg`, `N≥1 → cledyu-pg-e{N}` | `postgres-cnpg` backup |
| EKS DR recovery **소스** | `postgres/` | 온프렘 운영과 동일 = f(N) | `postgres-cnpg-dr` externalClusters |
| EKS DR 아카이브(진입) | `postgres-dr/` | `cledyu-pg-dr-e{N+1}` | `postgres-cnpg-dr` backup (real-DR만) |
| 온프렘 failback recovery **소스** | `postgres-dr/` | `cledyu-pg-dr-e{N+1}` | `postgres-cnpg-failback` externalClusters |

`N==0`일 때 온프렘 serverName은 접미 없는 `cledyu-pg`로 두어 **현행 프로덕션 아카이브를 지금 건드리지 않는다**(무-마이그레이션, YAGNI). epoch 접미는 첫 failback부터 등장한다.

### 3.2 사이클 흐름 (반복 재해 무손실 증명)

```
drEpoch=0 (정상)          온프렘 primary → 아카이브 postgres/cledyu-pg
   │
   ▼ 재해 #1 (failover, epoch 유지 0)
EKS recover ← postgres/cledyu-pg            (f(0))
EKS primary → 아카이브 postgres-dr/cledyu-pg-dr-e1   (backupEnabled=true, real-DR)
   │
   ▼ failback #1 → drEpoch=1
온프렘 recover ← postgres-dr/cledyu-pg-dr-e1
온프렘 primary → 아카이브 postgres/cledyu-pg-e1        (빈 경로, 충돌 없음)
   │
   ▼ 재해 #2 (failover, epoch 유지 1)
EKS recover ← postgres/cledyu-pg-e1          (f(1))
EKS primary → 아카이브 postgres-dr/cledyu-pg-dr-e2
   │
   ▼ failback #2 → drEpoch=2
온프렘 recover ← postgres-dr/cledyu-pg-dr-e2
온프렘 primary → 아카이브 postgres/cledyu-pg-e2
```

- 매 epoch의 serverName이 고유 → **Object Lock 충돌 원천 소거**, 반복 재해 성립.
- 각 전환이 직전 primary의 아카이브를 소스로 삼음 → **DR-창 데이터 무손실**(재검증 필요한 Kafka in-flight 제외).
- 계보가 git values 히스토리로 **감사 가능**.
- **각 새 epoch 경로의 anchor 보장**: 온프렘 adopt 후 **명시적 on-demand `Backup` CR**로 새 epoch(`postgres/cledyu-pg-e{N}`)에 base backup을 확보 → 다음 failover의 EKS recovery(소스 f(N))가 anchor를 갖는다. (운영 `ScheduledBackup(immediate:true)`의 즉시백업은 CR *생성* 시에만 발화 — adopt 시 path-swap prune→recreate 로 발화하긴 하나 그 부수효과에 **의존하지 않고** 명시 Backup으로 결정론화. 없으면 새 epoch에 WAL만 있고 base가 없어 그 창의 재해가 f(N) recovery 불가.) EKS 측 새 epoch(`postgres-dr/`)의 anchor는 §6 DR `ScheduledBackup(immediate:true)`가 담당 — 이쪽은 `backupEnabled` false→true flip으로 ScheduledBackup이 신규 *생성*되므로 immediate가 정상 발화. 즉 **양방향 모두 전환 직후 anchor가 선다.**

### 3.3 값 배선 (drEpoch를 어디에 두나)

`drEpoch`(정수)를 아래 values에 두고, 각 차트가 serverName을 이 값에서 렌더한다. failover·failback 런북이 **git 커밋 하나로 lockstep bump**한다:

- `gitops/apps/postgres-cnpg/values.yaml` (온프렘 운영, N) · `keycloak-pg/values.yaml`
- `gitops/apps/postgres-cnpg-dr/values.yaml` (EKS DR, N — recovery 소스 f(N) + 아카이브 f_dr(N+1) 둘 다 이 값에서 파생) · `keycloak-pg-dr/values.yaml`
- `gitops/apps/postgres-cnpg-failback/values.yaml` (온프렘 failback, recovery 소스 = f_dr(N+1)) · `keycloak-pg-failback/values.yaml`

**정합 계약:** 온프렘 운영·EKS DR의 `drEpoch`는 항상 동일 N. failover는 bump 안 함(여전히 N). failback 완료 후에만 N→N+1로 **세 파일군(운영·DR·failback) × 2 DB을 한 커밋으로** 올린다. 정확한 per-cycle 값 표는 런북(§9)에 둔다.

렌더 helper(구현 상세, 플랜에서 확정): 온프렘 접미 `{{- if gt (int .Values.drEpoch) 0 }}-e{{ .Values.drEpoch }}{{- end }}`, EKS DR 아카이브 접미 `-e{{ add (int .Values.drEpoch) 1 }}`.

---

## 4. 온프렘 복구 매니페스트 (별도 failback 차트)

DR 쪽 `postgres-cnpg-dr`의 **온프렘 대칭판**을 신설한다(TBD였던 "인플레이스 복구 매니페스트").

### 4.1 신규 차트 `gitops/apps/postgres-cnpg-failback/` (+ `keycloak-pg-failback/`)

`postgres-cnpg-dr`을 온프렘용으로 미러:

- `bootstrap.recovery` source = `postgres-dr/` serverName=`cledyu-pg-dr-e{N+1}` (EKS가 DR-창에 쓴 최신).
- storageClass **longhorn**(gp3 아님, 온프렘 스토리지), size = 운영 미러(postgres 10Gi·keycloak 20Gi — durability는 S3 barman, EBS/PV는 워킹셋).
- 자격증명 = **정적 키**(IRSA 아님) — `s3Credentials.secretAccessKey`가 ns의 `cledyu-backup-s3`(B5). serverAccountTemplate IRSA annotation **없음**.
- `managed.roles`로 비번 reconcile(DR 차트와 동일 — Vault 스냅샷 vs S3 백업 스큐 흡수).
- Cluster 명 = `cledyu-pg`(운영과 동일 — 소비자 `cledyu-pg-rw` svc 불변).
- 이미지 digest = 운영과 동일(B4). in-tree `barmanObjectStore` recovery는 온프렘 오퍼레이터(chart 0.26.1=**1.27.1**)에서 정상 동작(≥1.26 deprecated이나 1.27 지원, 실측 확인) — 운영·DR 차트와 동일한 in-tree 방식이라 향후 오퍼레이터 ≥1.28 상향 시 barman-cloud 플러그인으로 **셋이 함께** 이관(failback 고유 리스크 아님, 기존 승계).
- **`resources` 블록 = 운영 미러**(requests cpu100m/mem256Mi, limits cpu1/mem1Gi) — DR 차트는 BestEffort지만(B9) failback 차트는 **제약된 온프렘**에서 도므로 QoS 보장 필요(운영 차트 사유와 동일, OOM/noisy-neighbor 방지).
- **`ExternalSecret postgres-credentials-cnpg` 미러**(DR 차트 externalsecret.yaml과 동일: `deletionPolicy:Retain`, `cnpg.io/reload:"true"` 라벨) — path-swap 시 prune으로 orphan 되지 않게(B9). `cledyu-backup-s3`는 별도 `backup-secrets/` 앱이 뿌리므로 미러 불요.
- `backup` 블록·`ScheduledBackup`은 **없음**(recovery 전용, 자체 아카이빙 안 함). 전진 아카이빙은 adopt 후 운영 차트가 담당(§4.3).

### 4.2 운영 차트에 `drEpoch` 파라미터 추가 (필수 변경)

`postgres-cnpg`(및 `keycloak-pg`) `cluster.yaml`의 backup 블록은 현재 `serverName`을 명시하지 않아 CNPG가 cluster명(`cledyu-pg`)으로 default한다. failback 후 전진 아카이빙이 새 epoch 경로로 가야 하므로 **명시적 `serverName: cledyu-pg{{ epoch 접미 }}`**를 추가하고 `drEpoch: 0`(기본, 현행과 동일 렌더) values를 도입한다. `bootstrap.initdb.import` fail-safe·`externalClusters(old-postgres)`는 **그대로 둔다**([[feedback_no_delete_comments]] — Edit로만).

### 4.3 오케스트레이션 (git-driven path-swap + 수동 CR 삭제)

B6(root selfHeal, git이 유일 조작 경로)·[P1b](수동 CR 삭제로 recovery 재실행) 패턴을 온프렘에 미러:

1. **운영 앱 path-swap** — `data-postgres-cnpg` Application의 `source.path`를 `gitops/apps/postgres-cnpg` → `gitops/apps/postgres-cnpg-failback`로 git 커밋(단일 앱이 cledyu-pg를 관리 → dual-management 원천 차단). 별도 앱 신설 대신 path-swap을 택하는 이유: 두 앱이 같은 Cluster를 두고 selfHeal로 싸우는 상황을 만들지 않음.
2. **stale cluster 수동 삭제** — `kubectl -n postgres delete cluster cledyu-pg`(recovery bootstrap은 fresh 생성 시 1회만 → 삭제해야 재실행). [P1b]와 동형. **안전장치**: 삭제는 파괴적(PVC 소멸)이므로 **삭제 전 반드시 `-dr/` recovery 소스 건전성 확인**(base backup + WAL 존재). 만약 -dr 아카이브가 불완전한데 stale을 지우면 온프렘이 살아있는 DB 없이 남는다 → 다만 pre-disaster 상태는 `postgres/`(f(N))에 여전히 있어 최악의 경우 그쪽으로 재복원 가능(DR-창만 손실).
3. ArgoCD가 failback 차트 sync → `bootstrap.recovery`로 `cledyu-pg-dr-e{N+1}`에서 복원.
4. 데이터 정합 확인(§9 Step) 후 **drEpoch bump**(§3.3) + **path-swap 원복**(운영 차트로) 커밋.
5. **adopt** — 운영 차트 재-sync 시 cledyu-pg는 이미 존재(recovery로 생성) → bootstrap 재실행 없음(생성 시 1회만). backup serverName이 새 epoch로 바뀌어 전진 아카이빙 개시.

> **리스크(§12-R1)**: adopt 시 운영 차트의 `bootstrap`(import) ↔ live cluster(recovery로 생성)의 spec 차이를 ArgoCD ServerSideDiff가 OutOfSync로 잡을 수 있다. CNPG는 생성 후 bootstrap 변경을 무시하므로 기능엔 무해하나, drift 표시 여부·`ignoreDifferences` 필요성은 **드릴 실검증**.

---

## 5. IAM 변경 (`infra/terraform/aws/backup.tf`) — B1 블로커 해소

온프렘 failback recovery가 `-dr/` 프리픽스를 읽으려면 온프렘 writer 키에 **S3 read-only 확장**이 필요하다. **KMS는 무변경** — `UseBackupKmsKey`가 이미 `Decrypt`를 포함하므로(B1) 프리픽스 GetObject만 열리면 read가 성립한다. 세 statement를 수정한다(EKS 롤 `eks-dr-irsa.tf` 패턴과 동형):

- `postgres` writer:
  1. **신규 `ReadDrPrefix` statement** — `s3:GetObject` on `postgres-dr/*`. **PutObject/AbortMultipart는 부여하지 않음**(온프렘은 -dr에 쓰지 않음 = 최소권한, read 전용).
  2. `ListOwnPrefix`의 `s3:prefix` StringLike 값에 `postgres-dr/*`·`postgres-dr` 추가(barman의 -dr 아카이브 목록).
  3. `DenyForeignPrefixListing`의 `s3:prefix` StringNotLike 예외 값에도 `postgres-dr/*`·`postgres-dr` 추가 — **누락 시 `postgres-dr/*`가 `postgres/*` 패턴과 불일치해 이 Deny에 걸려 ListBucket 차단됨**(핵심: 프리픽스명이 `postgres/`가 아니라 `postgres-`로 시작해 StringLike 미스매치).
- `keycloak` writer: `keycloak-dr/*` 동일 3-statement 수정.
- `vault`·`velero` writer: **무변경**(-dr 개념 없음).

blast-radius 영향: postgres 키가 자기 DR 형제 프리픽스(동일 데이터 클래스)를 read할 수 있게 될 뿐, 교차-DB(vault/keycloak) 격리는 유지.

> **커밋 규율**([[feedback_terraform_docs]]·[[feedback_terraform_target_plan]]): 변경 시 재생성된 `infra/terraform/aws/README.md` 동반 add, plan은 `-target`(backup.tf 리소스)만.

---

## 6. DR 차트 변경 (real-DR 대응)

- `postgres-cnpg-dr`·`keycloak-pg-dr`: `backupEnabled` 기본값은 **false 유지**(드릴 불변 — 반복 드릴 시 -dr 아카이브 미생성으로 충돌 회피, values 주석대로). **real-DR failover 런북이 `backupEnabled=true`로 flip** + `drEpoch`로 아카이브 serverName(`cledyu-pg-dr-e{N+1}`) 파생.
- externalClusters recovery 소스 serverName을 `drEpoch`에서 파생(f(N)) → [P1b] 재-failover가 올바른 epoch를 읽게 함. (현행 하드코딩 `cledyu-pg`는 `drEpoch=0`일 때 동일 렌더 → 드릴 불변.)
- **`postgres-cnpg-dr`에 `ScheduledBackup(immediate:true, backupEnabled 게이트)` 신규 추가**(B8 비대칭 해소 — `keycloak-pg-dr`엔 이미 있음). base backup이 없으면 `-dr/`엔 WAL만 쌓여 온프렘 recovery의 anchor가 없다. `immediate:true`로 **DR primary 기동 직후 base backup 1회** + 이후 스케줄 → failback recovery가 항상 anchor를 갖는다. (backupEnabled=false인 드릴/평시엔 미렌더로 충돌 회피.)

---

## 7. Terraform failback (신규 리소스 없음, 절차 + `-target`)

1. **DNS 원복** — DR 중 CLI `route53 UPSERT`로 EKS ALB를 가리키게 바뀐 record는 **terraform state 밖의 드리프트**다(state는 여전히 온프렘 프록시 ALB=desired). 원복 명령(failover 런북 §공개 DNS 전환의 원복 지시와 동일 형태):
   ```
   terraform apply -var enable_public_ingress=true -target=aws_route53_record.public
   ```
   → record 세 개(api/app/auth for_each)를 desired(온프렘 프록시 `aws_lb.public`)로 되돌린다(B3).
   - ⚠️ **`-var enable_public_ingress=true` 필수**: 생략하면 기본값 false → `local.pub=0` → record의 `count/for_each`가 0으로 평가돼 terraform이 레코드를 **원복이 아니라 destroy**한다(런북 실측 경고). record는 `for_each`라 `-target=aws_route53_record.public`(전체)로 한 번에 잡는다(개별 키 인덱싱 불요).
   - **데이터 정합 확인 후 최후 단계**(split-brain 단일 권한 스위치). 전제: 공개 ingress 스택(프록시/ALB)은 DR과 무관하게 존속하고, 온프렘 복구로 프록시 tailnet upstream이 다시 살아있음.
2. **EKS 축소** — 세 var를 **모두 명시**하고 DR `-target` 목록(module.eks_dr_vpc·module.eks_dr·…·aws_instance.eks_dr_bastion, failover 런북과 동일)만 apply:
   ```
   terraform apply -var enable_eks_dr=true -var eks_dr_active=false -var eks_dr_node_desired=0 <DR -target 목록>
   ```
   → NAT·VPC 엔드포인트·bastion·노드 소멸, 컨트롤플레인 warm 유지. ⚠️ **`-var enable_eks_dr=true` 필수** — 생략 시 기본 false로 **warm 컨트롤플레인까지 destroy**된다(DNS의 enable_public_ingress와 동일한 var-생략 함정). tfvars 부재 → 전체 apply 금지([[feedback_terraform_target_plan]]).
3. **순서 계약**: DNS를 온프렘으로 **먼저**, EKS 축소는 **그 다음**(서빙 중인 EKS를 먼저 죽이지 않음).

---

## 8. Split-brain 방지 (절차 계약)

- 온프렘 앱(api·web·keycloak)은 DNS가 온프렘을 가리키고 **데이터 정합이 확인될 때까지** scale-to-zero/미서빙 유지. EKS는 **cutover 개시(=EKS 쓰기 quiesce, §9 step 1)까지** read-write 단독 보유 — 그 순간부터 어느 사이트도 새 쓰기를 받지 않고(온프렘 미서빙, EKS quiesce), DNS 전환(§9 step 6)으로 온프렘이 서비스 권한을 넘겨받는다. 이 **quiesce~DNS전환 창 = 계획된 write-downtime**이며, async barman-loopback에서 무손실을 얻는 대가다.
- **동시 write 불가 불변식**: quiesce 시점 이후 온프렘·EKS 어느 쪽도 write 없음 → recovery가 읽는 데이터셋이 고정되어 split-brain(양쪽 동시 write)이 원천 성립 불가.
- **Route 53 record = 단일 권한.** 자동 failback 없음 — 각 게이트(쓰기 quiesce·recovery 완료·정합 확인·DNS 전환·EKS 축소)는 수동 승인.
- api의 startup-1회 초기화 특성(`main.go`: DB/auth 1회만 init, 실패 시 degraded 유지) 때문에, 온프렘 앱 재개는 **CNPG·Keycloak·DNS Ready 후 `rollout restart`**로 재초기화(failover 런북과 동형).

---

## 9. 런북 `docs/RUNBOOK/dr-failback.md` (핵심 산출물)

순서(각 스텝 수동 승인 게이트):

0. **전제 확인** — 온프렘 인프라 정상 + 하트비트 재개(복구 신호) + EKS 여전히 서빙 중.
1. **EKS 쓰기 quiesce (계획된 write-downtime 시작)** — cutover 개시. EKS api를 쓰기 미수신으로 전환(`kubectl -n api scale deploy/api --replicas=0` 또는 유지보수 페이지)해 이후 **새 쓰기가 발생하지 않게** 한다. **이 단계가 없으면**: 아래 flush 이후 ~ DNS 전환(step 6) 사이 온프렘 recovery가 도는 수 분 동안 EKS가 받은 쓰기가 온프렘 recovery에 안 들어가 **cutover 시 소실된다**(async barman-loopback의 본질적 RPO 갭). 스트리밍 복제였다면 무중단이나(§ 사용자 기각 옵션), barman-loopback은 이 **짧은 quiesce로 무손실을 보장**한다. (읽기는 DNS 전환 전까지 EKS가 계속 제공 가능 — 완전 다운 대신 read-only가 이상적, api에 read-only 모드 없으면 scale-0 = 그 창만 다운.)
2. **EKS write frontier flush** — quiesce로 쓰기가 멈춘 상태에서 EKS primary:
   - `CHECKPOINT` + `SELECT pg_switch_wal()` — **정합성 핵심**: 마지막 커밋까지 WAL 세그먼트를 강제 아카이빙해 `postgres-dr/`에 flush(온프렘 recovery는 WAL 끝까지 재생 = quiesce 시점 상태 복원). anchor(base backup)는 §6 `ScheduledBackup(immediate:true)`가 이미 확보.
   - on-demand `Backup` CR — **최적화(선택)**: 최신 base backup으로 WAL 재생 구간 단축(정합성 필수 아님). (`pg_switch_wal` 실패 시 RPO = 마지막 성공 WAL 아카이브 시점.)
3. **온프렘 recovery** — **선-확인**: 삭제 전 `-dr/` 아카이브 건전성(base backup + WAL 존재, barman-cloud-check-wal-archive/backup list)을 먼저 검증(§4.3 안전장치) → 그다음 path-swap(→failback 차트) + stale cluster 삭제 + sync(§4.3 Step 1~3). `cledyu-pg-rw`·`keycloak-pg-rw` Ready(자동 S3 복원).
4. **데이터 정합 체크** — EKS vs 온프렘: `session_progress`·`session_steps`·`lab_completions` row count·`max(updated_at)`, keycloak user count 일치. (드릴 마커 row로 무손실 실증.) **불일치 시 cutover 중단** — quiesce 유지한 채 원인 규명(stale 클러스터는 이미 삭제됐으나 pre-disaster 상태는 `postgres/`(f(N))에, DR-창은 `postgres-dr/`에 남아 재복원 가능).
5. **drEpoch bump + adopt + 새 epoch anchor** — §3.3 lockstep bump 커밋 + path-swap 원복(→운영 차트) → 전진 아카이빙(새 epoch WAL) 개시. **명시적 on-demand `Backup` CR로 새 epoch(`cledyu-pg-e{N+1}`)에 base backup 확보**(§3.2 — ScheduledBackup immediate 부수효과에 의존 안 함). base backup completed 확인(없으면 다음 failover recovery 불가).
6. **수동 승인 → DNS 원복** — `terraform apply -var enable_public_ingress=true -target=aws_route53_record.public`로 온프렘 프록시로 원복(§7-1, **var 생략 시 레코드 destroy 주의**). (여기서 서비스 권한이 온프렘으로 넘어감 = write-downtime 종료 지점.)
7. **온프렘 앱 재개** — `rollout restart` api·web·keycloak. 로그인·진도 영속(in-memory 폴백 아님) 검증(§8).
8. **EKS 축소** — `eks_dr_active=false`+`eks_dr_node_desired=0`(§7-2). EKS 아카이빙 중지. (quiesce로 내린 EKS api 파드는 노드 소멸과 함께 정리.)
9. **사후 확인** — 온프렘 연속 아카이빙(새 epoch 경로) 재개·RPO 정상. `[P1b]`용 warm etcd stale CR은 다음 failover가 처리.

`infra/terraform/aws/public-ingress.tf`(또는 관련 tf)에 "failback = 이 record를 온프렘으로 apply" 주석 명시.

---

## 10. Keycloak 대칭

위 전 절차를 keycloak DB에 대칭 적용: `keycloak-pg-failback` 차트, `keycloak`/`keycloak-dr` 프리픽스, serverName `keycloak-pg-e{N}`/`keycloak-pg-dr-e{N+1}`, `keycloak` writer 키 `-dr` read 확장, keycloak user count 정합 체크. 이미지 18.2 동일(B4). Keycloak Ready 후에만 `auth.cledyu.com` 관련 검증(failover 런북과 동형).

---

## 11. 드릴 검증 (`docs/RUNBOOK/dr-restore-drill.md` 확장)

failback 드릴 섹션 추가:

- **마커 주입** — failover 상태의 EKS DR primary에 고유 마커 row 삽입(cledyu-pg + keycloak-pg).
- **failback 수행** — §9 전 절차.
- **무손실 실증** — quiesce **직전** EKS에 넣은 마커가 온프렘에 존재 확인(역복제 성공).
- **quiesce 갭 실증** — quiesce **이후** EKS 쓰기 시도가 거부/불가였고(그런 쓰기가 애초에 없음), recovery 데이터셋이 quiesce 시점에 고정됐음 확인 → recovery 창 write-loss 없음.
- **split-brain 부재 실증** — DNS 전환 **전** 온프렘이 미서빙 + quiesce 이후 양쪽 write 부재(§8 불변식) 확인.
- **반복 성립 실증** — 재-failover(epoch N+1 소스 읽음) → 재-failback(epoch N+2)까지 1사이클 더 돌려 Object Lock 충돌 없음 확인.
- **failback RTO 실측** — 각 스텝 타임스탬프 기록.

---

## 12. 리스크 · 미결 (플랜/드릴서 해소)

- **R1 adopt drift** — §4.3, 운영(import) vs live(recovery) spec 차이의 ArgoCD OutOfSync 여부·`ignoreDifferences` 필요성 드릴 실검증.
- **R2 epoch lockstep 실수** — 6개 values 파일을 한 커밋으로 bump해야 함. 정합 깨지면(운영≠DR) 다음 failover가 틀린 epoch 소스를 읽음 → 런북에 값 표 + 사후 grep 가드(모든 drEpoch 동일 확인).
- **R3 동일 epoch 재시도 충돌** — real-failover가 아카이빙 시작 후 롤백·재시도되면 `cledyu-pg-dr-e{N+1}`가 이미 non-empty → 재시도 전 stale -dr 아카이브 확인·서브접미(예 `-e{N+1}b`) 결정. 드릴서 절차화.
- **R4 base-backup anchor 부재** — `-dr/`에 base backup이 하나도 없으면(EKS DR primary가 §6 `ScheduledBackup(immediate)` 적용 전에 소실 등) 온프렘 recovery가 anchor 없이 실패. 완화: `immediate:true`로 DR 기동 즉시 anchor 확보 + 드릴서 "backupEnabled flip 후 base backup 도달"을 게이트로 확인. (KMS Decrypt는 이미 보유해 위험 아님 — B1로 해소.)
- **R5 정적 키 수명** — 장기 IAM 키 회전은 기존 승계 이슈([[project_dr_scope_delegation]]).
- **R6 write-downtime 창 길이** — 무손실의 대가인 quiesce~DNS전환 창(§9 step1~6)이 온프렘 recovery 시간만큼 지속(수 분~수십 분, DB 크기 의존). 계획된 failback이라 수용하나, 창이 과도하면 (a) on-demand Backup으로 WAL 재생 단축(§9 step2), (b) 궁극적으로 스트리밍 복제(사용자 기각 옵션) 재검토. failback RTO 실측(§11)으로 창 길이 확인.

---

## 13. 산출물 위치

- **신규 차트**: `gitops/apps/postgres-cnpg-failback/`(Chart.yaml·templates/cluster.yaml·templates/externalsecret.yaml·values.yaml) · `gitops/apps/keycloak-pg-failback/`(대칭). 각 cluster.yaml = recovery 전용(backup·ScheduledBackup 없음) + 운영 resources 미러 + longhorn SC; externalsecret.yaml = `postgres-credentials-cnpg`/`keycloak-pg-credentials` 미러.
- **신규 문서**: `docs/RUNBOOK/dr-failback.md`.
- **수정**: `gitops/apps/postgres-cnpg/`·`keycloak-pg/`(drEpoch·명시 serverName, Edit) · `gitops/apps/postgres-cnpg-dr/`(drEpoch 파생 serverName + **신규 `templates/scheduledbackup.yaml`**, backupEnabled 게이트)·`keycloak-pg-dr/`(drEpoch 파생 serverName; ScheduledBackup 이미 존재) · `infra/terraform/aws/backup.tf`(postgres·keycloak writer S3 read 확장, +`README.md` 재생성) · `infra/terraform/aws/public-ingress.tf`(failback 주석) · `docs/RUNBOOK/dr-eks-bootstrap.md`(real-DR failover: backupEnabled=true·epoch flip 섹션) · `docs/RUNBOOK/dr-restore-drill.md`(failback 드릴).
- **오케스트레이션(런북 절차, 신규 파일 아님)**: `data-postgres-cnpg`·`data-keycloak-pg` Application의 path-swap 커밋(전환 중 transient).

---

## 14. 비목표 (YAGNI)

- **Vault 역복제** — 재해 중 새 시크릿이 거의 안 생기고 스냅샷이 source of truth. failback 시 Vault는 온프렘 CronJob 스냅샷 재개만 확인(절차 문구). 역복제 대상 아님.
- **Kafka 역복제** — 메시지는 in-flight, 결과는 Postgres로 영속. 재검증으로 무손실(문구만).
- **진행 중 EC2 실습 세션 보존** — failback 시 EKS 축소(§9 step8)로 DR 백엔드의 활성 EC2 실습 VM은 종료된다. 실습 **결과**(진도·수료)는 Postgres로 역복제되나 진행 중이던 VM 상태는 휘발 → 사용자 재시작(Kafka in-flight와 동일 성격, 컴퓨트 수명은 데이터 failback 범위 밖).
- **failback 자동화(Step Functions)** — Plan C가 자동 failback을 명시적으로 금지(split-brain 위험). 수동 런북 + 지원 매니페스트/`-target`만.
- **drEpoch=0 프로덕션 마이그레이션** — 현행 아카이브를 지금 -e0로 바꾸지 않음(첫 failback부터 접미 등장).
- **무중단 failback** — barman-loopback(async)은 recovery 창 동안 EKS 쓰기 quiesce가 무손실의 전제(§8·§9). 무중단(quiesce 없이)은 스트리밍/논리 복제가 필요하나 사용자가 복잡도 이유로 기각. **계획된 짧은 write-downtime을 수용**하는 것이 이 스펙의 선택.

---

## 15. 구현 순서 (하나의 스펙 → 플랜)

1. **IAM 선행**(§5) — backup.tf writer read 확장 + README. failback recovery의 전제(B1). `-target` plan.
2. **차트·values**(§4·6) — failback 차트 2종(recovery 전용+ExternalSecret+resources 미러) + 운영/DR drEpoch 파라미터화 + `postgres-cnpg-dr` ScheduledBackup 추가(B8). `helm template | kubeconform` 검증.
3. **failover 런북 확장**(§6) — real-DR: backupEnabled=true·epoch flip(드릴 경로 불변 확인).
4. **failback 런북**(§9) + **드릴 섹션**(§11).
5. **드릴 라이브 검증** — 마커 무손실·split-brain 부재·반복(2사이클) 성립·adopt drift(R1)·RTO 실측.

→ 다음 단계: `writing-plans` 스킬로 위 순서를 파일·인터페이스·검증 단위의 구현 플랜으로 전개.
