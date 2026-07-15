# DR 원클릭 페일오버 — Discord 승인 오케스트레이션 설계

> Plan C(`2026-07-03-dr-backup-plan-c-orchestration.md`)의 Task 3 나머지·Task 4 를 현실 배선에 맞춰
> 재설계한다. 감지·알림(Task 1·2 + Task 3 알림분)은 이미 프로덕션 배포·무장됨(#310/#311).

**Goal:** 재해 감지 알림을 받은 담당자가 사이트·로그를 직접 확인해 진짜 재해임을 확정한 뒤, **Discord 버튼 한 번**으로
EKS 기동부터 공개 DNS 전환까지 자동 완료한다. Vault 스냅샷 시점은 같은 메시지의 드롭다운에서 고른다(기본=최신).

**Non-goal:** failback 자동화. failback 은 수동 런북(`docs/RUNBOOK/dr-failback.md`)으로 남긴다 — 이유는 §8.

---

## 1. 현재 상태 (실측 2026-07-15, origin/main 기준)

| Plan C Task | 상태 | 근거 |
|---|---|---|
| 1. push 하트비트 | 완료 | `gitops/apps/dr-heartbeat/`, `dr-detection.tf` `aws_iam_user.dr_heartbeat` |
| 2. pull 프로브 + 복합알람(AND) | 완료 | `aws_route53_health_check.onprem_pull`, `aws_cloudwatch_composite_alarm.disaster` |
| 3. EventBridge → 알림 → 승인 게이트 | **절반** | SNS→Lambda→Discord 만 존재. EventBridge 규칙·승인 게이트 없음 |
| 4. Step Functions + Lambda | **미착수** | `aws_sfn_state_machine` grep 0건 |
| 5. 복원 IAM 롤 | 충족(다른 경로) | `eks-dr-irsa.tf` — IRSA 로 존재. Lambda 실행 롤은 별도 필요 |
| 6. failback 런북 | 완료 | `docs/RUNBOOK/dr-failback.md` + #300 |
| 7. E2E 드릴 | 부분 | 감지 드릴 무장 실측 완료. 자동 경로는 대상 자체가 없음 |

현재 배선은 `복합알람 → SNS → Lambda → Discord → 사람이 수동 런북`에서 끝난다
(`docs/RUNBOOK/dr-detection.md`: "복구는 사람이 판단·검증 후 기존 수동 런북으로 실행한다(자동 오케스트레이션 없음)").
이 설계가 그 뒤를 잇는다.

---

## 2. 확정된 설계 결정

| 항목 | 결정 | 근거 |
|---|---|---|
| 자동화 범위 | 승인 → **DNS 전환까지 전부** | 승인 전 사람이 이미 사이트·로그로 판단함. 2차 게이트는 판단할 새 정보가 없고, 승인자가 자리를 비우면 장애 연장 장치가 됨 |
| 시퀀스 중 온프렘 복귀 | **무시하고 강행** | 중단·재승인(A안)은 반나절 추가. 겪은 뒤 올리기로 함 — 승인 기계가 이미 있어 나중에 A 로 올리는 건 반나절 |
| 승인 권한 | **사용자 ID 허용목록** | Ed25519 검증은 Discord 강제사항이라 어차피 구현. ID 대조는 +3줄 |
| terraform 실행 | **CodeBuild** | Lambda 15분 초과 위험. AWS SDK 직접 생성은 state 밖 고아 → failback `terraform apply` 전제를 깸 |
| SSM 대기 | **자식 상태 머신** | SFN 에 SSM `.sync` 통합 없음(§5). 통짜 스크립트는 실패 지점 소실 |
| 노드 수 | **3 고정** | 런북 값 |
| `-target` 단일 출처 | **하지 않음** | 단기 프로젝트, DR 리소스 추가 예정 없음 (YAGNI) |
| failback | **수동 런북** | §8 |
| `backupEnabled` 토글 | **유지**(제거 검토했다 철회) | 토글은 워트가 아니라 **failover 보호 장치**다 — §8.1 |
| `backupEnabled` flip 소유자 | **[13] 알림 + failback step 0 게이트** | 자동화(git push)는 한 줄 값에 비해 과함 — §8.1 |

---

## 3. 아키텍처

### 3.1 실행 주체가 셋으로 갈리는 이유 (권한·네트워크가 강제)

| 주체 | 담당 | 강제 이유 |
|---|---|---|
| **CodeBuild** | terraform apply | 15분 초과 가능. S3 state·락 사용 |
| **bastion (SSM RunCommand)** | kubectl·helm·Vault | private-only 엔드포인트라 kubectl 은 bastion 에서만 가능. bastion 롤이 `access_entries` 에 cluster admin 으로 배선됨(`eks-dr.tf:83-93`). Vault 복원용 S3/KMS/Secrets 권한 이미 보유(`eks-dr-bastion.tf:63-104`) |
| **Lambda** | Discord, DNS, 노드/애드온 CLI, 알림 | route53/wafv2/elbv2 권한은 bastion 에 **없음**(런북 명시: bastion 실행 시 AccessDenied). Route53 은 공개 API 라 VPC 연결 불요 |

**Lambda 를 VPC 에 넣지 않는다** — bastion 이 private EKS 발판 문제를 이미 해결해 두었다.

### 3.2 리전 배선

| 리소스 | 리전 | 이유 |
|---|---|---|
| 복합알람·SNS·`dr-alert` Lambda | **us-east-1** | Route53 `HealthCheckStatus` 메트릭이 us-east-1 전용 (`dr-detection.tf` `provider aws.use1`) |
| EventBridge 규칙·`failover-trigger` Lambda | **us-east-1** | 알람 상태변화 이벤트는 알람의 리전 기본 버스에만 발생 |
| Step Functions·CodeBuild·bastion·EKS DR | **ap-northeast-2** | `var.region` (validation 으로 ap-northeast-2 고정) |

크로스리전 hop 은 `failover-trigger` Lambda 하나가 `sfn.start_execution(region=ap-northeast-2)` 로 넘긴다
(EventBridge 크로스리전 버스보다 단순).

### 3.3 컴포넌트

| 컴포넌트 | 리전 | 런타임 | 역할 |
|---|---|---|---|
| `failover-trigger` Lambda | us-east-1 | python3.12 | EventBridge → `sfn.start_execution` |
| `approval-request` Lambda | ap-northeast-2 | python3.12 | S3 스냅샷 목록 → **Bot API** 로 Discord 승인 메시지(§3.6) |
| `interaction` Lambda | ap-northeast-2 | **nodejs20** | Function URL, Ed25519 검증, `SendTaskSuccess` |
| `addon-install` Lambda | ap-northeast-2 | python3.12 | coredns·ebs-csi 멱등 설치 |
| `dns-switch` Lambda | ap-northeast-2 | python3.12 | WAF 확인 + Route53 UPSERT |
| `notify` Lambda | ap-northeast-2 | python3.12 | 완료·실패 Discord 알림 |
| `approvals` DynamoDB | ap-northeast-2 | — | `approvalId → taskToken` (TTL 24h) |
| `cledyu-dr-failover-tf` CodeBuild | ap-northeast-2 | — | terraform apply |
| `cledyu-dr-failover` SFN | ap-northeast-2 | — | 메인 상태 머신 |
| `cledyu-dr-run-on-bastion` SFN | ap-northeast-2 | — | 자식(SSM 폴링) |

### 3.4 런타임이 갈리는 이유 (interaction Lambda 만 Node.js)

Discord Interactions Endpoint 는 **Ed25519 서명 검증이 필수**다 — Discord 가 무작위로 가짜 서명을 보내
테스트하고, 401 을 뱉지 않으면 엔드포인트를 등록 해제한다(Discord 공식 문서).

- `python3.12` 표준 라이브러리에 Ed25519 **없음** → PyNaCl/`cryptography` 필요 → 현재의 무의존
  `archive_file` 패턴이 깨지고 레이어·빌드 스텝 발생
- Node 내장 `crypto` 는 Ed25519 **네이티브 지원**(`crypto.verify(null, data, publicKey, sig)`) → 의존성 0.
  Discord 가 주는 Public Key 는 hex 문자열이므로, `crypto.createPublicKey` 에 넘길 키 객체 구성 방식은
  구현 시 실측 확인한다(리뷰 지적 — 초안의 "raw-public 포맷" 주장은 근거 없이 쓴 것이라 삭제).

나머지 Lambda 는 기존 `dr-alert`(python3.12 + boto3, `archive_file`) 패턴을 그대로 따른다.

### 3.5 DynamoDB 가 필요한 이유

Discord `custom_id` 는 **최대 100자**(공식 문서). Step Functions 태스크 토큰은 수백 자라 버튼에 직접 실을 수 없다.
→ 짧은 `approvalId` 만 `custom_id` 에 싣고 토큰은 DynamoDB 에 둔다. TTL 24h 로 자동 청소.

스냅샷 드롭다운은 String Select 로 **최대 25개**(공식 문서) — 6시간 주기 스냅샷 기준 약 6일치. 기본값 = 최신.

**드롭다운 선택도 DynamoDB 에 저장해야 한다(리뷰 지적).** Discord 에서 select menu 조작과 버튼 클릭은 **별개의
interaction** 으로 도착한다 — 버튼 클릭 payload 에 드롭다운의 현재 선택값이 실려 오지 않는다. 따라서:

| 이벤트 | interaction Lambda 동작 |
|---|---|
| select menu 선택 | DynamoDB item 의 `snapshot` 필드 **UPDATE** (deferred update 응답) |
| 버튼 클릭 | DynamoDB GET → `{taskToken, snapshot}` → `snapshot` 없으면 **최신으로 폴백** → SendTaskSuccess |

item 스키마: `{approvalId(PK), taskToken, snapshot, latestSnapshot, ttl}`. `latestSnapshot` 은 [1] 이 목록을
만들 때 함께 써 두어, 사용자가 드롭다운을 건드리지 않고 바로 승인했을 때의 폴백값으로 쓴다.

> **`snapshot` 은 DynamoDB 예약어다** — 표현식에 직접 쓰면 **항상** `ValidationException`. `UpdateItem` 은
> `ExpressionAttributeNames`(`"SET #snap = :s"` + `{"#snap": "snapshot"}`)로 우회한다. 쓰는 쪽([1])은
> `PutItem` 의 `Item` 맵이라 표현식을 안 거쳐 무관하다 — 그래서 **읽는 쪽만** 걸린다(초안이 놓친 이유).

### 3.6 승인 메시지는 웹훅이 아니라 **Bot API** 로 보낸다 (실측 2026-07-15 — 초안의 근본 오류)

**초안은 기존 `dr-alert` 웹훅(#310) 재사용을 전제했으나 그것으로는 버튼·드롭다운을 보낼 수 없다.**
Discord 공식 문서: "**Non-application-owned webhooks cannot send interactive components, and the
`components` field will be ignored**"(`with_components` 를 붙여도 **비**대화형 컴포넌트만 허용).
채널 설정에서 만든 일반 incoming 웹훅은 application 소유가 아니다.

**증상이 조용해서 위험하다:** Discord 가 에러가 아니라 **2xx 를 주고 `components` 만 버린다** → Lambda 는
성공(로그 깨끗), DynamoDB 저장까지 정상, SFN 은 토큰 대기 → **"성공했는데 메시지에 버튼만 없음"**.
정적검증·코드리뷰로는 원리적으로 잡을 수 없고 실제 POST 를 쏴야만 드러난다(3라운드 적대적 리뷰 + 최종
리뷰 opus 전부 통과시킴 — 아무도 "웹훅으로 버튼을 보낼 수 있나?"를 묻지 않았다).

→ **승인 메시지만 봇으로 보낸다:**

| | 주체 | 이유 |
|---|---|---|
| `dr-alert` 평문 알림(#310) | **웹훅 유지** | 컴포넌트가 없어 웹훅으로 충분. 변경하지 않는다 |
| 승인 요청(버튼+드롭다운) | **Bot API** | `POST /channels/{id}/messages` + `Authorization: Bot <token>` |

- 신규 시크릿 `${var.name_prefix}-dr-discord-bot-token`(**ap-northeast-2** — Lambda 와 같은 리전이라
  §3.3 의 ARN 리전 파싱이 불요하다. us-east-1 에 있는 웹훅 시크릿과 다른 점)
- 신규 변수 `dr_discord_channel_id`(공개 식별자 → `variables.tf` default 로 커밋, `dr_approver_ids` 와 동일 근거)
- **봇은 해당 채널에 `Send Messages` 권한이 필요하다** — 없으면 `403 Missing Access`
- 선행 작업(§9)에 Bot 생성·토큰·서버 초대가 추가된다

---

## 4. 전체 흐름

```
[us-east-1]  복합알람 ALARM (기존, 무장됨)
   ├─→ SNS → dr-alert Lambda → Discord 알림              (기존 #310/#311 — 변경 없음)
   │      ※ 이 갈래만 actions_enabled(=dr_detection_armed)로 억제된다
   └─→ EventBridge 규칙  ※ count = local.pub && dr_detection_armed && dr_orchestration_armed (§7.4)
          └→ failover-trigger Lambda → sfn.start_execution ──┐
                                                             │ (리전 넘음)
[ap-northeast-2]                                    Step Functions
                                                             │
                    [1] approval-request Lambda (.waitForTaskToken, 24h)
                         · s3 ls s3://cledyu-lab-dr-backups/vault/ → 최신순 25개
                         · DynamoDB PUT {approvalId, taskToken, latestSnapshot}
                         · Discord POST: [승인] 버튼 + 스냅샷 드롭다운(기본=최신)
                                                             │  ⏸
                (사람: 사이트 직접 접속 · 온프렘 콘솔 · 일시 장애 여부 확인 → 승인 클릭)
                                                             │
                    Function URL → interaction Lambda (nodejs20)
                         · Ed25519 검증 실패 → 401   (Discord 가 가짜 서명으로 상시 테스트)
                         · user ID 허용목록 밖 → "권한 없음" 응답
                         · select menu → DynamoDB UPDATE snapshot
                         · 버튼 → DynamoDB GET → SendTaskSuccess({snapshot ?? latestSnapshot})
                                                             ↓
                                                     [2] ... [13]
```

---

## 5. 메인 상태 머신 `cledyu-dr-failover`

| # | 상태 | 주체 | 예상 | 내용 |
|---|---|---|---|---|
| 1 | RequestApproval | Lambda `.waitForTaskToken` | 사람 | 승인 + 스냅샷 선택. 24h 타임아웃 → NotifyAborted |
| 2 | TerraformApply | CodeBuild `.sync` | 2-3분 | **`-var` 3개 + `-target` 17개** → NAT·엔드포인트·bastion |
| 2.5 | ResolveBastion | SDK | ~5초 | `ec2:describeInstances`(Name 태그 + running) → `instanceId` + **stale SSM 파라미터 삭제** |
| 3 | **CleanWarmEtcd** | RunOnBastion | 2-3분 | cloud-init 대기 + **고아 ALB webhook 정리**(P1c) |
| 4 | ScaleNodes | SDK + 폴링 | 3-5분 | `update-nodegroup-config` 0→3 → `nodegroup-active` 폴링 |
| 5 | InstallAddons | Lambda | 2-3분 | coredns·ebs-csi 멱등(describe→create/update) → `addon-active` 폴링 |
| 6 | BootstrapApps | RunOnBastion | 5-10분 | git clone → argocd helm(멱등) → 가드 → root-app apply → 플랫폼 Ready |
| 7 | RestoreVault | RunOnBastion | ~5분 | 선택 스냅샷 복원 → generate-root → k8s auth 재설정 → **ESO rollout restart** |
| 8 | RestoreData | RunOnBastion | 10-15분 | CNPG 구 CR 가드 → `cledyu-pg-rw`·`keycloak-pg-rw` Ready |
| 9 | WaitAppsReady | RunOnBastion | 5-10분 | Kafka·validation-engine·**Keycloak Ready** → **ALB 호스트명을 SSM 파라미터에 기록** |
| 10 | SwitchDNS | Lambda | ~1분 | SSM 파라미터에서 ALB 취득(**없으면 실패**) → **WAF 확인** → api·app·auth UPSERT |
| 11 | RestartApps | RunOnBastion | ~2분 | api/web rollout restart → `/ready` **+ api 로그 db 모드 확인**(둘 다) |
| 12 | **VerifyServing** | RunOnBastion | ~2분 | realm 응답 + **복원 데이터 존재 확인**(무인증, §5.1.4) |
| 13 | NotifyComplete | Lambda | — | **RTO 2단 보고** + **`backupEnabled` flip PR 지시** + failback 런북 링크 |

**승인 이후 총 ~40-55분.**

> [12]·[13] 은 런북 `:370` 의 체크리스트 항목 "**검증(로컬 테스트유저 로그인·복원 데이터 서빙) + RTO 실측**"에
> 대응한다. 초안은 RTO 반쪽만 가져오고 검증을 통째로 빠뜨렸다(리뷰 지적). 검증이 없으면 [11] 의 게이트를 통과한
> 뒤에도 "복원된 데이터가 실제로 서빙되는가"를 아무도 확인하지 않은 채 완료 알림이 나간다.
>
> 검증은 bastion(kubectl·클러스터 내부)에서, 알림은 Lambda(Discord)에서 하므로 **두 상태로 분리**한다 —
> 초안은 "RunOnBastion → Lambda" 를 한 행에 적어 상태 수가 모호했다(적대적 리뷰 F5).

### 5.1.5 [13] 의 RTO 는 2단으로 보고한다 (적대적 리뷰 3회차 H4)

SFN 실행은 **알람 ALARM 시점**에 시작하고 [1] 은 승인을 **최대 24h** 기다린다. 실행 전체 시간을 "RTO" 로
보고하면 **사람이 자던 8시간이 섞여** 지표가 무의미해진다. 두 구간을 나눠 싣는다:

| 구간 | 의미 | 개선 주체 |
|---|---|---|
| 감지 → 승인 | 사람 지연(인지·판단·확인) | 온콜·알림 체계 |
| **승인 → 서빙**([12] 통과) | **자동화 RTO** | 이 설계 |

`감지` = SFN 실행 시작 시각(EventBridge), `승인` = interaction Lambda 의 `SendTaskSuccess` 시각
(DynamoDB item 에 함께 기록), `서빙` = [12] 성공 시각. 스펙 §5 의 "승인 이후 총 ~40-55분" 은
**두 번째 구간의 목표치**다.

### 5.1 순서가 강제된 지점 3곳

- **[3] → [5]**: 고아 ALB webhook 은 warm etcd 에 있고, API 서버는 노드 0 에도 상시 존재하므로 노드 없이 kubectl 로
  지울 수 있다. bastion 은 [2] 가 만든다. 이 정리는 **coredns 설치([5])보다 먼저** 끝나야 한다 — P1c 가 죽인 지점.
- **[7] 내부 ESO 재기동**: 런북 `dr-eks-bootstrap.md:177-181` 의 드릴 실측 — ESO 컨트롤러는 Vault 가 늦게(복원 후)
  살아나면 **실패한 provider client 를 캐시**해 store 를 `InvalidProviderConfig` 로 붙잡는다. k8s auth 재설정
  **직후 반드시** `kubectl -n external-secrets rollout restart` 로 캐시를 버린다. 이게 빠지면 이후 모든 Secret 이
  안 생겨 [8]·[9] 가 줄줄이 실패한다(초안 누락 — 리뷰 지적).
- **[9] → [10]**: 런북 명시 — "auth 는 Keycloak CR Ready(issuer 응답 가능) 이후에만 넘긴다. 그 전에 넘기면 ALB
  keycloak 타겟 unhealthy 로 404/503".

### 5.1.1 [2] TerraformApply 의 `-var` (리뷰 지적 — 초안 누락)

초안은 `-target` 만 적었으나, **`-var` 없이는 apply 가 destroy 가 된다.** `.gitignore:5` 가 `*.tfvars` 라
CodeBuild 체크아웃엔 tfvars 가 없고, DR 게이트 변수들이 기본값(`false`)으로 평가되어 `count = 0` → 기존 warm
스택까지 파괴 대상이 된다. 런북과 동일하게 넘긴다:

```bash
terraform apply -auto-approve \
  -var enable_eks_dr=true -var eks_dr_active=true -var eks_dr_node_desired=0 \
  -target=module.eks_dr_vpc ... (17개)
```

`eks_dr_node_desired=0` 은 런북과 동일 — 모듈이 desired 변경을 `ignore_changes` 하므로 실제 스케일은 [4] 가 CLI 로 한다.

**`default` 없는 변수는 0건**(`variables.tf` 실측) — 위 3개만 넘기면 CodeBuild 의 비대화형 terraform 이
값 프롬프트로 행 걸릴 일이 없다.

### 5.1.1a [2.5] ResolveBastion — instance id 를 CodeBuild 에서 받지 않는다 (적대적 리뷰 F4)

초안은 "[2] 가 bastion instance id 를 반환"이라고만 쓰고 **메커니즘을 정하지 않았다.** CodeBuild `.sync` 가
임의 값을 반환하려면 buildspec `exported-variables` 를 거쳐야 해 결합이 하나 는다.

→ **bastion Name 태그가 결정적**(`tags = merge(local.eks_dr_tags, { Name = "${local.eks_dr_name}-bastion" })`
→ `cledyu-dr-bastion`)이므로 조회로 끝낸다. 런북도 같은 경로를 제시한다(`:231` — "bastion instance id:
terraform output / `aws ec2 describe-instances Name=cledyu-dr-bastion`").

```
aws-sdk:ec2:describeInstances
  Filters: [{Name: "tag:Name", Values: ["cledyu-dr-bastion"]},
            {Name: "instance-state-name", Values: ["running"]}]
```

> **`instance-state-name=running` 필터는 필수다.** `aws_instance.eks_dr_bastion` 은
> `user_data_replace_on_change = true` 라 user_data 변경 시 교체되며, 직전 인스턴스가 `shutting-down`
> 상태로 잠시 남는다. 상태 필터가 없으면 **죽어가는 인스턴스의 id 를 집어** 이후 모든 SSM 단계가 실패한다.
> 결과가 0건이거나 2건 이상이면 즉시 Fail(모호한 대상에 명령을 쏘지 않는다).

### 5.1.2 [10] SwitchDNS 가 ALB 를 알아내는 법 (리뷰 지적 — 초안 누락)

초안은 [10] 을 non-VPC Lambda 로 두면서 **ALB 신원을 넘겨줄 경로를 만들지 않았다.** 런북은 ALB 호스트명을
`kubectl -n api get ingress api -o jsonpath='{.status.loadBalancer.ingress[0].hostname}'` 로 얻고 거기서
`ALB_ZONE`·`ALB_ARN` 을 파생하는데, Lambda 는 private EKS 에 닿지 못하고 §5.2 의 자식 SM 은 stdout 을 S3 로 버린다.

→ **[9] 의 스크립트 끝에서 bastion 이 ALB 호스트명을 SSM 파라미터에 쓴다:**

```bash
ALB=$(kubectl -n api get ingress api -o jsonpath='{.status.loadBalancer.ingress[0].hostname}')
[ -n "$ALB" ] || { echo "ALB 호스트명 비어있음 — Ingress 미프로비저닝"; exit 1; }
aws ssm put-parameter --name /cledyu-dr/failover/alb-hostname --type String --overwrite --value "$ALB"
```

[10] 은 이 파라미터를 읽고 `describe-load-balancers` 로 `CanonicalHostedZoneId`·`LoadBalancerArn` 을 파생한다
(런북과 동일한 유도). bastion 롤에 `ssm:PutParameter`(해당 경로만), dns-switch 롤에 `ssm:GetParameter` 추가.

> 태그 기반 discovery(`elbv2.k8s.aws/cluster=cledyu-dr`)도 가능하나, 태그 키가 ALB 컨트롤러 버전에 종속이고
> 미검증이라 채택하지 않는다 — 런북이 실제로 검증한 kubectl 유도를 그대로 쓰고 전달만 추가한다.

#### stale 파라미터 방어 — 이 전달 경로가 만드는 새 위험 (적대적 리뷰 F2)

SSM 파라미터는 **failover 사이클을 넘어 살아남는다.** [9] 가 쓰기 전에 죽거나 이전 드릴 값이 남아 있으면,
[10] 이 **이미 삭제된 ALB 의 호스트명**을 읽어 api·app·auth 를 그리로 UPSERT 한다 → DNS 가 존재하지 않는
대상을 가리켜 완전 장애. **이는 7/14 드릴이 발견한 P1d(stale hostAlias = 로테이션된 ALB IP)와 같은 버그
클래스**다 — stale 상태를 정리하는 설계가 새 stale 상태를 만들어선 안 된다.

→ 2중 방어(둘 다 fail-closed):

1. **[2.5] ResolveBastion 이 파라미터를 삭제한다** — `aws-sdk:ssm:deleteParameter`(없으면 무시).
   [9] 가 쓰기 전이므로 항상 비어 있는 상태로 진입한다.
2. **[10] 은 파라미터가 없으면 즉시 Fail** — 폴백·추측 금지. "[9] 가 안 썼다 = ALB 를 못 얻었다"이므로
   DNS 를 건드리지 않고 멈추는 것이 옳다(DNS 는 온프렘을 가리킨 채 남아 상태가 안전하다, §5.3).

> **왜 [3] 이 아니라 [2.5] 인가 (적대적 리뷰 2회차 G3).** 초안은 삭제를 [3] CleanWarmEtcd 에 뒀으나,
> [3] 은 **bastion 에서 SSM 으로 도는** 상태다 — 파라미터 삭제는 AWS API 호출이라 bastion 이 필요 없고,
> 거기 두면 (a) bastion 롤에 `ssm:DeleteParameter` 를 더 줘야 하고 (b) **가장 불안정한 상태**(bastion·SSM
> 에이전트 등록 의존)에 안전장치를 매달게 된다. [2.5] 는 SDK 상태라 SFN 롤 하나로 끝나고 bastion 과 무관하다.

### 5.1.3 [11] 의 성공 게이트는 두 개다 (리뷰 지적 — 초안이 절반으로 축소)

런북 `:430` 은 **두 가지**를 요구한다. 초안은 `/ready` 만 남겨 두었는데, `/ready` 에는 **db 체크가 없다** —
`apps/api/internal/api/handlers/health.go` 의 checks 맵은 labs/keycloak/kubevirt/validation 뿐이고,
`apps/api/cmd/server/main.go` 는 db 연결 실패 시 in-memory 로 **계속 서빙**한다. 따라서 `/ready` 만 보면
**영구 degraded api 가 200 을 뱉고 [12] 가 "DR 완료"를 선언**한다.

```bash
kubectl -n api rollout status deploy/api
curl -sf .../ready | grep keycloak=connected                   # 게이트 1

# 게이트 2 — DB 모드 확인. 부분매칭 금지(아래 ⚠️): 실패 거부 → 성공 전문 매치 순서.
LOG=$(kubectl -n api logs deploy/api --tail=200)
echo "$LOG" | grep -q "in-memory 전용" && { echo "❌ in-memory 폴백 — DB 미연결"; exit 1; }
echo "$LOG" | grep -q "db 연결 — 유저/진행 상태 영속화 활성" || { echo "❌ db 연결 성공 로그 없음"; exit 1; }
```

> **⚠️ 부분매칭 함정 (적대적 리뷰 2회차 G1).** 초안의 게이트는 `grep -q "db 연결"` 이었는데,
> 실제 로그가 `main.go:195` `"db 연결 **실패** — 진행 상태 영속화 비활성(in-memory 전용)"` /
> `main.go:199` `"db 연결 — 유저/진행 상태 영속화 활성"` 이라 **실패 문자열이 성공 패턴을 포함**한다 →
> **degraded api 가 그대로 통과**한다. 즉 리뷰 지적 #6("`/ready` 만 보면 degraded 가 통과")을 고치겠다며
> 같은 결함을 재도입한 것이다. 런북 원본이 `grep -E "db 연결|in-memory"` 인 것은 **사람에게 둘 다 보여
> 판단시키려는 용도**이며, 그대로 기계 게이트로 옮기면 안 된다 — 성공 문자열 전문 매치 + 실패 문자열 명시 거부.

### 5.1.4 [12] VerifyServing — 자격증명 없이 검증한다 (적대적 리뷰 3회차 H1)

런북 `:370` 의 "로컬 테스트유저 로그인·복원 데이터 서빙" 을 **자격증명 없이** 확인한다.

```bash
# (1) Keycloak + DNS + 복원된 realm — Route53 헬스체크와 동일 신호
curl -sf https://auth.cledyu.com/realms/cledyu-learn | grep -q cledyu-learn \
  || { echo "❌ realm 미응답 — Keycloak/DNS/복원 실패"; exit 1; }
# (2) 복원 데이터가 실제로 들어왔는가 — DB 직접
ROWS=$(kubectl -n postgres exec cledyu-pg-1 -- psql -tAc "SELECT count(*) FROM users")
[ "$ROWS" -gt 0 ] || { echo "❌ 복원본에 데이터 없음(users=0)"; exit 1; }
```

(1) 은 **이 레포가 이미 쓰는 신호**다 — `aws_route53_health_check.onprem_pull` 이 정확히 같은 엔드포인트를
`HTTPS_STR_MATCH`(search_string `cledyu-learn`)로 감시한다. 따라서 새 부품이 아니라 검증된 신호의 재사용이다.

> **왜 로그인 계정을 쓰지 않는가 (H1 — 2회차 G4 수정이 동작 불가였다).** G4 는 "bastion 이
> `vault kv get cledyu/dr/verify-user` 로 꺼낸다"고 했으나 **무슨 토큰으로 인증하는지가 없었다**:
> [7] 의 `$NEWROOT`(generate-root 산물)는 [7] 스크립트 안의 셸 변수이고, [12] 는 별도 SSM RunCommand =
> **새 셸**이며, §5.2 가 stdout 을 S3 로 버린다 → **전달 경로가 없다.** (§5.2 가 "bastion→Lambda 전달은
> 별도 경로 필요"라고 스스로 적어둔 그 함정에 다시 빠진 것이다.) 더구나 G4 는 realm 에 **password grant
> (direct access grants)가 켜져 있다**고 미확인 가정했다.
> 위 방식은 Vault 경로·Keycloak 전용 계정·ExternalSecret **세 부품을 모두 없애고** 그 가정도 제거한다.
> 트레이드오프: 전체 OIDC 로그인 플로우(브라우저 리다이렉트)는 검증하지 않는다 — 그것까지 자동화하려면
> 헤드리스 브라우저가 필요해 재해 경로에 둘 만한 것이 아니다. [11] 의 `/ready` `keycloak=connected` 와
> (1) 의 realm 응답으로 "Keycloak 이 서빙 가능한 상태"까지는 덮인다.

### 5.2 자식 상태 머신 `cledyu-dr-run-on-bastion`

```
WaitForSsmAgent  ─ ssm:describeInstanceInformation(InstanceId) → 등록됐나?
   │  ├ 아니오 → Wait 20s → 재시도 (최대 ~5분)
   │  └ 예 ↓
SendCommand(aws-sdk:ssm:sendCommand, InstanceIds=[instanceId], OutputS3BucketName=...)
   │  · Retry: InvalidInstanceId / InvalidInstanceInformationFilterValue → 20s 간격 15회
   ↓
Wait 30s → GetCommandInvocation → Choice
     ├ Pending/InProgress/Delayed → Wait 로 루프
     ├ Success                    → {status, responseCode, stdoutTail, stdoutUrl, stderrUrl} 반환
     └ Failed/TimedOut/Cancelled/Undeliverable → Fail(같은 필드 포함)
```

입력 `{instanceId, script, timeoutSeconds, label}`. 메인에서 `states:startExecution.sync` 로 호출.
`instanceId` 는 [2] TerraformApply 가 반환한 bastion instance id 를 메인 SM 이 계속 들고 다니며 넘긴다
(초안은 입력에 `instanceId` 가 없어 `sendCommand` 의 필수 파라미터를 만들 수 없었다 — 리뷰 지적).

**왜 자식 SM 인가:** Step Functions optimized integrations 표(AWS 공식 문서)에 SSM 이 없다. AWS SDK 통합으로만
호출 가능하고 거기엔 `.sync` 가 **Not supported** 다 — `ssm:sendCommand` 는 CommandId 만 즉시 반환하고
명령 완료를 기다리지 않는다. SSM 단계가 **6개**([3][6][7][8][9][11])라 폴링을 인라인하면 24개 상태가 되고,
통짜 스크립트로 합치면 실패 지점을 잃는다(재해 중 "Vault 복원에서 죽음" vs "Kafka 대기에서 죽음"은 대응이 갈림).

**SSM 에이전트 등록 레이스 (리뷰 지적 — 초안의 치명 결함):** `module.eks_dr_endpoints` 는 **s3/kms/sts 만**
만들고 ssm/ssmmessages/ec2messages 인터페이스 엔드포인트가 없다 → private 서브넷 bastion 의 SSM 에이전트는
**NAT 로 나가서 등록**해야 하는데, 그 NAT 는 [2] 의 같은 apply 에서 방금 생겼다. CodeBuild `.sync` 는 인스턴스가
`running` 이면 반환하지 종료를 기다리지 않으므로, [3] 이 곧바로 `sendCommand` 를 쏘면 **`InvalidInstanceId` 가
동기 예외로 터진다** — CommandId 가 안 나오니 Choice 브랜치도 Wait 루프도 타지 않는다. 초안엔 Retry 가 없어
**매 failover 가 [3] 에서 죽는다.** 위 `WaitForSsmAgent` 게이트 + SendCommand Retry 로 막는다.
(런북 `:280` 이 같은 창을 이미 기록한다 — "부팅 직후 NAT 준비 전이면 예전엔 kubectl 설치가 깨졌다".)

**SSM RunCommand 는 호출마다 새 쉘**이므로, 런북이 "동일한 bastion 쉘 세션"을 요구하는 블록들
(`REPO_ROOT` cwd 의존)은 반드시 **하나의 스크립트로 합쳐** 던진다.

**출력 절단 (AWS 공식 문서):** `GetCommandInvocation` 은 `StandardOutputContent` 를 **선두 24,000자**,
`StandardErrorContent` 를 **선두 8,000자**로 자른다. 전문은 `SendCommand` 에 `OutputS3BucketName` 을
지정했을 때만 `StandardOutputUrl`/`StandardErrorUrl` 로 받을 수 있다.

→ `SendCommand` 에 **`OutputS3BucketName` 을 지정**하고, 자식 SM 은 stdout 전문이 아니라
**`{status, responseCode, stdoutTail, stdoutUrl, stderrUrl}`** 만 반환한다(위 다이어그램과 동일 — 초안은
다이어그램이 `{stdout}` 반환이라 산문과 모순됐다. 리뷰 지적, 이 계약 하나만 유효).
Step Functions 상태 페이로드 상한이 256KB 라, 24,000자 stdout 을 6단계 누적하면 상한에 근접한다.
Discord 실패 알림([5.3])에는 `stdoutTail`(마지막 ~1,500자)과 S3 URL 을 싣는다 — 재해 중에 필요한 건
전문이 아니라 "어디서 죽었나"다.

**stdout 을 버리므로 bastion→Lambda 데이터 전달은 별도 경로가 필요하다** — [9]→[10] 의 ALB 호스트명이
유일한 사례이고, SSM 파라미터로 넘긴다(§5.1.2).

### 5.3 실패 처리 — 롤백하지 않는다

모든 상태에 `Catch` → **NotifyFailed**(실패 단계·stdout·SFN 실행 링크를 Discord 로) 후 **정지**.

재해 중엔 부분 완성이 0보다 낫다. [7] 에서 실패해도 EKS·노드·ArgoCD·플랫폼은 이미 떠 있고, 사람이 런북을 열어
그 지점부터 이어받는 게 가장 빠르다. 자동 롤백은 **사람이 손댈 발판까지 치워** 복구를 늦춘다.
DNS 는 아직 온프렘을 가리키므로 상태도 안전하다.

### 5.4 보안 표면 — Discord 버튼이 당기는 권한

이 설계는 **공개 인터넷의 HTTP 엔드포인트(Function URL) 뒤에 프로덕션 인프라 권한을 놓는다**. 표면을 명시해 둔다.

| 주체 | 필요 권한 | 왜 큰가 |
|---|---|---|
| **CodeBuild 실행 롤** | DR 스택 terraform apply — VPC/NAT/EC2/EKS/IAM/EIP 생성·수정 | 사실상 **DR 리소스 범위의 admin**. `-target` 은 terraform 인자일 뿐 IAM 경계가 아니다 |
| **bastion 롤** (기존) | EKS cluster admin, Vault 스냅샷 S3 read, KMS decrypt, `cledyu/vault/*` GetSecretValue | 이미 존재. 이 설계가 늘리지 않음 |
| `dns-switch` Lambda | route53 change-resource-record-sets, elbv2/wafv2 describe | 공개 DNS 전환 |

**Function URL 은 `AuthType: NONE` 이어야 한다 (적대적 리뷰 3회차 H5).** Discord 가 IAM 서명 없이 POST 하므로
공개 엔드포인트가 강제된다 → **Ed25519 서명 검증이 유일한 관문**이다. 더불어 **reserved concurrency 를 소수
(예: 5)로 제한**한다 — 공개 URL 이라 누구나 두들길 수 있고, 무제한이면 계정 Lambda 동시성을 소진해
같은 리전의 다른 Lambda(알림 포함)까지 굶길 수 있다. 승인은 초당 몇 건이면 충분하다.

#### AuthType=NONE 만으로는 부족하다 — InvokeFunction 도 열어야 한다 (실측 2026-07-15)

AWS 는 **2025-10 부터 Function URL 호출에 `lambda:InvokeFunctionUrl` 과 `lambda:InvokeFunction` 을 둘 다**
요구한다(공식 문서). 그런데 provider(aws v5.100.0)의 `aws_lambda_function_url` 은 구 동작대로
`InvokeFunctionUrl` statement **하나만** 자동 생성한다 → **403 Forbidden**(AccessDeniedException) + 로그
스트림 0건. 우리 코드의 401 조차 도달하지 못해 "서명 검증이 동작하는지"를 확인할 수 없다.

> **최종 리뷰(opus)가 이 가설을 세웠다가 "provider 가 둘 다 자동 추가(공식 문서 명시)"로 반증 처리했으나
> 그것이 틀렸다.** 리뷰어가 본 문서 예시는 2025-10 이후 갱신본(두 statement)이고 provider 는 그 전 동작에
> 머물러 있다 — 문서와 구현의 시차를 정적 검토로는 알 수 없었다.

**조건을 걸 수 없다 — 셋 다 막혀 있다(전부 실측):**

| 시도한 조건 | 결과 |
|---|---|
| `lambda:FunctionUrlAuthType` | AWS AddPermission **400 거부** — "only supported for `lambda:InvokeFunctionUrl` action" |
| `lambda:InvokedViaFunctionUrl`(AWS 권장) | **provider 미지원** — 오픈 이슈 hashicorp/terraform-provider-aws#44829. `aws_lambda_permission` 스키마에 인자 자체가 없다(스키마 덤프로 확인) |
| `principal_org_id` | 이 계정은 Organization **미소속** |

→ **조건 없이 `principal = "*"` 로 연다(감수):** 아무 AWS 계정이나 이 함수를 직접 Invoke 할 수 있고,
이 레포가 PUBLIC 이라 ARN(계정ID·함수명)이 사실상 공개다. 그럼에도 감수하는 근거:
1. **로직은 안전** — 서명 검증이 코드 안에 있어 헤더 없는 호출은 401 로 떨어진다
2. **실피해는 동시성(5) 소진 → 승인 버튼 429** 뿐이고, 그때도 **CLI 우회**(`aws stepfunctions send-task-success`)로
   DR 자체는 진행된다 → **이 우회를 런북에 반드시 명시한다. 그게 이 결정의 안전망이다**
3. provider 가 #44829 를 구현하면 `invoked_via_function_url = true` 를 얹어 이 노출을 제거한다

방어는 3겹이다:

1. **Ed25519 서명 검증** — Discord 가 보낸 요청인지. 강제사항이라 미구현 시 엔드포인트가 등록 해제됨
2. **사용자 ID 허용목록** — 누가 눌렀는지. **`dr_approver_ids` 는 `variables.tf` 의 `default` 로 커밋**한다 —
   tfvars 에 두면 `.gitignore:5` 의 `*.tfvars` 에 걸려 git 밖으로 나가고, "승인자 변경이 코드 리뷰를 거친다"는
   성질이 사라진다(초안이 tfvars 를 전제하면서 코드 리뷰를 주장해 모순이었다 — 리뷰 지적).

   > **트레이드오프 (적대적 리뷰 F6):** 이 레포는 **PUBLIC** 이라 커밋하면 승인자 목록이 전 세계에 공개된다 —
   > "이 Discord 계정들을 탈취하면 프로덕션 페일오버가 된다"는 표적 정보가 된다. 그럼에도 커밋을 택하는 이유는
   > (a) Discord user ID 는 서버 멤버에게 이미 보이는 공개 식별자이고, (b) tfvars 는 승인자 변경에서 코드 리뷰를
   > 없애며, (c) 실질 방어선은 목록의 비밀성이 아니라 **승인자 계정 2FA** 이기 때문이다. 초안은 이 비용을 적지
   > 않고 "공개 식별자이므로 무방"이라고만 썼다.
3. **`dr_orchestration_armed`** — EventBridge 규칙 자체의 존재 게이트(§7.4)

**남는 리스크:** 서명 검증과 허용목록을 모두 통과한 요청은 곧바로 CodeBuild 를 당긴다. 즉 **승인자 Discord 계정이
탈취되면 DR 페일오버가 트리거된다.** 실피해는 "멀쩡한 온프렘을 두고 DR 이 뜨고 DNS 가 넘어감" — failback 으로
복구 가능하나 비싸다(write-downtime 창 + drEpoch 2사이클). 승인자 계정 2FA 를 전제로 한다.

CodeBuild 롤의 권한은 좁힐 여지가 있으나(리소스 태그 조건 등), 단기 프로젝트 범위에서는 **위 3겹 + 2FA** 로 두고
좁히지 않는다.

---

## 6. 런북 검증 결과 (2026-07-15 실측)

### 6.1 통과 — 자동화가 그대로 옮겨도 되는 부분

| 런북 주장 | 실측 |
|---|---|
| `-target` 17개 | 전부 실재 |
| coredns·ebs-csi 가 `cluster_addons` 에서 빠짐 | kube-proxy·vpc-cni 만 존재 (`eks-dr.tf:103-106`) |
| 클러스터명 `cledyu-dr` 하드코딩 | `local.eks_dr_name = "cledyu-dr"` |
| IRSA 롤명 결정적(`cledyu-dr-ebs-csi`) | `role_name = "${local.eks_dr_name}-ebs-csi"` |
| bastion user_data 가 kubectl/git/helm/aws/jq 설치 | egress 대기 루프 + `--retry` 확인 |
| placeholder·브랜치핀 가드 | 둘 다 현재 no-op(잔존 없음) |
| `aws_route53_record.public` terraform 관리 | `public-ingress.tf:263` |
| bastion 롤이 EKS cluster admin | `eks-dr.tf:83-93` `access_entries` |
| 레포 private 여부 | **PUBLIC** → `git clone` 에 PAT 불요 (런북 조건부는 무시 가능) |

### 6.2 [중대] 2026-07-14 드릴 발견이 런북에 미반영

7/14 풀 E2E 드릴에서 warm-etcd 교차오염 2건이 나왔으나 런북에 한 건도 없다
(`webhook|hostAlias|coredns.*CREATE_FAILED` grep 0건. 런북 최종 수정 `eb47d51` 은 "ec2 세션 authkey"로 무관).

- **[P1c] 고아 ALB webhook → coredns CREATE_FAILED** — 자동화 [5] 를 직격한다. warm etcd 는 사이클 간
  살아남으므로 **반복 failover 가 정확히 이 지점에서 깨진다**. → [3] CleanWarmEtcd 신설로 대응.
- **[P1d] stale hostAlias(로테이션된 ALB IP) → api OIDC 10초 hang** — `git log -S hostAliases -- gitops/apps/api/`
  0건 → git 이 아니라 **warm etcd 런타임 잔존물**. → [3] 에서 함께 정리.

### 6.3 WAF ARN 하드코딩 — 확인 스텝으로 흡수(고치지 않음)

`gitops/apps/api/values-eks.yaml:43`·`web/values-eks.yaml:19` 에 계정 ID·webacl UUID 가 박혀 있다.
단기 프로젝트라 WAF 재생성 예정이 없어 값 자체는 두되, [10] 에 런북이 이미 시키는 확인을 **필수 게이트**로 넣는다:

```bash
aws wafv2 get-web-acl-for-resource --resource-arn "$ALB_ARN" --query "WebACL.Name" --output text
# → cledyu-lab-public 아니면 실패 처리 (조용히 /metrics 가 열리는 것을 방지)
```

### 6.4 런북 반영 (같은 PR)

자동화만 고치면 수동 경로가 깨진 채 남는다.

- CleanWarmEtcd 스텝 신설 (P1c) — `[P1b]` CNPG 가드와 같은 성격·같은 자리
- P1d 원인·조치 기록
- WAF 확인을 선택이 아니라 필수 게이트로

---

## 7. 검증 전략

### 7.1 정적 (커밋 전, 매번)
`terraform validate` + `fmt -check`, ASL 문법, Lambda 린트, **`-target` 17개 실재 확인 스크립트**.
배선 오타만 잡는다.

### 7.2 승인 경로 부분 검증 (과금 ~0)

**⚠️ 유효 서명은 우리가 만들 수 없다 (적대적 리뷰 F3).** Discord 가 개인키를 보유하고 앱에는 **공개키만** 준다.
따라서 "합성 payload + 유효 서명" 테스트는 **물리적으로 불가능**하다(초안이 그 표를 검증 없이 적었다).
유효 서명 경로를 실제로 통과시키는 방법은 두 가지뿐 — Discord 의 **PING** 과 **진짜 클릭**.

**(a) 잘못된 서명 → 401** (로컬에서 가능)
쓰레기 서명 헤더로 Function URL 에 POST → 401 확인. **이게 엔드포인트 등록 유지의 조건**이다
(Discord 가 상시 가짜 서명으로 테스트하고, 실패하면 URL 을 등록 해제한다).

**(b) Discord PING → PONG** (엔드포인트 등록 시 1회, 유효 서명 경로의 유일한 자동 검증)
Developer Portal 에 Interactions Endpoint URL 을 저장하면 Discord 가 `type: 1` PING 을 **유효 서명과 함께**
보낸다. `type: 1` PONG 으로 응답해야 저장이 성공한다 — 저장에 성공했다는 사실 자체가 서명 검증 통과의 증거다.

**(c) 승인 로직 — 테스트 전용 상태 머신 `cledyu-dr-approval-test`**
허용목록·드롭다운·버튼은 **진짜 Discord 클릭**으로만 검증된다. 그런데 진짜 클릭은 `SendTaskSuccess` 를 발생시켜
메인 SM 이라면 [2] 가 시작된다(=과금·인프라 기동). → **`RequestApproval(진짜 Lambda, .waitForTaskToken) → Succeed`**
두 상태짜리 테스트 SM 을 둔다.

| 케이스 | 기대 |
|---|---|
| 허용목록 안 user 가 승인 | 테스트 SM 이 `Succeeded`, 출력에 선택된 snapshot |
| 허용목록 **밖** user 가 승인 | "권한 없음" 응답, SM 은 계속 대기 |
| select menu → 버튼 | 출력 snapshot = 드롭다운에서 고른 값 |
| 버튼만(드롭다운 미조작) | 출력 snapshot = `latestSnapshot` |
| 드롭다운 렌더 | 25개 상한·기본값=최신·최신순 정렬 육안 확인 |

> **§7.3 의 "드릴 전용 코드 경로 금지"에 걸리지 않는다.** 금지의 취지는 *프로덕션 경로가 드릴에서 다르게
> 도는 것*이다. 이 테스트 SM 은 approval-request Lambda·interaction Lambda·DynamoDB·Discord 앱을 **전부 진짜로**
> 쓰고 하류만 없다 — 프로덕션 SM 의 동작을 바꾸지 않는 별도 하네스다.

**⚠️ 테스트 버튼이 운영 채널에 진짜처럼 뜬다 (적대적 리뷰 2회차 G2).** 테스트 SM 이 진짜 approval-request
Lambda 를 쓰므로 **같은 채널에 같은 모양의 승인 버튼**이 뜬다. (a) 테스트 중 진짜 재해 알림이 오면 구분이 안 되고,
(b) 테스트가 끝나도 버튼이 채널에 남아 **DynamoDB TTL 24h 동안 살아 있는 토큰**으로 클릭 가능하다.

→ 두 가지를 함께 넣는다:

1. **테스트 표식** — SFN 실행 입력에 `mode: "test"` 를 실어 [1] 이 제목을 `🧪 [테스트] …` 로 렌더하고
   버튼 라벨도 구분되게 한다.

   > **⚠️ fail-safe 를 명시적으로 박는다 (적대적 리뷰 3회차 H3).** 이 필드는 **메시지의 긴급도를 바꾸는
   > 스위치**라, 잘못 들어가면 진짜 재해 알림이 `🧪 [테스트]` 로 떠서 무시된다 — 알림 체계 전체를
   > 무력화하는 최악이다. 따라서 **`mode` 가 정확히 문자열 `"test"` 일 때만** 테스트로 렌더하고,
   > 그 외 모든 경우(필드 없음·null·오타·타입 불일치)는 **실재해로 렌더**한다. 프로덕션 경로는
   > 이 필드를 넣지 않으므로 항상 실재해다.
2. **승인 후 버튼 비활성화** — interaction Lambda 가 `SendTaskSuccess` 직후 원본 메시지를 편집해
   버튼을 `disabled: true` 로 바꾸고 "✅ <user> 가 승인함 · <시각>" 을 남긴다.
   **이건 테스트뿐 아니라 실제 승인에도 필요하다** — 초안은 승인 후에도 버튼이 계속 클릭 가능해
   재클릭 시 `TaskAlreadyCompleted`(무해하나 혼란)이고, 무엇보다 채널에 "누가 언제 승인했는지" 기록이 남지 않는다.

**Discord 배선 디버깅은 여기서 종결.** 메인 SM 의 실제 핸드오프([1]→[2])는 전체 드릴(§7.3)에서 검증된다.

### 7.3 전체 드릴 (필수 게이트, 과금)
[2]~[13] 은 실제로 굴려야만 검증된다. 특히 P1c 정리가 coredns 를 살리는지는 warm etcd 에 고아 webhook 이
있어야 재현되므로 정적으로 알 수 없다.

**드릴 전용 코드 경로를 만들지 않는다.** DNS 전환([10])을 드릴에서 건너뛰면 가장 위험한 단계가 영구 미검증으로
남는다(7/14 드릴은 실 Route53 으로 수행했다). "드릴 때만 다르게 도는 코드"는 그 다른 부분에서 실재해 때 터진다.

### 7.4 무장 게이트

**`dr_orchestration_armed`**(기본 false). false 면 EventBridge 규칙이 **생성되지 않아**(count=0) 배포만으로
자동 페일오버가 무장되지 않는다. 배선 확인 후 true 로 재apply.

**⚠️ `dr_detection_armed` 와 "동일 패턴"이 아니다 (리뷰 지적 — 초안의 거짓 주장).** 둘은 서로 다른 메커니즘을 막는다:

| | 무엇을 막나 | 복합알람이 ALARM 되면 |
|---|---|---|
| `dr_detection_armed=false` | `actions_enabled=false` → **알람 액션(SNS 발행)만** 억제 | CloudWatch 는 여전히 평가·전이하고 **state-change 이벤트를 EventBridge 기본 버스로 계속 쏜다** |
| `dr_orchestration_armed=false` | EventBridge 규칙 자체를 미생성 | 이벤트가 나가도 받는 규칙이 없음 |

즉 **감지를 무장 해제해도 오케스트레이션은 안 꺼진다.** `dr_detection_armed=false`(문서상 안전 상태) +
`dr_orchestration_armed=true` 인 bring-up 창에서 하트비트 동기화 지연으로 복합알람이 ALARM 이 되면 —
Discord **알림은 안 뜨는데 승인 버튼은 뜬다.** 감지를 껐다고 믿는 운영자에게 원클릭 프로덕션 페일오버가 제시된다.

→ **EventBridge 규칙을 세 조건의 AND 로 게이트한다:**

```hcl
count = (local.pub == 1 && var.dr_detection_armed && var.dr_orchestration_armed) ? 1 : 0
```

이러면 `dr_detection_armed` 가 두 경로 공통의 마스터 스위치가 되어, 기존 무장 절차·런북의 의미가 보존된다.
`dr_orchestration_armed` 는 그 안에서 오케스트레이션만 따로 끄는 하위 스위치가 된다.

**`local.pub` 이 반드시 들어가야 한다 (적대적 리뷰 F1).** 복합알람은 `count = local.pub`
(`dr-detection.tf:120`)이라 `enable_public_ingress=false` 면 **존재하지 않는다.** 규칙의 `event_pattern` 이
`aws_cloudwatch_composite_alarm.disaster[0].alarm_name` 을 참조하므로, pub 게이트가 없으면 그 인덱스가
없어 **terraform 이 apply 전체를 중단**한다. 이는 이 브랜치의 `e68064b`("감지 스택 enable_public_ingress
count 게이트 — precondition 전체중단 제거")가 방금 제거한 실패 모드와 **정확히 같은 클래스**다 — 감지 리소스는
공개 진입점과 생사를 같이하고, 오케스트레이션은 그 감지에 의존하므로 셋이 함께 게이트되어야 한다.

승인 게이트가 있어 오발의 실피해는 Discord 메시지 한 장이지만, bring-up 중 그 메시지가 반복되면 알림 피로가
생기고 그것이 진짜 재해 때의 무시로 이어진다.

### 7.5 비용
드릴 1회 = NAT·엔드포인트·bastion·노드 3대를 두어 시간. Plan B 드릴과 동일 수준.
컨트롤플레인(~$73/mo)은 warm 으로 이미 상시 지출 중.

---

## 8. failback 을 자동화하지 않는 이유

`docs/RUNBOOK/dr-failback.md` 첫 줄: "**자동 failback 없음** — 각 스텝 수동 승인."

1. **승인 게이트가 6개**이고 각각이 실제 중단 조건이다. 특히 4번 "데이터 정합 체크 — 불일치 시 cutover 중단"은
   사람이 봐야 하는 판단이다. 자동화하려면 정합 판정 로직을 새로 만들어야 하고, 그 로직이 틀리면 **데이터가
   영구 분기**한다.
2. **git 커밋 3회** 필요 — path-swap, 6개 values `drEpoch` N→N+1 lockstep bump, path-swap 원복.
   재해 경로에 GitOps 레포 write 토큰을 굴리게 되며, 유출 시 승인 버튼보다 훨씬 큰 권한이 넘어간다.
3. **이득이 정반대다.** failover 는 급하고 이미 바닥이라 실패해도 잃을 게 없다. failback 은 급하지 않고
   실패하면 **멀쩡히 돌던 서비스를 깬다**.
4. **실 DR 검증 이력이 없다** — 격리 드릴로 부품만 검증됨. 검증 안 된 절차의 자동화는 순서가 거꾸로다.
   failover 가 자동화 가능한 것은 Plan B 드릴이 "무엇을 어떻게"를 이미 다 밝혔기 때문이다.

[13] NotifyComplete 가 failback 런북 **링크**를 보낸다(버튼이 아니라).

### 8.1 `backupEnabled` — 토글 유지 + 소유자 명시

`postgres-cnpg-dr/values.yaml:20`·`keycloak-pg-dr/values.yaml` 의 한 줄. `false` 면 `cluster.yaml:63` 의
`{{- if .Values.backupEnabled }}` 가 **`backup:` 스탠자 전체를 지운다** → DR 클러스터가 자기 쓰기를 S3
(`postgres-dr/cledyu-pg-dr-e{N+1}`)에 아카이빙하지 않는다.

#### 초안의 결함 (리뷰 지적 — CONFIRMED)

초안은 "flip 은 failback 사이클의 일부이므로 failback 런북 첫 단계로 묶는다"고 했으나 **인용한 근거가 결론의
반대를 증명하고 있었다**:

- `dr-failback.md:5` — "전제: failover 시 real-DR 캡처(**dr-eks-bootstrap.md §real-DR**)로 `-dr/` 에 base+WAL 축적됨"
- 값 표(`:11`) — DR 차트 **진입값 = `backupEnabled=true`**
- step 5(`:97`) — `true → false` 로 **되돌리기만** 한다

즉 failback 은 **이미 켜져 있음을 전제**할 뿐 켜지 않는다. 켜는 자리는 `dr-eks-bootstrap.md:344-353`
(**failover 쪽 real-DR 스텝**, "anchor 도달 확인 … 반드시 게이트")뿐인데 13단계가 그 섹션을 건너뛴다.
→ **어느 경로에도 소유자가 없다.** 실피해: real DR 이 `backupEnabled=false` 로 돌고, failback step 2 의
`failback-cutover` Backup CR 이 `spec.backup` 부재로 completed 되지 못해 `--timeout=900s` 를 소진한다 —
그것도 **step 1 이 이미 `api`·Keycloak 을 scale-0 한 write-downtime 한복판에서**.

#### 토글 제거를 검토했다 철회 (2026-07-15)

"토글을 없애고 항상 `true`" 를 검토했으나 **하면 안 된다**:

1. **`drEpoch` 는 두 일을 겸한다** — 복원 소스 `f(N)`(온프렘이 쓴 경로, lockstep)과 자기 아카이브 `f_dr(N+1)`.
   드릴이 `drEpoch` 를 올리면 복원 소스가 `cledyu-pg-e1`(존재하지 않음)로 바뀌어 **드릴이 복원 자체를 못 한다.**
   "드릴이 epoch 를 올리면 된다"는 착상은 성립하지 않는다.
2. **토글은 failover 보호 장치다.** `values.yaml:18` 의 실측(2026-07-13)이 막는 건 백업이 아니라 **recovery** —
   경로가 더러우면 **DR 클러스터가 아예 안 뜬다**. 항상 켜면 경로 충돌이 "failback 블로커"에서
   **"failover 블로커"로 승격**된다(재해 났는데 DR 이 안 올라옴). 지금 `false` 는 아카이브 상태와 무관하게
   DR 이 뜨는 것을 보장한다 — 재해 경로에선 옳은 성질이다.
3. **대안도 다 막혀 있다.** 카운터로 분리(`drInstance`) → 사람이 드릴마다 bump, 깜빡하면 이제 failover 가 죽는다.
   드릴 전 아카이브 삭제 → Object Lock 이 GOVERNANCE(30d)라 `s3:BypassGovernanceRetention` 으로 뚫을 수는
   있으나(현 IAM 엔 delete·bypass 둘 다 없음), "failover→failback 없이 DR 사망→재failover" 순서에선 경로에
   **진짜 DR-창 데이터**가 있어 무턱대고 지우면 그걸 날린다. 동적 값(타임스탬프·랜덤) → ArgoCD sync 마다
   재렌더 → 영구 OutOfSync.

**git 에서 오는 안정적인 값이어야 하고, 그 값은 결국 사람이 정한다** — 구조적으로 토글을 없앨 수 없다.
진짜 결함은 토글의 존재가 아니라 **소유자 부재** 하나다.

#### 결정 — 소유자를 두 군데서 명시 (자동화하지 않음)

1. **[13] NotifyComplete** 가 완료 알림에 **"지금 이 PR 을 올리세요"** 를 링크와 함께 명시한다.
2. **`dr-failback.md` 에 step 0 게이트 신설** — "`postgres-cnpg-dr`·`keycloak-pg-dr` values 의
   `backupEnabled=true` 확인. `false` 면 **여기서 flip·커밋·sync 완료 후** 진행."
   **step 1(서비스 quiesce) 앞**이라, 누락 시 **서비스가 살아 있는 동안** 발견된다 — 초안이 만든
   "서비스 내려놓고 막히는" 최악을 제거한다.

**왜 자동화하지 않는가:** 13단계 안에 넣으려면 재해 중 main 에 push 할 GitHub 자격이 필요하다. PAT 만료 landmine 은
GitHub App(1시간 설치 토큰)으로 풀리지만, 재해 중 push 는 fetch·rebase·충돌 처리·ArgoCD sync 대기까지 자동화가
떠안아야 하고 **그게 한 줄 값을 위해서다.** 게다가 온프렘 앱들도 같은 `repoURL`·`main` 을 sync 하므로
(`gitops/argocd/apps/*.yaml`), main push 권한의 폭발 반경은 **살아 있는 운영 클러스터까지** 닿는다 —
bastion 의 EKS admin(DR 한정)보다 넓다. 그리고 이 flip 은 **RTO 경로 밖**이다.

**타이밍:** flip 시 ScheduledBackup(immediate) 이 그 시점의 전체 상태로 base backup 을 뜨므로 늦게 켜도
failback 은 성립한다(DR 디스크에 데이터가 살아 있는 한). 진짜 손실은 **flip 전에 DR 자체가 죽는 경우**뿐이고,
본 프로젝트는 **DR 운영 1~2일 전제**라 노출 창이 짧다.

---

### 8.2 알려진 한계 — Vault k8s auth 는 ~1시간 후 만료된다 (적대적 리뷰 3회차 H2)

`dr-eks-bootstrap.md:189-191` 이 기록한다:

> `token_reviewer_jwt` 는 파드 projected 토큰(**~1h 만료**). 재설정 직후 1회 sync 로 `cledyu-api-oidc`(Retain)
> 생성되므로 **드릴엔 무해**(이후 만료로 재-sync 가 막혀도 시크릿은 잔존). **장기 운영이면 비만료 Secret 기반
> reviewer 토큰 필요**(2026-07-04 인시던트).

**런북은 드릴(두어 시간)을 전제로 쓰였다. 이 설계의 전제는 DR 운영 1~2일이다(§8.1).**
자동화가 실재해를 상시 경로로 만들면서 **드릴 가정을 그대로 상속**했다 — 자동화가 아니었으면 드러나지 않았을
가정 불일치다.

**실제 거동:** [7] 의 k8s auth 재설정 후 ~1시간이 지나면 Vault 가 EKS 의 ServiceAccount 토큰을 검증하지 못해
**ESO 가 새 시크릿을 sync 하지 못한다.** 기존 시크릿은 Retain 이라 남으므로 **돌던 앱은 계속 돈다** —
DR 이 1시간 만에 죽는 것이 아니다. 문제는 **조용히** 망가진다는 점이다(refreshError 만 쌓인다).

**실피해:** DR 창(1~2일) 동안 시크릿 관련 문제가 생기면 ESO 경로로 복구할 수 없다.

**결정(2026-07-15): 한계로 명시만 하고 이 스펙에서 고치지 않는다.**
- 확률이 낮고, 발생해도 bastion(cluster admin + Vault 복원 주체)이 있어 수동 우회가 된다.
- 근본 수정(비만료 Secret 기반 reviewer 토큰)은 **Vault 부트스트랩을 건드리는** 변경이라
  2026-07-14 에 겨우 통과한 드릴을 다시 돌려야 한다 — DR 자동화와 독립된 별건이다.
- **후속 이슈로 분리한다.** 런북이 이미 해법을 적어두었다.
- DR 이 예상보다 길어지면(3일+) 이 한계를 먼저 해소할 것 — [13] 완료 알림에 이 문서 링크를 포함한다.

---

## 9. 선행 작업 (사용자)

1. **Discord Application 등록** — Developer Portal → Public Key 확보 → Interactions Endpoint URL 설정.
   현재의 outbound 웹훅으로는 버튼 클릭을 수신할 수 없다. Public Key 는 Secrets Manager 에 저장한다.
   (URL 저장 시 Discord 가 PING 을 보내므로 interaction Lambda 배포가 선행되어야 한다 — §7.2(b).)
2. **Bot 생성 + 서버 초대** (§3.6 — 승인 메시지는 웹훅으로 못 보낸다):
   - 같은 Application → **Bot** 탭 → **Reset Token** 으로 토큰 발급(생성 시 1회만 보이므로 Reset 이 정상 절차)
   - **OAuth2 → URL Generator** → scope `bot` + permission **Send Messages** → 생성된 URL 로 서버에 초대
   - 토큰은 `${var.name_prefix}-dr-discord-bot-token` 시크릿에 주입(TF 밖에서 `file://` 로 — shell history·`ps` 노출 회피)
   - 승인 채널 ID → `dr_discord_channel_id`
3. **승인자 Discord user ID 수집** → `dr_approver_ids` 기본값(§5.4). 계정 2FA 필수(§5.4 잔여 리스크).

> 3번(“DR 검증 전용 Keycloak 계정”)은 **삭제됨** — [12] 를 무인증 검증으로 재설계해 불필요해졌다(§5.1.4, H1).

---

## 10. 산출물

```
infra/terraform/aws/
  dr-orchestration.tf              # EventBridge 규칙(AND 게이트), SFN 2개, CodeBuild, DynamoDB, IAM
  dr-orchestration-lambda/
    failover-trigger/index.py      # us-east-1
    approval-request/index.py
    interaction/index.mjs          # nodejs20 (Ed25519)
    addon-install/index.py
    dns-switch/index.py            # SSM 파라미터에서 ALB 취득
    notify/index.py
  dr-failover-buildspec.yml        # CodeBuild terraform apply (-var 3개 + -target 17개)
  scripts/bastion/                 # SSM 로 던지는 스크립트 ([3][6][7][8][9][11] — 6개)
  variables.tf                     # dr_orchestration_armed, dr_approver_ids(default 로 커밋)
  README.md                        # terraform_docs 재생성 필수 (pre-commit 훅)

docs/RUNBOOK/
  dr-eks-bootstrap.md              # CleanWarmEtcd 스텝 신설, P1d 기록, WAF 확인 필수화
  dr-detection.md                  # "자동 오케스트레이션 없음" → 승인 게이트 흐름으로 갱신
  dr-failback.md                   # step 0 게이트 신설 (backupEnabled 확인 — §8.1)

.gitignore                         # Lambda 빌드 산출물 규칙 확장 — 현재 :13 이
                                   # infra/terraform/aws/dr-alert-lambda/dr-alert.zip 단일 경로
                                   # 하드코딩이라, 신규 Lambda 6개의 zip 이 커밋될 수 있다(리뷰 지적)
```

**§10 에 `dr-failback.md` 가 빠져 있던 것이 §8.1 결함의 직접 원인이었다** — "failback 첫 단계로 묶는다"고
해놓고 그 파일을 산출물에 넣지 않아, 제안한 자리가 애초에 생기지 않았다(리뷰 지적).

---

## 11. 리뷰 반영 이력

2026-07-15 `/code-review high` (finder 4 + verifier 21) — 확인된 결함 16건 반영:

| # | 결함 | 반영 |
|---|---|---|
| 1 | [2] `-var` 누락 → apply 가 destroy | §5.1.1 |
| 2 | [3] SSM 등록 레이스(`InvalidInstanceId`, Retry 없음) | §5.2 `WaitForSsmAgent` + Retry |
| 3 | [10] ALB 신원 전달 경로 없음 | §5.1.2 SSM 파라미터 |
| 4 | §8.1 `backupEnabled` 소유자 부재 | §8.1 전면 재작성 (토글 유지 + 2중 명시) |
| 5 | §7.4 `armed` 두 플래그 "동일 패턴" 거짓 | §7.4 AND 게이트 |
| 6 | [11] 게이트 절반 축소(`/ready` 엔 db 체크 없음) | §5.1.3 |
| 7 | [12] 검증 항목 누락(로그인·서빙) | §5 상태표 [12] |
| 8 | 자식 SM 입력에 `instanceId` 없음 | §5.2 |
| 9 | DynamoDB 에 드롭다운 선택 미저장 | §3.5 |
| 10 | §5.2 자기모순(다이어그램 `{stdout}` vs 산문) | §5.2 |
| 11 | §5.2 "SSM 5개" → 실제 6개 | §5.2 |
| 12 | `dr_approver_ids` tfvars → 코드리뷰 안 거침 | §5.4 (`variables.tf` default) |
| 13 | [7] ESO rollout restart 누락 | §5.1 |
| 14 | Node crypto "raw-public 포맷" 근거 없음 | §3.4 |
| 15 | `.gitignore` Lambda zip 단일 경로 하드코딩 | §10 |
| 16 | §7.2 부분 검증이 사람↔SFN 레이스 | §7.2 재설계 |

미반영 1건(REFUTED): §3.1 "노드/애드온 CLI" 관련 지적은 §5 상태표가 이미 폴링을 명시해 반증됨.

### 11.1 적대적 리뷰 (2026-07-15, 위 수정 자체를 대상)

위 16건을 고친 결과물을 다시 적대적으로 검토해 **수정이 만든 새 결함 6건**을 잡았다.
**F1·F2 는 이 레포가 이미 배운 교훈을 재도입한 것**이라 특히 무겁다.

| # | 결함 | 반영 |
|---|---|---|
| F1 | §7.4 AND 게이트가 `local.pub` 누락 → 복합알람 `[0]` 참조 불가 시 **apply 전체 중단**. `e68064b` 가 방금 제거한 실패 모드와 동일 클래스 | §7.4 3중 AND |
| F2 | 새로 도입한 SSM 파라미터가 사이클 간 잔존 → [10] 이 **삭제된 ALB 로 DNS UPSERT**. 7/14 드릴의 P1d 와 같은 버그 클래스 | §5.1.2 2중 fail-closed ([3] 삭제 + [10] 부재 시 Fail) |
| F3 | §7.2 의 "유효 서명 합성" 테스트가 **물리적으로 불가능**(Discord 가 개인키 보유, 앱엔 공개키만) | §7.2 재설계 (401 / PING / 테스트 SM) |
| F4 | [2] 의 instance id 반환 메커니즘 미명시 | §5.1.1a `describeInstances`(Name 태그 + **running 필터**) |
| F5 | [12] 를 "RunOnBastion → Lambda" 한 행에 적어 상태 수 모호 | [12] VerifyServing / [13] NotifyComplete 분리 → **총 13단계** |
| F6 | `dr_approver_ids` 커밋의 비용(PUBLIC 레포 = 승인자 목록 공개) 미기재 | §5.4 트레이드오프 명시 |

정적 확인 통과: `variables.tf` 에 `default` 없는 변수 **0건**(CodeBuild 비대화형 terraform 안전),
Discord PING/PONG 등록 흐름·`custom_id` 100자·select 25개 재확인, interaction Lambda 3초 응답 한도 여유.

### 11.2 적대적 리뷰 2회차 (2026-07-15, F1~F6 수정을 대상)

| # | 결함 | 반영 |
|---|---|---|
| **G1** | §5.1.3 의 `grep -q "db 연결"` 이 **실패 로그 `"db 연결 실패 …"` 에도 매치** → degraded api 통과. **리뷰 #6 을 고치겠다며 같은 결함을 재도입** | §5.1.3 성공 전문 매치 + 실패 명시 거부 |
| G2 | 테스트 SM 이 운영 채널에 진짜 같은 승인 버튼 게시, TTL 24h 동안 클릭 가능 | §7.2 `mode:"test"` 표식 + **승인 후 버튼 비활성화**(실승인에도 필요) |
| G3 | SSM 파라미터 삭제를 [3](bastion 의존, 최불안정)에 배치 | [2.5](SDK)로 이동 — bastion IAM·의존 불요 |
| G4 | [12] 의 테스트 계정 출처 미명시 | ~~Vault `cledyu/dr/verify-user` + §9 선행 작업~~ → **3회차 H1 에서 동작 불가로 판명, 무인증 검증으로 대체**(§5.1.4) |

반증 2건(스펙이 옳았음): **ALB 단일 가정** — api·web `values-eks` 와 keycloak `values.yaml:28` 이 모두
`group.name: cledyu-dr` 라 IngressGroup 으로 한 ALB 공유 → [9] 의 api Ingress 유도로 api·app·auth 전부 유효.
**Name 태그 충돌** — proxy 는 `cledyu-lab-kc-proxy`, bastion 은 `cledyu-dr-bastion` 으로 겹치지 않음.

> **G1 의 교훈:** 사람이 읽고 판단하라고 만든 런북의 `grep -E "A|B"` 를 **기계 게이트로 옮길 때는 부분매칭을
> 반드시 재검토**해야 한다. 성공/실패 메시지가 접두를 공유하는 경우가 흔하다.

### 11.3 적대적 리뷰 3회차 (2026-07-15, G1~G4 수정을 대상 — 최종)

| # | 결함 | 반영 |
|---|---|---|
| **H1** | G4 가 명세한 [12] 가 **동작 불가** — `vault kv get` 의 인증 토큰이 없다([7] 의 `$NEWROOT` 는 그 셸 안에서만 살고 §5.2 가 stdout 을 버린다). password grant 활성 여부도 미확인 가정 | §5.1.4 **무인증 재설계**(realm 응답 + DB 카운트) → Vault 경로·Keycloak 계정·ExternalSecret **3부품 삭제**, §9 선행작업 1건 삭제 |
| **H2** | 런북의 "드릴엔 무해"(`token_reviewer_jwt` ~1h 만료)를 **DR 1~2일 전제 설계가 상속** → ESO 가 1h 후 조용히 refresh 불능 | **§8.2 한계 명시**(후속 이슈 분리 — 결정 근거 포함) |
| H3 | `mode:"test"` 가 실재해를 테스트로 표시할 위험(알림 체계 무력화) | §7.2 **정확히 `"test"` 일 때만** 테스트, 그 외 전부 실재해 |
| H4 | RTO 기준점 모호 — 실행 시간에 **사람 대기(최대 24h)가 섞임** | §5.1.5 **2단 보고**(감지→승인 / 승인→서빙) |
| H5 | Function URL 인증 방식·동시성 제한 미명시 | §5.4 `AuthType: NONE`(강제) + **reserved concurrency 5** |

> **H1 의 교훈(F/G/H 3라운드 공통):** 이 스펙은 §5.2 에 "stdout 을 버리므로 bastion→Lambda 전달은 별도
> 경로가 필요하다"고 **스스로 적어두고도**, 두 라운드 뒤 같은 함정에 다시 빠졌다(G4). 결함을 고칠 때
> **그 결함의 일반 원리가 새 코드에도 적용되는지**를 매번 다시 물어야 한다 —
> F1(count 게이트)·F2(stale 상태)·G1(부분매칭)·H1(stdout 폐기) 넷 다 같은 실패 양상이다.

**수확 체감으로 3회차에서 종료한다:** 1회차 6건(치명 3) → 2회차 4건(치명 1) → 3회차 5건(치명 1, 나머지 경미).
남은 위험은 정적 검토로 더 줄지 않고 §7.2·§7.3 의 실측에서 드러나는 종류다.

### 11.4 실측 라운드 (2026-07-15) — **정적 검토가 원리적으로 못 잡는 결함 4건**

Plan 1 구현·배포·실호출에서 나왔다. **넷 다 apply 하거나 실제로 요청을 쏴야만 드러난다** — 3라운드 적대적
리뷰 + 최종 리뷰(opus)를 전부 통과한 것들이다.

| # | 결함 | 왜 정적으로 못 잡나 | 반영 |
|---|---|---|---|
| **P4** | **웹훅은 버튼·드롭다운을 못 보낸다.** Discord 가 에러가 아니라 **2xx + `components` 무음 폐기** → Lambda 성공·DDB 저장 정상인데 버튼만 없음 | Discord 플랫폼 제약. 아무도 "웹훅으로 버튼을 보낼 수 있나?"를 묻지 않았다 | **§3.6 신설**(Bot API 전환), §3.3·§9 |
| **P3** | **Function URL 403.** AWS 가 2025-10 부터 `InvokeFunction` 도 요구하는데 provider 는 `InvokeFunctionUrl` 만 자동 생성 → Lambda 호출조차 안 됨 | 문서(갱신됨)와 provider 구현의 **시차**. 최종 리뷰가 이 가설을 세웠다가 문서를 근거로 **반증 처리했고 그게 틀렸다** | §5.4 |
| **P1** | **IAM 정책이 숨은 의존이다.** Lambda/SM 은 `aws_iam_role` 만 참조하고 `aws_iam_role_policy` 는 참조하지 않는다 → (a) `-target` 이 정책을 안 끌고 오고 (b) 전체 apply 에선 형제라 **병렬 생성 레이스**. 둘 다 권한 없는 롤로 SFN 생성 → AccessDenied 2분+ hang | `terraform validate` 는 의존 그래프의 이 구멍을 검사하지 않는다 | **`depends_on`** (SFN·Lambda 3개). 초안의 "`-target` 에 정책 나열" 처방은 (a)만 막아 절반이었다 — codex 리뷰가 정정 |
| **P2** | **tfvars 게이트 누락.** `enable_eks_dr` 가 tfvars 에 없어 기본값 false → `-target` 없이 apply 하면 **warm DR 129개 리소스 전멸**. pilot-light 를 `-var` 로 apply 해서 tfvars 에 안 남음 | state 와 tfvars 의 불일치는 plan 을 돌려야 보인다 | §7.1 경고. **미해결 — tfvars 보강 필요** |

**교훈:** 이 설계의 남은 위험은 "더 생각해서" 줄지 않는다. **P4 는 3라운드 리뷰가 전부 통과시킨 뒤 첫 실호출에서
1분 만에 드러났다.** §7.2 의 과금 ~0 승인 경로 검증이 이걸 잡은 장치다 — 그게 없었으면 실재해에서 처음 알았을 것이다.

### 11.5 PR 리뷰 (codex) — 2건

| # | 지적 | 판정 |
|---|---|---|
| **P5** | **failover-trigger 의 `StartExecution` 이 멱등하지 않다.** EventBridge→Lambda 는 비동기(at-least-once)라 같은 알람 이벤트가 중복 전달될 수 있고 Lambda 자체도 재시도한다. `name` 을 안 주면 중복마다 **새 실행 + 새 승인 토큰** → Discord 에 승인 버튼이 여러 개. **Plan 2 연결 후엔 한 재해가 여러 페일오버로 이어진다** | **타당 — 수정.** `name = event["id"]`(이벤트마다 유일한 UUID)로 고정, `ExecutionAlreadyExists` 를 성공 처리해 재시도를 끊는다. 알람이 OK→ALARM 을 두 번 오가는 **정당한 두 전이는 id 가 달라 각각 실행**된다(원하는 동작) |
| **P1 처방 정정** | SM 이 `aws_iam_role_policy.dr_sfn` 에 의존하지 않아 생성 순서가 안 고정된다 | **타당 — 내 처방이 절반이었다**(위 P1 행 참조). `depends_on` 으로 교체 |

> **P1 이 두 번 정정된 것이 이 프로젝트의 축소판이다.** 진단(숨은 의존)은 맞았으나 처방(`-target` 나열)이
> `-target` 경로만 막았고, **레포에 이미 정답(`depends_on`)이 있었는데**(`public-ingress.tf:214-218` 이
> 같은 상황을 주석으로 설명 중) 그걸 안 보고 새로 발명했다. 기존 패턴을 먼저 찾을 것.

### 11.6 Plan 2 계획서 적대적 검증 (2026-07-15) — 구현 전

Plan 2 계획서(`2026-07-15-dr-failover-orchestration.md`)를 구현 착수 전에 공격해 **4건**을 잡았다.

| # | 결함 | 정정 |
|---|---|---|
| **A1** | **"bastion 스크립트 7개 전부 런북 이식"이 거짓.** 3개(`03`·`09`·`12`)는 런북에 원본이 없다. 특히 `09` 가 가리킨 `:357-370` 은 **사람용 체크리스트**(`- [ ]` + 백틱 조각)이고 [6][7][8][10][11][12] 까지 다루는 마스터 표라 [9] 의 원본이 아니다 | 대응표를 **이식 4 / 신규 3** 으로 재작성. "이식"의 정의를 Global Constraints 에 못박음 |
| A2 | 런북 줄번호 3건 오류 — `06` 이 CNPG 가드를, `08` 이 **real-DR `backupEnabled` flip 을 먹고 있었다**(§8.1 이 "수동 PR"로 결정한 것 → 자동화 유입 위험) | 실제 `### ` 경계로 정정(272-331 / 75-160+161-194 / **332-343**) |
| A3 | `03` 의 정리 대상 webhook 이름이 **계획 작성자의 창작** — 틀리면 `--ignore-not-found` 로 조용히 통과해 P1c 가 안 고쳐진다 | **미확정으로 명시** + T3 Step 9 에서 실측 확정 |
| A4 | `09` 가 **G1 함정 자리** — 사람용 확인을 기계 게이트로 옮김. 런북이 "미배포는 정상"이라 적은 것(ServiceMonitor·CiliumNetworkPolicy·lab-ssh-key)을 게이트하면 **건강한 DR 에서 오탐** | 오퍼레이터 `condition=Ready` 에만 의존. 토픽은 이름 하드코딩 대신 `--all`(랩 추가 시 자동 확장). bootstrap svc 검사 제외(TLS 9093 — 어설픈 검사가 G1 을 부른다) |

**교훈:** 계획서도 코드처럼 틀린다. **"이식"이라는 단어가 검증을 건너뛰게 만들었다** — 이식이면 런북이
보증하니까 안 봐도 된다고 믿은 것이다. 실제로는 3개가 창작이었고, 그중 `03` 은 **조용히 실패하는** 종류다.

### 11.7 Plan 2 계획서 적대적 검증 3회차 (2026-07-15) — 구현 전

A1~A4·C1~C6 를 반영한 계획서를 레포와 대조해 다시 공격, **9건**을 잡았다. **1·2·3 은 P0** 다.

| # | 결함 | 정정 |
|---|---|---|
| **F1** | **Task 2 가 terraform 순환을 만든다 — `validate` 부터 실패.** 자식 SM 이 `aws_iam_role_policy.dr_sfn` 을 `depends_on` 하는데 `data.aws_iam_policy_document.dr_sfn` 이 그 SM 의 `.arn` 을 참조 → `SM→policy→data→SM`. **스크래치패드에 같은 모양으로 재현해 `Error: Cycle` 확인** | 자식 SM 참조 statement 를 **별도 정책 `dr_sfn_child`** 로 분리. Global Constraints 에 "`depends_on` 거는 리소스를 그 정책이 참조하면 순환" 명시 |
| **F2** | **SFN 롤에 CodeBuild·Lambda·EKS 권한이 전무.** Plan 1 의 `InvokeApprovalRequest`(approval-request 1개)+`Logs` 가 전부라 [2]·[4]·[5]·[10]·[13] **과 NotifyFailed 까지** AccessDenied. **NotifyFailed 가 죽으면 모든 Catch 가 무음** → "실패해도 사람이 이어받는다"는 마지막 방어선 소멸. 자식 SM `.sync` 의 EventBridge 요구는 정확히 짚어놓고 **CodeBuild `.sync` 의 같은 요구는 놓쳤다** | **§SFN 롤 IAM 배선표** 신설(상태×API×정책×Task). statement 를 T1/T2/T4 에 분배 |
| **F3** | **bastion 롤에 `ssm:PutParameter` 가 없다**(`eks-dr-bastion.tf` 에 `ssm:` 액션 0건, `AmazonSSMManagedInstanceCore` 는 `GetParameter` 만 준다). `09-` 의 **마지막 줄**이 put-parameter라 **~40분 복구를 다 끝내고** 죽고, [10] 은 **설계대로** fail-closed → **전부 복구됐는데 서비스가 안 돌아온다.** §5.1.2 가 명시했는데 초안은 dns-switch 쪽만 반영 | T3 에 **Step 8 신설**(정책+`depends_on`+`-target` 18번째). T3 Files 에 `.tf` 추가 |
| F4 | **`ClearAlbParam` 상태가 없다.** §5.1.2 의 stale 2중 방어 ①이 증발. 그런데 `03-` 주석은 *"[2.5] ResolveBastion 이 한다"* 며 **존재하지 않는 구현을 가리켜** 리뷰어를 통과시킨다 | [2.4] `ClearAlbParam` 신설(SFN Task=API 1개라 ResolveBastion 과 못 합침). 에러명은 **미확정 표시** |
| F5 | **notify 의 입력을 채우는 곳이 없다.** `Payload` 매핑 부재로 **헤드라인 산출물인 RTO 2단이 `?`** 로 나온다 — C2 에서 고친 `_ts()` 가 값을 못 받는다 | NotifyComplete/NotifyFailed 정의 추가. `detectedAt` 은 `$.detail.state.timestamp`(테스트 실행에 없음→States.Runtime) 대신 **`$$.Execution.StartTime`**. NotifyFailed 는 `$.approval` 미참조([1] 실패 시 없음) |
| F6 | **계획이 자기 계약을 자기 테스트로 위반.** 계약은 "`env` 항상"인데 T2 Step 5 두 커맨드 + T3 Step 9 `run()` 이 전부 `env` 누락 → **첫 실측이 States.Runtime 으로 죽고 운영자가 멀쩡한 ASL 을 뜯는다** | 세 곳 다 `env` 추가. `run()` 에 3번째 인자. env 주입 스모크 신설 |
| F7 | `AgentReady?` 가 **자기가 막으려던 상황에서 깨질 수 있다** — 미등록 시 빈 목록이라 `[0].PingStatus` 경로 부재. **Step 5 스모크는 에이전트가 이미 Online 이라 이 분기를 원리적으로 못 밟는다**(C2 와 같은 패턴) | `IsPresent` 가드 선행. Choice 의 미존재 경로 거동은 미확정으로 남기되 **어느 쪽이든 안전하게** |
| F8 | [4] `ScaleNodes` 가 **표에만 있고** HCL·IAM 부재 | `ScaleNodes`→`UpdateNodegroup`→`WaitNodes`→`CheckNodes`→`NodesActive?` 정의. `DEGRADED`·`CREATE_FAILED` 명시 거부 |
| F9 | `addon-install` 계약이 **세 군데서 다르다** — Interfaces `{coredns, ebsCsi}` vs 코드 `{started}`/`{status,done}` vs Step 5 Expected. Step 5 의 invoke 는 payload 가 없어 **check 경로로 빠져 `ResourceNotFoundException`** | Interfaces 를 코드에 맞춤. invoke 에 `action` 추가 |

**교훈: 잡힌 것과 못 잡은 것의 성격이 갈렸다.** A1~A4·C1~C6 은 전부 **한 파일·한 상태 안에서 완결되는**
결함(창작한 이름·`States.Format` 파손·타임스탬프 파싱·`set -x` 유출)이었다. 반면 3회차 9건 중 5건(F1~F5)은
**"A 가 만들고 B 가 쓰는 것"의 배선**이고, 하필 그게 **`Interfaces` 가 선언만 하고 어느 Task 도 구현을
책임지지 않는 자리**다. **T3 가 `.sh` 만 만들고 `.tf` 를 안 건드린 것이 F3 의 직접 원인**이다.

> **계획서를 Task 단위로 자기완결적으로 쓰면 Task 경계를 넘는 것이 통째로 사라진다.** 랩 검증의
> 폭포수 dead-end 와 같은 구조다 — 각 스텝은 멀쩡한데 스텝 **사이**가 비어 있다. 다음 계획부터는
> **"각 상태가 호출하는 API × 실제 롤 권한" 표를 착수 전에 그린다**(이번엔 §SFN 롤 IAM 배선표로 신설).

### 11.8 Plan 2 계획서 적대적 검증 4회차 (2026-07-15) — 구현 전

3회차 반영본을 다시 공격, **6건**. 각도를 바꿨다 — **"이식"이라 라벨된 4개의 *내용*을 원본과 한 줄씩 대조**했다.
**A1(2회차)이 개수(7→4)만 고치고 내용은 아무도 안 봤다는 것**을 노렸고, 그 4개 안에 P1 이 2건 있었다.

| # | 결함 | 정정 |
|---|---|---|
| **H1** | **`06` 을 런북대로 옮기면 건강한 DR 에서 실패한다.** 런북 272-331 끝의 `kubectl get clusterissuer` · `kubectl -n api get configmap cledyu-root-ca-bundle` 은 사람이 **폴링**하는 확인인데, 변환 규칙 2(`set -euo pipefail`)를 먹으면 하드 게이트가 된다. **`service-api.yaml:12` 가 `sync-wave: "2"`** 라 그 시점엔 **api ns 자체가 없다** → `NotFound` → `exit 1`. **Global Constraints 가 "과대 게이트 → 건강한 DR 오탐"이라 경고한 G1 함정 그 자체인데, 그 경고를 `09` 에만 적용하고 `06` 은 "이식/낮음"으로 평가** | 두 줄 **제거**([9] 가 자연히 게이트한다 — Kafka 의존이 곧 cert-manager CA + Bundle). 변환 규칙 4 를 "**그 시점에 이미 참인 것만** 게이트"로 재작성 |
| **H2** | **`git clone` 은 멱등이 아니고 `set -e` 가 그 실패를 안 잡는다**(실측: `fatal: already exists` 뒤에도 스크립트 계속). `A && B` 는 AND-OR 리스트라 **A 의 실패가 set -e 면제**. → cd 가 안 된 채 진행하다 **뒤의 `git rev-parse` 에서 엉뚱한 에러로 죽는다.** T3 Step 10 이 "실패→고침→재실행"이라 **증분 드릴에서 반드시 밟는다.** 초안의 "`helm upgrade --install` 은 멱등이라 재실행 안전"이 **멱등한 건 helm 인데 clone 까지 안심시켰다** | `[ -d ~/Cledyu/.git ] && fetch+reset --hard \|\| clone` 로 멱등화. `cd` 를 `&&` 로 잇지 않는다. 변환 규칙 5(`A && B` 는 게이트가 아니다)·6(멱등) 신설 |
| **H3** | **`12` 의 `psql -U cledyu` 가 레포 선례 5건과 어긋난다** — `dr-failback.md:85`·`dr-failback-isolated-drill.md:85·87·90·91·97 **전부 `psql -d <db> -tAc`, `-U` 없음**. CNPG 파드 OS 유저는 postgres 라 local peer 인증에서 `-U cledyu` 는 OS유저≠롤. **[12] 는 페일오버의 마지막 게이트**라 **완벽히 복구된 DR 이 ❌ 실패 알림**을 보낸다(F5 와 같은 결과) | `-U` 제거, 선례 준수. peer 거동은 단정하지 않고 **Step 10 실측으로 확정** |
| H4 | **"런북 순서 유지"가 사실이 아니다.** 런북 체크리스트는 **Kafka → Vault → CNPG** 인데 우리는 **Vault → CNPG → Kafka** — **이미 바꿔놓고** "런북 순서를 지키니 안전하다"로 자기 변경을 정당화했다 | 근거 교체: Kafka 의존은 `cert-manager CA + trust-manager Bundle + gp3` 로 **Vault 무관**(체크리스트 `:359`). **드릴이 검증한 건 "의존 순서"이지 "줄 순서"가 아니다** |
| H5 | **`08` 은 이식이 아니라 재배치.** 런북은 "**root-app 직후, 차트가 CR 을 만들기 전에**" 지우라는데 우리는 [7](~30분) 뒤 = **ArgoCD 가 만든 CR 을 지우고 재생성을 기다리는 다른 동작**. 명령 2줄만 같다. **재배치 자체는 옳다**([7] 전엔 ESO 가 `postgres-credentials-cnpg` 를 못 만들어 CR 이 못 뜬다) | 라벨 🔀 재배치/중간. **새 의존(selfHeal) 확인함 — `data-postgres-cnpg-dr.yaml:31`·`data-keycloak-pg-dr.yaml:32` 둘 다 `true` ✅**. **🔴 PVC 재사용 미검증**(Step 10) |
| H6 | 런북 `:296` 주석이 "api/web **0**" 이라 적었으나 실제 `service-api.yaml:12` 는 **wave 2** | Task 6 에 정정 추가. **이 오해가 H1 을 키웠다** — wave 0 이면 "곧 뜬다"로 보인다 |

**교훈 1 — 라벨은 검증이 아니다.** 2회차(A1)는 *"'이식'이라는 단어가 검증을 건너뛰게 만들었다"* 고
**정확히 진단해놓고 개수만 고쳤다.** 남은 4개는 여전히 "이식"이라 적힌 채였고 아무도 원본을 안 열었다.
**진단이 맞아도 처방이 절반이면 같은 자리에서 또 터진다** — §11.5 가 "P1 이 두 번 정정된 것이 이
프로젝트의 축소판"이라 쓴 그 패턴이 **세 번째**로 반복됐다.

**교훈 2 — "사람용 → 기계" 변환은 규칙이 서로를 배신한다.** 변환 규칙 2(`set -e`)와 4(확인→게이트)를
곱하면 **런북이 "폴링해서 보라"고 쓴 것이 "없으면 실패"가 된다**(H1). 같은 `A && B` 구문이 `11` 에선
안전장치이고 `06` 에선 함정이다(H2). **규칙을 스크립트마다 기계적으로 적용하지 말고, 그 줄이 원래
사람에게 무엇을 시키던 것인지**(확인? 폴링? 게이트?)**를 먼저 판정한다.**

**교훈 3 — 3·4회차가 각각 9건·6건을 냈다.** "이제 됐다"가 세 번 틀렸다. 정적 리뷰의 수확은 아직
체감하지 않았으나, **남은 결함은 대부분 실행해야 보이는 종류**(PVC 재사용·peer 인증·에러명)로 수렴 중이다
— 다음은 T1 실측이다.

### 11.9 T1 실측 발견 (2026-07-15) — CodeBuild 첫 실행

**4라운드 정적 리뷰(15건)를 통과한 계획이 첫 실측 5분 만에 P1 을 냈다.** 계획서의 점진적 드릴 전략이
정확히 의도대로 작동한 것이다. 셋 다 **`.tf` 파일만 봐서는 원리적으로 안 보이는** 종류다.

#### T1-1 (P1) — apply 주체가 바뀌자 EKS access entry·KMS 키 정책이 넘어갔다

첫 CodeBuild 실행이 `Apply complete! Resources: 9 added, 1 changed, 2 destroyed` 를 냈다.
**그 `2 destroyed` 와 `1 changed` 가 무해하지 않았다:**

| | 이전(사람이 apply) | 이후(CodeBuild 가 apply) |
|---|---|---|
| EKS access entry `cluster_creator` | `user/kcy` | `role/cledyu-lab-dr-failover-tf` |
| KMS 키 `KeyAdministration` | `user/kcy` | `role/cledyu-lab-dr-failover-tf` |

**원인 — eks 모듈이 caller identity 를 기본값으로 쓰는 곳이 두 군데다:**
```hcl
# eks-dr.tf:79 (당시)
enable_cluster_creator_admin_permissions = true   # → main.tf:243 이 caller 를 merge
# kms_key_administrators 미지정
# → main.tf:316  coalescelist(var.kms_key_administrators, [session_context.issuer_arn])
```
지금까지는 **런북대로 사람이 apply** 해서 늘 `user/kcy` 였다. [2] TerraformApply 가 **CodeBuild** 로
apply 하게 되면서 주체가 바뀌었다.

**페일오버 자체는 안 깨진다** — bastion entry 는 `access_entries` 로 명시돼 있고(살아남음), 클러스터는
private-only 라 `user/kcy` 의 노트북 kubectl 은 애초에 불가했으며, KMS 는 root 문이 있어 계정 락아웃이 없다.
**진짜 문제는 플립플롭이다:** 사람이 apply → kcy, CodeBuild 가 apply → 롤, **terraform 이 영원히
수렴하지 않는다.** 그리고 **페일오버 경로에 destroy 가 상시로 뜨면 운영자가 destroy 줄을 안 읽게 된다** —
그건 `-var` 누락으로 warm DR 129개가 날아가는 사고를 놓치는 훈련이다.

**정정:** caller identity 의존을 양쪽 다 제거하고 **명시**로 바꿨다.
```hcl
locals { eks_dr_admin_arn = "arn:aws:iam::${data.aws_caller_identity.current.account_id}:user/kcy" }
enable_cluster_creator_admin_permissions = false
kms_key_administrators                   = [local.eks_dr_admin_arn]
access_entries = { bastion = {...}, operator = { principal_arn = local.eks_dr_admin_arn, ... } }
```
`account_id` 는 어느 주체든 같은 계정이라 결정적이다. **CodeBuild 롤은 일부러 안 넣었다** —
`versions.tf` 에 kubernetes provider 가 없어 terraform 이 k8s API 를 호출하지 않는다. 받아간 admin 은
**애초에 불필요한 권한**이었다.

**검증(실측):** CodeBuild 2회 연속 → 1회차 `2 added, 1 changed, 2 destroyed`(kcy 복구) → 2회차
**`0 added, 0 changed, 0 destroyed`**. 사람 plan 도 `No changes`. **양쪽 주체 수렴 확인.**
덤으로 **[2] 가 멱등**임이 증명됐다(계획서가 "hot 이 이미 떠 있으면 [2] 는 no-op" 이라 쓴 전제).

#### T1-2 — terraform state 락은 하나다

빌드 2개가 **4초 간격**으로 시작돼 뒤엣것이 `Error acquiring the state lock`
(DynamoDB `ConditionalCheckFailedException`)으로 죽었다.

- **막은 것:** `concurrent_build_limit = 1` — 빌드↔빌드 충돌은 **시작 자체를 막아** 즉시·명확히 실패시킨다
  (락 에러는 20초 뒤에 나고 메시지가 원인을 안 가리켜 진단이 오래 걸린다)
- **🔴 안 막힌 것 — [2] 의 Retry (T5 에서 판단):** **사람↔빌드** 충돌은 여전하다. 그리고 **재해 중엔
  운영자가 terraform 을 만지고 있을 확률이 오히려 높다**(장애 확인하려고 `plan` 을 친다). 그 순간 승인을
  누르면 **[2] 가 락 충돌로 죽는다.** 락은 초~분 단위로 풀리니 Retry 로 삼킬 수 있으나, "모든 실패를
  재시도"하면 `-var` 누락 같은 진짜 실패도 30분 늘어진다 → **락 에러만 골라낼 수 있는지 T5 에서 실측.**

#### T1-3 — 코드 출처가 둘로 갈렸다 (설계의 본질, 제거 불가)

| 주체 | 코드 출처 | git 인식 |
|---|---|---|
| 로컬 `terraform apply` | **운영자 디스크** | ❌ 미커밋도 그대로 씀 |
| CodeBuild [2] | **GitHub clone** | ✅ **푸시된 것만** |

**둘 다 같은 S3 state 에 쓴다.** T1 실측 중 실제로 사고가 났다 — 로컬에서 T1-1 을 고쳐 apply(→kcy 복구)
했는데, **커밋·푸시 전에** 빌드를 돌려 **옛 코드가 되돌렸다**(`158ec196`). 제거할 수 없는 성질이다 —
CodeBuild 는 git 에서 읽어야 재해 때 **검증된 코드**가 돌기 때문이다.

**T1 전에는 없던 위험이다.** 사람만 terraform 을 돌렸고 **디스크 = 진실**이었다. 이제는:
> **재해가 나서 [2] 가 돌면, 운영자 디스크의 미커밋 수정은 무시되고 main 코드가 apply 된다.**
> 즉 **로컬에서 급히 고쳐놓은 것이 재해 중에 조용히 롤백된다.**

→ Task 6 에서 **런북에 명시**한다(기술적 방어 수단이 없다 — 문서화가 유일한 완화).

#### T1-4 — T5 드릴은 main 을 봐야 한다 (미결정, T5 에서)

프로젝트 기본값이 `source_version = "main"` 이고 **실재해는 이게 맞다**(재해 중에 브랜치를 굴리지 않는다).
드릴은 `start-build --source-version <브랜치>` 로 **호출 시 오버라이드**한다 — 프로젝트는 안 건드린다.

**그런데 T5 는 SFN 을 통해 도는데 [2] 는 `SourceVersion` 을 안 넘긴다** → 프로젝트 기본값(main)을 쓴다
→ **머지 전이면 main 에 buildspec 이 없어 T5 드릴이 죽는다.** 선택지:
1. **T5 전에 main 으로 머지**(권장) — **fidelity 가 더 높다.** 실재해가 돌리는 게 정확히 main 이라,
   브랜치를 드릴하면 **정작 실제로 도는 코드를 한 번도 안 테스트**하는 셈이다
2. `source_version` 을 변수로 빼서 드릴 때 브랜치로 apply → 되돌리기. **되돌리기를 잊으면 실재해가
   브랜치를 돌린다**

**교훈 — 규칙을 적는 것과 지키는 것은 별개다.** T1 구현 중 "커밋·푸시가 실측보다 먼저"를 발견해
**계획서 T1 의 스텝 순서를 뒤집어 놓고**(Step 4 Commit+push → Step 5 실측), **그 직후 eks-dr.tf 수정에서
똑같은 실수를 했다** — 커밋 안 한 채 "빌드 돌려서 검증하자"고 한 것이다. 그 검증은 **원리적으로 성립할 수
없었다.** §11.7 의 F 계열(배선 누락)·§11.8 의 H 계열(라벨 맹신)과 같은 뿌리다: **아는 것이 지키는 것을
보장하지 않는다.**

### 11.10 T2 착수 전 발견 (2026-07-15) — SSM 출력 경로

계획서의 자식 SM 은 SSM 명령 출력을 **S3(`dr_backups`)로** 보내게 설계돼 있었다
(`OutputS3BucketName = aws_s3_bucket.dr_backups.id`). **3중으로 막혀 있었다:**

| # | 사실 | 결과 |
|---|---|---|
| 1 | bastion 롤에 **`s3:PutObject` 없음** (`GetObject on vault/*` 만) | 업로드 실패 — SSM 은 **인스턴스 자격증명**으로 올린다 |
| 2 | 버킷이 **SSE-KMS** 인데 bastion 엔 `kms:Decrypt`·`DescribeKey` 만 | 쓰기용 `kms:GenerateDataKey` 없음 |
| 3 | 버킷이 **Object Lock GOVERNANCE 30일**(실측 확인) | 드릴 로그가 **30일간 삭제 불가**로 누적 |

**1·2 는 IAM 으로 고쳐지지만 3 은 설계 문제다.** `dr_backups` 는 *"삭제·변조 불가로 굳혀 writer 키 유출·
랜섬웨어·실수 삭제로부터 보호"*하는 **WORM 금고**다(`backup.tf:11`). **백업 금고와 운영 디버그 로그는
성격이 정반대다** — 라이프사이클(`expiration 35일`)도 Lock 때문에 제 역할을 못 한다.

**정정 — CloudWatch Logs 로 보낸다.** 이 설계의 다른 로그(SFN·Lambda·CodeBuild)와 같은 곳이고,
retention(30일)으로 자동 정리되며 `aws logs tail` 로 바로 읽는다(T1 에서 CodeBuild 디버깅하던 그 방식).

```hcl
CloudWatchOutputConfig = {
  CloudWatchLogGroupName  = aws_cloudwatch_log_group.dr_bastion_commands.name  # /aws/ssm/cledyu-lab-dr-failover
  CloudWatchOutputEnabled = true
}
```
**출력 계약이 바뀐다:** `{..., stdoutUrl, stderrUrl}` → `{..., commandId, logGroup}`.

**IAM 도 같이 만들었다 — F3 와 같은 클래스다.** SSM 의 CloudWatch 출력도 **bastion 자격증명**으로 쓰는데,
붙어 있는 `AmazonSSMManagedInstanceCore` 는 **logs 권한을 하나도 주지 않는다**(실측: 정책에 `logs:*` 0건,
`s3:*` 0건). 빠뜨렸으면 **stdout 전문이 조용히 유실**되고 잘린 `stdoutTail` 만 남았을 것이다.
→ `aws_iam_role_policy.eks_dr_bastion_command_logs`(`CreateLogStream`·`PutLogEvents`·`DescribeLogStreams`).

> **왜 전문이 필요한가 — `stdoutTail` 로는 부족할 수 있다.** `GetCommandInvocation` 의
> `StandardOutputContent` 는 문서상 **"the first 24,000 characters"** 다. **뒤가 아니라 앞**이라면,
> `set -x` 로 길게 뱉는 `06-bootstrap-apps.sh` 가 **끝에서 실패했을 때 정작 그 에러가 안 담긴다**
> (notify 가 Discord 에 싣는 `stdoutTail[-1200:]` 도 "앞 24k 의 끝"일 뿐이다).
> **🔴 "first" 인지는 미실측** — T2 Step 5 스모크에서 긴 출력으로 확인한다.

**교훈 — F3 는 한 번으로 안 끝났다.** §11.7 F3 는 "`09-` 가 `ssm:PutParameter` 를 부르는데 IAM 이 없다"
였고, 정정으로 **Global Constraints 에 "스크립트를 만드는 Task 는 그 스크립트가 호출하는 AWS API 의 IAM 도
같이 만든다"** 를 넣었다. 그런데 이번 건은 **스크립트가 아니라 SSM 에이전트가** 부르는 것이라 그 규칙의
문면에 안 걸렸다. **"bastion 에서 AWS API 를 부르는 주체"는 스크립트만이 아니다** — 에이전트·사이드카·
오퍼레이터 전부다. 규칙을 문면대로만 읽으면 같은 함정을 다른 이름으로 다시 밟는다.
