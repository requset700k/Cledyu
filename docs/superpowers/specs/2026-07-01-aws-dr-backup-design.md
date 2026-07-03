# AWS DR / 백업 설계

- 작성일: 2026-07-01
- 담당: 김찬영
- 상태: 설계 승인, 구현 계획 착수 전
- 관련 로드맵: `docs/architecture/phases.md` Phase 9(원안 Velero+GKE) → EKS+AWS 네이티브로 피벗
  (GKE→EKS, 오케스트레이션은 Lambda/EventBridge). 단, **클러스터 오브젝트 상태 백업은 Velero로
  재도입**한다(§ 클러스터 상태 백업 참조) — GitOps 재동기화만으로는 git에 선언되지 않고 런타임에
  동적 생성되는 리소스를 복원할 수 없다는 것이 확인됨(예: `lab-sessions` 네임스페이스).

## 배경

Cledyu는 온프렘(KubeVirt) 세션 풀을 primary로 쓰고, 리소스가 만석이면 AWS EC2로
버스트한다(Phase 13, 이미 구현: `docs/RUNBOOK/ec2-overflow.md`). 여기에 **재해복구(DR)와
백업**을 추가한다. EKS는 상시 운영이 아니라 재해 시에만 Cold 로 기동한다.

**DR 성공 기준**: 유저 불편 최소화가 아니라 **과금된 기능(수료증·진도·리더보드 등)이 재해 후에도
정상 동작하는가**다. 진행 중이던 랩(세션) 초기화는 이 기준에서 허용 가능한 손실로 본다.

## 목표 / 비목표

### 목표
- 온프렘 데이터센터 상실 시 컨트롤플레인 + durable 데이터를 EKS에서 복구
- durable 데이터의 오프사이트(S3) 백업과 측정 가능한 RPO/RTO
- 재해 감지 → 복구를 AWS 네이티브(EventBridge/Lambda)로 자동화
- 백업 실패·RPO 위반 알림 체계

### 비목표 (YAGNI)
- 진행 중 랩 VM 라이브 마이그레이션 — 폭포수 모델이라 실익 없음. 학습자는 진도 유지한 채 랩 재시작
- Kafka / Redis 데이터 백업 — DR 시 GitOps로 빈 채 재기동
- security-logs 파이프라인 — 아직 미구축. 추후 lab-events 패턴 재사용
- Pilot-light 상시 EKS — 비용 이유로 Cold 채택
- 온프렘 세션 체크포인트/재개(Longhorn 스냅샷) — 별도 백로그. DR과 분리

## 아키텍처: 3층 구조

```
평상시:  온프렘 KubeVirt (primary 세션 풀)
만석 시:  → EC2 오버플로우 (Phase 13, 구현됨)      # 용량 문제 대응
재해 시:  → EKS Cold DR (본 설계)                   # 가용성 문제 대응
```

- **DR이 복구하는 것**: api / web / ai-tutor 컨트롤플레인 + Postgres / Vault / Keycloak durable 데이터
- **DR이 버리는 것**: 진행 중이던 랩 VM 상태. 학습자는 진도 유지, 랩만 처음부터 재시작
- **DR 중 세션 실행**: 온프렘 KubeVirt 부재 → **기존 EC2 오버플로우 경로를 재사용**한다.
  EKS의 api가 모든 신규 세션을 EC2로 라우팅(기존 SSM 채점 + tailnet 터미널). 신규 개발 불필요.

## 백업 계층 (온프렘 → S3)

| 대상 | 메커니즘 | RPO | 근거 |
|---|---|---|---|
| Postgres (cledyu) | wal-g 베이스백업 + WAL 연속 아카이빙 → S3 | 5~15분 | 학습자 진도의 유일 원본, PITR |
| Keycloak DB | wal-g 또는 pg_dump 크론 → S3 | Postgres와 정렬 | 학습자 신원 원본(앱 users는 미러) |
| Vault | `vault operator raft snapshot save` CronJob → S3 | 1~24h | 시크릿 저빈도 변경 |
| 범용 PVC | Longhorn RecurringJob backup → S3 | 볼륨별 정책 | crash-consistent 볼륨 백업 |

