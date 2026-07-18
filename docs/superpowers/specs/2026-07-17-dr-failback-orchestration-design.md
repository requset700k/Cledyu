# DR Failback 오케스트레이션 — 스냅샷 기반 단순 failback 설계

- 날짜: 2026-07-17
- 상태: 초안 (리뷰 대기)
- 관련: `2026-07-15-dr-failover-orchestration.md`, `2026-07-15-dr-discord-approval-orchestration-design.md`, `2026-07-14-dr-detection-alerting-design.md`, `2026-07-11-dr-plan-b-pilot-light.md`, `docs/RUNBOOK/dr-failback.md`
- 대체(scope 축소)하는 것: `2026-07-13-dr-failback-reconciliation-design.md`(drEpoch 역복제) — 본 설계는 역복제를 **하지 않는다**(아래 Non-goals).

---

## 1. 목표

온프렘이 회복됐을 때 **자동 감지 → Discord 승인 게이트 → 자동 failback**을 failover와 대칭으로 구현한다.
failback = **온프렘을 자기 최신 스냅샷으로 복원한 상태로 되돌리고, DNS를 온프렘으로 원복하고, EKS DR의 hot 레이어를 되무는 것.**

failover 흐름을 **정확히 뒤집은 것**으로 설계한다:

| | failover | failback (역순) |
|---|---|---|
| 트리거 | pull+push 복합알람 **ALARM** | push 하트비트 **OK 복귀** |
| 방향 | 온프렘 → EKS DR | EKS DR → 온프렘 |
| DNS | api·app·auth → EKS ALB | api·app·auth → 온프렘 공개 ALB(`*-public`) |
| 인프라 | hot 기동(`eks_dr_active=true`) + 노드 스케일 업 | hot 되무름(`eks_dr_active=false`) + 노드 0 축소 |
| 데이터 | 온프렘 스냅샷 → EKS 복원 | 온프렘이 자기 스냅샷으로 복원(회복 자체) + EKS 데이터 폐기 |

## 2. Non-goals (의도된 범위 축소)

- **역복제(EKS → 온프렘 데이터 동기화) 없음.** DR 기간 중 EKS에서 발생한 쓰기는 **RPO 손실로 감수**하고 폐기한다.
- **온프렘 복원은 AWS 자동화 밖 — 관리자 몫이다(R9).** 온프렘 운영 CNPG 매니페스트(`gitops/apps/postgres-cnpg/templates/cluster.yaml`)의 bootstrap은 **`initdb.import`(구 postgres에서 논리 import) fail-safe**이며 **S3 recovery는 의도적으로 안 넣음**(같은 serverName 재아카이빙 → "Expected empty archive" 충돌). 따라서 온프렘 복원은 자동 GitOps recovery가 **아니다**:
  - **온프렘 PVC 생존**(정전·네트워크 등, Longhorn 디스크 무사): 온프렘 재기동 시 CNPG가 디스크에서 resume(재해 직전 상태). 자동.
  - **온프렘 완전 소실→재구축**: 운영 bootstrap.import는 소스 없어 실패(fail-safe). **최신 스냅샷 복원은 별도 복구 매니페스트를 관리자가 선-적용**(Plan C가 EKS에 쓰던 그 매니페스트).
  - 두 경우 모두 **관리자가 온프렘을 복원·서빙 확인**한 뒤 승인 버튼을 누른다. AWS failback 자동화의 책임은 **감지·승인·DNS 원복·EKS hot 회수·고아 정리**이며 온프렘 DB엔 손대지 않는다(닿지도 못함).
- **EKS warm 컨트롤플레인은 폐기하지 않는다.** pilot-light 모델대로 `enable_eks_dr=true`는 상시 유지(컨트롤플레인만 ~$73/mo). failback은 hot 레이어 + EKS가 만든 out-of-band 리소스(ALB·EBS·ENI·GuardDuty 엔드포인트)만 되문다.
- **부분 실패 failover의 자동 failback 없음.** 자동 failback은 **정상 완료된 failover**(활성 플래그가 세팅된 경우)만 되돌린다. 부분 실패는 기존 런북 수동 경로.

## 3. 검증된 현재 상태 (설계 앵커 — origin/main 실측 2026-07-17)

