# DR 재해 감지 + 알림 설계 (Plan C 감지 계층)

- 작성일: 2026-07-14
- 담당: 김찬영
- 상태: 설계 승인 대기
- 범위: Plan C(오케스트레이션) 계획서의 **Task 1~3(감지 + 알림)만** 떼어낸 첫 증분.
  복구 오케스트레이션(Task 4~5)은 이 스펙의 비목표.
- 관련 문서:
  - `docs/superpowers/plans/2026-07-03-dr-backup-plan-c-orchestration.md` (Plan C 전체 계획, Task 1~3 원안)
  - `docs/superpowers/specs/2026-07-01-aws-dr-backup-design.md` (§ 재해 감지 4분면 · RTO)
  - `docs/RUNBOOK/dr-eks-bootstrap.md` (감지 후 사람이 실행할 기존 수동 복구 런북)

## 배경

Cledyu DR은 이미 **수동으로 작동**한다 — 온프렘 상실 시 `dr-eks-bootstrap.md` 런북으로
EKS(pilot-light, 컨트롤플레인 상시 warm)를 기동해 복구한다. Plan A(백업 계층)·Plan B(EKS
오버레이·pilot-light)·Plan C-6(failback)은 완료됐다.

**남은 가장 큰 실질 갭은 "감지가 0"이라는 점이다.** 온프렘이 새벽에 죽어도 이를 자동으로
알아채고 사람을 깨우는 장치가 현재 하나도 없다(레포 실측: 하트비트 CronJob·Route53
health check·CloudWatch 복합알람·재해 알림용 Lambda 전부 부재). 지금은 사용자나 학습자가
"로그인이 안 된다"고 신고해야 비로소 인지하는 구조다.

이 스펙은 그 갭만 메운다: **온프렘 상실을 오탐 없이 감지해 AWS 독립 경로로 사람에게 알린다.**
복구는 계속 검증된 수동 런북으로 한다(소규모 팀에서는 사람이 "진짜 재해 vs 일시 장애"를
판단해 검증된 절차를 실행하는 편이 오케스트레이터 오작동 리스크보다 안전하다는 판단).
자동 오케스트레이션은 감지 계층이 실측 튜닝으로 안정화된 뒤의 다음 증분으로 미룬다.

## 목표 / 비목표

### 목표
- 온프렘 데이터센터/클러스터 상실을 **오탐 없이** 감지한다(pull + push 두 독립 신호의 AND).
- 감지 시 **온프렘에 의존하지 않는 AWS-side 경로**로 담당자에게 알린다(Discord).
- 감지 임계값·알림 경로를 드릴로 실증하고 실측값을 남긴다(포폴 재료).

### 비목표 (명시적 제외)
- **자동 복구 오케스트레이션** (Plan C Task 4~5: Step Functions/EKS 자동 기동/복원/DNS 전환).
  복구는 기존 수동 런북 유지.
- **인플레이스 복구 런북** (cledyu-pg만 손실·온프렘 생존 시나리오) — 별건.
- **Plan D: 백업 상태 알림 규칙** (WAL 아카이빙 지연·RPO 위반·base backup 노후 Prometheus
  alert). 별건이며, 그 알림은 온프렘 Alertmanager에서 돈다 — 이 스펙의 "온프렘 죽음" 알림과
  경로·성격이 다르다.

## 아키텍처

감지·알림 경로 전체가 **AWS-side로 완결**되어야 한다 — 감지 대상(온프렘)이 죽는 것이므로,
경로 어디에도 온프렘 의존이 있으면 정작 재해 때 알림이 실패한다.

```
독립 신호 2개 ── AND ── 알림

  [pull] Route53 health check ─────┐
    fqdn = auth.cledyu.com         │
    HTTPS_STR_MATCH "cledyu-learn" │
    /realms/cledyu-learn           │
    (AWS-side, 온프렘 의존 0.       ├─→ CloudWatch ──→ SNS ──→ Lambda ──→ Discord 웹훅
     온프렘 죽으면 ALB 업스트림      │   복합알람          토픽      (Secrets      (기존 채널
     = tailnet 프록시 끊겨 5xx)     │  (pull AND push)             Manager       재사용,
                                    │                              웹훅)         신규 웹훅 권장)
  [push] 온프렘 heartbeat ─────────┘                    │
    dr-heartbeat CronJob(@1분)                          └─→ (후속) 이메일 구독 추가 가능
    → CloudWatch PutMetricData
      Cledyu/DR OnPremHeartbeat=1
    (dead man's switch)

  → Discord 알림 → 사람이 판단 → dr-eks-bootstrap.md 수동 실행 (자동 오케스트레이션 없음)
```

