# AWS DR / 백업 설계

- 작성일: 2026-07-01
- 담당: 김찬영
- 상태: 설계 승인, 구현 계획 착수 전
- 관련 로드맵: `docs/architecture/phases.md` Phase 9(원안 Velero+GKE) → 본 문서로 EKS+AWS 네이티브 피벗

## 배경

Cledyu는 온프렘(KubeVirt) 세션 풀을 primary로 쓰고, 리소스가 만석이면 AWS EC2로
버스트한다(Phase 13, 이미 구현: `docs/RUNBOOK/ec2-overflow.md`). 여기에 **재해복구(DR)와
백업**을 추가한다. EKS는 상시 운영이 아니라 재해 시에만 Cold 로 기동한다.

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

- **S3 백업 버킷**: 버전ing 활성, 교차리전 복제 옵션. 자격증명은 Vault→ESO로 주입.
- **Keycloak realm 설정은 백업하지 않는다** — `infra/terraform/keycloak`로 재생성.
  백업 대상은 학습자 계정이 쌓이는 Keycloak DB뿐.
- **왜 Longhorn 볼륨 백업이 아닌 wal-g인가(Postgres)**: 볼륨 백업은 crash-consistent이고
  RPO가 백업 주기만큼 거칠다. wal-g는 WAL 연속 아카이빙으로 RPO를 수분으로 낮추고 PITR을 제공한다.

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

- **RTO 목표**: Cold 기준 ~1~2시간. Lambda 자동화로 수동 단계를 제거해 상한을 관리한다.
- **재해 신호 정의**: 오탐 방지를 위해 N회 연속 실패 임계 + (옵션)수동 승인 게이트. false-positive
  DR 발동은 비용·혼란 유발.
- **복원 자격증명은 IAM 롤로**(정적 키 금지): S3 백업 접근 정적 키는 Vault 안에 있고 Vault 자체가
  복원 대상이라 순환이 생긴다(닭-달걀). 복원 컴퓨트(Lambda 실행롤 / EKS IRSA)에 백업 버킷 read
  IAM 롤을 부여해 S3에서 백업을 꺼낸다 → 복원 경로에 정적 키가 없다. 온프렘 backup-writer 정적
  키(`infra/terraform/aws/backup.tf`)는 온프렘 쓰기 전용(온프렘은 IAM 롤 불가)으로만 남긴다.
  `terraform output`은 순수 break-glass 폴백.
- **Vault 부트스트랩 체인**: 복원한 Vault 는 sealed 로 뜬다. auto-unseal(GCP KMS, `values-gcpckms.yaml`)
  접근도 복원 컴퓨트 롤에 포함해야 unseal→ESO 정상화 순으로 복귀한다.

### 핵심 구현 과제

1. **EKS 오버레이**: 온프렘 GitOps는 EKS에 그대로 배포되지 않는다. 교체 필요 —
   - Longhorn → EBS CSI
   - MetalLB → AWS Load Balancer Controller
   - KubeVirt → 제외(세션은 EC2 경로)
   - Cilium → 유지 가능(EKS overlay)
   kustomize/Helm values 오버레이를 사전 작성·검증해 둔다. **구현 난이도의 대부분.**
2. **재해 신호/프로브**: AWS 쪽에서 온프렘 헬스를 관측하는 외부 프로브 + EventBridge 규칙.

## 알림 체계

- **백업 실패**: wal-g / raft 스냅샷 / Longhorn backup 잡 실패 → Prometheus alert
  (kube-prometheus-stack 기존 활용)
- **RPO 위반**: 마지막 성공 백업이 목표 RPO 초과(예: WAL 아카이브 지연 > 15분) 시 경보
- **DR 드릴**: 분기 1회 복구 리허설로 실제 RTO 측정·기록
- **비용**: EKS/EC2 과금 → AWS Budgets (`infra/terraform/aws/budgets.tf` 기존 활용)

## 산출물 위치(예정)

- 백업: `gitops/apps/`(wal-g/raft CronJob, Longhorn RecurringJob), S3 버킷은 `infra/terraform/aws/`
- DR 오케스트레이션: `infra/terraform/aws/`(EventBridge/Lambda/Step Functions, EKS)
- EKS 오버레이: `gitops/` 하위 EKS 오버레이 디렉터리
- lab-events: `apps/airflow/dags/lab_events_to_bq.py`

## 미결 / 후속

- 재해 신호의 정확한 임계·수동 게이트 여부는 구현 계획에서 확정
- EKS 프로비저닝을 사전생성 빈 클러스터 vs 완전 IaC 생성 중 무엇으로 할지 비용/RTO 트레이드오프 재검토