- **감지** (`infra/terraform/aws/dr-detection.tf`, us-east-1):
  - `aws_cloudwatch_metric_alarm.push` — `Cledyu/DR`::`OnPremHeartbeat`, `LessThanThreshold 1`, `evaluation_periods=3`(3분), `treat_missing_data=breaching`. 온프렘 하트비트 부재 시 ALARM, 복귀 시 OK. **count 게이트 없음(pub 무관·항상 존재).**
  - `aws_cloudwatch_metric_alarm.pull` — Route53 HealthCheckStatus(`auth.cledyu.com`/`/realms/cledyu-learn`). **failover의 SwitchDNS가 auth를 EKS ALB로 바꾸므로, failover 후 pull은 온프렘이 아니라 DR을 잰다 → 온프렘 회복 신호로 쓸 수 없다.** ∴ failback 트리거는 **push 단독**.
  - `aws_cloudwatch_composite_alarm.disaster` — `ALARM(pull) AND ALARM(push)`. `count = local.pub`.
- **failover 트리거 배선**: `aws_cloudwatch_event_rule.dr_disaster`(us-east-1, detail-type `CloudWatch Alarm State Change`, `alarmName=[disaster]`, `state.value=[ALARM]`) → `aws_lambda_function.dr_failover_trigger`(이벤트 id 멱등) → `aws_sfn_state_machine.dr_failover` `start_execution`. 규칙 게이트: `local.pub==1 && dr_detection_armed && dr_orchestration_armed`.
- **`dr_failover` SFN**: 승인(`dr_approval_request`, `.waitForTaskToken`) → **TerraformApply**(CodeBuild `.sync`, buildspec `dr-failover-buildspec.yml`, 19 `-target`, `-var enable_eks_dr=true -var eks_dr_active=true -var eks_dr_node_desired=0`) → ClearAlbParam → ResolveBastion → bastion 복원 스크립트(`dr_run_on_bastion` 자식 SM, SSM SendCommand) → **[9] SSM `/cledyu-dr/failover/alb-hostname` 기록** → **SwitchDNS**(`dr_dns_switch` Lambda) → RestartApps → VerifyServing → **NotifyComplete**(`dr_notify` Lambda).
- **`dr_notify` 성공 메시지**(`dr-orchestration-lambda/notify/index.py`, `outcome=="success"`)에 **제거 대상 꼬리** 존재: `"다음 할 일 — failback 준비: postgres-cnpg-dr/keycloak-pg-dr backupEnabled false→true PR + 런북 링크"`.
- **DNS 전환 메커니즘**(`dns-switch/index.py`): SSM `/cledyu-dr/failover/alb-hostname` 읽음 → ALB를 DNSName으로 조회 → WAF 연결 확인 → 공개 hosted zone(비-private `cledyu.com`) 확인 → api·app·auth A-alias UPSERT(EvaluateTargetHealth **False**). **non-VPC Lambda는 사설 EKS/온프렘 k8s API에 못 닿음**(주석 명시).
- **DNS 원복 대상**(`public-ingress.tf`): `aws_lb.public`(`${name_prefix}-public`) — 443 ACM 종단 → tailnet 프록시 EC2(`aws_instance.proxy`) → 온프렘 Keycloak/Traefik. `aws_route53_record.public`(A-alias, EvaluateTargetHealth **true**). 게이트 `enable_public_ingress`(기본 false). **주석에 수동 failback 절차 명시**: `terraform apply -var enable_public_ingress=true -target=aws_route53_record.public`. **proxy·public ALB는 failover와 무관(별도 변수 게이트)이며 DNS 원복의 목적지 → 절대 teardown 대상 아님.**
- **EKS DR 모델 = pilot-light**(`eks-dr.tf`, `variables.tf`):
  - warm(`enable_eks_dr`, `local.eks_dr_enabled`): `module.eks_dr_vpc`(VPC/서브넷), `module.eks_dr`(클러스터·노드그룹 min0), 전 IRSA, SG, bastion 롤/정책/프로필. 상시 유지.
  - hot(`eks_dr_active`, `local.eks_dr_active`): NAT(`module.eks_dr_vpc`의 `enable_nat_gateway=var.eks_dr_active`), `module.eks_dr_endpoints`(count=active), `aws_instance.eks_dr_bastion`(count=active). 재해 시만.
  - 노드 스케일: 모듈이 `desired_size` `ignore_changes` → **terraform 아님, CLI `aws eks update-nodegroup-config`**(pilot-light Global P1).
  - 검증 조건: `!eks_dr_active || enable_eks_dr`(hot은 warm의 [0] 인덱스 참조).