### 신호 A — pull 프로브 (Route53 health check)
AWS Route53이 30초마다 공개 엔드포인트 `auth.cledyu.com`을 딥 HTTP로 친다(`HTTPS_STR_MATCH`,
경로 `/realms/cledyu-learn`, 본문에 `cledyu-learn` 문자열 존재 확인). 이 경로는
ALB → tailnet 리버스프록시 → 온프렘 Keycloak으로 이어지므로, 온프렘이 죽으면 프록시 업스트림이
끊겨 5xx가 나고 health check가 실패한다. **온프렘 인프라에 대한 의존이 전혀 없는** AWS-side 신호.

### 신호 B — push 하트비트 (dead man's switch)
온프렘 클러스터의 `dr-heartbeat` CronJob이 1분마다 CloudWatch에 custom metric
(`Cledyu/DR` 네임스페이스, `OnPremHeartbeat=1`)을 기록한다. M분간 지표가 없으면 "온프렘이
살아서 신호를 보내지 못하는 상태"로 판정. push 알람은 `treat_missing_data=breaching`으로
설정해 데이터 부재 자체를 위반으로 본다.

### 결합 — 복합알람 (AND)
`pull ALARM AND push ALARM`일 때만 재해로 판정한다. 단일 신호 오탐을 막는다:

| pull | push | 판정 | 의미 |
|---|---|---|---|
| OK | OK | 정상 | — |
| ALARM | OK | **정상 유지** | AWS↔온프렘 네트워크 일시 끊김. 온프렘은 살아 heartbeat 전송 중 |
| OK | ALARM | **정상 유지** | heartbeat 파드만 크래시. 서비스는 정상 응답 중 |
| ALARM | ALARM | **재해** | 두 독립 경로 모두 온프렘 불통 → 알림 발동 |

### 알림 — SNS → Lambda → Discord
복합알람 상태변화(→ALARM)가 SNS 토픽으로 가고, 구독한 Lambda가 Discord 웹훅으로 메시지를
POST한다. 기존 Discord 알림은 온프렘 Alertmanager → `alertmanager-discord-proxy`(in-cluster)를
거쳐 **온프렘과 함께 죽으므로 재사용 불가** — 그래서 이 경로는 순수 AWS-side로 새로 만든다.

## 핵심 설계 결정

1. **알림용 시크릿은 AWS Secrets Manager에 둔다 (온프렘 Vault 비의존).**
   Lambda가 쓰는 Discord 웹훅 URL을 온프렘 Vault/ESO에서 받으면, 재해 시 Vault가 죽어 있어
   알림 자체가 실패한다. `eks-dr-bastion.tf`가 이미 쓰는 Secrets Manager 패턴을 따른다.
   ※ 대비: **heartbeat IAM 키**(컴포넌트 1)는 온프렘 Vault/ESO에서 받아도 된다 — heartbeat는
   온프렘에서 도는 프로세스라, Vault가 죽었으면 온프렘이 죽은 것이고 heartbeat 중단 자체가 곧
   우리가 원하는 신호다. "AWS-side 비의존"이 필수인 것은 **알림 경로**(AWS Lambda가 소비하는
   웹훅 시크릿)뿐이다. 온프렘 쪽 시크릿과 AWS 알림 쪽 시크릿은 저장소가 다르다.

2. **push egress는 실측으로 확인됨 (2026-07-14).**
   온프렘 파드(default ns, curlimages/curl)에서 `https://monitoring.ap-northeast-2.amazonaws.com/`
   호출 결과 `HTTP 404, connect 0.026s` — TLS+SNI 통과, AWS 응답 확인. push heartbeat가
   1급 신호로 성립한다. 따라서 복합알람(AND)을 처음부터 배포한다(pull-only 선배포 후 승격 단계 불요).
   ※ 테스트는 default ns 기준(egress NetworkPolicy 없음). 실제 heartbeat는 `dr-system` ns에서
   돌므로, 그 ns 생성 후 동일 테스트 1회 재확인을 배포 절차에 포함한다.

3. **heartbeat는 1분 CronJob으로 단순화한다.**
   Plan C 원안은 30초 주기 상주 Deployment(`while true; sleep 30`)였으나, push 임계값이 M=3분이라
   1분 granularity면 충분하다. 상주 sleep-loop보다 CronJob이 k8s 관용적이고 관측(마지막 성공
   시각)도 명확하다. push 알람은 `evaluation_periods=3 @60s`로 맞춘다.

4. **알림 채널은 SNS→Lambda→Discord (사용자 결정, 2026-07-14).**
   이메일/SMS 대신 기존 Discord 운영 채널 재사용. 단 on-prem `discord-proxy`와 별개의 새 웹훅·
   채널을 권장(경로 독립성). 이메일 구독은 후속 이중화 옵션으로 열어둠.