lab-events는 이 표(온프렘→S3 백업) 대상이 아니다. BQ(GCP)로 안착하는 온프렘 durability 항목이며
DR 백업 범위 밖이다 — 상세는 아래 § lab-events 내구성 참조.

- **S3 백업 버킷**: 버전ing 활성, **SSE-KMS(고객 관리 CMK)** 암호화. 자격증명은 Vault→ESO로 주입.
  - **왜 SSE-KMS**: 버킷에 Vault 시크릿·학습자 PII가 모이므로 "버킷 접근(s3)"과 "복호화(kms)"를
    분리해 자물쇠를 둘로 만든다. CloudTrail 복호화 감사·키 비활성화로 즉시 봉인 가능. bucket key로
    KMS 호출 비용 절감. 복원 롤에는 이 키의 `kms:Decrypt`를 부여(Plan C Task 5).
  - **교차리전 복제(CRR)는 제외**: 리전 전체 장애만 막는데 복원 대상 EKS도 동일 리전이라 백업만
    살고 복구는 불가 → 반쪽. 멀티리전 DR은 본 프로젝트 범위 밖.
  - **S3 Object Lock(GOVERNANCE 30일) 도입**: 백업 자체 손실(writer 키 유출·삭제·랜섬)을 CRR이
    아니라 불변성으로 막는다. versioning이 못 막는 "권한 있는 삭제"까지 30일간 차단. writer엔
    BypassGovernanceRetention 미부여(키 유출 시 우회 방지). 단 **Longhorn 매시 백업은 30일 락과
    충돌**(720개 누적)하므로 락 없는 별도 버킷으로 분리(비-DR·온프렘 로컬 복구라 우선순위 낮음).
- **Keycloak realm 설정은 백업하지 않는다** — `infra/terraform/keycloak`로 재생성.
  백업 대상은 학습자 계정이 쌓이는 Keycloak DB뿐.
- **왜 Longhorn 볼륨 백업이 아닌 wal-g인가(Postgres)**: 볼륨 백업은 crash-consistent이고
  RPO가 백업 주기만큼 거칠다. wal-g는 WAL 연속 아카이빙으로 RPO를 수분으로 낮추고 PITR을 제공한다.

### 백업 우선순위

두 가지 축을 구분한다. 헷갈리기 쉬워서 명시해 둔다.

- **기술적 복원 순서**(의존성 제약): Vault가 먼저 unseal되어야 ESO가 다른 시크릿을 정상 주입한다.
  따라서 실제 복원 절차는 Vault → (ESO 정상화) → Postgres/Keycloak DB 순으로 진행된다
  (§ Vault 부트스트랩 체인 참조).
- **비즈니스 우선순위**(무엇을 가장 엄격히 보호하는가): 장애 시 가장 중요한 것은 Postgres
  (학습자 진도·수료 이력)다. 이미 RPO 표에서 Postgres가 가장 타이트한 값(5~15분)으로 반영돼
  있다 — 별도 조치 불필요, 다만 "복원은 Vault부터 시작하지만 보호 우선순위는 Postgres가
  1순위"라는 점을 혼동하지 않는다.

## 클러스터 상태 백업 (Velero)

### 배경

GitOps(ArgoCD) 재동기화는 **git에 선언된** 리소스만 재현한다. 런타임에 동적으로 생성되고
git에 없는 리소스는 GitOps만으로 복원되지 않는다.

실측 사례: `lab-sessions` 네임스페이스는 GitOps 관리 대상이 아니라 `validation-engine`이
런타임에 임시로 생성한다([[project_lab_sessions_missing]], Session API로 소유권 이관 예정이나
그 전까지는 이 갭이 존재). 이런 리소스는 새 EKS에 ArgoCD를 아무리 완벽하게 부트스트랩해도
재현되지 않는다.

