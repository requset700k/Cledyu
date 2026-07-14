# RUNBOOK: DR 재해 감지 + 알림

온프렘 데이터센터/클러스터 상실을 자동으로 감지하여 AWS-side 경로로 담당자에게 알린다.
복구는 사람이 판단·검증 후 기존 수동 런북(`docs/RUNBOOK/dr-eks-bootstrap.md`)으로 실행한다(자동 오케스트레이션 없음).

## 1. 아키텍처 한눈에

감지는 두 독립 신호의 **AND 결합**으로 오탐을 방지한다.

```
  [pull 신호]  Route53 health check
  fqdn = auth.cledyu.com
  경로 = /realms/cledyu-learn
  조건 = 본문에 "cledyu-learn" 문자열
  (AWS-side, 온프렘 의존 0)
        ▼
      ┌─ AND 복합알람 ──→ SNS 토픽
      │   (pull AND push)    │
      ▼                      ├──→ Lambda
      
  [push 신호]  온프렘 heartbeat  │      (Secrets Manager
  dr-heartbeat CronJob(@1분)   │       웹훅 조회)
  → CloudWatch PutMetricData   │
  Cledyu/DR OnPremHeartbeat=1 │      ▼
  (dead man's switch)          └──→ Discord 웹훅
                                    (기존 채널 또는 신규)
                                    ▼
                              담당자 인지 → 판단 → 수동 복구
```

**us-east-1 앵커 이유:** Route53 health check의 CloudWatch 메트릭(`AWS/Route53 HealthCheckStatus`)은 **us-east-1에만 발행**된다.
복합알람(`aws_cloudwatch_composite_alarm`)은 멤버 알람과 **동일 리전·계정**이어야 하므로, pull 알람뿐 아니라
push 알람·복합알람·SNS·Lambda까지 **전부 us-east-1**에 배포된다. 이는 Route53 기반 DR 감지의 표준 패턴이다.

---

## 2. 감지 드릴 절차 (운영자)

> ⚠️ **이 드릴은 Step 3에서 실제 Discord 알림(복합알람 ALARM)을 발생시킨다.**
> 운영 온콜 채널을 실수로 깨우지 않도록 — **사전에 팀에 공지하고 점검창에서 실행**하거나,
> 웹훅을 임시로 테스트 채널로 돌린 뒤 진행한다. 또한 Step 3는 운영 `auth.cledyu.com`
> 서비스를 내리지 않고 **Route53 health check 설정만** 일시 변경해 pull 실패를 흉내내며,
> Step 4에서 **반드시 원복**한다(원복 누락 시 감지가 계속 오동작).
> 또한 app-of-apps + `selfHeal=true` 구조라, 드릴은 **Step 2에서 root-apps·자식 앱의 자동
> sync 를 껐다가 Step 4에서 되켠다**(안 끄면 suspend 가 되돌려져 push 알람이 재현 안 됨).
> ⚠️ root-apps 를 끄면 드릴 동안 **클러스터 전체 GitOps self-heal 이 멈추니** Step 4 원복 필수.
> (대안: cronjob 에 `suspend` 값을 두어 GitOps 로 suspend — 이 PR 범위 밖, 후속.)

각 단계의 **벽시계 시각**을 기록하여 실제 감지 지연을 실측한다.

### 2.1 Step 1: 정상 확인

```bash
# 현재 상태 확인: 모두 OK여야 함
# ※ --alarm-types 를 반드시 지정 — 없으면 describe-alarms 는 metric 알람만 반환하고
#   복합알람(cledyu-lab-dr-disaster)은 응답에서 빠진다(AWS 기본 동작).
aws cloudwatch describe-alarms --region us-east-1 \
  --alarm-types CompositeAlarm MetricAlarm \
  --alarm-names "cledyu-lab-dr-pull" "cledyu-lab-dr-push" "cledyu-lab-dr-disaster"

# 출력에서 StateValue: ALARM → 이미 문제 상태, 원인 파악 후 진행
# StateValue: OK → 정상 진행
```

**기록:** 스텝 1 시작 시각 (`T0`)

---

### 2.2 Step 2: 오탐 방지 확인 (push만 차단)

heartbeat CronJob을 suspend해 push 신호만 끊는다 (pull은 정상).
3~4분 후 `push=ALARM`이지만 `composite=OK` 상태 확인 → **AND 로직이 오탐을 차단**하는지 증명.