- **상태 저장소**: `aws_dynamodb_table.dr_approvals`(approvalId 해시, TTL 24h — 승인 단위). SFN은 `/cledyu-dr/failover/*`에 `ssm:PutParameter`/`deleteParameter` 권한 보유.

## 4. 트리거 설계 — push OK 복귀 + 활성 게이트

### 4.1 활성 플래그 (신규 SSM 파라미터 `/cledyu-dr/failover/active`)

- **세팅**: `dr_failover` SFN이 **VerifyServing 통과 후 NotifyComplete 앞**에 신규 상태 `MarkFailoverActive`를 두어 `ssm:putParameter` `/cledyu-dr/failover/active`(값=executionArn, **ap-northeast-2** — SM 리전) 기록. ⚠️ **[최종리뷰 C1] `dr_sfn` 롤은 원래 `ssm:DeleteParameter`만 있어 PutParameter 추가 필수**(안 하면 AccessDenied→Catch가 삼켜 failover는 ✅ 뜨는데 플래그 미기록→failback 영영 미무장). ClearAlbParam statement에 `ssm:PutParameter` 추가함.
- **클리어**: `dr_failback` SFN 종료 단계에서 `deleteParameter` `/cledyu-dr/failover/active`(+ `/cledyu-dr/failover/alb-hostname`).
- **의미**: "지금 정상 완료된 failover 상태다". 이게 있어야만 failback 트리거가 발화 → 평상시 하트비트 깜빡임/부분실패는 자동 failback을 유발하지 않음.

### 4.2 감지 배선 (신규, `dr_failover_trigger` 미러)

- **신규 EventBridge 규칙** `dr_recovery`(us-east-1), `dr_disaster` 미러:
  ```
  source        = ["aws.cloudwatch"]
  detail-type   = ["CloudWatch Alarm State Change"]
  detail.alarmName   = [aws_cloudwatch_metric_alarm.push.alarm_name]
  detail.state.value = ["OK"]
  ```
  게이트: `dr_disaster`와 동일(`local.pub==1 && dr_detection_armed && dr_orchestration_armed`).
  - **[R3] `previousState` 필터를 걸지 않는다.** `ALARM→OK`만 잡으면 `ALARM→INSUFFICIENT_DATA→OK`(알람 재설정 등) 경로를 놓쳐 **회복을 조용히 miss**한다. 평상시 push는 steady OK라 →OK 전이 자체가 없으므로(상태변화 이벤트는 실제 변화 시에만), 필터를 빼도 오발이 없다. 진짜 필터는 아래 active 게이트.
- **신규 Lambda** `dr_failback_trigger`(us-east-1 실행 — push 알람 이벤트가 us-east-1, `failover_trigger` 미러):
  1. SSM `/cledyu-dr/failover/active` 조회. ⚠️ **[최종리뷰 C2] 이 파라미터는 dr_failover SM이 쓰는 ap-northeast-2에 있다 — Lambda는 us-east-1이지만 `region_name=SFN_REGION`으로 크로스리전 읽기**(기본 리전 us-east-1로 읽으면 ParameterNotFound→트리거 영영 no-op). 없음 → `{"started": false, "reason": "not-failed-over"}` no-op(하트비트 깜빡임·평상시 무시).
  2. 있음 → `dr_failback` SFN(ap-northeast-2) `start_execution`. **[R2] 실행 이름 = active 플래그 값(=failover 실행 id)에서 파생**(예: `failback-<hash>`), **event id 아님**. 하트비트 flapping으로 push OK가 여러 번 떠도 **같은 failover를 가리키니 실행 이름이 동일** → 2번째부터 `ExecutionAlreadyExists`로 무시 → **failover당 failback 딱 1개, 중복 승인 메시지 없음**. (승인은 어차피 사람 게이트라 teardown 경합은 애초에 없지만, 혼란스러운 2번째 메시지를 원천 차단.)
  - IAM: `ssm:GetParameter` `arn:aws:ssm:${var.region}:...:parameter/cledyu-dr/failover/active`(**ap-northeast-2**, us-east-1 아님), `states:StartExecution` `dr_failback`.

## 5. 승인 게이트 — 재사용 + failback 모드