→ **온프렘 라이브 클러스터 상태(떠 있던 파드 수, 사용 중이던 네임스페이스 등)를 그대로
복제하려면 Velero가 필요하다.** 원안(Phase 9)의 Velero를 GKE 대신 EKS 타깃으로 재도입한다.

### 백업 범위 (selective)

Velero로 클러스터 전체를 무차별 백업하면 **비목표로 정한 "진행 중 랩 상태는 버림"과 충돌**한다
(랩/세션 리소스까지 같이 복원되어 버림). 따라서 네임스페이스/라벨 셀렉터로 범위를 제한한다:

- **포함**: 컨트롤플레인 네임스페이스(api/web/keycloak/vault/postgres 등)의 오브젝트
- **제외**: `lab-sessions`, KubeVirt 관련 네임스페이스/리소스 — 세션은 DR 후 EC2 오버플로우
  경로로 새로 뜬다(§ 아키텍처: 3층 구조)

### Postgres/Vault와의 역할 분담

데이터 자체는 기존 전용 메커니즘을 유지한다(PITR이 필요한 Postgres는 wal-g가 Velero의 crash-consistent
스냅샷보다 우월 — § 왜 Longhorn 볼륨 백업이 아닌 wal-g인가 참조). Velero는 **그 외 클러스터
오브젝트**(네임스페이스, ConfigMap, CRD, 동적 생성 리소스)만 담당한다. Postgres/Vault의 PVC는
Velero 백업 라벨 셀렉터에서 제외해 PV 스냅샷 중복을 피한다.

### 미결 (확인 필요)

- **MinIO 중간 계층 필요 여부**: Velero의 Backup Storage Location이 S3를 직접 지원하므로,
  온프렘에 MinIO 같은 별도 오브젝트 스토리지가 실제로 필요한지 확인 필요. 필요 없다면 컴포넌트를
  하나 줄일 수 있다.
- **클러스터 상태 RPO(스냅샷 주기)**: 데이터 RPO(Postgres 5~15분)와는 별도 축. 얼마나 자주
  Velero 스냅샷을 뜰지 미정.

## lab-events 내구성 (DR 작업 아님 — 온프렘 durability 수정)

이 항목은 **DR 작업이 아니라 온프렘의 현존 데이터 durability 버그**다. DR 스펙에는 가정/의존성으로만
남기고, 실제 수정은 별도 온프렘 백로그로 다룬다.

현상: `apps/api`가 lab-events(힌트요청·중도포기·실패패턴 등 분석 이벤트)를 Kafka로 발행하나
durable 싱크가 평시 동작하지 않는다. DAG(`apps/airflow/dags/lab_events_to_bq.py`)는 존재하나
`schedule=None`(수동 데모)이고 BQ 적재가 상시 돌지 않는다. 토픽 retention 7일이라 **DR과 무관하게
온프렘 단독으로도 7일 경과 시 유실**된다.

- **온프렘 조치(백로그)**: DAG를 돌아가게 만든다(크론 부여 예: `*/10 * * * *` + BQ 연결 확인).
  이것으로 평상시 7일 유실 버그를 해소한다.
- **DR에는 별도 조치 불필요**: 위 싱크가 동작하면 데이터는 BQ(GCP)에 안착하고, BQ는 온프렘 밖
  durable 스토리지이므로 온프렘 상실 시에도 이미 오프사이트에 있다. DR은 lab-events를 위해 따로
  할 일이 없다(Kafka에 남아 미적재된 몇 분치만 손실, 허용 범위).
- **DR 중 지속**: 재해 중 새 lab-events는 EKS의 api→EKS Kafka로 발행되고, Airflow도 다른 앱과
  함께 EKS로 재배포되어 EKS Kafka→BQ 적재를 이어간다. 이는 lab-events 전용 조치가 아니라
  플랫폼 재배포의 일부다.