```bash
# 0) ArgoCD self-heal 차단 (드릴 동안, Step 4 에서 원복). app-of-apps 구조라 2단계로 끈다:
#    - root-apps(Ansible 배포·self-managed 아님)가 자식 Application spec 을 git 과 강제 일치시키므로,
#      root-apps 를 먼저 꺼야 자식의 sync 정지가 몇 분 뒤 되돌려지지 않는다.
#    - 그 다음 platform-dr-heartbeat 를 꺼야 cronjob suspend drift 가 되돌려지지 않는다.
#    ⚠️ root-apps auto-sync 를 끄면 드릴 동안 클러스터 전체 GitOps self-heal 이 멈춘다 → Step 4 원복 필수.
argocd app set root-apps --sync-policy none
argocd app set platform-dr-heartbeat --sync-policy none
#    argocd CLI 없으면 각각:
#    kubectl -n argocd patch application <app-이름> --type merge \
#      -p '{"spec":{"syncPolicy":{"automated":null}}}'

# heartbeat CronJob suspend (push 신호 차단)
kubectl -n dr-system patch cronjob dr-heartbeat \
  -p '{"spec":{"suspend":true}}'

# 확인
kubectl -n dr-system get cronjob dr-heartbeat -o jsonpath='{.spec.suspend}'
# true 출력 → 성공
```

**기록:** suspend 시각 (`T1`)

```bash
# 3분 경과 후 상태 확인 (T1+3분)
aws cloudwatch describe-alarms --region us-east-1 \
  --alarm-types CompositeAlarm MetricAlarm \
  --alarm-names "cledyu-lab-dr-push" "cledyu-lab-dr-disaster"

# 기대값:
# - cledyu-lab-dr-push: StateValue=ALARM (하트비트 부재)
# - cledyu-lab-dr-disaster: StateValue=OK (pull=OK이므로 AND는 성립 안 함)
```

**기록:** push ALARM 확인 시각 (`T2`) - (T1+3분)까지의 지연 기록

---

### 2.3 Step 3: 진짜 재해 감지 (둘 다 차단)

heartbeat suspend 유지 + pull 신호까지 차단하여 둘 다 ALARM 상태로 만든다.
그 결과 `composite=ALARM` → **Discord에 실제 알림이 도착**하는지 확인.

#### 3a) pull 신호 차단 (짧은 시간)

방법: health check의 resource_path를 임시로 존재하지 않는 경로로 바꿔 pull 실패를
흉내낸다(운영 `auth.cledyu.com` 서비스 자체는 안 내린다).
```bash
# auth.cledyu.com 의 health check ID 조회 (route53 는 global — --region 불필요)
HC_ID=$(aws route53 list-health-checks \
  --query "HealthChecks[?HealthCheckConfig.FullyQualifiedDomainName=='auth.cledyu.com'].Id | [0]" \
  --output text)
echo "HC_ID=$HC_ID"   # None/빈값이면 enable_public_ingress 미배포 — 조회 조건 확인

# resource_path 를 없는 경로로 변경 → search_string 불일치로 pull 실패 유도
aws route53 update-health-check \
  --health-check-id "$HC_ID" \
  --resource-path "/realms/nonexistent"
```

**기록:** pull 차단 시각 (`T3`)

#### 3b) 복합알람 ALARM 확인 + Discord 알림 수신

```bash
# 1~2분 후 상태 확인 (pull failure_threshold=5@30s → ~2.5분)
aws cloudwatch describe-alarms --region us-east-1 \
  --alarm-types CompositeAlarm MetricAlarm \
  --alarm-names "cledyu-lab-dr-pull" "cledyu-lab-dr-push" "cledyu-lab-dr-disaster"

# 기대값:
# - cledyu-lab-dr-pull: StateValue=ALARM
# - cledyu-lab-dr-push: StateValue=ALARM
# - cledyu-lab-dr-disaster: StateValue=ALARM (pull AND push)
```

**기록:** composite ALARM 확인 시각 (`T4`)

**critical:** Discord 채널에서 **실제 알림 메시지 도착** 확인
- 메시지 수신 시각 (`T4_discord`)
- (T3 ~ T4_discord) 간 지연 기록

---

### 2.4 Step 4: 원복

heartbeat suspend 해제 + health check 경로 원복