- `dr_failback` SFN 첫 상태 = **RequestApproval**, `dr_approval_request` Lambda 재사용.
- **`mode="failback"` 분기 신설**(approval-request):
  - failover 모드: Vault 스냅샷 목록 등 옵션 렌더.
  - **failback 모드: 옵션 없음. 단일 승인 버튼 1개** + 안내문("온프렘 하트비트 복귀 감지 — 온프렘 CNPG/Keycloak 서빙을 직접 확인한 뒤 승인하세요. 승인 시 DNS가 온프렘으로 원복되고 EKS hot 레이어가 회수되며 DR 데이터는 폐기됩니다").
  - `.waitForTaskToken`으로 DDB에 승인 항목 기록 후 대기(failover와 동일).
- **`interaction` Lambda(index.mjs) 재사용** — 서명 검증 + 버튼 custom_id 파싱 → `SendTaskSuccess`. failback approve custom_id 처리 추가(옵션 없으므로 단순). **멱등 승인 처리(`fix/dr-approve-idempotent` 계열)를 그대로 상속** → 중복 클릭·경합 안전.
- **관문의 의미**: 관리자가 "온프렘이 정말 복원·서빙되는가"(CNPG 헬시, Keycloak 로그인)를 확인하는 지점. 이 확인이 곧 DNS 원복을 split-brain 없는 **단일 권한 전환**으로 만든다.

## 6. 실행 — `dr_failback` SFN (approach B = AWS 레벨 정리, bastion 불요)

**설계 근거**: 정리를 bastion+kubectl(in-cluster) 대신 **AWS API로 직접** 한다. 2026-07-16 teardown 드릴로 검증된 방식이며([[project_dr_failback_teardown_playbook]]) — bastion 의존 제거(어차피 teardown 대상), in-cluster 컨트롤러 생존에 의존 안 함(더 견고), EKS 데이터는 어차피 폐기라 graceful 종료 불요. 전 스텝이 SFN aws-sdk 태스크 / Lambda / CodeBuild로 **완전 자동**(사람은 승인 버튼 1회뿐).

```
RequestApproval (§5, 사람 게이트 = 온프렘 healthy 확인)
  → RevertDNS            (Lambda, →온프렘 *-public ALB, fail-closed)
  → ListNodegroup→ScaleToZero  (aws-sdk: nodegroup desired 0)
  → WaitScaleApplied     (Wait 30s: [NEW-2] ASG desired=0 반영 대기, 강제종료 전)
  → CleanupOrphans       (Lambda: 노드 강제종료+볼륨 available 대기 → ALB·TG·k8s-*SG·EBS·aws-K8S-* ENI·GuardDuty 삭제)
  → TeardownHot          (CodeBuild, eks_dr_active=false, 17-target)
  → VerifyNoOrphans→OrphanCheck (aws-sdk: 잔존 cluster-태그 EBS → 있으면 경고 첨부)
  → ClearFlags
  → NotifyFailbackComplete
(모든 상태 Catch → NotifyFailbackFailed, dr_notify 재사용)
```

### 6.1 RevertDNS (신규 Lambda `dr_dns_revert`, `dns_switch` 미러)

- api·app·auth A-alias UPSERT → `${name_prefix}-public` ALB(DNSName으로 조회), **EvaluateTargetHealth=true**(public-ingress.tf 정의와 일치).
- **fail-closed 게이트**(dns-switch 대칭): public ALB 미발견 / 온프렘 프록시 타깃 unhealthy / 공개 zone 부재 시 즉시 실패(DNS를 EKS에 둔 채 멈춤 = 안전).
- **승인 뒤 맨 앞 = 트래픽을 온프렘으로 먼저 넘긴 뒤 EKS를 내림.** ⚠️ **gate-0 실증**(2026-07-16 드릴): 온프렘 미서빙 상태로 DNS 원복 시 pull 헬스체크 타임아웃 + 하트비트 소실 → **disaster 복합알람 발화 → 재-failover** 트리거됨. 그래서 승인 게이트(사람이 온프렘 healthy 확인)와 이 fail-closed가 **둘 다** 필수. `_proxy_healthy`는 프록시 생존만 증명하므로 사람 확인이 1차, 이건 2차 안전망.

### 6.2 ScaleToZero + WaitScaleApplied (aws-sdk, `[4] ScaleNodes` 역)