5. **감지 스택 리전 = us-east-1 (사용자 결정, 2026-07-14).**
   Route53는 글로벌 서비스라 health check의 CloudWatch 메트릭(`AWS/Route53 HealthCheckStatus`)이
   **us-east-1에만 발행**된다. 따라서 pull 알람은 us-east-1이어야 하고,
   `aws_cloudwatch_composite_alarm`은 멤버 알람과 **동일 리전·계정**이어야 하므로 → **push 알람·
   복합알람·SNS·Lambda까지 전부 us-east-1**에 둔다. heartbeat CronJob도 **us-east-1로 발행**
   (`--region us-east-1`). 나머지 Cledyu AWS 스택(ap-northeast-2)과 리전이 갈리는 유일한 예외이며,
   Route53-기반 DR 감지의 표준 패턴이다. dr-detection.tf는 us-east-1 provider alias를 쓴다.
   ※ egress 재검증 대상이 `monitoring.us-east-1.amazonaws.com`으로 바뀐다(§ 검증). 만약 이 엔드포인트
   egress가 막혀 있으면 pull을 ap-northeast-2 CloudWatch Synthetics canary로 바꿔 전 스택을
   ap-northeast-2로 되돌리는 폴백을 택한다.

6. **거짓 재해 방지 — 무장(arming) + pull 경로 보강 (2026-07-14).**
   `treat_missing_data=breaching`+AND 는 "미배포/설정 미완료"와 "진짜 재해"를 구분 못 한다. AND 가
   *단일* 신호 설정실패는 이미 막지만(한쪽 healthy면 OK), **두 신호가 동시에 설정 이유로 unhealthy**
   (주로 초기 bring-up: heartbeat 동기화 지연 + pull 미준비)면 거짓 알림이 나갈 수 있다. 두 축으로 막는다:
   - **무장:** 복합알람을 `actions_enabled = var.dr_detection_armed`(기본 false)로 생성 → bring-up 에
     ALARM 이 돼도 알림 안 감. 두 신호 healthy 실측 후 `dr_detection_armed=true` 재apply 로 무장.
     (런타임 `enable-alarm-actions` 는 다음 apply 에 되돌려지므로 변수로 둔다.)
   - **pull 경로 보강:** `public_ingress_allowed_cidrs` 를 좁혀도 Route53 health checker 가 ALB 443 에
     도달하도록, `data.aws_ip_ranges`(route53_healthchecks)로 checker 대역을 ALB SG 에 항상 허용
     (`public-ingress.tf`). pull 이 설정 이유로 상시 ALARM 이 되는 것을 막는다.
   - **잔여(문서 경고):** 프록시 인스턴스 SPOF·WAF managed rule·ACM·realm 개명은 pull 을 unhealthy 로
     만들 수 있으나, 무장 상태에선 push 가 healthy 면 복합알람이 OK 라 오발동하지 않는다.

## 컴포넌트 / 파일

| # | 위치 | 종류 | 내용 |
|---|---|---|---|
| 1 | `gitops/apps/dr-heartbeat/` | 신규 Helm 차트 | `Chart.yaml`, `values.yaml`, `templates/cronjob.yaml`(@1분, `aws cloudwatch put-metric-data`), `templates/externalsecret.yaml`(heartbeat 전용 IAM 키, Vault `aws/dr-heartbeat`) |
| 2 | `gitops/argocd/apps/platform-dr-heartbeat.yaml` | 신규 ArgoCD App | 위 차트 배포(namespace `dr-system`) |
| 3 | `infra/terraform/aws/dr-detection.tf` | 신규 TF | heartbeat IAM 사용자+정책(`cloudwatch:PutMetricData`, `Cledyu/DR` 네임스페이스 조건) · Route53 health check(pull) · pull 알람 · push 알람 · 복합알람(AND) · SNS 토픽 |
| 4 | `infra/terraform/aws/dr-alert-lambda.tf` + `dr-alert-lambda/` (소스) | 신규 TF + Lambda | SNS→Discord 웹훅 Lambda(레포 최초 Lambda) · Secrets Manager 웹훅 시크릿 · Lambda 실행 IAM 롤(Secrets Manager read + 기본 로깅) · SNS→Lambda 구독 |
| 5 | `infra/terraform/aws/README.md` | 재생성 | terraform_docs 훅 — dr-detection/dr-alert-lambda 변수·출력 반영해 함께 add(안 하면 pre-commit 훅이 커밋 중단) |