```bash
# heartbeat suspend 해제
kubectl -n dr-system patch cronjob dr-heartbeat \
  -p '{"spec":{"suspend":false}}'

# health check 경로 원복 (새 셸이면 HC_ID 재조회)
HC_ID=${HC_ID:-$(aws route53 list-health-checks \
  --query "HealthChecks[?HealthCheckConfig.FullyQualifiedDomainName=='auth.cledyu.com'].Id | [0]" \
  --output text)}
aws route53 update-health-check \
  --health-check-id "$HC_ID" \
  --resource-path "/realms/cledyu-learn"

# ArgoCD 자동 sync 재개 — Step 2 역순(자식 먼저, root-apps 나중). ⚠️ 반드시 실행(안 하면 전체 GitOps self-heal 이 멈춘 채 방치).
argocd app set platform-dr-heartbeat --sync-policy automated --auto-prune --self-heal
argocd app set root-apps --sync-policy automated --auto-prune --self-heal
#   kubectl 대안: kubectl -n argocd patch application <app-이름> --type merge \
#     -p '{"spec":{"syncPolicy":{"automated":{"prune":true,"selfHeal":true}}}}'
```

**기록:** 원복 실행 시각 (`T5`)

```bash
# 2~3분 후 상태 확인 (평가 기간 고려)
aws cloudwatch describe-alarms --region us-east-1 \
  --alarm-types CompositeAlarm MetricAlarm \
  --alarm-names "cledyu-lab-dr-disaster"

# 기대값:
# - cledyu-lab-dr-disaster: StateValue=OK (복귀)
```

**기록:** composite OK 복귀 시각 (`T6`)

---

## 3. 감지 로직 테이블 (4분면)

pull과 push의 상태 조합에 따른 composite 알람 동작:

| pull | push | 복합알람 (AND) | 판정 | 설명 |
|------|------|---|---|---|
| OK | OK | OK | 정상 | — |
| ALARM | OK | OK | 정상 유지 | AWS↔온프렘 네트워크 일시 끊김. 온프렘은 살아서 heartbeat 전송 중 |
| OK | ALARM | OK | 정상 유지 | heartbeat 파드만 크래시. 서비스는 정상 응답 중 |
| ALARM | ALARM | **ALARM** | **재해** | 두 독립 경로 모두 온프렘 불통 → Discord 알림 발동 |

이 테이블이 **드릴 Step 2·3의 기대값 근거**이다.
Step 2에서 push=ALARM이지만 pull=OK라 composite=OK 유지,
Step 3에서 둘 다 ALARM이므로 composite=ALARM으로 전환한다.

---

## 4. 감지 지연 실측 (드릴 후 기입)

### 4.1 예상 지연 (이론값)

- pull: `request_interval=30s` × `failure_threshold=5` → 약 **2.5분** 연속 실패 후 ALARM
- push: `period=60s` × `evaluation_periods=3` → **3분** 데이터 부재 후 ALARM
- 복합알람: pull AND push 모두 ALARM 도달 후 즉시 발동
- 전체 AND 발동 시간: 대략 **2.5~3분**
- Discord 수신(Lambda): SNS 발행 후 초 단위 (무시할 수 있음)

### 4.2 실측값 (드릴 후 실측 기입)

아래 표는 **드릴 완료 후 운영자가 기입**한다.
벽시계 시각을 이용해 각 전이의 실제 지연을 기록한다.

| 전이 | 시작 시각 | 확인 시각 | 실측 지연 | 비고 |
|---|---|---|---|---|
| Step 2: heartbeat suspend → push=ALARM | `T1` | `T2` | _(드릴 후 실측 기입)_ | pull은 OK 유지 |
| Step 3a: pull 경로 차단 | `T3` | — | _(차단 실행 시각만 기록)_ | — |
| Step 3b: pull=ALARM + composite=ALARM | `T3` | `T4` | _(드릴 후 실측 기입)_ | pull failure_threshold 도달 |
| Step 3c: Discord 알림 수신 | `T3` | `T4_discord` | _(드릴 후 실측 기입)_ | SNS→Lambda→웹훅 지연 |
| Step 4: 원복 → composite=OK | `T5` | `T6` | _(드릴 후 실측 기입)_ | 정상 복귀 확인 |