- `eks:listNodegroups`로 이름 발견 → `eks:updateNodegroupConfig`(desired 0). 강제종료는 CleanupOrphans Lambda가 함(PDB 15분 매달림 회피).
- **[NEW-2] WaitScaleApplied(Wait 30s)**: ScaleToZero 직후 강제종료하면 ASG desired가 아직 N이라 종료 노드를 **재생성**할 수 있다 → desired=0 반영을 짧게 대기한 뒤 CleanupOrphans로. (드릴은 수동 지연으로 통과 — 자동화는 명시 대기로 레이스 제거. 라이브 드릴서 무재생성 실측 확인.)

### 6.3 CleanupOrphans (신규 Lambda `dr_teardown_cleanup`)

노드 강제종료 후, EKS가 out-of-band로 만든(=terraform 밖) 리소스를 **AWS API로 직접 삭제**. DR VPC id·클러스터명(`cledyu-dr`)으로 필터. 멱등(이미 없으면 skip):

0. **노드 강제종료 + 볼륨 대기**: cluster-태그 running 인스턴스 `ec2:terminateInstances`(desired=0 반영 후라 ASG 재생성 없음) → 볼륨이 `available` 될 때까지 bounded 폴링.
1. **ALB → 타깃그룹 → `k8s-*` SG**: `DescribeLoadBalancers`(VpcId=DR VPC) → **[NEW-1] `DeleteLoadBalancer` 먼저(listener 제거)** → 수집해둔 TG ARN `DeleteTargetGroup`(TG 먼저 지우면 listener 참조로 ResourceInUse) → **[NEW-3]** `k8s-*` SG는 ALB ENI detach까지 재시도 삭제.
2. **EBS**: `ec2:DescribeVolumes`(tag `kubernetes.io/cluster/cledyu-dr=owned`, state=available) → `DeleteVolume`. **이게 EKS 데이터 실제 폐기**(vault·CNPG·kafka PVC 전부, 태그로 일괄 — 앱별 구분 불요).
3. **고아 ENI**: DR VPC 내 `status=available` + Description `aws-K8S-*` → `DeleteNetworkInterface`.
4. **GuardDuty 엔드포인트**: `guardduty-data`(GuardDutyManaged, ~$20/mo) → `DeleteVpcEndpoints`. 다음 failover에 자동 재생성되므로 삭제 안전.
- ⚠️ **반복 재해 정합성**: warm etcd에 남는 k8s 객체(Ingress·PVC·CNPG CR)는 무해 — 다음 failover가 [6]`helm upgrade argocd`로 컨트롤러 복구 + [8] 구 CNPG CR 삭제→최신 S3 재복원 + [3] CleanWarmEtcd로 흡수(origin/main 실측). 우리는 **AWS 리소스만** 지우면 됨.

### 6.4 TeardownHot (기존 `dr_failover_tf` CodeBuild 재사용 + 신규 teardown buildspec)

**결정 (a) 재사용**: failover가 hot을 **올리는** `dr_failover_tf` 프로젝트를 그대로 쓴다(terraform+admin 자격은 생성·삭제 모두 가능, `eks_dr_active` 값만 다름). SFN TeardownHot이 `codebuild:startBuild.sync`에 **`BuildspecOverride="infra/terraform/aws/dr-failback-teardown-buildspec.yml"`** 지정. 신규 프로젝트/IAM 없음.

buildspec 핵심(**[R5] 드릴-검증 17-target** — dr-eks-bootstrap.md §failback과 동일):
```
terraform init -input=false -lock-timeout=5m
terraform apply -input=false -auto-approve -lock-timeout=5m \
  -var enable_eks_dr=true -var eks_dr_active=false -var eks_dr_node_desired=0 \
  -var enable_public_ingress=true \
  <17개 -target: module.eks_dr_vpc·module.eks_dr·module.eks_dr_ebs_csi_irsa·module.eks_dr_alb_irsa·
   aws_security_group.eks_dr_endpoints·aws_security_group.eks_dr_bastion·module.eks_dr_endpoints·
   module.eks_dr_vault_unseal_irsa·aws_iam_policy.eks_dr_vault_unseal·aws_iam_policy.eks_dr_cnpg_restore·
   module.eks_dr_cnpg_restore_irsa·aws_iam_role.eks_dr_bastion·aws_iam_role_policy_attachment.eks_dr_bastion_ssm·
   aws_iam_role_policy.eks_dr_bastion_describe·aws_iam_role_policy.eks_dr_bastion_vault_restore·
   aws_iam_instance_profile.eks_dr_bastion·aws_instance.eks_dr_bastion>
```
- `eks_dr_active=false` → NAT·엔드포인트·bastion 소멸. `enable_eks_dr=true`라 warm(컨트롤플레인·VPC·IRSA·노드그룹0) 보존.
- **⚠️ `-target` 필수 + `enable_eks_dr=true` 필수.** tfvars가 `.gitignore`라 CodeBuild 체크아웃에 없음 → `-target` 없으면 `enable_public_ingress` 기본 false로 proxy·public ALB·Route53 destroy, `enable_eks_dr` 생략 시 warm 컨트롤플레인까지 destroy(`feedback_terraform_target_plan`).