- **본 스펙의 가정**: "온프렘 lab-events 싱크가 동작한다"를 전제한다. 이 전제가 깨지면 그것은
  온프렘 버그이지 DR의 결함이 아니다.

## DR 오케스트레이션 (Cold + EventBridge/Lambda)

```
[재해 신호]  외부(AWS) 프로브가 온프렘 헬스 엔드포인트 주기 체크 → N회 연속 실패
    │
    ▼
EventBridge (신호 수신 / 규칙 매칭)
    │
    ▼
Lambda 또는 Step Functions (순차 복구)
    ├─ 1. EKS 클러스터 프로비저닝(또는 사전생성 빈 클러스터 기동)
    ├─ 2. ArgoCD 부트스트랩 → GitOps 동기화 (EKS 오버레이)
    ├─ 3. S3에서 Postgres / Keycloak / Vault 복원
    ├─ 4. DNS 전환 (auth.cledyu.com / api → EKS 엔드포인트)
    └─ 5. 복구 완료 알림 (RTO 타이머 종료)
```

- **복원 자격증명은 IAM 롤로**(정적 키 금지): S3 백업 접근 정적 키는 Vault 안에 있고 Vault 자체가
  복원 대상이라 순환이 생긴다(닭-달걀). 복원 컴퓨트(Lambda 실행롤 / EKS IRSA)에 백업 버킷 read
  IAM 롤을 부여해 S3에서 백업을 꺼낸다 → 복원 경로에 정적 키가 없다. 온프렘 backup-writer 정적
  키(`infra/terraform/aws/backup.tf`)는 온프렘 쓰기 전용(온프렘은 IAM 롤 불가)으로만 남긴다.
  `terraform output`은 순수 break-glass 폴백.
- **Vault 부트스트랩 체인**: 복원한 Vault 는 sealed 로 뜬다. auto-unseal(GCP KMS, `values-gcpckms.yaml`)
  접근도 복원 컴퓨트 롤에 포함해야 unseal→ESO 정상화 순으로 복귀한다.

### RTO 설계 (장애 발생 기준, end-to-end)

RTO를 정직하게 정의한다 — **시작선 = 장애 발생 시점**(발동 시점 아님. 학습자 다운타임은 장애
순간부터고 감지·승인 시간도 실 다운타임), **종료선 = 데이터 복구(PITR) 완료 시점**(서비스 기동
아님. 앱이 떠도 데이터가 옛날이면 성공 기준을 못 채움).

| 구간 | 목표 | 성격 | 근거 |
|---|---|---|---|
| 감지 | ~5분 | 자동 | 신호가 "몇 분 연속 실패"를 확인하는 오탐 방지 완충 |
| 판단·승인 | ≤30분 | 사람 | 알림 인지+상황 판단. 유일한 사람 변수(야간 지연 가능) |
| 복구 실행 | ~90분 | 자동 | EKS 기동 30 + ArgoCD·앱 15 + PITR 재생 30 + DNS 전파 10 (순차 의존이라 합산) |
| 버퍼 | ~25분 | — | 검증·재시도·전파 지연 여유 |
| **총 RTO** | **~2.5시간** | | |

- **PITR**(Point-In-Time Recovery): 베이스 백업 + 이후 WAL을 순서대로 재생해 장애 직전 시점으로
  복원. 복구 실행 90분 중 "PITR 재생 30분"이 이 replay 시간.
- **왜 쪼개나**: 자동(감지·실행) vs 사람(승인) 구간을 분리해야 RTO 단축 지점이 보인다. 현재는
  승인 30분이 최대 변수 — 완전 자동화로 없앨 수 있으나 오탐 시 데이터 분기 위험 때문에 의도적으로
  사람 게이트를 남긴 트레이드오프. EKS 기동 30분은 Warm Standby로 줄지만 상시 비용과 맞바꿈.

### 재해 감지 — 오탐 최소화 (pull + push AND)