**주의:** 이 표는 **가상의 예시값이 아니며** 반드시 **실제 드릴 후 타임스탬프를 기입**한다.
포폴이나 회고에서 "진짜 드릴을 해봤는가"를 증명하는 근거 자료다.

---

## 5. 알림 수신 시 대응

Discord에서 "`cledyu-lab-dr-disaster` alarm triggered" 메시지를 받으면:

1. **상황 판단 (사람의 책임)**
   - 온프렘 대시보드/모니터링 확인: 진짜 다운 vs 네트워크 일시 장애?
   - 팀원과 연락: 누군가 의도적 점검 중인가?
   - pull/push 각각 상태 확인: `aws cloudwatch describe-alarms --region us-east-1`

2. **복구 실행**
   - 온프렘이 진짜 죽었다고 판단 → `docs/RUNBOOK/dr-eks-bootstrap.md` 참고해 수동 복구 실행
   - 해당 런북에는 EKS cold DR(pilot-light) 기동·복원·DNS 전환·failback 절차가 명시됨

3. **주의: 자동 오케스트레이션 없음**
   - 이 감지 스택은 **알림만 제공**한다 — 복구는 사람이 한다
   - 자동 오케스트레이션(Step Functions/Lambda로 EKS 자동 기동)은 명시적으로 제외했다
     (감지 임계값이 아직 드릴 튜닝 중이고, 소규모 팀에서는 사람이 판단하는 편이 안전)

---

## 6. 임계값 + 튜닝

### 6.1 현재값 (권장)

```
pull:
  - request_interval: 30 seconds
  - failure_threshold: 5 (약 2.5분 연속 실패)

push:
  - evaluation_periods: 3
  - period: 60 seconds (약 3분 공백)
  - treat_missing_data: breaching (데이터 부재 자체를 위반으로)
```

### 6.2 튜닝 근거

- pull `failure_threshold=5`: Route53 프로브는 AWS 표준. 일시 네트워크 지연(수십 초)에는 강건하지만,
  실제 온프렘 다운은 반드시 5회 연속 실패를 일으킨다.
- push `evaluation_periods=3 @60s`: heartbeat CronJob이 1분 주기이므로, 3분(데이터 3개 주기) 공백이
  "파드 크래시"(영구)와 "일시 지연"을 구별하는 경계다.
- AND 결합: 단일 신호 오탐(네트워크 지연, 파드 재시작)을 배제하고, 진짜 온프렘 상실만 걸러낸다.

### 6.3 조정 필요 시

드릴에서 측정한 실제 지연이 예상과 크게 다르거나, 오탐/불감이 발생하면:
- **pull을 빠르게 하려면** `failure_threshold` 감소 (단, 네트워크 지터에 민감해짐)
- **pull을 늦추려면** `failure_threshold` 증가
- **push를 빠르게 하려면** `evaluation_periods` 감소 (단, 파드 크래시 vs 재시작 구별 어려움)

변경 후 **반드시 재드릴**로 새로운 임계값을 실증한다.

---

## 7. 운영 절차

### 7.1 Discord 웹훅 로테이션

Secrets Manager에 저장된 웹훅 URL을 새로 발급·업데이트하는 절차:

```bash
# 1) Discord 워크스페이스 관리자가 새 웹훅 생성 → 새 URL 획득

# 2) Secrets Manager 업데이트 (AWS us-east-1 권한 필요)
#    웹훅 토큰을 shell history·프로세스목록(ps)·argv 에 남기지 않는다:
#    - read -rs: 입력이 화면·히스토리에 안 남음(에코 off)
#    - printf 는 bash 빌트인이라 ps 에 인자가 안 뜬다
#    - --secret-string file://: 값 대신 파일 경로만 argv 에 실린다(0600 임시파일)
umask 077
tmp=$(mktemp)
read -rs -p "새 Discord webhook URL: " WEBHOOK_URL; echo
printf '{"url":"%s"}' "$WEBHOOK_URL" > "$tmp"
aws secretsmanager put-secret-value \
  --region us-east-1 \
  --secret-id cledyu-lab-dr-discord-webhook \
  --secret-string file://"$tmp"
shred -u "$tmp" 2>/dev/null || rm -f "$tmp"
unset WEBHOOK_URL

# 3) Lambda가 다음 invocation 때 새 URL을 읽음 (캐싱 없음)
#    따라서 추가 배포 필요 없음
```

**주기:** 분기별 또는 웹훅 URL 유출 시마다