### 6.5 VerifyNoOrphans + ClearFlags + NotifyFailbackComplete

- **[R8] VerifyNoOrphans** (aws-sdk): `elbv2:DescribeLoadBalancers`(DR VPC)·`ec2:DescribeVolumes`(cluster 태그) → 비어야 정상. 비어있지 않으면 `$.orphans`에 담아 완료 알림에 "⚠️ 고아 의심 N개" 첨부(조용한 고아를 보이는 경보로).
- **ClearFlags**: `deleteParameter` `/cledyu-dr/failover/active`, `/cledyu-dr/failover/alb-hostname`(`ParameterNotFound` 삼킴).
- **NotifyFailbackComplete**: `dr_notify` **failback 성공 브랜치 신설**. **[R7] RTO/RPO 라벨 없음** — failback은 재해가 아니라 계획된 복귀이므로 재해복구 지표(RTO/RPO)를 붙이지 않는다. 메시지 = `"✅ DR failback 완료 · DNS: 온프렘(*-public ALB) · EKS: warm 회수, DR 데이터 폐기 · 소요 N분"`(소요는 중립 표기, RTO 아님) + VerifyNoOrphans 경고(있으면).
- **실패 경로**: `dr_notify` failback 실패 브랜치 — 실패 단계 + "롤백 안 함, 런북 이어받기" + DNS 현재 위치(RevertDNS 통과 = `$.dns.alb` IsPresent 지상진실, failover DnsSwitched? 미러).

## 7. 기존 코드 수정 (edit — 신규 아님)

1. `notify/index.py` 성공 메시지에서 **failback 준비/backupEnabled/런북 꼬리 제거**(스냅샷-only failback은 DR-창 쓰기 아카이빙이 불필요하므로 지침 자체가 obsolete). ✅ 완료 + 시간 2구간 + ALB만 유지.
2. `notify/index.py`에 **failback 성공/실패 브랜치 추가**(§6.5, **[R7] RTO/RPO 라벨 없음**) — `outcome` 값 `failback-success`/`failback-failed` 분기.
3. `approval-request/index.py`에 **`mode="failback"` 분기**(§5) — 옵션 없는 단일 버튼.
4. `interaction/index.mjs` 승인 경로 **`latestSnapshot` 접근 하드닝**(`?? null`) — failback 항목엔 스냅샷이 없어 기존 `got.Item.latestSnapshot.S`가 TypeError. custom_id는 `dr-approve:{id}` 공용 재사용(멱등 상속).
5. `dr_failover` SFN ASL에 **`MarkFailoverActive` 상태 삽입**(VerifyServing → MarkFailoverActive → NotifyComplete), 값=`$$.Execution.Id`(R2 실행이름 파생용).

## 8. 신규 리소스

- Lambda: `dr_failback_trigger`(us-east-1), `dr_dns_revert`(ap-northeast-2), **`dr_teardown_cleanup`(ap-northeast-2, CleanupOrphans)** + 각 IAM 롤/정책/로그그룹.
- EventBridge: `dr_recovery` 규칙 + target + lambda permission(us-east-1).
- SFN: `dr_failback` 상태머신 + IAM(승인·dns-revert·cleanup Lambda 호출 · `eks:UpdateNodegroupConfig`/`ListNodegroups` · `ec2:TerminateInstances`/`Describe*` · codebuild · `ssm:DeleteParameter` · notify).
- CodeBuild buildspec: `dr-failback-teardown-buildspec.yml`(신규 파일). **CodeBuild 프로젝트/IAM 롤은 신규 없음** — 기존 `dr_failover_tf`를 `BuildspecOverride`로 재사용(§6.4, 결정 (a)).
- SSM 파라미터: `/cledyu-dr/failover/active`(런타임 생성, TF 리소스 아님).
- **bastion 스크립트 없음**(approach B = AWS 레벨 정리, bastion 불요).