Terraform 컨벤션: `var.name_prefix`(`cledyu-lab`), 정책은 `data.aws_iam_policy_document`,
시크릿 output 없음. **plan은 `-target` 부분 plan만**(tfvars 부재 상태에서 전체 plan 시 게이트
리소스 오-destroy 위험).

## 임계값 (제안값 — 드릴에서 튜닝)

- pull: `request_interval=30s`, `failure_threshold=5` (약 2.5분 연속 실패)
- push: `period=60s`, `evaluation_periods=3` (3분 공백), `treat_missing_data=breaching`
- 복합알람: `ALARM(pull) AND ALARM(push)`

두 신호가 각자 임계에 도달한 뒤 AND가 성립하므로, 실제 알림까지 체감 지연은 대략 2.5~3분.
야간 무응답·다채널 이중화(이메일 추가)는 후속에서 판단.

## 엣지케이스 / 실패모드

- **push egress 차단 (해소됨, 재확인 절차 유지):** 만약 `dr-system` ns의 NetworkPolicy가
  egress를 막으면 push 알람이 영구 ALARM으로 고착되어 AND가 pull-only로 붕괴한다. → 배포 시
  해당 ns에서 egress 재확인(결정 2). 현재 default ns 기준으로는 열려 있음이 실측됨.
- **DR 진행 후 신호 자기해소:** 복구로 `auth.cledyu.com` DNS가 EKS로 전환되면 pull이 다시
  통과해 복합알람이 OK로 돌아간다. 감지는 "전환 트리거 순간"용이므로 정상 동작(알림은 상태변화
  시점에 이미 발송됨).
- **알림 경로 자체의 온프렘 의존 금지:** SNS·Lambda·Secrets Manager 전부 AWS-side라 온프렘
  사망과 무관하게 동작. (결정 1이 이를 보장)
- **공개 진입점 의존 (실측 확인됨, 2026-07-14):** pull 프로브는 `enable_public_ingress=true`
  (auth.cledyu.com ALB 스택) 전제. 실측: `auth.cledyu.com`이 서울 리전 ALB IP 4개로 해석되고,
  `GET /realms/cledyu-learn` → `HTTP 200`(122ms) 본문에 `"realm":"cledyu-learn"` 포함 →
  health check의 `HTTPS_STR_MATCH("cledyu-learn")`가 현재 healthy로 성립. 온프렘 사망 시 이
  200이 5xx로 바뀌어 pull 신호가 발동한다. **이 전제는 `dr-detection.tf`의 `aws_route53_health_check.onprem_pull`
  `lifecycle.precondition`으로 강제**한다 — `enable_public_ingress=false`면 apply 가 실패해, 공개 진입점 없이
  감지 스택이 배포돼 복합알람이 "구성 미완료"를 재해로 오탐하는 것을 막는다.
- **heartbeat 키 유출 반경 최소화:** heartbeat IAM 사용자는 `PutMetricData`(네임스페이스 조건)
  단일 권한만. 백업 writer 키와 분리(권한 분리).

## 검증 / 드릴

- **정적 검증:** `helm template gitops/apps/dr-heartbeat -n dr-system | kubeconform -strict
  -ignore-missing-schemas` · `cd infra/terraform/aws && terraform validate && terraform fmt -check`
- **지표 도달 확인:** heartbeat sync 후 `aws cloudwatch get-metric-statistics ... OnPremHeartbeat`
  로 최근 데이터포인트(Sum≈1/분) 확인.
- **감지 드릴 (오탐 방지 실증):**
  1. heartbeat scale 0 → M분 후 `push=ALARM` 이지만 `복합=OK`(pull 정상) 확인 → AND 오탐 방지 실증
  2. (격리·짧게) pull까지 실패 유도 → `복합=ALARM` → **Discord에 실제 메시지 도착** 확인
  3. heartbeat 원복 → 복합알람 OK 복귀 확인
- 각 전이의 타임스탬프를 기록해 체감 감지 지연 실측(런북에 남김).

## 의존성

- **Plan A/B와 독립.** Task 4~5(복구)만 Plan B에 의존하며, 이 스펙엔 없음.
- 전제: `enable_public_ingress=true`(pull 대상 존재), 온프렘→CloudWatch egress(실측 확인됨),
  기존 Discord 워크스페이스(신규 웹훅 발급).
- 단일 클라우드: apply·런타임 모두 AWS 자격증명만으로 완결(GCP 불요).

## 미결 / 후속

- 임계값(pull N=5, push M=3분) 실측 튜닝.
- 야간 무응답 대비 이메일/다채널 이중화(SNS 이메일 구독 추가).
- **다음 증분 후보:** 자동 오케스트레이션(Plan C Task 4~5) — 감지 안정화 후 착수 판단.
- Plan D(백업 상태 알림 규칙)는 별도 스펙.