---

### 7.2 heartbeat IAM 키 로테이션

heartbeat CronJob이 CloudWatch에 지표를 기록할 때 사용하는 IAM 액세스 키를 교체:

```bash
# 1) 새 액세스 키 생성. SecretAccessKey 를 화면·history 에 찍지 않고 변수로 받는다.
#    (IAM은 global 서비스, 리전 무관)
umask 077
CREDS=$(aws iam create-access-key --user-name cledyu-lab-dr-heartbeat --output json)
NEW_ID=$(printf '%s' "$CREDS" | python3 -c "import sys,json;print(json.load(sys.stdin)['AccessKey']['AccessKeyId'])")

# 2) Vault에 새 키 저장 (온프렘, Vault 관리자).
#    SecretAccessKey 는 argv 에 싣지 않고 stdin(=-)으로 넘긴다 — history·ps·로그에 안 남는다.
#    access_key_id 는 비밀이 아니라 그대로 둔다.
# sys.stdout.write 로 개행 없이 넘긴다(print 의 trailing \n 이 secret 에 섞이면 인증 깨짐).
printf '%s' "$CREDS" | python3 -c "import sys,json;sys.stdout.write(json.load(sys.stdin)['AccessKey']['SecretAccessKey'])" \
  | vault kv put cledyu/aws/dr-heartbeat access_key_id="$NEW_ID" secret_access_key=-
unset CREDS
echo "새 AccessKeyId: $NEW_ID"   # 5단계에서 <OLD_ID> 삭제 시 참고(ID는 비밀 아님)

# 3) ESO 강제 즉시 동기화 — refreshInterval=1h 를 기다리지 않는다.
#    (이 강제 sync 가 없으면 새 Vault 값 반영이 최대 1시간 지연될 수 있다)
kubectl -n dr-system annotate externalsecret dr-heartbeat-creds \
  force-sync=$(date +%s) --overwrite

# 4) Kubernetes Secret 에 새 키가 실제로 반영됐는지 검증.
#    이 출력이 <NEW_ID> 와 일치해야만 다음 단계로 넘어간다. (일치 전에 옛 키를 지우면
#    매분 새로 뜨는 heartbeat Job 이 옛 Secret 으로 PutMetricData 에 실패해
#    push 알람이 재해 신호로 뒤집힐 수 있다.)
kubectl -n dr-system get secret dr-heartbeat-creds \
  -o jsonpath='{.data.ACCESS_KEY_ID}' | base64 -d; echo
#    (CronJob 은 매번 파드 신규 생성이라 별도 재시작 불필요)

# 5) 새 키 반영을 확인한 뒤에만 기존 액세스 키 삭제 (IAM은 global, 리전 무관)
#    옛 키 ID 식별 — 아래 목록에서 위 $NEW_ID 가 아닌 것이 <OLD_ID>다.
#    (새 키를 지우면 heartbeat 가 즉시 깨지므로 반드시 확인)
aws iam list-access-keys --user-name cledyu-lab-dr-heartbeat \
  --query "AccessKeyMetadata[].AccessKeyId" --output text
aws iam delete-access-key \
  --user-name cledyu-lab-dr-heartbeat \
  --access-key-id '<OLD_ID>'
```

**주기:** 분기별 또는 키 유출 시마다

---

## 8. 트러블슈팅

### 8.1 push 알람이 계속 ALARM 상태

**원인 가능성:**
1. heartbeat CronJob이 실패 중
   ```bash
   kubectl -n dr-system get cronjob dr-heartbeat
   kubectl -n dr-system get pods --sort-by=.metadata.creationTimestamp | grep dr-heartbeat | tail -3
   kubectl -n dr-system logs <pod> -c heartbeat
   ```
2. dr-system ns 네트워크 egress가 CloudWatch로 차단됨
   ```bash
   # dr-system 은 Kyverno baseline-workload-security(runAsNonRoot Enforce) 대상이라
   # bare pod 는 admission 에서 거부된다 → securityContext 를 --overrides 로 주입한다.
   kubectl -n dr-system run test-curl --rm -i --restart=Never --image=curlimages/curl \
     --overrides='{"spec":{"securityContext":{"runAsNonRoot":true,"runAsUser":100,"seccompProfile":{"type":"RuntimeDefault"}},"containers":[{"name":"test-curl","image":"curlimages/curl","securityContext":{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]},"runAsNonRoot":true,"seccompProfile":{"type":"RuntimeDefault"}},"args":["curl","-sS","-m","10","-o","/dev/null","-w","HTTP %{http_code}\n","https://monitoring.us-east-1.amazonaws.com/"]}]}}'
   # HTTP 404 등 아무 코드 = egress 정상(경로 없음) / 타임아웃·연결거부 = egress 차단
   ```