## 9. 엣지·실패 처리

- **멱등**: trigger는 event id로, 승인은 상속된 멱등 핸들러로, teardown terraform apply는 수렴적(이미 hot 없으면 no-op)이라 재실행 안전.
- **최초 사이클**: `/cledyu-dr/failover/active` 부재가 정상 → trigger no-op. ClearFlags의 `ParameterNotFound` 삼킴.
- **split-brain 방지 + gate-0**: DNS는 승인(사람이 온프렘 healthy 확인) 후에만 원복 + RevertDNS fail-closed. 미서빙 온프렘으로 원복 시 disaster 알람 재발화(2026-07-16 드릴 실증) → 두 게이트가 방어.
- **[R2] 동시성**: teardown은 승인 후에만 발생 + 승인자 1명이라 실질 경합 없음. 추가로 실행이름을 failover-id에서 파생해 중복 승인 메시지 자체를 차단.
- **부분 실패 failover**: active 플래그 미세팅 → 자동 failback 안 뜸(수동 런북). 안전한 축소.
- **teardown 중 트래픽**: RevertDNS가 맨 앞 → 이후 어느 단계 실패해도 트래픽은 이미 온프렘(안전). Catch가 실패 단계 알림, 운영자 런북 인계.
- **[R6] 수동 failback**: 런북 수동 경로도 종료 시 `/cledyu-dr/failover/active` 삭제(안 하면 다음 하트비트 깜빡임에 자동 failback 오발). [[project_dr_failback_teardown_playbook]]에 스텝 추가.
- **반복 재해**: CleanupOrphans가 EBS 삭제(데이터 폐기) + 다음 failover [6]/[8]/[3]이 warm etcd 잔여 k8s 객체를 흡수(§6.3).

## 10. 시연 내레이션 (실제 동작과 일치, 방어 가능)

> "일시 장애는 pull+push 복합알람이 애초에 DR 진입을 막습니다. DR이 뜬다는 건 온프렘 실질 소실이므로, failback은 온프렘의 마지막 정상 스냅샷 기준 복원으로 갑니다. **DR 기간 중 EKS 쓰기는 의도된 RPO 손실로 감수**하고, 무손실 역복제는 설계까지 마쳤으나 실 DR 창 검증이 남아 로드맵입니다. 온프렘 하트비트가 복귀하면 자동으로 failback 승인 요청이 오고, 관리자가 온프렘 서빙을 확인해 승인하면 DNS가 온프렘으로 원복되고 EKS는 pilot-light warm으로 회수됩니다."

없는 기능을 있다고 말하지 않으며, 실제 파이프라인 동작과 정확히 일치한다.

## 11. 확정된 결정 (리뷰 반영 완료)

- **TeardownHot** = (a) `dr_failover_tf` 재사용 + `BuildspecOverride`(§6.4). ✅
- **정리 방식** = approach B(AWS 레벨, bastion 불요) — 2026-07-16 드릴 검증. bastion in-cluster 정리(approach A) 폐기. ✅
- **DrainNodes** = SFN aws-sdk(`updateNodegroupConfig` + `terminateInstances` 강제종료, PDB 15분 회피). ✅
- **트리거 필터**(R3) = `previousState` 미포함, `→OK`만 + active 게이트. ✅
- **동시성**(R2) = 실행이름 failover-id 파생(A안), DDB claim 불요(사람 게이트가 직렬화). ✅
- **failback 메시지**(R7) = RTO/RPO 라벨 없음. ✅

**남은 구현 시 확인**: `k8s-*` SG가 ALB 삭제로 함께 지워지는지 vs 별도 삭제 필요(CleanupOrphans에 포함해두되 실측 확인) · GuardDuty 엔드포인트 id 조회 필터 · WaitDetached 대기 시간 튜닝.

## 12. 드릴/시연 주의 (자동화 밖)

- **알람 disarm은 드릴 전용**(재과금 방지), **실 failback은 알람 armed 유지**(온프렘 healthy면 disaster 알람은 OK라 재발화 안 함). 자동화엔 미포함.
- TeardownHot buildspec은 CodeBuild가 체크아웃하는 리비전(SourceVersion=main)에 있어야 함 — 머지 전 드릴은 SourceVersion=브랜치.
- failback 자동 경로 테스트는 failover가 선행돼야(active 플래그). 트리거 no-op(active 부재)만 독립 테스트 가능.