**제약**: 온프렘이 NAT 뒤라 AWS가 직접 프로브 불가. 외부에서 온프렘에 닿는 모든 pull 경로가
**Tailscale 오버레이**를 통과한다(`auth.cledyu.com` → ALB → 프록시 EC2 → tailnet → Keycloak).
→ Tailscale 순단을 온프렘 장애로 오판할 위험. Route 53 다위치 다수결도 16개 체커가 같은 tailnet
병목으로 수렴해 무력화된다.

**결정**: 실패 지점이 독립인 두 신호를 **AND**로 묶는다.

- **pull** (AWS→온프렘, tailnet 경유): Route 53/Lambda가 `auth.cledyu.com`을 1분 주기 HTTP GET,
  딥 헬스 + 응답 본문 문자열 매칭, N회 연속 실패 시 알람. (너무 깊으면 DB 순단도 재해로 오판하니
  "앱이 응답한다"까지만)
- **push 하트비트** (온프렘→AWS, tailnet 무관): 온프렘이 30초마다 CloudWatch에 "살아있음"을 기록
  (dead man's switch, "데이터 없음=위반" 알람). 온프렘이 **먼저 밖으로 나가는** 아웃바운드라 NAT가
  안 막고 tailnet도 안 탄다(S3 백업·BigQuery 적재가 이미 같은 직접 HTTPS 경로 — 코드로 확인).

두 신호의 실패 원인이 겹치지 않는다(pull=tailnet 인바운드, push=온프렘 아웃바운드). CloudWatch
복합 알람에서 AND로 묶어 **둘 다 실패할 때만** 재해 후보로 본다:

| pull | push | 상황 | 판단 |
|---|---|---|---|
| ✅ | ✅ | 정상 | — |
| ❌ | ✅ | Tailscale만 끊김, 온프렘 살아있음 | 발동 안 함(오탐 방지) |
| ✅ | ❌ | 아웃바운드만 문제, 온프렘 살아있음 | 발동 안 함 |
| ❌ | ❌ | 양쪽 경로 다 죽음 = 재해 | 승인 요청 |

- **한계**: 온프렘 인터넷 전면 단절·AWS 리전 동시 이상·정전 재부팅 중이면 두 신호가 다 실패해
  "재해로 보이나 곧 돌아올" 상황이 생긴다. 자동 로직으론 구분 불가 → **수동 승인 게이트**로 사람이
  "진짜 죽음 vs 일시적"을 최종 판단. (승인은 감지 정확도 문제가 아니라 "오판 시 데이터 영구 분기"를
  막는 다른 층의 보험 — 과금 시스템은 사람 확인이 정석)
- **흐름**: `pull N회 실패 AND push M초 끊김` → 복합 알람 → EventBridge → 알림 → [사람 승인] → Lambda 복구.
- **부수효과**: 같은 하트비트가 "온프렘 복구(재개)" 신호도 겸한다 → failback 트리거로 재사용(아래).

### 온프렘 복귀(Failback) / 스플릿 브레인 방지

**현재 스펙에 없던 갭.** 위 오케스트레이션은 "재해 감지 → EKS로 전환"까지만 다루고, **온프렘이
복구된 이후의 절차가 없다.** 온프렘이 살아났는데 DNS/트래픽이 아직 EKS를 가리키고 있으면 두
클러스터가 동시에 쓰기 가능한 상태(스플릿 브레인)가 되어 Postgres가 갈라질 위험이 있다.

- **원칙**: 온프렘이 복구되어도 **자동으로 트래픽을 되돌리지 않는다.** EKS가 read-write를
  계속 쥐고, 온프렘은 수동 승인 전까지 트래픽을 받지 않는다(예: DNS 전환 전 온프렘 앱을
  scale-to-zero 또는 read-only로 유지).
- **DNS 단일 권한**: "지금 누가 서비스하나"를 Route 53 한 곳이 결정. 온프렘이 살아나도 DNS가 EKS를
  가리키는 한 트래픽이 온프렘으로 안 가 → 두 곳이 동시에 write 받는 상황 자체가 성립 안 함.
- **failback 순서**(초안, 확정 필요): ① 온프렘 인프라 정상 확인 → ② EKS의 최신 데이터를
  온프렘으로 역방향 복제/복원 → ③ 데이터 정합성 확인 → ④ 수동 승인 후 DNS를 온프렘으로 전환
  → ⑤ EKS를 다시 Cold 상태로 축소.
  - **왜 역복제가 필수인가**: 재해 중 최신 데이터는 EKS에 쌓였고 온프렘은 재해 시점 옛 데이터에
    멈춰 있다. 그대로 온프렘을 열면 옛 데이터로 서비스해 데이터가 갈라진다 → 돌아가기 전에
    EKS→온프렘으로 먼저 맞춰야 한다. failover(들어갈 때)보다 failback(나올 때)을 더 엄격히 확인.
- **미결**: 구체적 failback 자동화 수준(수동 vs 반자동)과 역방향 데이터 복제 방식은 Plan C에서
  확정한다.

### 핵심 구현 과제

1. **EKS 오버레이**: 온프렘 GitOps는 EKS에 그대로 배포되지 않는다. 교체 필요 —
   - Longhorn → EBS CSI
   - MetalLB → AWS Load Balancer Controller
   - KubeVirt → 제외(세션은 EC2 경로)
   - Cilium → 유지 가능(EKS overlay)
   kustomize/Helm values 오버레이를 사전 작성·검증해 둔다. **구현 난이도의 대부분.**
2. **재해 신호/프로브**: pull(Route 53/Lambda가 tailnet 경유로 온프렘 헬스 관측) + push(온프렘
   CloudWatch 하트비트) 두 신호를 복합 알람 AND로 결합 → EventBridge 규칙. 상세는 § 재해 감지 참조.

## 알림 체계

- **백업 실패**: wal-g / raft 스냅샷 / Longhorn backup 잡 실패 → Prometheus alert
  (kube-prometheus-stack 기존 활용)
- **RPO 위반**: 마지막 성공 백업이 목표 RPO 초과(예: WAL 아카이브 지연 > 15분) 시 경보
- **DR 드릴**: 분기 1회 복구 리허설로 실제 RTO 측정·기록
- **비용**: EKS/EC2 과금 → AWS Budgets (`infra/terraform/aws/budgets.tf` 기존 활용)

## 산출물 위치(예정)

- 백업: `gitops/apps/`(wal-g/raft CronJob, Longhorn RecurringJob, Velero), S3 버킷은 `infra/terraform/aws/`
- DR 오케스트레이션: `infra/terraform/aws/`(EventBridge/Lambda/Step Functions, EKS)
- EKS 오버레이: `gitops/` 하위 EKS 오버레이 디렉터리
- lab-events: `apps/airflow/dags/lab_events_to_bq.py`

## 미결 / 후속

- 재해 감지 신호 구조는 확정(pull+push AND + 수동 게이트, § 재해 감지). 남은 것은 임계값 실측
  튜닝 — pull N회·push M초 구체값, 승인 게이트 야간 응답 대응(다채널 알림)
- 온프렘 CloudWatch egress가 tailnet 없이 열려 있는지 실측 확인(push 하트비트 전제)
- EKS 프로비저닝을 사전생성 빈 클러스터 vs 완전 IaC 생성 중 무엇으로 할지 비용/RTO 트레이드오프 재검토
- Velero 클러스터 상태 스냅샷 주기(클러스터 상태 RPO), MinIO 중간 계층 실제 필요 여부
- Longhorn 백업용 무-락 별도 버킷 신설(Object Lock 30일과 매시 백업 충돌 해소, Plan A Task 6)
- 온프렘 failback 절차 구체화 — 수동 vs 반자동 자동화 수준, 역방향 데이터 복제 방식(§ 온프렘 복귀)