3. IAM 키 유효기간 만료 또는 권한 부족
   ```bash
   kubectl -n dr-system get externalsecret dr-heartbeat-creds
   kubectl -n dr-system get secret dr-heartbeat-creds -o jsonpath='{.data.ACCESS_KEY_ID}' | base64 -d
   # Vault에 저장된 값과 일치하는지 확인
   ```

### 8.2 pull 알람이 계속 OK 상태

**원인 가능성:**
1. auth.cledyu.com이 공개 진입점으로 도달 불가
   ```bash
   curl -v https://auth.cledyu.com/realms/cledyu-learn | grep cledyu-learn
   # HTTP 200 + "cledyu-learn" 문자열 확인
   ```
2. health check의 조건이 맞지 않음 (resource_path, search_string)
   ```bash
   # route53 는 global 서비스 — --region 불필요
   HC_ID=$(aws route53 list-health-checks \
     --query "HealthChecks[?HealthCheckConfig.FullyQualifiedDomainName=='auth.cledyu.com'].Id | [0]" \
     --output text)
   aws route53 get-health-check --health-check-id "$HC_ID"
   # ResourcePath=/realms/cledyu-learn, SearchString=cledyu-learn 인지 확인
   ```

### 8.3 복합알람 상태 응답이 느림

복합알람은 멤버 알람들의 **변경 감지 후** 자신의 상태를 업데이트한다.
각 멤버가 독립 평가 주기를 가지므로:
- pull: 최대 2.5분 지연
- push: 최대 3분 지연
- AND 전환: 그 후 추가 1~2분

→ 총 4~5분 지연 가능 (전력 다운 등 극단적 상황). 정상 범위.

### 8.4 Discord 알림이 오지 않음

**원인 가능성:**
1. SNS 토픽이 Lambda 구독을 갖지 않음
   ```bash
   # ARN 을 동적으로 조회(계정 ID 를 손으로 넣지 않는다)
   TOPIC_ARN=$(aws sns list-topics --region us-east-1 \
     --query "Topics[?ends_with(TopicArn, ':cledyu-lab-dr-alert')].TopicArn" --output text)
   aws sns list-subscriptions-by-topic --topic-arn "$TOPIC_ARN" --region us-east-1
   ```
2. Secrets Manager 웹훅 시크릿이 없거나 손상됨
   ```bash
   # 존재·메타데이터만 확인 (값=웹훅 토큰을 stdout에 노출하지 않는다)
   aws secretsmanager describe-secret \
     --region us-east-1 \
     --secret-id cledyu-lab-dr-discord-webhook

   # 값 형식 검증이 필요하면 — URL 자체는 찍지 않고 형식만 확인:
   aws secretsmanager get-secret-value --region us-east-1 \
     --secret-id cledyu-lab-dr-discord-webhook --query SecretString --output text \
     | python3 -c "import sys,json; d=json.load(sys.stdin); print('url present:', 'url' in d, '| https:', d.get('url','').startswith('https://'))"
   ```
3. Lambda 실행 로그 확인
   ```bash
   aws logs tail /aws/lambda/cledyu-lab-dr-alert --region us-east-1 --follow
   ```

---

## 9. 관련 문서

- 감지 설계 스펙: `docs/superpowers/specs/2026-07-14-dr-detection-alerting-design.md`
- Plan C 전체 계획: `docs/superpowers/plans/2026-07-03-dr-backup-plan-c-orchestration.md`
- 수동 복구 런북: `docs/RUNBOOK/dr-eks-bootstrap.md` **(알림 후 사람이 실행할 것)**
- AWS DR 설계: `docs/superpowers/specs/2026-07-01-aws-dr-backup-design.md`

---

## 10. 변경 이력

| 날짜 | 담당 | 변경사항 |
|---|---|---|
| 2026-07-14 | 김찬영 | 최초 작성 (Task 5: 감지 드릴 + 런북) |
