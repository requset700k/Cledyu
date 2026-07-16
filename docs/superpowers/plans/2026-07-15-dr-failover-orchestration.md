# DR 페일오버 오케스트레이션 Implementation Plan (Plan 2/2)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Plan 1 의 승인 버튼 뒤에 실제 복구를 붙인다 — 승인 클릭 한 번으로 EKS 기동 → ArgoCD 부트스트랩 →
Vault·CNPG 복원 → 공개 DNS 전환까지 자동 완료하고, RTO 를 실측한다.

**Architecture:** Step Functions 13단계. 실행 주체가 셋으로 갈린다 — **CodeBuild**(terraform apply, 15분 초과
가능) · **bastion + SSM RunCommand**(kubectl·helm·Vault — private EKS 에 닿는 유일한 발판) · **Lambda**(route53/
wafv2/elbv2 — bastion 롤에 없는 권한, Route53 은 공개 API 라 VPC 불요). SSM 대기는 자식 SM 이 폴링한다
(SFN 에 SSM `.sync` 통합이 없음).

**Tech Stack:** Terraform(SFN·CodeBuild·Lambda·IAM), python3.12, bash(SSM RunCommand), 기존 EKS DR 오버레이(Plan B).

**설계 근거:** `docs/superpowers/specs/2026-07-15-dr-discord-approval-orchestration-design.md` (§5 상태표, §11.4 실측 발견)
**이식 원본:** `docs/RUNBOOK/dr-eks-bootstrap.md` — **7/14 풀 E2E 드릴로 검증된 명령이다. 재작성하지 말고 이식한다.**
**선행:** Plan 1(`2026-07-15-dr-discord-approval-gate.md`) 머지 — 이 계획은 그 위에 쌓인다.

## Global Constraints

- **리전:** SFN·CodeBuild·bastion·EKS DR·Lambda = `ap-northeast-2`. EventBridge·failover-trigger = `us-east-1`(Plan 1 기배포)
- **작업 브랜치:** Plan 1 머지 후 `main` 기준 신규 브랜치(`feat/dr-failover-orchestration`)
- **⚠️ `-target` 없는 plan/apply 금지** — 전체 apply 는 **87 destroy + 프록시 교체**(실측 2026-07-15).
  tfvars 는 `enable_public_ingress`·`dr_detection_armed`·`alert_email` 만 설정하고 `enable_eks_dr` 가 없어
  기본값 `false` → warm DR 129개가 날아간다. **`-target` 이 이 레포의 정상 운영 방식이다**(우회 아님)
- **⚠️ IAM 정책은 `depends_on` 으로 묶는다** — `-target` 은 의존성만 따라가고 의존하는 것은 안 따라간다.
  role 만 참조하면 정책이 숨은 의존이라 (a) `-target` 이 정책을 안 끌고 오고 (b) 전체 apply 에서 병렬 생성
  레이스. `depends_on` 이 둘 다 해소한다(`eks-dr-bastion.tf:127`·`public-ingress.tf:218` 기존 패턴)
- **⚠️ 그러나 `depends_on` 을 거는 리소스를 그 정책이 참조하면 terraform 순환이다.** SM 이
  `aws_iam_role_policy.X` 를 `depends_on` 하는데 `data.aws_iam_policy_document.X` 가 그 SM 의 `.arn` 을
  `resources` 에 넣으면 `SM → policy → data → SM` 사이클로 **`terraform validate` 부터 실패**한다
  (적대적 검증 3회차 F1 — 실제 재현 확인). Plan 1 의 `dr_approval_test` 가 무사한 건 그 SM 을 참조하는
  정책이 **다른 롤**(`dr_failover_trigger`)의 것이기 때문이다. **자기 롤의 정책이 자기를 참조하면
  정책을 별도 `aws_iam_role_policy` 로 분리**한다(§SFN 롤 IAM 배선표)
- **⚠️ IAM 은 Task 별로 채우지 말고 §SFN 롤 IAM 배선표를 보고 채운다.** 적대적 검증 3회차에서 나온
  9건 중 5건이 "A 가 만들고 B 가 쓰는 것"의 배선 누락이었다 — `Interfaces` 가 선언만 하고 어느 Task 도
  구현을 책임지지 않는 자리다. **AWS API 를 부르는 것을 만드는 Task 는 그 IAM 도 같이 만든다**
  (T3 가 `.sh` 만 만들고 `.tf` 를 안 건드린 것이 F3 의 직접 원인)
- **⚠️ "부르는 주체"는 스크립트만이 아니다.** F3 는 **세 번** 다른 층에서 터졌다(스펙 §11.7·§11.10·§11.11):
  스크립트(`09-`→`ssm:PutParameter`) → **SSM 에이전트**(→CloudWatch, "스크립트"라는 문면에 안 걸림) →
  **같은 에이전트인데 IAM 내용이 틀림**. 에이전트·오퍼레이터·오퍼레이터가 만든 파드 전부가 주체다.
- **⚠️ IAM 을 추론으로 채우지 않는다 — 호출 주체의 실제 요청을 본다.** §11.11 에서 "로그그룹은 terraform 이
  만드니 `CreateLogStream`·`PutLogEvents` 면 충분하다"고 추론했으나 **에이전트는 그룹이 있어도
  `DescribeLogGroups`→`CreateLogGroup` 을 먼저 부르고, 막히면 출력을 통째로 포기**했다(명령은 Success).
  답을 준 건 문서도 추론도 아니라 **에이전트 로그 한 줄**이었다. 막히면 CloudTrail·에이전트 로그를 볼 것.
- **⚠️ 부수효과를 목적으로 하는 변경은 그 부수효과를 직접 확인한다.** `SUCCEEDED`·`responseCode: 0` 은
  "명령이 돌았다"이지 **"로그가 남았다"가 아니다**(§11.11 — 스모크 3종이 전부 통과하는데 전문은 유실).
  Global Constraints 의 "과소 게이트 — 존재만 보고 값을 안 봄"이 **실측 판정 기준 자체**에도 적용된다
- **⚠️ SSM RunCommand 는 TTY 가 없다** — 런북의 `kubectl exec -it` 에서 **`-it` 를 반드시 제거**한다
- **⚠️ "이식"은 명령을 그대로 옮기는 것이다 — 런북에 없는 것을 "이식"이라 적지 않는다.**
  이 계획의 초안은 bastion 스크립트 7개를 전부 "런북 이식"이라 적었으나 **3개는 런북에 없었다**(적대적
  검증 2026-07-15). 신규 작성은 **명시적으로 표시**하고, 근거(왜 런북에 없나)와 **미확정 항목**을 남긴다.
  구현자가 "이식"을 믿고 런북을 찾았는데 없으면 지어내거나 멈춘다.
- **⚠️ 사람용 확인을 기계 게이트로 옮길 때 G1 함정을 재검토한다.** 런북의 `grep -E "A|B"` 나 "눈으로
  확인" 항목은 **사람이 둘 다 보고 판단하라**고 만든 것이다. 기계로 옮기면 조용히 틀린다:
  - **부분매칭** — `grep -q "db 연결"` 이 `"db 연결 **실패**"` 에도 매치 → degraded 가 통과(실측 G1)
  - **과소 게이트** — 존재만 보고 값을 안 봄
  - **과대 게이트** — 런북이 "미배포는 정상"이라 적어둔 것(ServiceMonitor·CiliumNetworkPolicy·lab-ssh-key)을
    기계가 결함으로 판정 → **건강한 DR 에서 오탐 실패**
  → **성공 문자열은 전문 매치, 실패 문자열은 명시 거부.** 오퍼레이터가 관리하는 조건(`condition=Ready`)이
  있으면 그걸 쓴다 — 직접 파싱하지 않는다
- **⚠️ SFN 로깅은 `include_execution_data = false`** — `true` 면 taskToken 이 평문 로그에 남는다.
  토큰은 `SendTaskSuccess` 의 유일한 bearer 자격증명이라 3겹 방어를 전부 우회당한다(Plan 1 §5.4)
- **terraform 컨벤션:** `var.name_prefix` 접두, 정책은 `data.aws_iam_policy_document`, 시크릿 output 금지
- **terraform docs:** `infra/terraform/aws` 의 리소스/변수/출력 변경 커밋엔 재생성된 `README.md` 를 반드시 함께 `git add`
- **서브에이전트는 파일작성 + 정적검증(`terraform fmt/validate`, `ruff`, `node --check`)까지만.**
  커밋·apply·드릴·실측은 **운영자**가 한다
- **커밋:** 사용자가 직접 실행. 각 Task 의 Commit 스텝은 **명령어만 제시**하고 실행하지 않는다.
  커밋 메시지에 `Co-Authored-By` 금지, heredoc 금지(`git commit -m` 방식)

## SFN 롤 IAM 배선표 (적대적 검증 3회차 F2·F3 — 초안에 전부 없었다)

**초안의 IAM 은 Plan 1 의 `InvokeApprovalRequest`(approval-request **한 개**) + `Logs` 가 전부였고,
T2 가 SSM·자식 SM 만 더했다.** 그래서 13단계 중 [2]·[4]·[5]·[10]·[13] 과 **NotifyFailed 까지** 전부
AccessDenied 였다. 특히 **NotifyFailed 가 못 돌면 모든 `Catch` 가 무음**이 되어 "실패해도 사람이 이어받는다"는
이 설계의 마지막 방어선이 사라진다.

**정책이 3개로 갈리는 이유는 사이클(F1)이다** — `dr_run_on_bastion` 이 `aws_iam_role_policy.dr_sfn` 을
`depends_on` 하므로, **그 자식 SM 의 ARN 을 참조하는 statement 는 같은 정책에 있으면 안 된다.**

| # | 상태 | 호출 API | statement | 들어갈 정책 | 넣는 Task |
|---|---|---|---|---|---|
| 1 | `RequestApproval` | `lambda:InvokeFunction` | `InvokeApprovalRequest` ✅ 기존 | `dr_sfn` | Plan 1 |
| 2 | `TerraformApply` | `codebuild:StartBuild`·`StopBuild`·`BatchGetBuilds` + EventBridge 규칙 | `StartCodeBuild`·`CodeBuildSyncEvents` | `dr_sfn` | **T1 Step 2** |
| 2.4 | `ClearAlbParam` | `ssm:DeleteParameter` | `ClearAlbParam` | `dr_sfn` | **T2 Step 1** |
| 2.5 | `ResolveBastion` | `ec2:DescribeInstances` | `ResolveBastion` | `dr_sfn` | **T2 Step 1** |
| 3·6·7·8·9·11·12 | 자식 SM 호출 | `states:StartExecution`·`DescribeExecution`·`StopExecution` + EventBridge 규칙 | `StartChildSm`·`ChildSmSync`·`ChildSmSyncEvents` | **`dr_sfn_child`(별도!)** | **T2 Step 1** |
| — | 자식 SM 내부 | `ssm:SendCommand`·`GetCommandInvocation`·`DescribeInstanceInformation` | `RunOnBastion` | `dr_sfn` | T2 Step 1 |
| 4 | `ScaleNodes` | `eks:UpdateNodegroupConfig`·`DescribeNodegroup`·`ListNodegroups` | `ScaleNodes` | `dr_sfn` | **T2 Step 1** |
| 5·5b | `InstallAddons`·`CheckAddons` | `lambda:InvokeFunction` | `InvokeFailoverLambdas` | `dr_sfn` | **T4 Step 4** |
| 10 | `SwitchDNS` | `lambda:InvokeFunction` | `InvokeFailoverLambdas` | `dr_sfn` | **T4 Step 4** |
| 13 | `NotifyComplete` · **`NotifyFailed`** | `lambda:InvokeFunction` | `InvokeFailoverLambdas` | `dr_sfn` | **T4 Step 4** |

**SFN 롤이 아닌 것 — 이게 F3 이고 가장 위험했다:**

| 주체 | 필요 권한 | 현재 | 넣는 Task |
|---|---|---|---|
| **bastion 인스턴스 롤** | **`ssm:PutParameter`** (`/cledyu-dr/failover/*` 한정) | ❌ **없음** — `eks-dr-bastion.tf` 에 `ssm:` 액션 0건. 붙어 있는 `AmazonSSMManagedInstanceCore` 는 `GetParameter` 만 주고 **`PutParameter` 는 안 준다** | **T3 Step 8(신설)** |
| `dns-switch` Lambda | `ssm:GetParameter` | ❌ 없음(신규 Lambda) | T4 Step 4 |

> **⚠️ bastion 의 `ssm:PutParameter` 누락이 왜 최악인가.** `09-wait-apps-ready.sh` 의 **마지막 줄**이
> `aws ssm put-parameter` 다. Kafka·VE·Keycloak 이 다 뜨고 Vault·CNPG 복원까지 끝난 **~40분 뒤**,
> `set -euo pipefail` 이 그 줄에서 걸려 [9] 가 실패한다. 그리고 [10] 은 **설계대로** fail-closed 라
> DNS 를 안 넘긴다 → **모든 게 정상 복구됐는데 서비스는 영원히 안 돌아온다.** 스펙 §5.1.2 가
> "bastion 롤에 `ssm:PutParameter`(해당 경로만), dns-switch 롤에 `ssm:GetParameter` 추가"라고
> **명시했는데 초안은 dns-switch 쪽만 반영했다.**

## 점진적 드릴 (이 계획의 핵심 전략)

**T1~T5 는 각 Task 끝에서 즉시 실측한다.** 오늘(2026-07-15) Plan 1 에서 **정적 리뷰가 원리적으로 못 잡는
결함이 4건** 나왔다(스펙 §11.4) — 웹훅 컴포넌트 무음 폐기, Function URL 403, `-target` IAM 누락, tfvars 게이트.
넷 다 **실제로 굴려야만** 드러났고, P4 는 3라운드 적대적 리뷰 + 최종 리뷰(opus)를 통과한 뒤 **첫 실호출 1분 만에**
나왔다.

**13단계를 다 만들고 T7 에서 처음 돌리면**, [3] 에서 죽고 → 고치고 → [6] 에서 죽고 → … 를 반복하며 매번
40분씩 태운다. **상태를 붙일 때마다 거기까지 도달하는지 확인한다.**

**비용:** T1 에서 hot 리소스(NAT·엔드포인트·bastion·노드 3)를 올리고 **T7 끝까지 유지**한다. 그 창 동안만 과금된다.
Plan B 드릴과 동일 수준. T7 마지막에 파괴한다.

---

### Task 1: CodeBuild — terraform apply 실행기

> [2] TerraformApply 의 실행 주체. Lambda 는 15분 제한이 있고(NAT 생성만 몇 분), AWS SDK 직접 생성은
> state 밖 고아를 만들어 failback 의 `terraform apply` 전제를 깬다 → CodeBuild 가 유일한 선택.

**Files:**
- Create: `infra/terraform/aws/dr-failover-buildspec.yml`
- Modify: `infra/terraform/aws/dr-orchestration.tf` (CodeBuild 프로젝트·IAM·로그그룹 추가)
- Modify: `infra/terraform/aws/README.md`

**Interfaces:**
- Produces: `aws_codebuild_project.dr_failover_tf` — Task 5 의 [2] 가 `codebuild:startBuild.sync` 로 호출
- Consumes: 없음(Plan 1 리소스 미참조)

- [ ] **Step 1: buildspec 작성**

`infra/terraform/aws/dr-failover-buildspec.yml`:

```yaml
version: 0.2

# DR 페일오버 hot 리소스 기동 — Step Functions [2] 가 codebuild:startBuild.sync 로 호출한다.
#
# ⚠️ -target 목록은 docs/RUNBOOK/dr-eks-bootstrap.md §Phase 1(213-272)의 검증된 **17개**를 그대로 쓴다.
#    재발명 금지 — 7/14 드릴로 검증된 목록이고, 2026-07-15 에 목록을 임의로 줄였다가
#    IAM 정책이 빠져 2분+ hang 을 겪었다. 이 17개엔 bastion IAM 3종(role_policy 2 + attachment 1)이 있다.
#    (개수 검증: sed -n '213,272p' 런북 | grep -oE '\-target=[a-z_.0-9]+' | sort -u | wc -l → 17)
#    ⚠️ **T3 Step 8 이 18번째(aws_iam_role_policy.eks_dr_bastion_ssm_param)를 추가한다** — 그 리소스는
#    T3 이 신설하므로 T1 시점엔 존재하지 않는다. 여기서 미리 넣지 말 것(T1 Step 4 실측이 깨진다).
# ⚠️ -var 3개 필수 — tfvars 에 enable_eks_dr 가 없어 기본값 false 다. 안 넘기면
#    apply 가 생성이 아니라 **destroy** 가 된다(warm DR 129개).
phases:
  install:
    runtime-versions:
      python: 3.12
    commands:
      - curl -fsSL --retry 5 https://releases.hashicorp.com/terraform/${TF_VERSION}/terraform_${TF_VERSION}_linux_amd64.zip -o /tmp/tf.zip
      - unzip -q /tmp/tf.zip -d /usr/local/bin && terraform version
  build:
    commands:
      - cd infra/terraform/aws
      - terraform init -input=false
      # 런북 §Phase 1 과 동일. 노드 desired 는 모듈이 ignore 하므로 여기선 0 — [4] 가 CLI 로 올린다.
      - |
        terraform apply -input=false -auto-approve \
          -var enable_eks_dr=true -var eks_dr_active=true -var eks_dr_node_desired=0 \
          -target=module.eks_dr_vpc -target=module.eks_dr -target=module.eks_dr_ebs_csi_irsa \
          -target=module.eks_dr_alb_irsa -target=aws_security_group.eks_dr_endpoints \
          -target=aws_security_group.eks_dr_bastion -target=module.eks_dr_endpoints \
          -target=module.eks_dr_vault_unseal_irsa -target=aws_iam_policy.eks_dr_vault_unseal \
          -target=aws_iam_policy.eks_dr_cnpg_restore -target=module.eks_dr_cnpg_restore_irsa \
          -target=aws_iam_role.eks_dr_bastion -target=aws_iam_role_policy_attachment.eks_dr_bastion_ssm \
          -target=aws_iam_role_policy.eks_dr_bastion_describe -target=aws_iam_role_policy.eks_dr_bastion_vault_restore \
          -target=aws_iam_instance_profile.eks_dr_bastion -target=aws_instance.eks_dr_bastion
```

> **전사 검증(T1 구현 시 실행함 — 4회차 교훈 "라벨은 검증이 아니다").** 계획서의 목록을 믿지 말고
> 런북과 **집합으로** 대조한다(개수만 세면 오탈자를 놓친다):
> ```bash
> sed -n '213,272p' docs/RUNBOOK/dr-eks-bootstrap.md | grep -oE '\-target=[a-zA-Z_.0-9]+' | sort -u > /tmp/rb.txt
> grep -oE '\-target=[a-zA-Z_.0-9]+' infra/terraform/aws/dr-failover-buildspec.yml | sort -u > /tmp/bs.txt
> diff /tmp/rb.txt /tmp/bs.txt   # 비어야 정상
> ```
> **2026-07-15 T1 구현 시 실행 → 17개 집합 일치·`-var` 3개 일치 확인됨 ✅**

- [ ] **Step 2: terraform — CodeBuild 프로젝트 + IAM**

`dr-orchestration.tf` 에 append. **IAM 은 DR 범위 admin 이 된다** — 스펙 §5.4 가 명시한 표면이다.

```hcl
# ── [2] TerraformApply 실행기 (CodeBuild) ──
# Lambda 15분 제한(NAT 생성만 몇 분) + terraform 바이너리 필요 → CodeBuild.
# VPC 연결 불요 — terraform 은 AWS API 만 호출한다(EKS 엔드포인트를 안 건드림).
data "aws_iam_policy_document" "dr_codebuild_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["codebuild.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "dr_failover_tf" {
  name               = "${var.name_prefix}-dr-failover-tf"
  assume_role_policy = data.aws_iam_policy_document.dr_codebuild_assume.json
}

# ⚠️ 이 롤은 사실상 DR 범위 admin 이다(설계 §5.4). -target 은 terraform 인자일 뿐 IAM 경계가 아니므로
# 좁힐 수 없다. 방어선은 승인 게이트 3겹(Ed25519·허용목록·armed)이고, 이 롤 자체는 SFN 만 호출한다.
resource "aws_iam_role_policy_attachment" "dr_failover_tf_admin" {
  role       = aws_iam_role.dr_failover_tf.name
  policy_arn = "arn:aws:iam::aws:policy/AdministratorAccess"
}

resource "aws_cloudwatch_log_group" "dr_failover_tf" {
  name              = "/aws/codebuild/${var.name_prefix}-dr-failover-tf"
  retention_in_days = 30
}

resource "aws_codebuild_project" "dr_failover_tf" {
  name         = "${var.name_prefix}-dr-failover-tf"
  service_role = aws_iam_role.dr_failover_tf.arn
  depends_on   = [aws_iam_role_policy_attachment.dr_failover_tf_admin]

  artifacts { type = "NO_ARTIFACTS" }

  environment {
    compute_type    = "BUILD_GENERAL1_SMALL"
    image           = "aws/codebuild/amazonlinux2-x86_64-standard:5.0"
    type            = "LINUX_CONTAINER"
    environment_variable {
      name  = "TF_VERSION"
      value = "1.9.8" # versions.tf 의 required_version >= 1.9.0 을 만족하는 고정 버전
    }
  }

  source {
    type      = "GITHUB"
    location  = "https://github.com/requset700k/Cledyu.git"
    buildspec = "infra/terraform/aws/dr-failover-buildspec.yml"
  }
  source_version = "main"

  logs_config {
    cloudwatch_logs {
      group_name = aws_cloudwatch_log_group.dr_failover_tf.name
    }
  }

  build_timeout = 30 # 분. NAT·엔드포인트·bastion 생성 ~3분 + 여유
  tags          = local.eks_dr_tags
}
```

**같은 Step 에서 SFN 롤에 CodeBuild 권한을 넣는다** — `data.aws_iam_policy_document.dr_sfn` 에 statement 추가
(§SFN 롤 IAM 배선표). **초안엔 이게 없어서 [2] 가 AccessDenied 였다**(적대적 검증 3회차 F2):

```hcl
  statement {
    sid       = "StartCodeBuild"
    actions   = ["codebuild:StartBuild", "codebuild:StopBuild", "codebuild:BatchGetBuilds"]
    resources = [aws_codebuild_project.dr_failover_tf.arn]
  }
  statement {
    sid = "CodeBuildSyncEvents"
    # ⚠️ codebuild:startBuild.sync 도 자식 SM 의 .sync 와 똑같이 EventBridge 관리형 규칙으로 완료를
    # 감지한다(AWS 문서). 초안은 자식 SM 쪽 ChildSmSyncEvents 는 정확히 짚어놓고 **CodeBuild 쪽을
    # 놓쳤다** — 같은 숨은 요구가 두 군데 있는데 한 곳만 본 것이다.
    actions   = ["events:PutTargets", "events:PutRule", "events:DescribeRule"]
    resources = ["arn:aws:events:${var.region}:*:rule/StepFunctionsGetEventForCodeBuildStartBuildRule"]
  }
```

> **사이클(F1) 걱정 없다** — `dr_failover_tf` 는 CodeBuild 프로젝트고 `aws_iam_role_policy.dr_sfn` 을
> `depends_on` 하지 않는다. 사이클이 나는 건 **자식 SM** 뿐이라 그것만 별도 정책으로 뺀다(T2 Step 1).

- [ ] **Step 3: 정적 검증**

Run:
```bash
cd /home/user/Cledyu/infra/terraform/aws
terraform fmt -check dr-failover-buildspec.yml dr-orchestration.tf 2>/dev/null; terraform fmt -check dr-orchestration.tf
terraform validate
python3 -c "import yaml,sys; yaml.safe_load(open('dr-failover-buildspec.yml')); print('buildspec YAML OK')"
```
Expected: fmt 통과, `Success! The configuration is valid.`, `buildspec YAML OK`

- [ ] **Step 4: Commit + push** (사용자가 실행) — **⚠️ 실측보다 먼저다**

> **⚠️ 이 Task 만 커밋·푸시가 실측 앞에 온다(T1 구현 시 발견).** 다른 Task 의 실측은 로컬
> `terraform apply` 로 끝나지만, **[2] 는 CodeBuild 가 GitHub 에서 클론해서 돈다.** 커밋 안 한 파일도,
> **푸시 안 한 브랜치도** CodeBuild 는 못 본다(`git ls-remote --heads origin <브랜치>` → 없음이면 실패).
> 계획 초안은 "실측(Step 4) → 커밋(Step 5)" 순이었는데 **T1 에선 성립하지 않는다.**

```bash
cd /home/user/Cledyu
git add infra/terraform/aws/dr-failover-buildspec.yml infra/terraform/aws/dr-orchestration.tf \
        infra/terraform/aws/README.md docs/superpowers/plans/2026-07-15-dr-failover-orchestration.md
git commit -m "feat(dr): CodeBuild terraform apply 실행기 (페일오버 hot 리소스 기동)"
git push -u origin feat/dr-failover-orchestration   # ← CodeBuild 가 GitHub 에서 클론하므로 필수
```

- [ ] **Step 5: 운영자 실측 — CodeBuild 가 실제로 hot 리소스를 올리나** ⚡ 과금 시작

> **여기서 hot 리소스가 올라가고 T7 까지 유지된다.** NAT·엔드포인트·bastion·(이후 노드 3).

> **⚠️ `--source-version` 필수 — 머지 전엔 이게 없으면 반드시 실패한다(T1 구현 시 발견).**
> 프로젝트의 `source_version = "main"` 인데 **buildspec 은 이 브랜치에만 있다**(`git cat-file -e
> origin/main:infra/terraform/aws/dr-failover-buildspec.yml` → 없음). CodeBuild 가 main 을 체크아웃해
> buildspec 을 못 찾고 죽는다. **프로젝트를 고치지 말고 호출 시 오버라이드**한다 — 실재해는 검증된
> main 을 돌려야 하므로 기본값은 `main` 이 맞다.

```bash
cd /home/user/Cledyu/infra/terraform/aws
terraform apply -target=aws_codebuild_project.dr_failover_tf

# ⚠️ 머지 전이므로 --source-version 으로 이 브랜치를 지정한다(머지 후엔 생략 = main).
BRANCH=$(git -C /home/user/Cledyu branch --show-current)
BUILD=$(aws codebuild start-build --region ap-northeast-2 --project-name cledyu-lab-dr-failover-tf \
  --source-version "$BRANCH" --query 'build.id' --output text) && echo "$BUILD"

# 로그 따라가기 (5~10분)
aws logs tail /aws/codebuild/cledyu-lab-dr-failover-tf --region ap-northeast-2 --follow
```
Expected: `Apply complete!`. 그리고 **bastion 이 생겼는지**:
```bash
aws ec2 describe-instances --region ap-northeast-2 \
  --filters Name=tag:Name,Values=cledyu-dr-bastion Name=instance-state-name,Values=running \
  --query 'Reservations[].Instances[].InstanceId' --output text
```
**실패 시 흔한 원인(위에서부터 확률순):**
- **buildspec not found** — `--source-version` 누락(위 경고). main 에 아직 파일이 없다
- **GitHub source 연결 미인증** — 이 레포 **첫 CodeBuild** 라 선례가 없다. PUBLIC 레포는 토큰 없이
  clone 된다고 알려져 있으나 **미검증**이다. 막히면 `aws_codebuild_source_credential` 또는 콘솔에서 연결
- **`python: 3.12` 런타임 부재** — AL2 standard 5.0 이 3.12 를 주는지 **미검증**(계획서 가정).
  install 페이즈에서 죽으면 `python: 3.11` 로 내리거나 runtime-versions 를 이미지 기본값에 맞춘다.
  (terraform 자체는 python 을 안 쓴다 — AL2 standard 5.0 이 **섹션 자체를 요구**해서 넣은 것뿐이다)
- `-var` 누락으로 destroy plan · IAM 전파 지연

---

### Task 2: 자식 상태 머신 — bastion 스크립트 실행기

> SFN 에 SSM `.sync` 통합이 **없다**(AWS optimized integrations 표 — SSM 은 목록에 없고 AWS SDK 통합엔
> `.sync` 가 Not supported). `ssm:sendCommand` 는 CommandId 만 즉시 반환하므로 폴링을 직접 만든다.
> SSM 단계가 6개([3][6][7][8][9][11])라 인라인하면 24개 상태가 되고, 통짜 스크립트로 합치면 실패 지점을 잃는다.

**Files:**
- Modify: `infra/terraform/aws/dr-orchestration.tf`
- Modify: `infra/terraform/aws/README.md`

> **🆕 CloudWatch 출력을 택하면서 리소스 2개가 늘었다**(T2 구현, 스펙 §11.10):
> `aws_cloudwatch_log_group.dr_bastion_commands` + `aws_iam_role_policy.eks_dr_bastion_command_logs`.
> **후자를 빠뜨리면 stdout 전문이 조용히 유실된다** — SSM 의 CloudWatch 출력은 **bastion 자격증명**으로
> 쓰는데, 붙어 있는 `AmazonSSMManagedInstanceCore` 는 **logs 권한을 하나도 주지 않는다**(실측 확인).
> F3(bastion 의 `ssm:PutParameter` 누락)와 **같은 클래스**다: 설계가 bastion 에서 AWS API 를 부르는데
> 그 IAM 을 아무도 안 만든다.

**Interfaces:**
- Consumes: `aws_iam_role.dr_sfn`(Plan 1) — 정책에 SSM 액션 추가 필요
- Produces: `aws_sfn_state_machine.dr_run_on_bastion` — Task 5 가 `states:startExecution.sync` 로 호출
- **입력 계약:** `{instanceId, script, env, timeoutSeconds, label}` — **`env` 는 항상 채운다**(기본 `":"` = 셸 no-op).
  스크립트 앞에 실릴 셸 한 줄이며 [7] 의 스냅샷 주입에만 실값을 쓴다. **문자열 조립을 하지 않는 이유는 §Task 5 [7]**
  (스크립트 전문을 `States.Format` 리터럴에 넣으면 따옴표·중괄호·개행이 intrinsic 을 깨 정의가 거부됨)
- **출력 계약:** `{status, responseCode, stdoutTail, commandId, logGroup}` — **stdout 전문을 반환하지 않는다.**
  전문은 **CloudWatch 로그그룹**(`/aws/ssm/cledyu-lab-dr-failover`)에 있고 `commandId` 로 찾는다:
  `aws logs tail <logGroup> --log-stream-name-prefix <commandId>`
  > **⚠️ 초안은 `stdoutUrl`·`stderrUrl`(S3) 이었으나 S3 경로가 3중으로 막혀 있었다**(T2 착수 전 발견, 스펙 §11.10):
  > (1) bastion 롤에 `s3:PutObject` 없음 — SSM 은 **인스턴스 자격증명**으로 올린다 (2) 버킷이 SSE-KMS 인데
  > bastion 엔 `kms:Decrypt` 만 (3) 버킷이 **Object Lock GOVERNANCE 30일** — 드릴 로그가 30일간 삭제 불가.
  > (1)(2)는 IAM 으로 고쳐지나 **(3)은 설계 문제**다: `dr_backups` 는 WORM 금고이고 운영 로그와 성격이 정반대다.
- **상태 흐름:** `WaitForSsmAgent → AgentReady? → BuildCommands(Pass) → SendCommand → WaitCmd → GetResult → Done?`
  (+ `WaitAgent` 루프, 종료: `Succeeded`(End) / `Failed`(Fail))

- [ ] **Step 1: SFN 롤 정책 확장 — 정책이 2개로 갈린다(사이클 회피)**

**(a) `data.aws_iam_policy_document.dr_sfn` 에 statement 추가**(기존 `InvokeApprovalRequest`·`Logs`,
T1 의 `StartCodeBuild`·`CodeBuildSyncEvents` 는 유지). **여기엔 자식 SM 의 `.arn` 을 넣지 않는다:**

```hcl
  statement {
    sid = "RunOnBastion"
    actions = [
      "ssm:SendCommand",
      "ssm:GetCommandInvocation",
      "ssm:DescribeInstanceInformation",
    ]
    # SendCommand 는 문서와 인스턴스 양쪽에 권한이 필요하다. 인스턴스는 태그로 한정.
    resources = ["*"]
  }
  statement {
    sid       = "ResolveBastion"
    actions   = ["ec2:DescribeInstances"]
    resources = ["*"] # DescribeInstances 는 리소스 한정을 지원하지 않는다(AWS 문서)
  }
  statement {
    # [2.4] ClearAlbParam — 스펙 §5.1.2 의 stale 방어 ①. 초안엔 이 권한도 상태도 없었다(F4).
    sid       = "ClearAlbParam"
    actions   = ["ssm:DeleteParameter"]
    resources = ["arn:aws:ssm:${var.region}:${data.aws_caller_identity.current.account_id}:parameter/cledyu-dr/failover/*"]
  }
  statement {
    # [4] ScaleNodes — 초안은 이 상태를 표에만 두고 IAM·HCL 을 안 만들었다(F8).
    sid       = "ScaleNodes"
    actions   = ["eks:UpdateNodegroupConfig", "eks:DescribeNodegroup", "eks:ListNodegroups"]
    resources = ["*"] # 노드그룹 ARN 은 이름을 런타임에 조회하므로 사전 특정 불가
  }
```

> `data.aws_caller_identity.current` 는 `image-baker.tf:1` 에 이미 있다 — 새로 선언하지 않는다.

**(b) 자식 SM 참조 statement 는 별도 정책으로 뺀다 — 이게 F1 사이클의 해법이다:**

```hcl
# ⚠️ 이 statement 들을 data.aws_iam_policy_document.dr_sfn 에 두면 terraform 사이클이다:
#   dr_run_on_bastion --depends_on--> aws_iam_role_policy.dr_sfn --policy--> data.dr_sfn
#     --resources--> dr_run_on_bastion.arn   ← 순환
# `terraform validate` 가 "Error: Cycle: ..." 로 거부한다(적대적 검증 3회차 F1 — 재현 확인).
# 자식 SM 은 dr_sfn 만 depends_on 하고 이 정책은 depends_on 하지 않으므로 여기선 안전하다.
# (메인 SM dr_failover 는 두 정책 다 depends_on 한다 — 자신이 참조되지 않으므로 사이클 없음)
data "aws_iam_policy_document" "dr_sfn_child" {
  statement {
    sid       = "StartChildSm"
    actions   = ["states:StartExecution"]
    resources = [aws_sfn_state_machine.dr_run_on_bastion.arn]
  }
  statement {
    sid = "ChildSmSync"
    # states:startExecution.sync 는 자식 실행을 폴링·중단하기 위해 아래가 필요하다(AWS 문서).
    actions   = ["states:DescribeExecution", "states:StopExecution"]
    resources = ["*"]
  }
  statement {
    sid = "ChildSmSyncEvents"
    # .sync 통합은 EventBridge 관리형 규칙으로 완료를 감지한다(AWS 문서).
    actions   = ["events:PutTargets", "events:PutRule", "events:DescribeRule"]
    resources = ["arn:aws:events:${var.region}:*:rule/StepFunctionsGetEventsForStepFunctionsExecutionRule"]
  }
}

resource "aws_iam_role_policy" "dr_sfn_child" {
  name   = "${var.name_prefix}-dr-sfn-child"
  role   = aws_iam_role.dr_sfn.id
  policy = data.aws_iam_policy_document.dr_sfn_child.json
}
```

- [ ] **Step 2: 자식 SM 정의**

```hcl
# ── 자식 SM: bastion 에서 스크립트 실행 (SSM 폴링) ──
# 폴링 로직을 여기 한 군데만 둔다. 메인 SM 은 states:startExecution.sync 로 호출하고,
# 실패 시 SFN 콘솔에 "어느 단계에서 죽었나"가 그대로 찍힌다.
resource "aws_sfn_state_machine" "dr_run_on_bastion" {
  name       = "${var.name_prefix}-dr-run-on-bastion"
  role_arn   = aws_iam_role.dr_sfn.arn
  depends_on = [aws_iam_role_policy.dr_sfn]

  logging_configuration {
    log_destination = "${aws_cloudwatch_log_group.dr_sfn.arn}:*"
    # 부모와 동일 — 실행 데이터에 taskToken 이 실릴 수 있다(Plan 1 §5.4).
    include_execution_data = false
    level                  = "ALL"
  }

  definition = jsonencode({
    Comment = "bastion 에서 스크립트 실행 — SSM 폴링(SFN 에 SSM .sync 통합 없음)"
    StartAt = "WaitForSsmAgent"
    States = {
      # ⚠️ module.eks_dr_endpoints 는 s3/kms/sts 만 만든다 — ssm/ssmmessages/ec2messages 인터페이스
      # 엔드포인트가 없어 bastion 의 SSM 에이전트는 **NAT 로 나가서 등록**해야 한다. 그 NAT 는 [2] 의
      # 같은 apply 에서 방금 생겼다. 등록 전 SendCommand 는 InvalidInstanceId 를 **동기 예외**로 던져
      # Choice·Wait 를 타지 못한다 → 등록을 먼저 기다린다(런북 :280 이 같은 창을 기록).
      WaitForSsmAgent = {
        Type     = "Task"
        Resource = "arn:aws:states:::aws-sdk:ssm:describeInstanceInformation"
        Parameters = {
          Filters = [{ Key = "InstanceIds", Values = [{ "Fn::Sub" = "" }] }]
        }
        # 실제 구현은 Step 3 참조 — Parameters 에 instanceId 를 넣고 결과가 비면 Wait 로 루프.
        Next = "SendCommand"
      }
      # (전체 ASL 은 Step 3 에서 완성한다)
    }
  })
}
```

> **Step 2 는 골격이다.** ASL 전체는 Step 3 에서 완성한다 — `jsonencode` 안의 JSONPath 는
> terraform `validate` 가 검사하지 않으므로 Step 4 의 실측이 유일한 검증이다.

- [ ] **Step 3: ASL 전체 작성**

`definition` 을 아래로 교체. **폴링 루프 4상태 + 에이전트 대기 3상태.**

```hcl
  definition = jsonencode({
    Comment = "bastion 에서 스크립트 실행 — SSM 폴링(SFN 에 SSM .sync 통합 없음)"
    # ⚠️ 실행 전체 상한. WaitCmd→GetResult→Done?→WaitCmd 는 **무한 루프**이고 Done? 의 Default 가
    # Failed 지만 Status 가 InProgress 로 계속 오면 영원히 돈다. SSM 의 executionTimeout 이 먼저 걸려
    # TimedOut 을 주는 게 정상 경로이나, 그마저 안 오는 경우(에이전트 죽음 등)의 backstop 이다.
    # 가장 긴 스크립트(08=3600) + 폴링 여유. 초과 시 States.Timeout → 부모의 Catch 가 잡는다.
    TimeoutSeconds = 4200
    StartAt        = "WaitForSsmAgent"
    States = {
      WaitForSsmAgent = {
        Type     = "Task"
        Resource = "arn:aws:states:::aws-sdk:ssm:describeInstanceInformation"
        Parameters = {
          Filters = [{ Key = "InstanceIds", "Values.$" = "States.Array($.instanceId)" }]
        }
        ResultPath = "$.agent"
        Retry = [{
          # ⚠️ 이 Retry 는 API 에러용이고 **미등록 인스턴스에는 안 걸린다** —
          # describeInstanceInformation 은 미등록 대상에 에러가 아니라 **빈 목록**을 준다.
          # 등록 대기는 아래 AgentReady?→WaitAgent 루프가 한다.
          ErrorEquals     = ["States.ALL"]
          IntervalSeconds = 20
          MaxAttempts     = 15
          BackoffRate     = 1.0
        }]
        Next = "AgentReady?"
      }
      # ⚠️ IsPresent 가드 필수(적대적 검증 3회차 F7). 에이전트 미등록이면 InstanceInformationList 가
      # **빈 배열**이라 $.agent.InstanceInformationList[0].PingStatus 경로 자체가 없다. 경로 없는
      # Variable 을 Choice 가 어떻게 다루는지(States.Runtime vs Default 낙하)는 **미확정**이나,
      # States.Runtime 이면 **어떤 Catch 로도 못 잡는다**(아래 §Task 5 Catch 절). IsPresent 를 먼저 두면
      # 어느 쪽이든 안전하므로 확인 전에 넣는다.
      #
      # ⚠️ **Step 5 스모크는 이 분기를 원리적으로 못 밟는다** — bastion 이 뜬 지 한참 뒤라 에이전트가
      # 이미 Online 이다. 즉 **드릴은 통과하고 실재해(방금 만든 bastion)에서만 터지는** 자리다.
      # C2(손으로 쓴 Z 포맷은 통과, 진짜 +0000 만 터짐)와 같은 패턴이라 코드로 막는다.
      "AgentReady?" = {
        Type = "Choice"
        Choices = [{
          And = [
            { Variable = "$.agent.InstanceInformationList[0].PingStatus", IsPresent = true },
            { Variable = "$.agent.InstanceInformationList[0].PingStatus", StringEquals = "Online" },
          ]
          Next = "BuildCommands"
        }]
        Default = "WaitAgent"
      }
      # env 와 script 를 배열 2원소로 만든다 — 문자열 조립을 하지 않는다(§Task 5 [7] 참조).
      # AWS-RunShellScript 는 commands 를 순서대로 같은 셸에서 실행하므로 env 의 export 가 script 에 적용된다.
      BuildCommands = {
        Type = "Pass"
        Parameters = {
          "instanceId.$"     = "$.instanceId"
          "timeoutSeconds.$" = "$.timeoutSeconds"
          "label.$"          = "$.label"
          "commands.$"       = "States.Array($.env, $.script)"
        }
        Next = "SendCommand"
      }
      WaitAgent = {
        Type    = "Wait"
        Seconds = 20
        Next    = "WaitForSsmAgent"
      }
      SendCommand = {
        Type     = "Task"
        Resource = "arn:aws:states:::aws-sdk:ssm:sendCommand"
        Parameters = {
          "InstanceIds.$" = "States.Array($.instanceId)"
          DocumentName    = "AWS-RunShellScript"
          "Comment.$"     = "$.label"
          # ⚠️ SendCommand 의 TimeoutSeconds 는 **배달 타임아웃**이다 — AWS 문서: "If this time is reached
          # and the command **hasn't already started running**, it won't run." 스크립트 실행 시간을
          # 제한하지 않는다. 초안은 여기에 $.timeoutSeconds 를 넣고 executionTimeout 을 3600 으로
          # 하드코딩해서, 스크립트별 타임아웃(03=600·07=1800…)이 **아무것도 제한하지 않고** 모든
          # 스크립트가 1시간까지 매달릴 수 있었다(적대적 검증 2026-07-15).
          # → 배달은 짧게 고정(에이전트가 이미 Online 인 것을 WaitForSsmAgent 가 확인했으므로 60s 면 충분),
          #    실행 제한은 executionTimeout 에 $.timeoutSeconds 를 넣는다.
          TimeoutSeconds = 60
          # ⚠️ S3(dr_backups) 로 보내지 않는다 — 3중으로 막혀 있다(스펙 §11.10):
          # bastion 에 s3:PutObject 없음 · SSE-KMS 쓰기용 GenerateDataKey 없음 ·
          # **Object Lock GOVERNANCE 30일**(WORM 금고에 드릴 로그가 30일씩 잠긴다).
          CloudWatchOutputConfig = {
            CloudWatchLogGroupName  = aws_cloudwatch_log_group.dr_bastion_commands.name
            CloudWatchOutputEnabled = true
          }
          Parameters = {
            # BuildCommands(Pass)가 만든 [env, script] 2원소 배열. AWS-RunShellScript 는 commands 를
            # 순서대로 **같은 셸**에서 실행하므로 앞 원소의 export 가 뒤 스크립트에 적용된다.
            # ⚠️ 문자열 조립(States.Format 에 스크립트 전문)을 하지 않는 이유는 §Task 5 [7] 참조 —
            # 스크립트의 작은따옴표·중괄호·개행이 intrinsic 을 깨서 정의가 거부된다.
            "commands.$" = "$.commands"
            # AWS-RunShellScript 의 executionTimeout 은 **문자열 배열**이다(SSM 문서 파라미터 규격).
            "executionTimeout.$" = "States.Array(States.Format('{}', $.timeoutSeconds))"
          }
        }
        ResultPath = "$.cmd"
        Retry = [{
          # 에이전트 등록 직후에도 잠깐 InvalidInstanceId 가 날 수 있다(전파 지연).
          # ⚠️ States.ALL 은 **반드시 단독**이어야 하고 마지막 retrier 여야 한다(AWS 문서).
          #    초안은 ["Ssm.InvalidInstanceIdException", "States.ALL"] 이었는데 CreateStateMachine 이
          #    **정의를 거부**해 terraform apply 가 실패한다. 게다가 그 에러명은 미검증 창작이었다.
          #    → States.ALL 단독으로 충분하다(어차피 모든 에러를 잡고, 등록 대기는 WaitForSsmAgent 가 한다).
          ErrorEquals     = ["States.ALL"]
          IntervalSeconds = 20
          MaxAttempts     = 10
          BackoffRate     = 1.5
        }]
        Next = "WaitCmd"
      }
      WaitCmd = {
        Type    = "Wait"
        Seconds = 30
        Next    = "GetResult"
      }
      GetResult = {
        Type     = "Task"
        Resource = "arn:aws:states:::aws-sdk:ssm:getCommandInvocation"
        Parameters = {
          "CommandId.$"  = "$.cmd.Command.CommandId"
          "InstanceId.$" = "$.instanceId"
        }
        ResultPath = "$.result"
        Retry = [{
          # 명령 직후엔 InvocationDoesNotExist 가 날 수 있다(전파).
          ErrorEquals     = ["States.ALL"]
          IntervalSeconds = 10
          MaxAttempts     = 5
          BackoffRate     = 1.5
        }]
        Next = "Done?"
      }
      "Done?" = {
        Type = "Choice"
        Choices = [
          {
            Or = [
              { Variable = "$.result.Status", StringEquals = "Pending" },
              { Variable = "$.result.Status", StringEquals = "InProgress" },
              { Variable = "$.result.Status", StringEquals = "Delayed" },
            ]
            Next = "WaitCmd"
          },
          { Variable = "$.result.Status", StringEquals = "Success", Next = "Succeeded" },
        ]
        Default = "Failed"
      }
      Succeeded = {
        Type = "Pass"
        # stdout 전문을 반환하지 않는다 — GetCommandInvocation 이 stdout 24,000자/stderr 8,000자로
        # 자르고, 그걸 6단계 누적하면 SFN 페이로드 상한(256KB)에 근접한다. 전문은 S3 에 있다.
        Parameters = {
          "status.$"       = "$.result.Status"
          "responseCode.$" = "$.result.ResponseCode"
          "stdoutTail.$"   = "$.result.StandardOutputContent"
          "commandId.$"    = "$.cmd.Command.CommandId"
          logGroup         = aws_cloudwatch_log_group.dr_bastion_commands.name
        }
        End = true
      }
      Failed = {
        Type  = "Fail"
        Error = "BastionScriptFailed"
        Cause = "SSM 명령 실패 — 자식 SM 실행 이력의 GetResult 결과에서 commandId 를 찾아 CloudWatch 로그그룹 ${...dr_bastion_commands.name} 에서 전문 확인: aws logs tail <그룹> --log-stream-name-prefix <commandId>"
      }
    }
  })
```

- [ ] **Step 4: 정적 검증**

Run:
```bash
cd /home/user/Cledyu/infra/terraform/aws
terraform fmt -check dr-orchestration.tf && terraform validate
```
Expected: 통과. **ASL 의 JSONPath 오류는 여기서 안 잡힌다** — Step 5 가 유일한 검증이다.
**단 F1 사이클은 여기서 잡힌다** — `Error: Cycle: data.aws_iam_policy_document.dr_sfn,
aws_iam_role_policy.dr_sfn, aws_sfn_state_machine.dr_run_on_bastion` 가 뜨면 Step 1 (b) 의
정책 분리가 안 된 것이다.

- [ ] **Step 5: 운영자 실측 — 자식 SM 이 실제로 도나**

> **⚠️ `env` 를 반드시 넘긴다(적대적 검증 3회차 F6).** 입력 계약이 "`env` 는 항상 채운다"이고
> `BuildCommands` 가 `States.Array($.env, $.script)` 를 하므로, **`env` 없이 호출하면 `$.env` 가 없어
> `States.Runtime` 으로 즉시 죽는다.** 초안의 이 Step 은 두 커맨드 다 `env` 를 빠뜨려서, **첫 실측이
> 실패하고 운영자가 멀쩡한 ASL 을 뜯게** 되어 있었다. 계획이 자기 계약을 자기 테스트로 위반한 것이다.

> **⚠️ `-target` 3개 필수 — SM 하나만 주면 절반만 적용된다(T2 실측 발견).** Global Constraints 의
> *"`-target` 은 의존성만 따라가고 의존하는 것은 안 따라간다"* 가 여기 그대로 적용된다:
> - ✅ 딸려옴: `dr_bastion_commands`(SM 이 참조) · `dr_sfn` 정책(SM 이 `depends_on`) ·
>   **`dr_failover_tf`**(그 정책이 CodeBuild ARN 을 참조 — 의존 사슬을 타고 여기까지 간다)
> - ❌ 안 딸려옴: **`dr_sfn_child`**(SM 을 *참조하는* 쪽 = 역방향) ·
>   **`eks_dr_bastion_command_logs`**(bastion 롤이라 SM 의 의존 그래프 밖)
>
> 후자를 빠뜨리면 **스모크는 통과하는데 로그 전문이 유실된다**(§11.11).
> ⚠️ **`-var` 3개도 필수** — bastion IAM 이 `count = local.eks_dr_enabled` 라 빠뜨리면 destroy 플랜이 된다.

```bash
cd /home/user/Cledyu/infra/terraform/aws
terraform apply -var enable_eks_dr=true -var eks_dr_active=true -var eks_dr_node_desired=0 \
  -target=aws_sfn_state_machine.dr_run_on_bastion \
  -target=aws_iam_role_policy.eks_dr_bastion_command_logs \
  -target=aws_iam_role_policy.dr_sfn_child
# Expected: Plan: 4 to add, 2 to change, 0 to destroy.
#   (2 change = dr_sfn 정책 statement 추가 + dr_failover_tf 의 concurrent_build_limit)

BASTION=$(aws ec2 describe-instances --region ap-northeast-2 \
  --filters Name=tag:Name,Values=cledyu-dr-bastion Name=instance-state-name,Values=running \
  --query 'Reservations[0].Instances[0].InstanceId' --output text)
ARN=$(aws stepfunctions list-state-machines --region ap-northeast-2 \
  --query "stateMachines[?name=='cledyu-lab-dr-run-on-bastion'].stateMachineArn" --output text)

# 성공 경로 — env=":" (셸 no-op), 메인 SM 의 6개 상태와 동일한 형태
aws stepfunctions start-execution --region ap-northeast-2 --state-machine-arn "$ARN" \
  --input "{\"instanceId\":\"$BASTION\",\"script\":\"echo hello && whoami\",\"env\":\":\",\"timeoutSeconds\":60,\"label\":\"smoke\"}" \
  --query executionArn --output text
```
Expected: `SUCCEEDED`, output 의 `stdoutTail` 에 `hello`, `responseCode: 0`.

```bash
# 실패 경로 — Fail 로 떨어지나
aws stepfunctions start-execution --region ap-northeast-2 --state-machine-arn "$ARN" \
  --input "{\"instanceId\":\"$BASTION\",\"script\":\"exit 3\",\"env\":\":\",\"timeoutSeconds\":60,\"label\":\"fail-test\"}"
```
Expected: `FAILED`, error `BastionScriptFailed`. **이게 안 되면 메인 SM 이 실패를 못 잡는다.**

**⚠️ `SUCCEEDED` 만 보고 넘어가면 안 된다 — CloudWatch 스트림을 직접 센다(T2 실측 발견, 스펙 §11.11).**
초안은 성공/실패/env 3종만 확인했는데, **셋 다 통과하는데 로그 전문이 유실**되고 있었다
(에이전트가 `DescribeLogGroups`/`CreateLogGroup` 에서 막히면 CloudWatch 출력을 포기하지만 **명령 자체는
Success 로 끝난다**). `SUCCEEDED` 는 "명령이 돌았다"이지 "로그가 남았다"가 아니다:

```bash
# 출력의 commandId 로 스트림이 실제로 생겼는지 — **개수를 센다**
CMD=<위 실행 output 의 commandId>
aws logs describe-log-streams --region ap-northeast-2 --log-group-name /aws/ssm/cledyu-lab-dr-failover \
  --log-stream-name-prefix "$CMD" --query 'logStreams[].logStreamName' --output json
aws logs tail /aws/ssm/cledyu-lab-dr-failover --region ap-northeast-2 --since 10m --log-stream-name-prefix "$CMD"
```
Expected: `<commandId>/<instanceId>/aws-runShellScript/stdout` **1개 이상** + 스크립트 출력이 실제로 읽힘.
**`[]` 면 bastion 의 logs IAM 이 틀린 것이다** — 에이전트 로그가 정답을 알려준다(추론하지 말 것):
```bash
aws ssm send-command --region ap-northeast-2 --instance-ids <BASTION> --document-name AWS-RunShellScript \
  --parameters 'commands=["grep -iE \"cloudwatch|denied\" /var/log/amazon/ssm/amazon-ssm-agent.log | tail -20"]'
```

```bash
# env 주입 경로 — [7] 이 SNAPSHOT_KEY 를 받는 바로 그 메커니즘. 여기서 검증해두면 T5 에서 안 헤맨다.
aws stepfunctions start-execution --region ap-northeast-2 --state-machine-arn "$ARN" \
  --input "{\"instanceId\":\"$BASTION\",\"script\":\"echo \\\"got=\$SNAPSHOT_KEY\\\"\",\"env\":\"export SNAPSHOT_KEY=vault/test.snap\",\"timeoutSeconds\":60,\"label\":\"env-test\"}"
```
Expected: `SUCCEEDED`, `stdoutTail` 에 `got=vault/test.snap`. **이게 되면 드롭다운→[7] 경로가 산다.**

- [ ] **Step 6: Commit** (사용자가 실행)

```bash
cd /home/user/Cledyu
git add infra/terraform/aws/dr-orchestration.tf infra/terraform/aws/README.md
git commit -m "feat(dr): bastion 스크립트 실행 자식 상태 머신(SSM 에이전트 대기 + 폴링)"
```

---

### Task 3: bastion 스크립트 7개 + bastion IAM

> **런북(`docs/RUNBOOK/dr-eks-bootstrap.md`)이 진실의 원천이다. 재작성하지 말고 이식한다** —
> 7/14 풀 E2E 드릴로 검증된 명령이다.

**Files:**
- Create: `infra/terraform/aws/scripts/bastion/03-clean-warm-etcd.sh`
- Create: `infra/terraform/aws/scripts/bastion/06-bootstrap-apps.sh`
- Create: `infra/terraform/aws/scripts/bastion/07-restore-vault.sh`
- Create: `infra/terraform/aws/scripts/bastion/08-restore-data.sh`
- Create: `infra/terraform/aws/scripts/bastion/09-wait-apps-ready.sh`
- Create: `infra/terraform/aws/scripts/bastion/11-restart-apps.sh`
- Create: `infra/terraform/aws/scripts/bastion/12-verify-serving.sh`
- **Modify: `infra/terraform/aws/eks-dr-bastion.tf`** — bastion 롤에 `ssm:PutParameter`(Step 8)
- **Modify: `infra/terraform/aws/dr-failover-buildspec.yml`** — `-target` 에 새 정책 추가(Step 8)
- Modify: `infra/terraform/aws/README.md`

> **⚠️ 초안의 Files 엔 `.tf` 가 없었다 — 그게 F3 의 직접 원인이다**(적대적 검증 3회차). `09-` 가
> `aws ssm put-parameter` 를 호출하는데 그 IAM 을 아무 Task 도 만들지 않았다. **스크립트를 만드는 Task 는
> 그 스크립트가 호출하는 AWS API 의 IAM 도 같이 만든다**(Global Constraints).

**Interfaces:**
- Consumes: 없음(각 스크립트는 bastion 에서 독립 실행)
- Produces: [9] 가 SSM 파라미터 `/cledyu-dr/failover/alb-hostname` 에 ALB 호스트명을 쓴다 → Task 4 의 dns-switch 가 읽는다
- Produces: [7] 이 `SNAPSHOT_KEY` 환경변수를 받는다 — Task 5 의 [7] 이 승인 output 의 `snapshot` 을 주입

**대응표 — 이식 3 / 재배치 1 / 신규 3 (적대적 검증 **4회차** 2026-07-15 로 재정정)**

> **초안은 7개 전부 "런북 이식"이라 적었으나 거짓이었다.** 3개는 런북에 원본이 없다. 이식과 신규는
> **난이도·위험이 다르다** — 이식은 7/14 드릴로 검증된 명령을 옮기는 것이고, 신규는 틀릴 수 있다.
> 줄번호도 3건이 틀려서 `06-` 이 CNPG 가드를, `08-` 이 real-DR flip 을 먹고 있었다(실제 `### ` 경계로 정정).
>
> **⚠️ 그런데 2회차(A1)는 개수(7→4)만 고치고 남은 4개의 *내용*은 아무도 대조하지 않았다.** 4회차에서
> 실제로 원본과 대조하니 **`06` 은 그대로 옮기면 건강한 DR 에서 실패**하고(H1), **`08` 은 이식이 아니라
> 의미 재배치**였다(H5). **진단은 맞았는데 처방이 절반이었다** — 스펙 §11.5 가 기록한 그 패턴의 재발이다.
> **"이식"이라 적힌 것도 원본을 열어 한 줄씩 대조한다. 라벨은 검증이 아니다.**

| 스크립트 | 성격 | 원본 | 위험 |
|---|---|---|---|
| `03-clean-warm-etcd.sh` | 🆕 **신규** | **없음** — P1c 는 7/14 드릴 *관찰*만 있고 런북에 미반영 | ⚠️ **정리 대상 이름 미확정**(Step 1) |
| `06-bootstrap-apps.sh` | 📋 이식 **+ 게이트 2줄 제거·clone 멱등화** | **272-331** (§apps-eks 부트스트랩) | ⚠️ **중간 — H1·H2**(그대로 옮기면 오탐 실패 + 재실행 불가) |
| `07-restore-vault.sh` | 📋 이식 | **75-160** (§복원 절차) + **161-194** (§Vault k8s auth 재설정) | ⚠️ 높음 — 변환 4곳 |
| `08-restore-data.sh` | 🔀 **재배치**(이식 아님) | **332-343** 의 delete 2줄을 **다른 시점에** 실행 | ⚠️ **중간 — PVC 재사용 미검증**(Step 10 ③) |
| `09-wait-apps-ready.sh` | 🆕 **신규 조립** | 체크리스트 조각(`:359`·`:364`) + `:409`(Keycloak wait) **재배치** | 중간 |
| `11-restart-apps.sh` | 📋 이식 | **420-433** (§api·web 재기동) | 중간 — 게이트 2개(스펙 §5.1.3) |
| `12-verify-serving.sh` | 🆕 **신규** | **없음** — 체크리스트 `:370` 은 한 줄짜리 항목. 스펙 §5.1.4 가 설계 | ⚠️ **중간 — `psql` 호출 선례 준수**(H3) |

**⚠️ `08-` 은 343 까지다.** 344 부터는 §real-DR: DR-창 쓰기 캡처(`backupEnabled=true` flip)인데,
**스펙 §8.1 이 "수동 PR"로 결정**한 것이라 **자동화에 들어가면 안 된다**(git push 토큰이 재해 경로에 필요해짐).

**⚠️ `09-` 는 이식이 아니다.** 체크리스트 `:357-370` 은 `- [ ]` 항목에 명령 조각이 백틱으로 박힌
**사람용 확인표**이고, 게다가 [6][7][8][10][11][12] 까지 전부 다루는 **마스터 체크리스트**다 — [9] 의
원본이 아니다. 그리고 **기다리는 명령이 없다**:

| 대상 | 런북에 있는 것 | 우리가 써야 하는 것 |
|---|---|---|
| Kafka | `get kafka`(READY=True) — **눈으로 확인** | `wait --for=condition=Ready` |
| validation-engine | `get deploy`(Available) — **눈으로 확인** | `rollout status` |
| Keycloak | `wait --for=condition=Ready` ✅ | 그대로 — 단 `:409` 는 **§DNS 전환 섹션 안**이라 [9] 로 **재배치** |

**SSM 변환 규칙 — 7개 전부 공통:**

1. **`kubectl exec -it` → `-it` 제거.** SSM RunCommand 엔 TTY 가 없다
2. 상단에 `#!/bin/bash` + `set -euo pipefail`
2.5. **⚠️ `export HOME=/root` 를 반드시 넣는다(T3 실측 발견, 스펙 §11.12).**
   SSM RunCommand 는 root 로 돌지만 **`HOME` 을 설정하지 않는다**(실측: `HOME=[]`, `PWD=/usr/bin`).
   `aws eks update-kubeconfig` 는 `/root/.kube/config` 에 **정상적으로 쓰는데**, kubectl 은 `$HOME/.kube/config`
   를 찾다가 못 찾고 **기본값 `localhost:8080` 으로 폴백**해 `connection refused` 로 죽는다.
   **에러가 원인을 안 가리킨다** — "설정 없음"이 아니라 "연결 거부"로 보인다.
   - 런북이 이걸 못 잡은 이유: 런북은 **사람용**이고 `aws ssm start-session` 은 로그인 셸이라 `HOME=/root` 다.
     **명령은 맞는데 실행 환경이 다르다** — 규칙 1(TTY 없음)과 **같은 뿌리**(대화형 셸이 아니다)인데
     초안은 증상 하나만 잡았다.
   - `KUBECONFIG` 명시보다 `HOME` 이 낫다 — `helm`(06)·`aws` 도 `$HOME` 을 쓴다.
3. **수동 플레이스홀더를 변수 캡처로** — 런북의 `<INIT_ROOT>`·`<TS>` 등은 사람이 눈으로 보고 넣는 값이다
4. **확인 스텝을 게이트로 — 단 "그 시점에 이미 참인 것"만.** 런북이 "확인한다"라고만 한 곳을
   `|| exit 1` 로 강제하되, **아직 참이 아닌 게 정상인 것을 게이트하면 건강한 DR 에서 오탐한다.**
   런북의 확인 항목은 사람이 **폴링**하는 것이라 "지금 없으면 좀 있다 다시 본다"가 기본 동작이다.
   → **없는 게 정상인 시점이면 게이트가 아니라 `wait`/폴링으로 옮기거나 아예 뺀다**(H1 이 이 함정이다)
5. **`A && B` 를 게이트로 착각하지 않는다(H2).** `set -e` 는 AND-OR 리스트의 **마지막이 아닌** 명령의
   실패를 **안 잡는다**(실측 확인). 런북은 이 관용구를 많이 쓴다:
   - `git clone ... && cd ~/Cledyu` → **clone 이 실패해도 스크립트가 계속 간다**(H2 — 06 재실행에서 발현)
   - `command -v kubectl git helm aws >/dev/null && echo OK || echo "⚠ ..."` → **항상 성공**한다(게이트 아님)
   - 반대로 `grep -q X && { exit 1; }` 는 **이 성질 덕에 안전**하다(매치 실패가 스크립트를 안 죽인다)
   → 게이트로 만들려면 `A || { echo ...; exit 1; }` 형태로 **명시 변환**한다
6. **재실행 가능해야 한다(멱등).** T3 Step 10 이 스크립트를 하나씩 돌리며 "실패 → 고침 → 재실행" 하고,
   SFN 도 재시도할 수 있다. `git clone`·`create` 계열은 두 번째 실행에서 실패하므로 명시 처리한다
7. `set -x` 로 실행 로그를 남긴다(stdout 이 S3 로 가므로 디버깅 자산)

> **⚠️ 7번(`set -x`)은 `07-restore-vault.sh` 에 그대로 적용하면 안 된다(리뷰 지적 C6).** `set -x` 는 실행되는
> 명령을 **인자까지** 추적하므로 Vault init 출력·`$INIT_ROOT`·`$NEWROOT`·recovery 키가 stdout/stderr 에
> 찍힌다. 그 출력은 자식 SM 이 **CloudWatch 로그그룹**(`CloudWatchOutputConfig`)으로 보내고 `stdoutTail` 로
> **Discord 실패 알림**에도 실린다 → **Vault 루트 토큰 평문 노출.**
> → `07-` 은 **시크릿 구간에서 `set +x`** 로 끄고 그 밖에서만 켠다(아래 Step 3).

- [ ] **Step 1: `03-clean-warm-etcd.sh` (🆕 신규 — P1c)** ⚠️ **정리 대상 이름이 미확정이다**

> **이 스크립트는 Step 10 실측 전까지 미완성이다.** 아래 webhook 이름(`aws-load-balancer-webhook`)은
> **계획 작성자가 지어낸 것**이고 ALB 컨트롤러의 실제 리소스명을 확인한 적이 없다. 틀리면
> `--ignore-not-found` 때문에 **조용히 통과**하고 coredns 는 여전히 CREATE_FAILED 로 죽는다 —
> **P1c 를 고쳤다고 믿는데 안 고쳐진 상태**가 된다. 런북에 원본이 없어 베낄 수도 없다(7/14 드릴 *관찰*만 존재).
>
> **Step 10 에서 실제 이름을 확정한 뒤 이 스크립트를 완성한다:**
> ```bash
> # 노드가 뜨기 전, bastion 에서 (kubectl 은 warm API 서버에 닿는다)
> kubectl get validatingwebhookconfiguration -o name | grep -i 'load-balancer\|alb'
> kubectl get mutatingwebhookconfiguration   -o name | grep -i 'load-balancer\|alb'
> ```
> 반복 failover 가 아니면 **아무것도 안 나오는 게 정상**이다(고아는 이전 사이클 잔존물). 그 경우
> "이름 미확정"이 남으므로 계획에 그대로 두고, **재-failover 드릴에서 확정**한다.

```bash
#!/bin/bash
# [3] CleanWarmEtcd — 이전 사이클 잔존물 정리.
#
# warm etcd 는 failover 사이클 간 살아남는다. 7/14 드릴 발견(P1c): 고아 ALB webhook 이 남아 있으면
# coredns 애드온이 CREATE_FAILED 로 죽는다 → [5] InstallAddons 가 타임아웃. **런북에 미반영이라 신규 작성.**
# 노드 없이도 kubectl 로 지울 수 있다(API 서버는 warm 에 상시).
set -euo pipefail
set -x

cloud-init status --wait
command -v kubectl aws >/dev/null || { echo "❌ user_data 미완 — /var/log/cloud-init-output.log"; exit 1; }
aws eks update-kubeconfig --name cledyu-dr --region ap-northeast-2

# stale SSM 파라미터 삭제는 [2.4] ClearAlbParam(SDK)이 한다 — 여기가 아니다(스펙 §5.1.2).
# bastion·SSM 에이전트에 의존하지 않는 게 안전하고 IAM 도 SFN 롤 하나로 끝난다.
# (초안은 이 주석이 [2.5] ResolveBastion 을 가리켰는데 **그 구현이 없었다** — F4. 이제 T5 에 실재한다)

# ⚠️ 아래 이름은 **미확정**이다(위 경고 참조). Step 10 에서 실측 후 확정한다.
kubectl delete validatingwebhookconfiguration aws-load-balancer-webhook --ignore-not-found
kubectl delete mutatingwebhookconfiguration aws-load-balancer-webhook --ignore-not-found

# P1d(stale hostAlias)도 같은 성격의 warm etcd 잔존물이다. api Deployment 가 아직 없으면 no-op.
# git 에 hostAliases 가 없으므로(git log -S 확인) 이건 런타임 주입분이다.
kubectl -n api get deploy api >/dev/null 2>&1 && \
  kubectl -n api patch deploy api --type json \
    -p '[{"op":"remove","path":"/spec/template/spec/hostAliases"}]' 2>/dev/null || true

echo "✅ warm etcd 정리 완료"
```

- [ ] **Step 2: `06-bootstrap-apps.sh`**

런북 **272-331**(§apps-eks 부트스트랩)의 **3개 bash 블록을 이어붙인다** — 런북이 "동일한 bastion 쉘
세션에서 이어서 실행"을 요구하고(가드의 상대경로 grep 이 앞 블록의 `cd`·`REPO_ROOT` 에 의존), SSM 은
호출마다 새 쉘이라 **한 스크립트로 합쳐야** 한다. 변환:
- `git clone` — 레포가 **PUBLIC** 이라 PAT 불요(런북의 조건부 무시). **단 멱등화 필수 — 아래 H2**
- placeholder 가드·targetRevision 가드는 **그대로 유지**(회귀 방어)
- `helm upgrade --install argocd ... --wait` 그대로 — 멱등이라 재실행 안전([C4])
- **런북 마지막 2줄(`clusterissuer`·`root-ca-bundle` 확인)은 게이트로 옮기지 않는다 — 아래 H1**

> **⚠️ H1 — 런북 마지막 2줄을 그대로 옮기면 건강한 DR 에서 실패한다(적대적 검증 4회차).**
> 런북 272-331 의 끝은 이렇다:
> ```bash
> kubectl get clusterissuer cledyu-ca
> kubectl -n api get configmap cledyu-root-ca-bundle   # trust-manager Bundle 분배 확인
> ```
> 사람용 **확인**이라 없으면 기다렸다 다시 보는 것이고, `set -euo pipefail` 아래선 **하드 게이트**가 된다.
> 그런데 **`service-api.yaml:12` 가 `sync-wave: "2"`** 라, root-app apply + cert-manager Available(300s)
> 직후엔 **api 네임스페이스 자체가 없다** → `Error from server (NotFound)` → `exit 1`.
> **이건 Global Constraints 가 "과대 게이트 → 건강한 DR 에서 오탐"이라 경고한 G1 함정 그 자체다** —
> 초안은 그 경고를 `09` 에만 적용하고 `06` 은 "위험: 낮음 / 이식"으로 평가했다.
> → **두 줄을 뺀다.** `clusterissuer`·Bundle 은 **[9] 가 기다리는 앱들의 선행 조건**이라
> ([9] 의 Kafka 가 `cert-manager CA + trust-manager Bundle` 에 의존 — 런북 체크리스트 `:359`)
> **[9] 에서 자연히 게이트된다.** 여기서 중복 검사할 이유가 없다(09 의 설계 결정 3 과 같은 논리).

> **⚠️ H2 — `git clone` 은 멱등이 아니고, `set -e` 가 그 실패를 안 잡는다(적대적 검증 4회차, 실측).**
> ```
> $ set -euo pipefail; git clone <repo> Cledyu && cd Cledyu; echo "살아남음"
> fatal: destination path 'Cledyu' already exists and is not an empty directory.
> 살아남음        ← set -e 가 안 죽였다
> ```
> `A && B` 는 AND-OR 리스트라 **마지막이 아닌 A 의 실패를 `set -e` 가 면제**한다(변환 규칙 5).
> 결과: clone 이 실패해도 계속 가고, `cd` 가 안 된 채 **뒤의 `REPO_ROOT=$(git rev-parse --show-toplevel)`
> 에서 엉뚱한 에러로 죽는다**(진짜 원인은 clone). **T3 Step 10 이 "실패 → 고침 → 재실행"이라 증분
> 드릴에서 반드시 밟는다.** 초안은 "`helm upgrade --install` 은 멱등이라 재실행 안전"이라 적었지만
> **clone 은 멱등이 아니다** — 멱등한 건 helm 이고 clone 이 아닌데 그 문장이 안심시켰다.
> → **명시적으로 멱등화한다:**
> ```bash
> # 재실행 안전 — 이미 있으면 최신 main 으로 맞춘다(clone 은 두 번째 실행에서 실패한다).
> if [ -d ~/Cledyu/.git ]; then
>   git -C ~/Cledyu fetch --prune origin
>   git -C ~/Cledyu reset --hard origin/main
> else
>   rm -rf ~/Cledyu
>   git clone https://github.com/requset700k/Cledyu.git ~/Cledyu
> fi
> cd ~/Cledyu   # ⚠️ && 로 잇지 않는다 — 실패가 묻힌다(위 실측)
> ```
> 런북의 도구 확인도 같은 함정이다 — `command -v kubectl git helm aws >/dev/null && echo OK || echo "⚠"`
> 는 **항상 성공**해서 게이트가 아니다. `03` 처럼 `|| { echo ...; exit 1; }` 로 명시 변환한다.

- [ ] **Step 3: `07-restore-vault.sh`** ⚠️ **가장 어려운 스크립트**

런북 **75-160**(§복원 절차) + **161-194**(§Vault k8s auth 재설정) 이식.

> **🔒 이 스크립트만 `set -x` 를 쓰지 않는다(리뷰 지적 C6).** Vault init 출력·`$INIT_ROOT`·`$NEWROOT`·
> recovery 키가 stdout 에 찍히면 자식 SM 이 그걸 **S3 백업 버킷**에 저장하고 `stdoutTail` 로
> **Discord 알림**에도 싣는다 → **루트 토큰 평문 노출.** 구조:
> ```bash
> set -euo pipefail
> set +x                      # ← 기본 OFF. 시크릿을 만지는 구간 전체.
> # ... init / restore / generate-root / k8s auth ...
> set -x                      # ← 시크릿을 다 쓴 뒤(ESO 재기동 등)만 켠다
> ```
> **echo 로도 흘리지 않는다** — 확인 게이트는 `grep -q` 로 **값을 출력하지 않고** 판정한다.

**핵심 변환 4가지:**

```bash
# (a) init 출력에서 root token 캡처 — 런북의 <INIT_ROOT> 플레이스홀더 대체
INIT=$(kubectl -n vault exec vault-0 -- sh -c \
  'VAULT_CACERT=/vault/tls/ca.crt vault operator init -format=json')
INIT_ROOT=$(echo "$INIT" | jq -r .root_token)

# (b) 스냅샷 키를 환경변수로 — 런북의 `s3 ls | tail -1`(최신 고정)이 아니라
#     **승인 시 고른 값**을 쓴다. 이게 드롭다운의 존재 이유다.
: "${SNAPSHOT_KEY:?SNAPSHOT_KEY 미지정 — 승인 output 의 snapshot 을 주입해야 한다}"
aws s3 cp "s3://cledyu-lab-dr-backups/${SNAPSHOT_KEY}" ./vault-raft.snap

# (c) generate-root 루프 — 런북 그대로(마지막 키만 encoded_root_token 을 낸다)
# (d) ESO 재기동 — 런북 :177-181 의 드릴 실측. 빠지면 이후 모든 Secret 이 안 생긴다.
kubectl -n external-secrets rollout restart deploy/external-secrets
kubectl -n external-secrets rollout status deploy/external-secrets --timeout=120s
```

**게이트(런북이 "확인한다"라고만 한 곳):**
```bash
# 복원 확인 — 비어 있으면 복원 실패
kubectl -n vault exec vault-0 -- sh -c \
  "VAULT_CACERT=/vault/tls/ca.crt VAULT_TOKEN=$NEWROOT vault kv list cledyu" | grep -q . \
  || { echo "❌ cledyu kv 가 비었다 — 복원 실패"; exit 1; }
```

- [ ] **Step 4: `08-restore-data.sh`** 🔀 **이식이 아니라 재배치다**

> **⚠️ H5 — 초안은 이걸 "📋 이식 / 위험: 낮음"이라 적었으나 둘 다 틀렸다(적대적 검증 4회차).**
> 런북 332-343 은 **"root-app 적용(위) 직후, CNPG 차트가 `Cluster` CR 을 만들기 전에"** 지우라고 명시한다
> — 즉 **차트가 CR 을 만들기 전 창에서 stale CR 을 치우는** 동작이다. 우리는 [7] Vault 복원(~30분) **뒤**에
> 지운다 = **ArgoCD 가 이미 만든 CR 을 지우고 재생성을 기다리는 다른 동작**이다. 명령 2줄이 같을 뿐이다.
>
> **재배치가 오히려 옳다(그래서 유지한다):** [7] 전엔 ESO 가 Vault 를 못 읽어
> `postgres-credentials-cnpg` 를 못 만들고, 그 Secret 은 `managed.roles[].passwordSecret` 이라
> [6] 시점에 생긴 CR 은 어차피 제대로 뜨지 못한다. [7] 뒤에 지우고 재생성하는 게 자연스럽다.
>
> **전제는 확인했다** — 재생성은 ArgoCD selfHeal 에 달렸고, `data-postgres-cnpg-dr.yaml:31` ·
> `data-keycloak-pg-dr.yaml:32` 둘 다 **`selfHeal: true`** 다 ✅ (런북 경로는 "첫 sync 가 만든다"라
> selfHeal 이 필요 없었다 — 재배치가 **새 의존을 만들었고** 그게 마침 충족된 것이다).
>
> **🔴 미검증 1건 — Step 10 에서 확인한다.** `kubectl delete cluster` 시 **PVC 가 함께 지워지는지**.
> 남아서 재생성된 Cluster 가 그걸 재사용하면 **S3 복원이 아니라 stale 데이터로 뜬다** — `wait
> --for=condition=Ready` 는 **통과하고**, [12] 의 `count(*) > 0` 도 **통과한다**(행이 있긴 하니까).
> **무음 데이터 오류**라 이 계획에서 가장 조용한 실패 경로다. 확인:
> ```bash
> kubectl -n postgres get pvc -l cnpg.io/cluster=cledyu-pg   # delete 전후로 비교
> ```
> 지워지지 않으면 `08-` 에 PVC 삭제를 명시 추가한다(delete 뒤·재생성 대기 앞).

런북 **332-343**(§CNPG 재-failover 가드)의 **delete 2줄** + **Ready 대기(신규 — 아래 주의)**:

```bash
#!/bin/bash
# [8] RestoreData — 구 CNPG CR 제거 → ArgoCD 재생성 → bootstrap.recovery 가 최신 S3 로 복원.
set -euo pipefail
set -x

# [P1b] 재-failover 시 잔존 CNPG CR 제거. 단발(첫) failover 는 CR 이 없어 no-op. (런북 338-342 이식)
kubectl -n postgres delete cluster cledyu-pg --ignore-not-found
kubectl -n keycloak delete cluster keycloak-pg --ignore-not-found

# ⚠️ 아래 대기는 **런북에 없다 — 신규다**(리뷰 지적). 런북 332-343 의 실제 내용은 delete 2줄이 전부고,
# 사람이 체크리스트(:363)로 "cledyu-pg-rw Ready" 를 눈으로 확인한다.
#
# **방금 지운 CR 을 바로 기다리면 안 된다** — ArgoCD 가 재생성하기 전이라 객체가 없어 `wait` 가
# "no matching resources found" 로 **즉시 에러**난다(초안의 결함). ArgoCD sync 를 기다린 뒤 대기한다.
for i in $(seq 1 60); do
  kubectl -n postgres get cluster cledyu-pg >/dev/null 2>&1 && \
  kubectl -n keycloak get cluster keycloak-pg >/dev/null 2>&1 && break
  echo "ArgoCD 가 CNPG CR 을 재생성하기를 대기 $i/60"; sleep 10
done
kubectl -n postgres get cluster cledyu-pg  >/dev/null || { echo "❌ ArgoCD 가 cledyu-pg 를 재생성하지 않음"; exit 1; }
kubectl -n keycloak get cluster keycloak-pg >/dev/null || { echo "❌ ArgoCD 가 keycloak-pg 를 재생성하지 않음"; exit 1; }

# bootstrap.recovery 는 S3 에서 base+WAL 을 받아 재생하므로 오래 걸린다.
kubectl -n postgres wait --for=condition=Ready cluster/cledyu-pg  --timeout=1200s
kubectl -n keycloak wait --for=condition=Ready cluster/keycloak-pg --timeout=1200s

echo "✅ cledyu-pg·keycloak-pg Ready (S3 복원 완료)"
```

> **⚠️ 344 부터는 이식하지 않는다.** §real-DR: DR-창 쓰기 캡처(`backupEnabled: false → true` flip)는
> **스펙 §8.1 이 "수동 PR"로 결정**한 것이다 — 자동화하면 재해 중에 main 으로 push 할 GitHub 자격이
> 필요해지고, 온프렘 앱들도 같은 repoURL·main 을 sync 하므로 폭발 반경이 **살아 있는 운영 클러스터**까지
> 닿는다. [13] NotifyComplete 가 "지금 이 PR 을 올리세요"로 대신한다.

- [ ] **Step 5: `09-wait-apps-ready.sh` (🆕 신규 조립)**

> **이식이 아니다.** 런북 체크리스트(`:357-370`)엔 **기다리는 명령이 없다** — 사람이 `get` 으로 보고
> 판단하는 표다. 조각을 모아 기계 게이트로 만든다. **G1 함정 자리다**(Global Constraints 참조).

**설계 결정 3가지:**

1. **셋 다 기다린 뒤 DNS 로 간다.** Kafka·VE 는 DNS 전환의 전제가 아니라 랩 채점용이라
   "Keycloak 만 기다리고 DNS 를 먼저 넘기면 RTO 가 몇 분 줄어든다". **그래도 다 기다린다** —
   (a) Cledyu 는 실습 플랫폼이라 **랩이 안 되는데 "DR 완료"는 반쪽**이다, (b) 이득이 몇 분인데
   RTO 병목은 사람의 승인 판단이지 여기가 아니다.
   > **⚠️ 초안은 근거에 "런북 순서 유지 — 7/14 드릴이 그 순서로 검증됐고 바꾸면 재검증이 필요하다"를
   > 들었으나 사실이 아니다(H4, 4회차).** 런북 체크리스트(`:357-370`)의 순서는
   > **Kafka Ready → Vault 복원 → CNPG 가드 → VE → Keycloak** 인데, 우리는 **[7] Vault → [8] CNPG →
   > [9] Kafka·VE·Keycloak** 이다 — **이미 Kafka 를 Vault 뒤로 옮겨놨다.** "런북 순서를 지키니까
   > 안전하다"는 논증으로 **자기가 이미 바꾼 것을 정당화**한 셈이다.
   > **무해하다는 근거는 따로 있다**: Kafka 의 의존은 `cert-manager CA + trust-manager Bundle + gp3`
   > 이고 **Vault 와 무관**하다(런북 체크리스트 `:359` 명시). VE 만 Vault→ESO 에 의존하는데 [9] 안에서
   > Kafka 뒤에 온다. **드릴이 검증한 것은 "의존 순서"이지 "줄 순서"가 아니다.**
2. **오퍼레이터의 Ready 조건에만 의존한다.** 상태를 직접 파싱하지 않는다 — G1 은 사람용 `grep` 을
   기계로 옮기다 났다. Strimzi 공식 문서가 `kubectl wait kafka/my-cluster --for=condition=Ready` 를 쓴다.
3. **bootstrap svc 응답 확인은 제외한다.** 런북 체크리스트가 `cledyu-kafka-kafka-bootstrap.kafka.svc:9093`
   응답을 보라고 하지만, **Kafka CR 이 Ready 면 리스너가 준비된 것**이고(Strimzi 가 `status.listeners` 를
   채움) 9093 은 TLS 라 `curl` 로 검사가 안 된다. **어설프게 만들면 G1 처럼 틀린다** — 중복 검사를 빼는 게 낫다.

```bash
#!/bin/bash
# [9] WaitAppsReady — 앱이 다 뜰 때까지 대기 + [10] 이 쓸 ALB 호스트명 기록.
#
# ⚠️ 런북 체크리스트(:357-370)는 사람용 확인표라 "기다리는 명령"이 없다 → 조각을 모아 조립했다.
#   Kafka        : :359 `get kafka`(READY=True)  → wait --for=condition=Ready (Strimzi 공식 문서 방식)
#   VE           : :364 `get deploy`(Available)  → rollout status
#   Keycloak     : :409 의 wait 를 **여기로 재배치** (원래 §DNS 전환 안에 있다 — [10] 은 non-VPC
#                  Lambda 라 kubectl 을 못 쓰므로 게이트가 여기로 와야 한다)
# ⚠️ 런북이 "미배포는 정상"이라 적어둔 것들(ServiceMonitor 2종·CiliumNetworkPolicy·plain NetworkPolicy·
#   lab-ssh-key)은 **게이트하지 않는다** — EKS 에선 없는 게 정상이라 검사하면 건강한 DR 에서 오탐한다.
set -euo pipefail
set -x

kubectl -n kafka wait --for=condition=Ready kafka/cledyu-kafka --timeout=900s

# 토픽도 CR 이고 오퍼레이터가 Ready 를 관리한다 → 이름 하드코딩 없이 전부 대기.
# ⚠️ KafkaTopic 에 Ready 조건이 있는지는 **Step 10 실측에서 확인**한다(Kafka CR 은 Strimzi 문서로
#   확인했으나 KafkaTopic 은 미확인). 없으면 이 줄을 빼고 Kafka CR Ready 만 게이트한다.
#   이름을 박지 않는 이유: 랩이 늘면 토픽이 느는데 스크립트를 안 고치면 **새 토픽을 안 기다리고 통과**한다.
kubectl -n kafka wait --for=condition=Ready kafkatopic --all --timeout=300s

# VE 선행: Kafka(KafkaUser·client cert) + **Vault 복원→ESO 로 cledyu-validation-engine-aws Secret**
# (AWS 키 non-optional) — [7]·[8] 이 끝난 뒤라 충족돼 있다(:365).
kubectl -n validation-engine rollout status deploy/validation-engine --timeout=600s

# auth 는 Keycloak Ready 이후에만 넘긴다 — 조기 전환 시 ALB keycloak 타겟 unhealthy → 404/503(:406).
kubectl -n keycloak wait --for=condition=Ready keycloak/cledyu-keycloak --timeout=600s

# [10] SwitchDNS 가 읽을 ALB 호스트명 — non-VPC Lambda 는 private EKS 에 못 닿고
# 자식 SM 은 stdout 을 S3 로 버리므로 SSM 파라미터가 유일한 전달 경로다(스펙 §5.1.2).
ALB=$(kubectl -n api get ingress api -o jsonpath='{.status.loadBalancer.ingress[0].hostname}')
[ -n "$ALB" ] || { echo "❌ ALB 호스트명 비어있음 — Ingress 미프로비저닝"; exit 1; }
aws ssm put-parameter --region ap-northeast-2 --name /cledyu-dr/failover/alb-hostname \
  --type String --overwrite --value "$ALB"

echo "✅ Kafka·VE·Keycloak Ready · ALB=$ALB"
```

- [ ] **Step 6: `11-restart-apps.sh`**

런북 420-433 이식. **게이트 2개 필수(설계 §5.1.3)**:
```bash
kubectl -n api rollout restart deploy/api && kubectl -n api rollout status deploy/api --timeout=300s
kubectl -n web rollout restart deploy/web && kubectl -n web rollout status deploy/web --timeout=300s

# ⚠️ 부분매칭 함정 — "db 연결 실패"가 "db 연결"을 포함한다. 실패 거부 → 성공 전문 매치 순서.
LOG=$(kubectl -n api logs deploy/api --tail=200)
echo "$LOG" | grep -q "in-memory 전용" && { echo "❌ in-memory 폴백 — DB 미연결"; exit 1; }
echo "$LOG" | grep -q "db 연결 — 유저/진행 상태 영속화 활성" || { echo "❌ db 연결 성공 로그 없음"; exit 1; }
```

- [ ] **Step 7: `12-verify-serving.sh` (🆕 신규)**

> **이식이 아니다.** 런북 `:370` 은 `- [ ] 검증(로컬 테스트유저 로그인·복원 데이터 서빙) + RTO 실측`
> **한 줄짜리 체크 항목**이고 명령이 없다. 스펙 §5.1.4 가 **무인증으로 재설계**한 것을 구현한다.

```bash
#!/bin/bash
# [12] VerifyServing — 복원본이 실제로 서빙되는지. 자격증명을 쓰지 않는다.
#
# ⚠️ 로그인 계정을 쓰지 않는 이유: [7] 의 generate-root 산물($NEWROOT)은 그 스크립트의 셸 변수이고,
# 이 스크립트는 별도 SSM RunCommand = **새 셸**이며, 자식 SM 은 stdout 을 S3 로 버린다 → Vault 토큰을
# 전달할 경로가 없다. password grant 활성 여부도 미확인 가정이었다(스펙 §5.1.4 H1).
set -euo pipefail
set -x

# (1) Keycloak + DNS + 복원된 realm — Route53 헬스체크(onprem_pull)와 동일 신호라 검증된 재사용이다.
curl -sf https://auth.cledyu.com/realms/cledyu-learn | grep -q cledyu-learn \
  || { echo "❌ realm 미응답 — Keycloak/DNS/복원 실패"; exit 1; }

# (2) 복원 데이터가 실제로 들어왔는가 — DB 직접
# ⚠️ -d cledyu 필수. 없으면 psql 이 접속 유저와 같은 이름의 DB(=postgres 컨테이너 기본 유저 postgres)에
# 붙어 users 테이블을 못 찾는다 → **정상 복원인데 항상 실패**한다(리뷰 지적, 실측 근거:
# postgres-cnpg/templates/cluster.yaml:50 `database: cledyu` / `owner: cledyu` — DR 차트는
# bootstrap.recovery 라 원본 DB 를 그대로 복원한다).
#
# ⚠️ **`-U cledyu` 를 쓰지 않는다(H3, 적대적 검증 4회차).** 초안은 `-U cledyu -d cledyu` 였는데
# **레포의 psql 선례 5건이 전부 `-U` 를 안 쓴다**: dr-failback.md:85 · dr-failback-isolated-drill.md
# :85·:87·:90·:91·:97 모두 `kubectl exec <pod> -- psql -d <db> -tAc "..."`.
# CNPG 파드의 OS 유저는 postgres 라 local 소켓 peer 인증에서 `-U cledyu` 는 OS유저≠롤로 어긋난다.
# (peer 인증 세부 거동은 미실측이라 단정하지 않는다 — 다만 **선례 5건이 만장일치**고 `-U` 는 이 계획이
#  새로 넣은 것이라 선례를 따른다. Step 10 에서 실제 실행으로 확정한다.)
# **[12] 는 페일오버 전체의 마지막 게이트**다 — 여기서 죽으면 **완벽히 복구된 DR 이 ❌ 실패 알림**을
# 보낸다(F5 와 같은 결과: 성공했는데 실패라고 알림).
ROWS=$(kubectl -n postgres exec cledyu-pg-1 -- psql -d cledyu -tAc "SELECT count(*) FROM users" | tr -d '[:space:]')
[ "${ROWS:-0}" -gt 0 ] || { echo "❌ 복원본에 데이터 없음(users=$ROWS)"; exit 1; }

echo "✅ realm 응답 + users=$ROWS"
```

> **트레이드오프:** 전체 OIDC 로그인 플로우(브라우저 리다이렉트)는 검증하지 않는다 — 헤드리스 브라우저가
> 필요해 재해 경로에 둘 만한 것이 아니다. [11] 의 `/ready` `keycloak=connected` 와 (1) 의 realm 응답으로
> "Keycloak 이 서빙 가능한 상태"까지는 덮인다.

- [ ] **Step 8: bastion 롤에 `ssm:PutParameter` 추가 (🆕 — F3, 이 Task 에서 가장 중요)**

> **`09-` 의 마지막 줄이 이 권한 없이는 AccessDenied 다.** 그 시점은 Kafka·VE·Keycloak 이 다 뜨고
> Vault·CNPG 복원까지 끝난 **~40분 뒤**이고, `set -euo pipefail` 이 걸려 [9] 가 실패하면 [10] 은
> **설계대로** fail-closed 라 DNS 를 안 넘긴다 → **전부 복구됐는데 서비스는 안 돌아온다.**
> 붙어 있는 `AmazonSSMManagedInstanceCore` 는 `GetParameter` 만 주고 `PutParameter` 는 **안 준다.**

`eks-dr-bastion.tf` 에 append. **기존 bastion IAM 과 같은 `count = local.eks_dr_enabled` + `[0]` 패턴을 쓴다:**

```hcl
# [9] 09-wait-apps-ready.sh 가 ALB 호스트명을 여기 써서 [10] SwitchDNS(non-VPC Lambda)에 넘긴다.
# non-VPC Lambda 는 private EKS 에 못 닿고 자식 SM 은 stdout 을 S3 로 버리므로 이 파라미터가
# 유일한 전달 경로다(스펙 §5.1.2). 경로를 /cledyu-dr/failover/* 로 한정해 bastion 이 다른
# 파라미터를 못 건드리게 한다.
data "aws_iam_policy_document" "eks_dr_bastion_ssm_param" {
  count = local.eks_dr_enabled
  statement {
    actions   = ["ssm:PutParameter"]
    resources = ["arn:aws:ssm:${var.region}:${data.aws_caller_identity.current.account_id}:parameter/cledyu-dr/failover/*"]
  }
}

resource "aws_iam_role_policy" "eks_dr_bastion_ssm_param" {
  count  = local.eks_dr_enabled
  name   = "ssm-put-failover-param"
  role   = aws_iam_role.eks_dr_bastion[0].id
  policy = data.aws_iam_policy_document.eks_dr_bastion_ssm_param[0].json
}
```

**그리고 `aws_instance.eks_dr_bastion` 의 `depends_on` 에 추가한다**(Global Constraints — `-target` 이
정책을 안 끌고 오는 것을 `depends_on` 이 해소한다. `eks-dr-bastion.tf:127` 의 기존 패턴에 한 줄 추가):

```hcl
  depends_on = [
    aws_iam_role_policy_attachment.eks_dr_bastion_ssm,
    aws_iam_role_policy.eks_dr_bastion_ssm_param, # ← 추가
  ]
```

**`dr-failover-buildspec.yml` 의 `-target` 에도 추가한다 → 17개에서 18개가 된다**(T1 Step 1 의 목록과
개수 주석을 함께 갱신). `depends_on` 이 있으므로 이론상 자동으로 끌려오지만, 이 레포는 **둘 다 하는 것이
컨벤션**이다(T1 의 17개에도 bastion 정책 2종이 명시돼 있다 — 2026-07-15 에 목록을 줄였다가 IAM 이 빠져
2분+ hang 을 겪은 이력):

```
          -target=aws_iam_role_policy.eks_dr_bastion_ssm_param \
```

- [ ] **Step 9: 정적 검증**

Run:
```bash
cd /home/user/Cledyu
for f in infra/terraform/aws/scripts/bastion/*.sh; do bash -n "$f" && echo "구문 OK: $f"; done

# ⚠️ **`shellcheck` 를 로컬 설치 여부로 건너뛰지 말 것 — pre-commit 훅으로 돌린다.**
# 이 레포의 훅은 자기 shellcheck·shfmt 를 쓰므로 로컬에 없어도 **커밋 때 반드시 걸린다.**
# T3 구현에서 "shellcheck 미설치 — 건너뜀"으로 넘겼다가 **커밋이 SC2015 로 거부**됐다(2026-07-15).
# shfmt 는 파일을 **수정**하므로(훅이 "files were modified" 로 실패) 먼저 돌려서 포맷을 확정한 뒤 add 한다.
pre-commit run shellcheck --files infra/terraform/aws/scripts/bastion/*.sh
pre-commit run shfmt      --files infra/terraform/aws/scripts/bastion/*.sh   # 파일을 고친다 → 통과할 때까지

# ⚠️ shfmt 는 줄번호를 밀므로 **재포맷 후 아래 회귀 체크를 다시 돌린다**(C6 체크가 줄번호 기반).
grep -rn 'exec -it' infra/terraform/aws/scripts/bastion/ && echo "❌ -it 잔존(SSM 엔 TTY 없음)" || echo "✅ -it 없음"

# ⚠️ **회귀 체크는 주석을 걷어내고 한다.** 스크립트 주석엔 "이렇게 쓰면 안 된다"는 **나쁜 예가 그대로
# 적혀 있어서**, 순진한 grep 은 전부 오탐한다(T3 구현 중 실측 — 3건이 오탐이었다).
code() { sed -e 's/[[:space:]]*#.*$//' -e '/^[[:space:]]*$/d' "$1"; }
B=infra/terraform/aws/scripts/bastion

# C6 회귀 — 07 의 **시크릿 구간**에 set -x 가 있으면 안 된다(루트 토큰이 CloudWatch·Discord 로 샌다).
# ⚠️ `grep -q '^set -x'` 는 안 된다 — 07 은 시크릿을 다 쓴 뒤(`unset NEWROOT`) **의도적으로 켠다.**
#    "있나 없나"가 아니라 **"어디 있나"** 를 봐야 한다(과소 게이트 회피).
awk '/^unset NEWROOT/{e=NR} /^set -x/{x=NR} END{
  if (x && (!e || x < e)) { print "❌ 07 의 시크릿 구간에 set -x (line "x") — 루트 토큰 유출(C6)"; exit 1 }
  else print "✅ 07 set -x 는 시크릿 구간 밖(line "x" > unset "e")" }' $B/07-restore-vault.sh

# F3 회귀 — 09 가 put-parameter 를 하면 그 IAM 이 반드시 있어야 한다
code $B/09-wait-apps-ready.sh | grep -q 'put-parameter' \
  && { grep -q 'ssm:PutParameter' infra/terraform/aws/eks-dr-bastion.tf \
    && echo "✅ put-parameter IAM 있음" || echo "❌ 09 가 put-parameter 하는데 bastion 롤에 권한 없음(F3)"; }

# §11.11 회귀 — CloudWatch 쓰기 IAM(없으면 명령은 Success 인데 로그 전문이 조용히 유실)
grep -q 'logs:CreateLogGroup' infra/terraform/aws/dr-orchestration.tf \
  && echo "✅ CloudWatch IAM 있음" || echo "❌ CreateLogGroup 없음 — 에이전트가 출력을 포기한다(§11.11)"

# H2 회귀 — `git clone ... &&` 는 set -e 가 실패를 안 잡는다(멱등도 아니다)
for f in $B/*.sh; do code "$f" | grep -q 'git clone.*&&' && echo "❌ $f: clone 을 && 로 이었다(H2)"; done; echo "✅ clone && 없음"

# H3 회귀 — psql 은 레포 선례대로 -U 없이(선례 5건: dr-failback.md·dr-failback-isolated-drill.md)
for f in $B/*.sh; do code "$f" | grep -q 'psql -U' && echo "❌ $f: psql -U — 선례 5건과 어긋난다(H3)"; done; echo "✅ psql -U 없음"

# H1 회귀 — 06 이 wave 2 리소스(api ns)를 게이트하면 건강한 DR 에서 오탐한다
code $B/06-bootstrap-apps.sh | grep -q 'get configmap cledyu-root-ca-bundle\|get clusterissuer' \
  && echo "❌ 06 이 아직 없는 게 정상인 것을 게이트한다(H1)" || echo "✅ 06 과대 게이트 없음"

# §11.12 회귀 — SSM 은 HOME 을 안 준다. **전부** 있어야 한다(하나라도 빠지면 kubectl 이 localhost:8080)
# ⚠️ 개수를 하드코딩하지 않는다 — T4 실측이 04-wait-nodes-ready.sh 를 더해 7→8 이 됐고(§11.14 (e)),
#    `-eq 7` 이던 초안은 그 순간 **정상인데 ❌ 를 뱉는 오탐**이 됐다. 파일 수와 비교한다.
[ "$(grep -l 'export HOME=/root' $B/*.sh | wc -l)" -eq "$(ls $B/*.sh | wc -l)" ] \
  && echo "✅ HOME $(ls $B/*.sh | wc -l)/$(ls $B/*.sh | wc -l)" \
  || echo "❌ export HOME=/root 누락 — kubectl 이 localhost:8080 으로 폴백한다"

cd infra/terraform/aws && terraform fmt -check eks-dr-bastion.tf && terraform validate
```
Expected: 전부 구문 OK, `-it` 0건, `07 set -x 없음`, `put-parameter IAM 있음`, `Success! The configuration is valid.`

- [ ] **Step 10: 운영자 실측 — 미확정 4건 확정 + 스크립트 하나씩 실행(각 2회 — 멱등)**

**먼저 미확정 4건을 확정한다**(이 계획이 정직하게 남겨둔 것들 — 확정 없이는 `03-`·`08-`·`09-`·`12-` 가
미완성이다). ③④ 는 4회차에서 추가됐다:

```bash
# ③ 08- : delete cluster 가 PVC 도 지우나 (H5 — 안 지우면 stale 데이터로 뜨고 모든 게이트를 통과한다)
kubectl -n postgres get pvc -l cnpg.io/cluster=cledyu-pg    # delete 전
kubectl -n postgres delete cluster cledyu-pg --ignore-not-found
sleep 10
kubectl -n postgres get pvc -l cnpg.io/cluster=cledyu-pg    # delete 후 — 남아 있으면 08- 에 PVC 삭제 추가
#   → 남으면: 재생성된 Cluster 가 S3 복원 대신 그 PVC 를 재사용할 수 있다. 08- 에 명시 삭제 추가.

# ④ 12- : psql 호출이 인증을 통과하나 (H3 — 레포 선례대로 -U 없이)
kubectl -n postgres exec cledyu-pg-1 -- psql -d cledyu -tAc "SELECT count(*) FROM users"
#   → 숫자가 나오면 확정. "Peer authentication failed" 가 나오면 선례를 다시 확인할 것.
#     (초안의 `-U cledyu` 는 선례 5건과 어긋나 제거했다)
```

**그리고 ①② 를 확정한다:**

```bash
# ① 03- 의 정리 대상 이름 (계획 작성자가 지어낸 이름이라 미확인)
#    반복 failover 가 아니면 아무것도 안 나오는 게 정상 — 그 경우 미확정으로 남기고 재-failover 드릴에서 확정.
BASTION=$(aws ec2 describe-instances --region ap-northeast-2 \
  --filters Name=tag:Name,Values=cledyu-dr-bastion Name=instance-state-name,Values=running \
  --query 'Reservations[0].Instances[0].InstanceId' --output text)
aws ssm start-session --target "$BASTION" --region ap-northeast-2
#   (세션 안에서)
kubectl get validatingwebhookconfiguration -o name | grep -i 'load-balancer\|alb'
kubectl get mutatingwebhookconfiguration   -o name | grep -i 'load-balancer\|alb'

# ② KafkaTopic 에 Ready 조건이 있나 (Kafka CR 은 Strimzi 문서로 확인했으나 KafkaTopic 은 미확인)
#    Kafka 가 뜬 뒤(= 06- 실행 후)에만 확인 가능하다.
kubectl -n kafka get kafkatopic -o jsonpath='{.items[0].status.conditions[*].type}'
#   → "Ready" 가 있으면 09- 의 `wait --for=condition=Ready kafkatopic --all` 유지
#   → 없으면 그 줄을 **삭제**하고 Kafka CR Ready 만 게이트한다(계획에 그 판단을 기록)
```

**그다음 스크립트를 하나씩 실행한다:**

**메인 SM 없이** 자식 SM(Task 2)으로 하나씩 돌린다. **여기서 대부분의 실패가 나온다.**

```bash
BASTION=$(aws ec2 describe-instances --region ap-northeast-2 \
  --filters Name=tag:Name,Values=cledyu-dr-bastion Name=instance-state-name,Values=running \
  --query 'Reservations[0].Instances[0].InstanceId' --output text)
ARN=$(aws stepfunctions list-state-machines --region ap-northeast-2 \
  --query "stateMachines[?name=='cledyu-lab-dr-run-on-bastion'].stateMachineArn" --output text)

# ⚠️ env 를 반드시 넣는다(F6) — 자식 SM 계약이 "env 는 항상 채운다"이고 BuildCommands 가
#    States.Array($.env, $.script) 를 하므로 빠지면 States.Runtime 으로 즉시 죽는다.
#    초안의 이 헬퍼는 env 가 없어서 **모든 스크립트 실측이 첫 줄에서 실패**하게 되어 있었다.
run() {  # run <스크립트> [타임아웃] [env]
  aws stepfunctions start-execution --region ap-northeast-2 --state-machine-arn "$ARN" \
    --input "$(jq -n --arg i "$BASTION" --arg s "$(cat "$1")" --argjson t "${2:-1800}" \
      --arg l "$(basename "$1")" --arg e "${3:-:}" \
      '{instanceId:$i, script:$s, env:$e, timeoutSeconds:$t, label:$l}')" --query executionArn --output text
}

run infra/terraform/aws/scripts/bastion/03-clean-warm-etcd.sh 600
# → SUCCEEDED 확인 후 다음. 순서대로: 03 → (노드 스케일·애드온은 Task 4 후) → 06 → 07 → 08 → 09 → 11 → 12
```
Expected: 각각 `SUCCEEDED`. **실패 시 전문은 CloudWatch 에서 본다**(출력이 S3 → CloudWatch 로 바뀌었다,
스펙 §11.10). 자식 SM 출력의 `commandId` 로:
```bash
aws logs tail /aws/ssm/cledyu-lab-dr-failover --region ap-northeast-2 --log-stream-name-prefix <commandId>
```

> **07 은 `SNAPSHOT_KEY` 가 필요하다** — `run` 의 3번째 인자(`env`)로 넘긴다. **이렇게 하면 T5 의 [7] 이
> 쓰는 것과 정확히 같은 경로를 실측하게 된다**(초안은 `<(printf ...; cat ...)` 로 스크립트에 직접
> 붙였는데, 그건 실제 배선과 다른 걸 테스트하는 것이라 C2 와 같은 함정이다):
> ```bash
> SNAP=$(aws s3api list-objects-v2 --bucket cledyu-lab-dr-backups --prefix vault/ \
>   --query 'reverse(sort_by(Contents,&LastModified))[0].Key' --output text)
> run infra/terraform/aws/scripts/bastion/07-restore-vault.sh 1800 "export SNAPSHOT_KEY=$SNAP"
> ```

- [ ] **Step 11: Commit** (사용자가 실행)

```bash
cd /home/user/Cledyu
git add infra/terraform/aws/scripts/bastion/ infra/terraform/aws/eks-dr-bastion.tf \
        infra/terraform/aws/dr-failover-buildspec.yml infra/terraform/aws/README.md
git commit -m "feat(dr): bastion 페일오버 스크립트 7종 + ALB 전달용 ssm:PutParameter"
```

---

### Task 4: Lambda 3개 — addon-install · dns-switch · notify

**Files:**
- Create: `infra/terraform/aws/dr-orchestration-lambda/addon-install/index.py`
- Create: `infra/terraform/aws/dr-orchestration-lambda/dns-switch/index.py`
- Create: `infra/terraform/aws/dr-orchestration-lambda/notify/index.py`
- Modify: `infra/terraform/aws/dr-orchestration.tf`
- Modify: `infra/terraform/aws/README.md`

**Interfaces:**
- `addon-install` — **입력 `{action: "start"|"check"}` (필수).**
  출력은 action 에 따라 다르다: `start` → `{started: ["coredns", "aws-ebs-csi-driver"]}` ·
  `check` → `{status: {coredns: "ACTIVE", "aws-ebs-csi-driver": "ACTIVE"}, done: true}`.
  런북 Phase 1 (3) 의 `install_addon` 멱등 함수 이식
  > **초안은 여기에 `입력 {} / 출력 {coredns, ebsCsi}` 라고 적었는데 코드·Step 5 Expected 와 **셋 다 달랐다**
  > (적대적 검증 3회차 F9). `action` 없이 호출하면 check 경로로 빠져 애드온 미설치 상태에서
  > `ResourceNotFoundException` 으로 죽는다. **Interfaces 는 다음 Task 구현자가 유일하게 읽는 계약이라
  > 코드와 어긋나면 그대로 버그가 된다.**
- `dns-switch` — 입력 `{}`, 출력 `{alb, records}`. SSM 파라미터에서 ALB 취득 → WAF 확인 → Route53 UPSERT
- `notify` — 입력 `{outcome, detectedAt, approvedAt, alb?, failedState?, stdoutTail?, executionArn?}`,
  출력 `{notified: true}`. [13] NotifyComplete 와 모든 상태의 `Catch` → NotifyFailed 가 **공용으로** 쓴다.
  **이 입력을 채우는 건 T5 Step 1 의 `Payload` 매핑이다** — 초안엔 그 매핑이 없어서 RTO 가 `?` 로
  나왔다(F5). T5 를 만들 때 이 필드명과 대조할 것

- [ ] **Step 1: `addon-install/index.py`**

런북 Phase 1 (3) 이식. **멱등 필수** — failback 이 애드온을 안 지우고 warm 에 남기므로 재-failover 시
`create-addon` 은 409 `ResourceInUseException` 으로 죽는다 → `describe` 후 있으면 `update`.

```python
"""[5] InstallAddons — coredns·ebs-csi 관리형 애드온 멱등 설치.

warm(node0) 에선 이 둘이 Deployment 라 DEGRADED 로 terraform apply 를 블록한다 → cluster_addons 에서
빼두고(eks-dr.tf) 노드가 뜬 뒤 CLI 로 설치한다(런북 Phase 1 (3)).
"""

# ⚠️ `import os` 를 넣지 않는다 — 이 파일은 os 를 안 쓴다. 레포 ruff 는 `F` 를 select 하므로
#    F401(unused import) 로 **Step 5 정적 검증이 즉시 깨진다**(T4 구현 중 실측).
import boto3

_eks = boto3.client("eks")
CLUSTER = "cledyu-dr"


def _start(name, **kw):
    """설치를 **시작만** 한다 — 기다리지 않는다.

    ⚠️ Lambda 하드 상한은 900s 다. 초안은 애드온 2개를 각각 최대 600s(15s x 40) **순차 대기**해서
    (※ `×`(U+00D7)를 쓰면 ruff RUF002 로 정적 검증이 깨진다 — 독스트링엔 ASCII `x`)
    최대 1200s → **타임아웃**이었다([2] 를 CodeBuild 로 만든 바로 그 이유에 다시 걸린 것 — 리뷰 지적 C4).
    → 시작만 하고 ACTIVE 대기는 SFN 의 Wait+Choice 폴링에 맡긴다(자식 SM 과 같은 패턴).
    """
    # failback 이 애드온을 warm 에 남기므로 재-failover 시 create 는 409 로 죽는다 → describe 후 분기.
    try:
        _eks.describe_addon(clusterName=CLUSTER, addonName=name)
        verb = _eks.update_addon
    except _eks.exceptions.ResourceNotFoundException:
        verb = _eks.create_addon
    verb(clusterName=CLUSTER, addonName=name, resolveConflicts="OVERWRITE", **kw)


def handler(event, context):
    """action="start" 면 설치 시작, action="check" 면 현재 상태 반환. SFN 이 폴링한다."""
    acct = boto3.client("sts").get_caller_identity()["Account"]
    names = {
        "coredns": {},
        "aws-ebs-csi-driver": {
            "serviceAccountRoleArn": f"arn:aws:iam::{acct}:role/cledyu-dr-ebs-csi"
        },
    }

    if event.get("action") == "start":
        for n, kw in names.items():
            _start(n, **kw)
        return {"started": list(names)}

    # check — 둘 다 ACTIVE 여야 done. CREATE_FAILED 면 즉시 실패시킨다(P1c 가 여기서 드러난다).
    st = {}
    for n in names:
        s = _eks.describe_addon(clusterName=CLUSTER, addonName=n)["addon"]["status"]
        if s in ("CREATE_FAILED", "UPDATE_FAILED", "DEGRADED"):
            raise RuntimeError(f"{n} 애드온 실패: {s} — 고아 ALB webhook(P1c)이 남았는지 확인")
        st[n] = s
    return {"status": st, "done": all(v == "ACTIVE" for v in st.values())}
```

**[5] InstallAddons 는 SFN 에서 3상태가 된다**(Lambda 가 기다리지 않으므로):
```hcl
      InstallAddons = {   # action=start → 즉시 반환
        Type = "Task", Resource = "arn:aws:states:::lambda:invoke"
        Parameters = { FunctionName = aws_lambda_function.dr_addon_install.arn, Payload = { action = "start" } }
        ResultPath = null, Next = "WaitAddons"
      }
      WaitAddons = { Type = "Wait", Seconds = 20, Next = "CheckAddons" }
      CheckAddons = {
        Type = "Task", Resource = "arn:aws:states:::lambda:invoke"
        Parameters = { FunctionName = aws_lambda_function.dr_addon_install.arn, Payload = { action = "check" } }
        ResultSelector = { "done.$" = "$.Payload.done" }
        ResultPath = "$.addons", Next = "AddonsDone?"
      }
      "AddonsDone?" = {
        Type = "Choice"
        Choices = [{ Variable = "$.addons.done", BooleanEquals = true, Next = "BootstrapApps" }]
        Default = "WaitAddons"
      }
```
> **무한 루프 방지:** 메인 SM 전체에 `TimeoutSeconds` 를 둔다(승인 24h + 복구 1h → **90000**).
> 애드온이 영영 ACTIVE 가 안 되면 여기서 걸린다.

> `update_addon` 은 `serviceAccountRoleArn` 을 받지만 `resolveConflicts` 값 집합이 create 와 다를 수 있다 —
> Step 4 실측에서 확인한다.

- [ ] **Step 2: `dns-switch/index.py`**

런북 §공개 DNS 전환 이식. **bastion 롤엔 route53/wafv2/elbv2 권한이 없다**(런북 명시: AccessDenied) →
Lambda 가 자기 롤로 한다. Route53 은 공개 API 라 VPC 불요.

```python
"""[10] SwitchDNS — api·app·auth 를 EKS ALB 로 전환.

ALB 호스트명은 [9] 가 SSM 파라미터에 써둔 값을 읽는다 — non-VPC Lambda 는 private EKS 에 못 닿고,
자식 SM 은 stdout 을 S3 로 버리므로 이게 유일한 전달 경로다(설계 §5.1.2).
파라미터가 없으면 **즉시 실패**한다 — 폴백·추측 금지. "[9] 가 안 썼다 = ALB 를 못 얻었다"이므로
DNS 를 건드리지 않고 멈추는 것이 옳다(DNS 는 온프렘을 가리킨 채 남아 상태가 안전).
"""

import boto3  # `import os` 금지 — 미사용이라 ruff F401 (위 addon-install 과 동일)

_ssm = boto3.client("ssm")
_elb = boto3.client("elbv2")
_waf = boto3.client("wafv2")
_r53 = boto3.client("route53")

HOSTS = ["api", "app", "auth"]
PARAM = "/cledyu-dr/failover/alb-hostname"
DOMAIN = "cledyu.com"
WAF_ACL = "cledyu-lab-public"  # = var.name_prefix("cledyu-lab") + "-public" (waf.tf:10)


def _find_zone():
    """공개 hosted zone 을 이름으로 찾는다 — **일치를 확인하고** 쓴다.

    ⚠️ 초안은 `list_hosted_zones_by_name(...)["HostedZones"][0]` 였다. 이 API 의 DNSName 은 필터가
    아니라 **정렬 시작점**이다 — cledyu.com 존이 없으면 알파벳순 다음 존이 [0] 으로 돌아오고
    **남의 존에 api·app·auth 를 UPSERT** 한다. 이 모듈 자신의 fail-closed 원칙(파라미터 없으면 즉시
    실패)과 어긋나는 fail-open 이었다(T4 구현 중 발견).
    private 존도 거른다 — 같은 이름의 split-horizon 존이 있으면 공개 전환이 조용히 내부에만 먹는다.

    ⚠️ terraform 의 data.aws_route53_zone.public 을 참조하지 않는 이유: 그건 count =
    local.pub(enable_public_ingress, 기본 false) 게이트라 이 게이트 없는 Lambda 가 env 로 받으면
    평시 apply 가 index out of range 로 깨진다. → 런타임 조회 + 이름 게이트.
    """
    for z in _r53.list_hosted_zones_by_name(DNSName=DOMAIN)["HostedZones"]:
        if z["Name"].rstrip(".") == DOMAIN and not z["Config"].get("PrivateZone"):
            return z["Id"]
    raise RuntimeError(f"공개 hosted zone 없음: {DOMAIN} — DNS 를 건드리지 않고 멈춘다")


def handler(event, context):
    alb = _ssm.get_parameter(Name=PARAM)["Parameter"]["Value"]  # 없으면 ParameterNotFound → 실패

    lbs = _elb.describe_load_balancers()["LoadBalancers"]
    # ⚠️ 초안의 `next(x for x in lbs if ...)` 는 못 찾으면 StopIteration 이 그대로 올라가
    # CloudWatch 에 **원인 없는 에러**만 남긴다 → 기본값 None + 명시 판정.
    lb = next((x for x in lbs if x["DNSName"] == alb), None)
    if lb is None:
        raise RuntimeError(f"ALB 를 못 찾음: {alb} — [9] 가 쓴 파라미터가 stale 인지 확인")

    # WAF(/metrics 차단)는 api·web values-eks 의 wafv2-acl-arn annotation 으로 ALB 생성과 동시에
    # 자동 연결된다 → 여기선 **붙었는지 확인만**. 안 붙었으면 /metrics 가 조용히 열린다(설계 §6.3).
    acl = _waf.get_web_acl_for_resource(ResourceArn=lb["LoadBalancerArn"]).get("WebACL")
    if not acl or acl["Name"] != WAF_ACL:
        raise RuntimeError(f"WAF 미연결 — values-eks 의 wafv2-acl-arn 이 stale: {acl}")

    zone = _find_zone()
    changes = [
        {
            "Action": "UPSERT",
            "ResourceRecordSet": {
                "Name": f"{h}.{DOMAIN}",
                "Type": "A",
                "AliasTarget": {
                    "HostedZoneId": lb["CanonicalHostedZoneId"],
                    "DNSName": alb,
                    "EvaluateTargetHealth": False,
                },
            },
        }
        for h in HOSTS
    ]
    _r53.change_resource_record_sets(HostedZoneId=zone, ChangeBatch={"Changes": changes})
    return {"alb": alb, "records": HOSTS}
```

- [ ] **Step 3: `notify/index.py`** — [13] 완료 알림 + 모든 Catch 의 실패 알림

**RTO 를 2단으로 보고한다**(스펙 §5.1.5) — 실행 시간을 그대로 쓰면 사람이 자던 시간이 섞여 무의미하다.

```python
"""[13] NotifyComplete + 모든 상태의 Catch → NotifyFailed 공용.

평문 알림이라 components 가 없다 → **웹훅으로 충분**(approval-request 가 Bot API 를 쓰는 것과 다름).
dr-alert(#310)와 같은 us-east-1 웹훅 시크릿을 읽으므로 **ARN 에서 리전을 파싱**한다 —
Secrets Manager 는 리전 서비스라 ap-northeast-2 클라이언트가 us-east-1 시크릿을 못 찾는다(스펙 §3.3).
"""
import json
import os  # notify 는 os.environ 을 실제로 쓴다(위 둘과 달리 필요)
import urllib.request

# ⚠️ `from datetime import datetime, timezone` + `timezone.utc` 는 ruff UP017 로 깨진다
#    (pyproject target-version = py311, 런타임 python3.12) → UTC 별칭을 쓴다.
from datetime import UTC, datetime

import boto3

RUNBOOK = "https://github.com/requset700k/Cledyu/blob/main/docs/RUNBOOK/dr-failback.md"


def _webhook_url():
    arn = os.environ["WEBHOOK_SECRET_ARN"]
    sm = boto3.client("secretsmanager", region_name=arn.split(":")[3])
    url = json.loads(sm.get_secret_value(SecretId=arn)["SecretString"])["url"]
    if not url.startswith("https://"):
        raise ValueError("webhook URL must be https")
    return url


def _ts(s):
    """ISO8601 파싱 — 두 출처의 형식이 다르다.

    ⚠️ strptime("%Y-%m-%dT%H:%M:%S.%fZ") 를 쓰면 안 된다(리뷰 지적 C2):
      · detectedAt = CloudWatch 알람 이벤트의 detail.state.timestamp → **"...328+0000"**(Z 아님)
      · approvedAt = interaction Lambda 의 new Date().toISOString()  → "...371Z"
    Z 포맷만 기대하면 detectedAt 에서 ValueError → [13] 이 죽고 Catch 가 NotifyFailed 로 보내
    **13단계를 다 성공하고도 "❌ 실패" 알림**이 간다. 게다가 T4 Step 5 의 테스트 payload 는
    손으로 쓴 "...000000Z" 라 **수동 검증은 통과하고 실재해에서만 터진다.**
    fromisoformat(py3.11+)은 offset·Z 둘 다 파싱한다(실측 확인).
    """
    if not s:
        return None
    try:
        return datetime.fromisoformat(s.replace("Z", "+00:00"))
    except ValueError:
        return None


def _mins(a, b):
    ta, tb = _ts(a), _ts(b)
    if not (ta and tb):
        return "?"
    return f"{(tb - ta).total_seconds() / 60:.0f}분"


def handler(event, context):
    now = datetime.now(UTC).isoformat()
    if event.get("outcome") == "success":
        text = (
            "✅ **DR 페일오버 완료**\n"
            # 두 구간을 나눈다 — 감지→승인은 사람 지연, 승인→서빙이 자동화 RTO 다.
            f"감지→승인: {_mins(event.get('detectedAt'), event.get('approvedAt'))} (사람 지연)\n"
            f"**승인→서빙: {_mins(event.get('approvedAt'), now)} ← 자동화 RTO**\n"
            f"ALB: {event.get('alb', '?')}\n\n"
            "**다음 할 일 — failback 준비:**\n"
            "`postgres-cnpg-dr/values.yaml`·`keycloak-pg-dr/values.yaml` 의 `backupEnabled: false → true` PR.\n"
            "안 켜면 DR-창 쓰기가 S3 에 안 남아 **failback 이 anchor 없이 실패**합니다.\n"
            f"런북: {RUNBOOK}"
        )
    else:
        text = (
            "❌ **DR 페일오버 실패**\n"
            f"실패 단계: `{event.get('failedState', '?')}`\n"
            f"실행: {event.get('executionArn', '?')}\n\n"
            f"```\n{(event.get('stdoutTail') or '')[-1200:]}\n```\n"
            "**롤백하지 않았습니다** — 여기까지 뜬 것은 그대로 있으니 런북으로 이어받으세요.\n"
            "DNS 는 아직 온프렘을 가리킵니다."
        )

    req = urllib.request.Request(  # noqa: S310
        _webhook_url(),
        data=json.dumps({"content": text[:1900]}).encode("utf-8"),
        headers={
            "Content-Type": "application/json",
            # Discord 는 Cloudflare 뒤라 기본 UA 를 403 으로 막는다(#311).
            "User-Agent": "Cledyu-DR-Notify/1.0 (+https://cledyu.com)",
        },
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=10) as resp:  # noqa: S310
        resp.read()
    return {"notified": True}
```

- [ ] **Step 4: terraform — Lambda 3개 + IAM (실행 롤 3개 + SFN 롤 확장)**

`depends_on = [aws_cloudwatch_log_group.X, aws_iam_role_policy.X]` **필수**(Global Constraints).

**(a) 각 Lambda 의 실행 롤:**
- `addon-install`: `eks:DescribeAddon`·`CreateAddon`·`UpdateAddon`(cledyu-dr 한정), `iam:PassRole`(ebs-csi 롤), `sts:GetCallerIdentity`
  > **🔴 `module.eks_dr_ebs_csi_irsa[0].iam_role_arn` 을 쓰지 말 것(T4 구현 중 실측 — `terraform validate`
  > 가 실제로 거부했다).** 그 모듈은 `count = local.eks_dr_enabled`(**기본 false** — tfvars 에 `enable_eks_dr`
  > 가 없다)라 **list of object** 다. 이 Lambda 들엔 게이트가 없으므로 참조하면 평시 apply 가
  > index out of range 로 깨진다. **eks-dr.tf:139 가 "롤명 `cledyu-dr-ebs-csi` 결정적"이라 명시한 게
  > 정확히 이 용도** → ARN 을 문자열로 조립한다:
  > `"arn:aws:iam::${data.aws_caller_identity.current.account_id}:role/${local.eks_dr_name}-ebs-csi"`
  > (dns-switch 가 Route53 존을 런타임 조회하는 것도 **같은 구조** — `data.aws_route53_zone.public` 이
  > `enable_public_ingress` 게이트라 참조 불가. **게이트된 것을 게이트 없는 것이 참조하면 안 된다**가 규칙이다.)
  > **`iam:PassedToService = eks.amazonaws.com` 조건을 함께 건다**(초안 누락) — 없으면 이 롤로 ebs-csi 롤을
  > EKS 아닌 서비스에도 넘길 수 있다.
- `dns-switch`: `ssm:GetParameter`(`/cledyu-dr/failover/*`), `elasticloadbalancing:DescribeLoadBalancers`,
  `wafv2:GetWebACLForResource`, `route53:ChangeResourceRecordSets`·`ListHostedZonesByName`(해당 존)
- `notify`: `secretsmanager:GetSecretValue`(**us-east-1 의 `discord_webhook` ARN** — dr-alert 와 공용)

**(b) SFN 롤이 이 셋을 호출할 권한 — 초안에 없었다(F2). 이게 없으면 [5]·[10]·[13] 과 NotifyFailed 가
전부 AccessDenied 다:**

```hcl
  # data.aws_iam_policy_document.dr_sfn 에 추가(§SFN 롤 IAM 배선표)
  statement {
    sid     = "InvokeFailoverLambdas"
    actions = ["lambda:InvokeFunction"]
    resources = [
      aws_lambda_function.dr_addon_install.arn,
      aws_lambda_function.dr_dns_switch.arn,
      aws_lambda_function.dr_notify.arn,
    ]
  }
```

> **⚠️ `notify` 를 빠뜨리면 실패가 무음이 된다.** 모든 `Catch` 가 `NotifyFailed` 로 가는데 그게
> AccessDenied 면 SFN 은 그냥 FAILED 로 끝나고 **Discord 에 아무것도 안 온다** — 재해 중에
> "승인 눌렀는데 소식이 없다"가 된다. 이 설계의 마지막 방어선이라 가장 먼저 확인한다.
>
> **사이클 없음** — Lambda 3개는 `aws_iam_role_policy.dr_sfn` 을 `depends_on` 하지 않는다(자기 실행
> 롤만 본다). 사이클이 나는 건 자식 SM 뿐이고 그건 T2 Step 1 (b) 에서 분리했다.

- [ ] **Step 5: 정적 검증 + 운영자 실측**

```bash
cd /home/user/Cledyu
ruff check infra/terraform/aws/dr-orchestration-lambda/{addon-install,dns-switch,notify}/index.py
ruff format --check infra/terraform/aws/dr-orchestration-lambda/{addon-install,dns-switch,notify}/index.py
cd infra/terraform/aws && terraform fmt -check dr-orchestration.tf && terraform validate
```

**운영자 실측 — 노드를 먼저 올려야 한다:**
```bash
# [4] ScaleNodes 를 손으로 (Task 5 의 SFN 상태로 만들기 전 검증)
NG=$(aws eks list-nodegroups --cluster-name cledyu-dr --region ap-northeast-2 --query 'nodegroups[0]' --output text)
aws eks update-nodegroup-config --cluster-name cledyu-dr --region ap-northeast-2 \
  --nodegroup-name "$NG" --scaling-config minSize=0,maxSize=6,desiredSize=3
aws eks wait nodegroup-active --cluster-name cledyu-dr --region ap-northeast-2 --nodegroup-name "$NG"

# [5] addon-install — ⚠️ action 필수(F9). payload 없이 부르면 check 경로로 빠져
#     애드온 미설치 상태에서 ResourceNotFoundException 으로 죽는다(초안의 커맨드가 그랬다).
terraform apply -target=aws_lambda_function.dr_addon_install
aws lambda invoke --function-name cledyu-lab-dr-addon-install --region ap-northeast-2 \
  --payload '{"action":"start"}' --cli-binary-format raw-in-base64-out /tmp/o.json && cat /tmp/o.json
```
Expected: `{"started": ["coredns", "aws-ebs-csi-driver"]}` — **즉시 반환한다**(기다리지 않는 게 설계다).

```bash
# ACTIVE 가 될 때까지 check 를 반복 — SFN 의 WaitAddons→CheckAddons→AddonsDone? 루프와 같은 것을 손으로
for i in $(seq 1 30); do
  aws lambda invoke --function-name cledyu-lab-dr-addon-install --region ap-northeast-2 \
    --payload '{"action":"check"}' --cli-binary-format raw-in-base64-out /tmp/o.json >/dev/null && cat /tmp/o.json
  grep -q '"done": true' /tmp/o.json && break; sleep 20
done
```
Expected: 끝에 `{"status": {"coredns": "ACTIVE", "aws-ebs-csi-driver": "ACTIVE"}, "done": true}`.

**`start` 를 두 번 돌려도 성공**해야 한다(멱등 — failback 이 애드온을 warm 에 남기므로 재-failover 시
`create` 는 409 로 죽는다. `describe` 후 `update` 로 분기하는 게 그 방어다).

> **🔴 실측(2026-07-15): 두 번은 통과하고 세 번째가 죽었다 — 이 성공 기준 자체가 과소했다(스펙 §11.14 (g)).**
> 3회차 `start` → `ResourceInUseException: cannot be updated as it is currently in UPDATING state`.
> `ResourceInUseException` 은 **(a) create 인데 이미 존재함**(describe 분기가 막음) 뿐 아니라
> **(b) update 인데 이미 변경 중**(안 막힘) 에도 난다. 2회차가 UPDATING 을 만들고 3회차가 부딪혔다.
> → `_start` 를 `contextlib.suppress(ResourceInUseException)` 로 정정. **검증은 3회 연속으로 한다** —
> 2회는 (b) 를 못 잡는다(2회차 시점엔 애드온이 ACTIVE 라 update 가 그냥 된다).
>
> **✅ 해소된 미확정:** `update_addon` 의 `resolveConflicts` 는 create 와 값 집합이 같다(OVERWRITE 수용).

**🔴 T5 로 넘어가는 발견 — `[4]` 의 게이트가 무효다(스펙 §11.14).** 이 Step 을 수동으로 돌리다 발견했다:
`aws eks wait nodegroup-active` 가 **부팅 8초차에 반환**했고(`Nodegroup.Status` 는 스케일 내내 ACTIVE),
경합을 재현하니 **노드 Ready 와 InstallAddons 가 같은 초**였다(여유 0초, 노드가 35s 만에 떠준 운).
→ 위 Task 5 의 `[4]` 를 `WaitNodesReady`(자식 SM) 로 **이미 정정해두었다.**

**dns-switch 는 [9] 가 파라미터를 쓴 뒤**(Task 3 Step 10 의 `09-` 실행 후) 검증한다 — 그전엔
`ParameterNotFound` 로 **실패하는 게 정상**이고, 그게 fail-closed 검증이기도 하다.

> **🔴 순서 구멍 (2026-07-16 발견·수정) — 이 Step 에 dns-switch 의 apply 가 없었다.**
>
> 계획서 전체의 `terraform apply -target` 은 **3줄뿐**이었다: `dr_failover_tf`(T1) · `dr_addon_install`
> (위) · `dr_failover`(T5 :2295). **dns-switch 와 notify 는 apply 줄이 없다.**
> 그런데 아래 완료 기준엔 **"dns-switch 가 SSM 파라미터 없으면 실패한다(fail-closed)"** 가 있다 —
> **Lambda 가 있어야 확인 가능한데 만드는 줄이 T4 에 없다.** T4 기준을 T5 없이는 못 채운다.
>
> **왜 T5 면 되나:** `-target` 은 **의존성을 따라간다**(이 레포 dr-orchestration.tf:411 이 이미 기록:
> "`-target` 은 의존성만 따라가고 의존하는 것은 안 따라감"). T5 의 SM 정의가
> `FunctionName = aws_lambda_function.dr_dns_switch.arn`(:2197)을 참조하므로 T5 의 apply 가
> dns-switch 를 **딸려 올린다.** 그래서 "코드에 있는데 AWS 엔 없는" 상태가 T5 까지 조용히 유지됐다
> (2026-07-16 실측: ap-northeast-2 의 DR Lambda 4개 중 dns-switch 만 부재).
>
> **⚠️ 이게 가능한 구조적 이유:** terraform 은 **자동 apply 가 없다**(워크플로 12개 중 `terraform apply`
> 0개). k8s 쪽은 ArgoCD `selfHeal: true`(19개 앱)가 코드=실물을 강제하지만, terraform 은 사람이 쳐야
> 반영되고 **코드와 실물의 드리프트를 아무도 알려주지 않는다.** 발견도 우연이었다(Lambda 목록 조회 중).
>
> **수정: 아래 apply 를 이 Step 에 명시한다.** T5 에서 다시 apply 되면 `0 to change` 로 지나가므로 손해 없다.

```bash
# dns-switch — apply 후 fail-closed 검증. [9] **전**이라 SSM 파라미터가 없는 지금이 적기다.
terraform apply -target=aws_lambda_function.dr_dns_switch
```
Expected: `4 to add, 0 to change, 0 to destroy` (Lambda + IAM 롤·정책 + 로그그룹).
**`0 to destroy` 를 반드시 확인한다** — tfvars 부재로 전체 apply 였다면 게이트 리소스가 날아간다.

```bash
# fail-closed — 파라미터가 없으므로 **실패해야 정상**이다.
aws lambda invoke --function-name cledyu-lab-dr-dns-switch --region ap-northeast-2 \
  --payload '{}' --cli-binary-format raw-in-base64-out /tmp/dns.json; cat /tmp/dns.json
```
Expected: `ParameterNotFound` 로 **에러**. **성공하면 그게 버그다** — 폴백·추측으로 DNS 를 건드렸다는 뜻.

```bash
# notify — 성공/실패 양쪽 렌더 확인 (Discord 에 실제로 뜬다)
#
# ⚠️ detectedAt 은 **실제 CloudWatch 형식(+0000)** 으로 넣는다. 손으로 "...000000Z" 를 쓰면
# 진짜 경로와 다른 걸 테스트하게 되고, C2(Z 만 파싱 → 성공했는데 "실패" 알림)가 이 방식으로
# 숨었다. approvedAt 은 interaction Lambda 의 toISOString() 형식(Z, 밀리초 3자리)이다.
aws lambda invoke --function-name cledyu-lab-dr-notify --region ap-northeast-2 \
  --payload '{"outcome":"success","detectedAt":"2026-07-15T05:00:00.328+0000","approvedAt":"2026-07-15T05:10:00.371Z","alb":"test-alb"}' \
  --cli-binary-format raw-in-base64-out /tmp/n.json && cat /tmp/n.json
aws lambda invoke --function-name cledyu-lab-dr-notify --region ap-northeast-2 \
  --payload '{"outcome":"failed","failedState":"RestoreVault","stdoutTail":"테스트 실패 메시지"}' \
  --cli-binary-format raw-in-base64-out /tmp/n.json
```
Expected: Discord 에 `✅ DR 페일오버 완료`(**RTO 2단이 `?` 가 아니라 실제 분 단위로** 찍혀야 한다 —
`?` 면 파싱 실패다)와 `❌ DR 페일오버 실패`가 각각.

> **실제 형식은 드릴에서 재확인한다.** 위 `+0000` 은 AWS 문서 기준이고, 진짜 값은 T7 드릴 때
> `aws stepfunctions describe-execution --query input` 으로 확인해 이 payload 를 갱신한다.

**✅ 실측 완료 (2026-07-15) — notify 양쪽 경로 통과. 상세는 스펙 §11.13:**
- `감지→승인: 10분` 렌더 = `+0000`·`Z` **양쪽 형식 파싱 확인**(C2 회귀 방어가 실제로 동작).
  `승인→서빙: 621분` 은 버그가 아니라 테스트 payload 의 과거 `approvedAt` 과 실시간 `now` 의 간격이다.
- 실패 경로도 렌더 확인(`실행: ?` 는 테스트 payload 에 `executionArn` 이 없어서 — 실제론 SFN 이 채운다).
- `terraform plan -target=aws_lambda_function.dr_notify` → **`4 to add, 0 to change, 0 to destroy`**.
  `-target` 이 게이트 리소스를 안 건드림을 확인(tfvars 에 `enable_eks_dr` 가 없어 전체 apply 였다면
  warm DR 129개 destroy 였다).

**🔴 실패 알림의 코드블록엔 스크립트 출력이 안 온다 — 정적 안내문이 온다(§11.13 (b)(c)(d)).**
`set -x` 트레이스와 명령 에러는 **stderr** 인데 `stdoutTail` 은 `StandardOutputContent`(stdout 전용)이고,
게다가 자식 SM 의 `Failed` 는 `Fail` 타입이라 **정적 `Cause` 문자열만** 실을 수 있다.
**결정: B안(현재 설계 유지)** — 실패 진단은 CloudWatch 경유, 알림엔 포인터만. 시크릿 유출 표면 0 을
얻는 대가로 "알림 → SFN 콘솔 → 자식 실행 → commandId → CloudWatch" **3~4홉**을 수용한다.
위 테스트 payload 의 `stdoutTail: "테스트 실패 메시지"` 는 **실제보다 예쁘다** — 진짜 경로에선 그 자리에
"SSM 명령 실패 — ... CloudWatch 로그그룹 ... 에서 전문 확인" 안내문이 찍힌다.

- [ ] **Step 6: Commit** (사용자가 실행)

```bash
cd /home/user/Cledyu
git add infra/terraform/aws/dr-orchestration-lambda/addon-install \
        infra/terraform/aws/dr-orchestration-lambda/dns-switch \
        infra/terraform/aws/dr-orchestration-lambda/notify \
        infra/terraform/aws/dr-orchestration.tf infra/terraform/aws/README.md
git commit -m "feat(dr): 애드온 멱등 설치·DNS 전환·완료/실패 알림 Lambda"
```

---

### Task 5: 메인 상태 머신 13단계

> Plan 1 의 테스트 SM(`dr_approval_test`)을 실제 페일오버 SM 으로 대체하고, EventBridge 타겟을 옮긴다.

**브랜치:** `feat/dr-failover-main-sm` (**`origin/main` 에서 딴다** — T1~T4 는 PR #317 로 squash 머지됐다.
기존 `feat/dr-failover-executors` 를 rebase 하면 이미 main 에 있는 43커밋이 재생된다)

**Files:**
- Modify: `infra/terraform/aws/dr-orchestration.tf` (메인 SM · 실패 경로 · 자식 SM `CausePath` · 트리거 배선)
- Modify: `infra/terraform/aws/README.md` (terraform_docs 재생성 — 안 하면 pre-commit 훅이 커밋 중단)
- Modify: `infra/terraform/aws/dr-orchestration-lambda/notify/index.py` (잔여 #4b — allowlist 제거 · Cause 파싱)
- Modify: `infra/terraform/aws/scripts/bastion/04-wait-nodes-ready.sh` (잔여 #5·#6)
- ~~`dr-failover-buildspec.yml`~~ (잔여 #2 는 `73aad01` 로 해소 — 손댈 것 없음)
- ~~`scripts/bastion/12-verify-serving.sh`~~ (잔여 #3 은 `1da0e9f` 로 해소 — 실환경 검증만 T7)

**Interfaces:**
- Consumes: `aws_lambda_function.dr_approval_request`(Plan 1) · `aws_codebuild_project.dr_failover_tf`(T1) ·
  `aws_sfn_state_machine.dr_run_on_bastion`(T2) · bastion 스크립트(T3) · Lambda 2개(T4)
- Produces: `aws_sfn_state_machine.dr_failover` — EventBridge 타겟·`failover-trigger` env 가 이걸 가리키게 교체
- **[1] 의 승인 output `{snapshot, approvedBy, approvedAt}` → [7] 의 `SNAPSHOT_KEY` 로 흐른다**

> **🆕 T5 에서 함께 처리할 잔여 (2026-07-16 브랜치 감사 — 스펙 §11.16 (g)). #2·#3 은 2026-07-16 해소.**
> 전부 "T5 가 없으면 손대봐야 소용없거나, T5 를 만들면서 자연히 닿는 자리"라 여기로 모았다.
>
> | # | 내용 | 안 하면 / 처리 |
> |---|---|---|
> | **1** | **`STATE_MACHINE_ARN` 이 하네스를 가리킨다** — `dr_approval_test`(State 가 `RequestApproval` 하나, `End=true`) | **T5 의 본체.** 지금 `dr_orchestration_armed=true` 면 실재해에 승인 버튼이 뜨고, 눌러도 **아무 일도 안 일어나며 실패 알림도 없다**(성공으로 끝나므로). 운영자는 "페일오버가 돌고 있다"고 믿는다 → **T5 전까지 무장 금지** |
> | **2** ✅ | ~~buildspec 에 `-lock-timeout` 없음~~ (codex P2 도 지적) | **해소** — `init`·`apply` 양쪽에 `-lock-timeout=5m`. `cledyu-tf-lock` 이 잡혀 있어도 5분 재시도로 흡수(즉시 실패 → 수동 force-unlock 회피) |
> | **3** ✅ | ~~`12` 의 `curl https://auth.cledyu.com` 에 재시도 없음~~ (codex P2 도 지적) | **해소(로직)** — 30회×10s 재시도 루프(`if curl\|grep`, set -e 안전). ⚠️ **실환경 검증은 T7** — `[10]` DNS 전환 후에야 전파·캐시 흡수를 실측할 수 있다(오늘은 로직만 검증) |
> | **4** | `notify` 에 재시도 없음 | Discord 429/장애면 **성공·실패 알림 둘 다 유실**. → **SFN `Retry`(5s×3, backoff 2)를 `NotifyComplete`·`NotifyFailed` 에.** `urlopen` 은 429·5xx 에 `HTTPError` 를 던지므로 Lambda 가 실패하고 SFN 이 재시도한다(Python 변경 0, 재시도 이력이 콘솔에 남음). ⚠️ **`NotifyComplete` 에 `Catch` 를 달지 않는다** — 달면 성공한 페일오버에 "❌ 실패" 알림이 가는 **C2 재현**이다. 소진 시 실행 FAILED 로 두고, 콘솔의 FAILED 가 "알림이 왜 안 왔나"의 단서가 된다 |
> | **4b** 🔴 | ~~`failedState` allowlist 로 DNS 안내 분기 — **"해소" 였다**~~ | **거짓이었다. 되돌린다(스펙 §11.18 (c) 실측).** `.sync:2` 가 자식 `Error` 를 감싸 `failedState` 엔 **`States.TaskFailed`** 가 온다 → allowlist 3개(`RestartApps`·`VerifyServing`·`NotifyComplete`) **전부 영원히 안 맞아 `post_dns` 는 도달 불가**. `[11]`·`[12]` 실패 = **DNS 가 이미 EKS 인데** "온프렘 — 트래픽은 안전합니다" 를 보낸다(수정 전보다 나쁘다: 틀린 내용에 근거 없는 안심이 붙었다). → **이름 추론을 버리고 `$.dns.alb` `IsPresent`(지상 진실)로 분기**(아래 Step 1) |
> | **5** | `04` 의 `grep -cw Ready` 가 `Ready,SchedulingDisabled` 를 Ready 로 셈(실측) | cordon 된 노드를 통과시킨다. **배제가 목적에 맞다** — cordon 된 노드엔 파드가 안 뜨니 애드온 DEGRADED 를 못 막는다(= `[4.5]` 의 존재 이유를 못 채운다). → `awk '$2=="Ready"'` 정확 매치 |
> | **6** | `04` 의 `WANT=3` 하드코딩 vs terraform nodegroup desired | 한쪽만 바뀌면 **조용히** 어긋난다. → **`local.dr_hot_node_desired` 단일 출처**로 `[4]` 의 `DesiredSize` 와 `[4.5]` 의 `env`(`export WANT_NODES=…`)를 함께 먹인다. `[7]` 의 `SNAPSHOT_KEY` 와 **같은 검증된 기전**(T2 실측 통과)이라 새 메커니즘이 없다. ⚠️ **04 가 런타임에 EKS API 로 desired 를 읽는 안은 기각** — (1) bastion 엔 `eks:DescribeCluster` 만 있고 `DescribeNodegroup` 이 없다(실측) (2) 더 나쁜 건, `[4]` 가 조용히 실패해 desired=0 이면 **"0대를 기다리면 된다"로 읽어 공허참 통과**한다(§11.14 (f) 의 그 함정을 되밟는다) |

> **✅ 착수 전 결정 2건 — **둘 다 종결**(2026-07-16). 원문은 T1 실측에서 도출(스펙 §11.9).**
>
> **(1) 드릴을 main 에 대고 할 것인가 → 종결: main 머지 완료(PR #317, squash).**
> 계획서는 이걸 "머지 전이면 buildspec not found 로 드릴이 죽는다"로만 봤는데 **더 깊었다**: `[2]` 는
> `source_version = "main"` 이고 SM 은 `SourceVersion` 을 안 넘기므로, 머지 전이면 buildspec 뿐 아니라
> **`-target` 목록의 `aws_iam_role_policy.eks_dr_bastion_ssm_param` 도 main 에 없어 §11.17 (a) 가 그대로
> 재현**된다(~40분 복구 후 `[9]` 마지막 줄 AccessDenied → `[10]` fail-closed → 전부 복구됐는데 서비스가
> 안 돌아옴). **드릴 편의가 아니라 원리적 불능**이었다. → T5 브랜치는 **`origin/main` 에서 딴다**
> (squash 라 기존 `feat/dr-failover-executors` 를 rebase 하면 이미 main 에 있는 43커밋이 재생된다).
>
> **(2) [2] 에 `Retry` 를 붙일 것인가 → 종결: 붙이지 않는다.**
> **이 글은 `-lock-timeout` 추가 전에 쓰였다.** `73aad01` 이 `init`·`apply` 양쪽에 `-lock-timeout=5m` 을
> 넣어 **사람↔빌드 락 충돌을 빌드 안에서 terraform 이 흡수**한다(잔여 #2). 남은 판단은 "락 에러만 골라낼
> 수 있나"인데, CodeBuild `.sync` 실패는 **락이든 `-var` 누락이든 SFN 엔 같은 에러**로 온다(스펙 §11.18
> (b) 가 `.sync` 계열의 에러 감싸기를 실측). → **계획서 자신의 규칙("구분이 안 되면 Retry 를 붙이지
> 않는다 — 재해 중 30분 지연이 락 충돌보다 나쁘다")대로 안 붙인다.** 런북에 "승인 전 `terraform` 을
> 만지지 말 것"은 그대로 명시(Task 6).

- [ ] **Step 1: 메인 SM 정의**

**상태 이름·주체·다음 상태를 그대로 쓴다**(스펙 §5 표). 이름을 지어내지 말 것 — 런북·스펙·원장이
이 이름으로 서로를 참조한다.

| # | 상태 이름 | 주체 | 입력 | Next |
|---|---|---|---|---|
| 1 | `RequestApproval` | Lambda `.waitForTaskToken` | — | `TerraformApply` |
| 2 | `TerraformApply` | CodeBuild `.sync` | — | `ClearAlbParam` |
| **2.4** | **`ClearAlbParam`** 🆕 | SDK `ssm:deleteParameter` | — | `ResolveBastion` |
| 2.5 | `ResolveBastion` | SDK `ec2:describeInstances` | — | `CleanWarmEtcd` |
| 3 | `CleanWarmEtcd` | 자식 SM | `03-clean-warm-etcd.sh` | `ScaleNodes` |
| 4 | `ScaleNodes` | SDK(list+update, 폴링 없음) | — | `WaitNodesReady` |
| **4.5** | **`WaitNodesReady`** 🆕 | 자식 SM | **`04-wait-nodes-ready.sh`** | `InstallAddons` |
| 5 | `InstallAddons` | Lambda `addon-install`(`action=start`) | — | `WaitAddons` |
| 5a | `WaitAddons` | Wait 20s | — | `CheckAddons` |
| 5b | `CheckAddons` | Lambda `addon-install`(`action=check`) | — | `AddonsDone?` |
| 5c | `AddonsDone?` | Choice | — | `BootstrapApps` / `WaitAddons` |
| 6 | `BootstrapApps` | 자식 SM | `06-bootstrap-apps.sh` | `RestoreVault` |
| 7 | `RestoreVault` | 자식 SM | `07-restore-vault.sh` + **`SNAPSHOT_KEY`** | `RestoreData` |
| 8 | `RestoreData` | 자식 SM | `08-restore-data.sh` | `WaitAppsReady` |
| 9 | `WaitAppsReady` | 자식 SM | `09-wait-apps-ready.sh` | `SwitchDNS` |
| 10 | `SwitchDNS` | Lambda `dns-switch` | — | `RestartApps` |
| 11 | `RestartApps` | 자식 SM | `11-restart-apps.sh` | `VerifyServing` |
| 12 | `VerifyServing` | 자식 SM | `12-verify-serving.sh` | `NotifyComplete` |
| 13 | `NotifyComplete` | Lambda `notify` | `outcome: "success"` | **End** |
| — | `NotifyFailed` | Lambda `notify` | `outcome: "failed"` | **Fail** |

**[9] → [10] 순서가 강제된다**(런북 명시): auth 는 Keycloak CR Ready 이후에만 넘긴다 — 조기 전환 시
ALB keycloak 타겟 unhealthy 로 404/503. `09` 가 Keycloak Ready 를 기다리고 ALB 를 기록한 뒤에야 `10` 이 돈다.

**메인 SM 전체에 `TimeoutSeconds = 90000`**(승인 대기 24h=86400 + 복구 ~1h). `AddonsDone?` 의 폴링 루프가
영영 안 끝나는 경우의 backstop 이다 — 자식 SM 의 4200 과 별개로 부모에도 상한이 필요하다.

**타임아웃(자식 SM `timeoutSeconds` = SSM `executionTimeout`)** — **스크립트 내부 `wait` 합보다 커야 한다**
(리뷰 지적: 초안은 `08`=1800 인데 내부에 `--timeout=1200s` 가 2개라 **느리지만 정상인 복원을 SSM 이 죽였다**):

| 스크립트 | 내부 대기 합(최악) | `timeoutSeconds` | 여유 |
|---|---|---|---|
| `03` | cloud-init wait | 600 | — |
| `06` | rollout 300 + wait 300 | 1200 | ~2× |
| `07` | restore + generate-root + ESO rollout 120 | 1800 | 넉넉 |
| `08` | ArgoCD 재생성 600 + 1200 + 1200 = **3000** | **3600** | +600 |
| `09` | kafka 900 + topic 300 + VE 600 + KC 600 = **2400** | **3000** | +600 |
| `11` | rollout 300×2 = 600 | 900 | +300 |
| `12` | curl + psql | 300 | — |

**자식 SM 의 `TimeoutSeconds`(backstop)도 함께 올린다** — 가장 긴 `08`(3600) + 폴링 여유 → **4200**.
그리고 SSM `executionTimeout` 의 상한은 **172800**(48h)이므로 문제없다.

**핵심 배선:**

```hcl
      # [1] 승인 — Plan 1 의 approval-request 를 그대로 재사용
      RequestApproval = {
        Type     = "Task"
        Resource = "arn:aws:states:::lambda:invoke.waitForTaskToken"
        Parameters = {
          FunctionName  = aws_lambda_function.dr_approval_request.arn
          Payload = {
            "taskToken.$" = "$$.Task.Token"
            "input.$"     = "$"   # ⚠️ "mode.$"="$.mode" 금지 — 실재해엔 mode 가 없어 States.Runtime
          }
        }
        ResultSelector = { "snapshot.$" = "$.snapshot", "approvedAt.$" = "$.approvedAt" }
        ResultPath     = "$.approval"
        TimeoutSeconds = 86400
        Next           = "TerraformApply"
      }

      # [2] CodeBuild .sync — 최적화 통합이라 완료까지 대기한다.
      # ⚠️ SourceVersion 을 넘기지 않는다 → 프로젝트 기본값 main 을 쓴다. 실재해는 **검증된 main** 을
      # 돌려야 한다(재해 중에 브랜치를 굴리지 않는다). 드릴만 start-build --source-version 으로 오버라이드.
      TerraformApply = {
        Type       = "Task"
        Resource   = "arn:aws:states:::codebuild:startBuild.sync"
        Parameters = { ProjectName = aws_codebuild_project.dr_failover_tf.name }
        ResultPath = null   # Build 객체가 크다 — 페이로드에 안 싣는다
        Next       = "ClearAlbParam"
      }

      # [2.4] stale ALB 파라미터 삭제 — 스펙 §5.1.2 의 2중 방어 ①.
      #
      # ⚠️ 초안엔 이 상태가 **없었다**(적대적 검증 3회차 F4). 그런데 03-clean-warm-etcd.sh 의 주석은
      # "stale SSM 파라미터 삭제는 [2.5] ResolveBastion(SDK)이 한다 — 여기가 아니다(스펙 §5.1.2)" 라고
      # **존재하지 않는 구현을 가리키고 있었다.** 리뷰어가 그 주석을 읽으면 "저기서 하는군" 하고 넘어간다.
      # 스펙이 P1d(stale hostAlias)와 **같은 버그 클래스**라고 경고한 바로 그 방어가 통째로 증발한 것이다.
      #
      # SFN Task 는 API 1개라 ResolveBastion 과 합칠 수 없어 **별도 상태**로 둔다(스펙 §5.1.2 의
      # "[2.5] 가 삭제한다"는 상태 1개를 가정한 표현이다). [9] 가 쓰기 전이므로 항상 비운 채 진입한다.
      ClearAlbParam = {
        Type     = "Task"
        Resource = "arn:aws:states:::aws-sdk:ssm:deleteParameter"
        Parameters = { Name = "/cledyu-dr/failover/alb-hostname" }
        # 첫 failover 엔 파라미터가 없는 게 정상이다 → ParameterNotFound 를 삼킨다("없으면 무시", 스펙 §5.1.2).
        # ⚠️ 다른 에러(AccessDenied 등)까지 삼키면 stale 방어가 조용히 죽으므로 이 에러만 잡는다.
        #
        # 🔴 **에러명 미확정 — Step 4 에서 실측 확정한다.** 아래 `Ssm.ParameterNotFoundException` 은
        #    AWS SDK 통합의 명명 규칙에서 **유추한 것이고 확인한 적이 없다.** 이건 이 계획이 이미 두 번
        #    당한 실수와 같은 종류다: A3(`03` 의 webhook 이름 창작) · C-fix(`Ssm.InvalidInstanceIdException`
        #    을 지어냈다가 States.ALL 단독으로 교체). **틀리면 첫 failover 에서 [2.4] 가 Catch 를 못 타고
        #    통째로 죽는다** — 그리고 그건 stale 방어가 아니라 페일오버 전체가 멈추는 것이다.
        #    확정 방법(Step 4):
        #      aws stepfunctions get-execution-history --region ap-northeast-2 --execution-arn <ARN> \
        #        --query 'events[?type==`TaskFailed`].taskFailedEventDetails.error' --output text
        #    → 나온 문자열을 그대로 ErrorEquals 에 넣는다. 지어내지 말 것.
        Catch = [{
          ErrorEquals = ["Ssm.ParameterNotFoundException"] # 🔴 Step 4 실측 후 확정
          ResultPath  = null
          Next        = "ResolveBastion"
        }]
        ResultPath = null
        Next       = "ResolveBastion"
      }

      # [2.5] bastion instance id — CodeBuild 에서 받지 않는다(exported-variables 결합 회피).
      # Name 태그가 결정적이고 런북도 같은 경로를 쓴다(:231).
      ResolveBastion = {
        Type     = "Task"
        Resource = "arn:aws:states:::aws-sdk:ec2:describeInstances"
        Parameters = {
          Filters = [
            { Name = "tag:Name", Values = ["cledyu-dr-bastion"] },
            # ⚠️ running 필터 필수 — user_data_replace_on_change=true 라 교체 시 옛 인스턴스가
            # shutting-down 으로 남는다. 없으면 죽어가는 id 를 집어 이후 SSM 이 전부 실패.
            { Name = "instance-state-name", Values = ["running"] },
          ]
        }
        ResultSelector = { "instanceId.$" = "$.Reservations[0].Instances[0].InstanceId" }
        ResultPath     = "$.bastion"
        Next           = "CleanWarmEtcd"
      }
```

**[4] ScaleNodes — 초안은 이 상태를 위 표에만 두고 HCL·IAM 을 안 만들었다(F8):**

```hcl
      # [4] ScaleNodes — warm(desired 0) → hot(desired 3). T4 Step 5 에서 손으로 돌리던 것의 SFN 판.
      # 노드그룹 이름을 하드코딩하지 않는다 — 모듈이 이름에 접미사를 붙이므로 런타임 조회가 안전하다.
      ScaleNodes = {
        Type     = "Task"
        Resource = "arn:aws:states:::aws-sdk:eks:listNodegroups"
        Parameters = { ClusterName = "cledyu-dr" }
        ResultSelector = { "name.$" = "$.Nodegroups[0]" }
        ResultPath     = "$.ng"
        Next           = "UpdateNodegroup"
      }
      UpdateNodegroup = {
        Type     = "Task"
        Resource = "arn:aws:states:::aws-sdk:eks:updateNodegroupConfig"
        Parameters = {
          ClusterName       = "cledyu-dr"
          "NodegroupName.$" = "$.ng.name"
          # ⚠️ 모듈이 desired 를 ignore_changes 하므로 [2] 의 terraform 이 아니라 여기서 올린다
          # (buildspec 이 -var eks_dr_node_desired=0 을 넘기는 이유). 런북 Phase 1 (2) 와 동일한 값.
          ScalingConfig = { MinSize = 0, MaxSize = 6, DesiredSize = 3 }
        }
        ResultPath = null
        # 🔴 **초안은 여기서 Next = "WaitNodes" 였다. 그 게이트는 무효다 — T4 실측으로 증명됨(스펙 §11.14).**
        Next = "WaitNodesReady"
      }

      # 🔴 **초안의 WaitNodes/CheckNodes/NodesActive? 3상태를 삭제하고 이걸로 대체한다.**
      #
      # **초안이 왜 틀렸나(실측 2회 — 스펙 §11.14):** `$.Nodegroup.Status` 는 스케일 설정 변경 중에도
      # **`ACTIVE` 에서 벗어나지 않는다.** 스케일 전·중·후 전부 ACTIVE 다. 즉 대답이 안 바뀌므로
      # 질문 자체가 무의미하다 → `WaitNodes`(30s) 한 번 돌고 **노드 부팅 30초차에 InstallAddons** 로 넘어간다.
      #   · 15:56:52 update InProgress → 15:57:21 wait 반환(ACTIVE)  = 부팅 8초차에 "다 됐다"
      #   · 16:25:30 scale → 16:26:04 status=ACTIVE → 16:26:05 InstallAddons / **노드 Ready 도 16:26:05**
      #     = **여유 0초.** 노드가 35s 만에 떠준 덕에 우연히 살았다(EKS 노드 부팅은 통상 60~120s).
      #
      # **왜 kubectl 인가:** SFN 은 private EKS 에 못 닿는다(§5.1.2 와 같은 제약). "노드가 k8s 에 Ready 인가"를
      # 아는 주체는 클러스터 안의 bastion 뿐이다. ASG 의 InService 도 **kubelet 조인이 아니라 EC2 헬스체크**라
      # 같은 함정이다(실측: 인스턴스 running 시각 < 노드 Ready 시각).
      #
      # **왜 [5] 의 애드온 루프에 맡기지 않나:** 그 루프는 실제로 진짜 게이트다(실측: done=true 가 +105s =
      # 노드 Ready +35s 보다 70s 뒤 — CREATING 을 제대로 기다렸다). 하지만 `check` 가 **DEGRADED 를 치명으로
      # 본다.** DEGRADED 는 두 뜻이다 — (a) 노드가 아직 없어서 / (b) 진짜 고장(P1c). 지금은 구분 불가라
      # 어느 쪽으로 짜도 반은 틀린다. **여기서 노드를 확실히 세우면 이후 DEGRADED 는 (b) 뿐**이므로
      # `check` 의 치명 판정이 비로소 옳아진다. 대안(DEGRADED 를 참고 폴링)은 (b) 일 때 TimeoutSeconds
      # (90000=25h)까지 매달린다 — 재해 중 25시간 매달림은 빠른 실패보다 나쁘다.
      #
      # 비용: 자식 SM 왕복 ~30s. RTO 40분에서 30초.
      # ⚠️ 스크립트는 `04-wait-nodes-ready.sh` 로 신설한다(T3 의 7종 + 1). SSM 변환 규칙 전부 적용:
      #    `export HOME=/root`(§11.12) 필수 — 없으면 kubectl 이 localhost:8080 으로 폴백한다.
      #    내용: `kubectl wait --for=condition=Ready node --all --timeout=600s` + 대수 검증
      #    (`[ "$(kubectl get nodes --no-headers | wc -l)" -eq 3 ]` — wait 는 **0대일 때도 즉시 통과**한다.
      #     "모든 노드가 Ready" 는 노드가 없으면 공허참이다 — 과소 게이트 회피).
      WaitNodesReady = {
        Type     = "Task"
        Resource = "arn:aws:states:::states:startExecution.sync:2"
        Parameters = {
          StateMachineArn = aws_sfn_state_machine.dr_run_on_bastion.arn
          Input = {
            "instanceId.$" = "$.bastion.instanceId"
            script         = file("${path.module}/scripts/bastion/04-wait-nodes-ready.sh")
            env            = ":"
            timeoutSeconds = 900
            label          = "WaitNodesReady"
          }
        }
        ResultPath = null
        Next       = "InstallAddons"
      }
```

> **[4] 가 [3] 뒤인 이유:** [3] CleanWarmEtcd 는 노드 없이 warm API 서버에만 붙어 고아 webhook 을
> 지운다. 노드를 먼저 올리면 **coredns 애드온이 그 고아 webhook 때문에 CREATE_FAILED** 로 죽는다(P1c).
> 순서를 바꾸지 말 것.

**[3][6][7][8][9][11] 은 전부 자식 SM 호출:**
```hcl
      CleanWarmEtcd = {
        Type     = "Task"
        Resource = "arn:aws:states:::states:startExecution.sync:2"
        Parameters = {
          StateMachineArn = aws_sfn_state_machine.dr_run_on_bastion.arn
          Input = {
            "instanceId.$" = "$.bastion.instanceId"
            script         = file("${path.module}/scripts/bastion/03-clean-warm-etcd.sh")
            env            = ":" # 셸 no-op — env 는 항상 채운다([7] 만 실값). 없으면 States.Runtime.
            timeoutSeconds = 600
            label          = "CleanWarmEtcd"
          }
        }
        ResultPath = null
        Next       = "ScaleNodes"
      }
```

**[7] 만 승인 스냅샷을 주입한다 — `States.Format` 을 쓰면 안 된다(리뷰 지적 C1):**

> **초안은 이렇게 했는데 원리적으로 안 된다:**
> ```hcl
> "script.$" = "States.Format('export SNAPSHOT_KEY={}\n{}', $.approval.snapshot, '${file(".../07-restore-vault.sh")}')"
> ```
> ASL intrinsic 의 작은따옴표 리터럴은 **첫 이스케이프 안 된 `'` 에서 끝난다.** 07 스크립트엔
> `sh -c 'VAULT_CACERT=...'` 가 있어 거기서 리터럴이 끊기고, 스크립트의 `{ echo ...; exit 1; }` 중괄호는
> `States.Format` 이 **플레이스홀더로** 읽으며(리터럴은 `\{`·`\}` 필요), `\n` 은 intrinsic 인자에 들어갈 수
> 없는 실제 개행이 된다. → **`CreateStateMachine` 이 정의를 거부해 terraform apply 가 실패**하고,
> `terraform validate` 는 이걸 못 잡는다. **드롭다운의 존재 이유인 스냅샷 주입이 배포조차 안 되는 것이다.**

→ **문자열 조립을 하지 않는다. 자식 SM 입력에 `env` 를 별도 필드로 넘기고, 자식 SM 이 SSM
`AWS-RunShellScript` 의 `commands` 배열에 두 원소로 싣는다** — 배열 원소는 각각 온전한 JSON 문자열이라
따옴표·중괄호·개행이 전부 안전하다.

**자식 SM 입력 계약이 바뀐다:** `{instanceId, script, env?, timeoutSeconds, label}`

```hcl
      RestoreVault = {
        Type     = "Task"
        Resource = "arn:aws:states:::states:startExecution.sync:2"
        Parameters = {
          StateMachineArn = aws_sfn_state_machine.dr_run_on_bastion.arn
          Input = {
            "instanceId.$" = "$.bastion.instanceId"
            script         = file("${path.module}/scripts/bastion/07-restore-vault.sh")
            # 승인 시 고른 스냅샷 — 문자열에 섞지 않고 별도 필드로 넘긴다(드롭다운의 존재 이유).
            "env.$"        = "States.Format('export SNAPSHOT_KEY={}', $.approval.snapshot)"
            timeoutSeconds = 1800
            label          = "RestoreVault"
          }
        }
        ResultPath = null
        Next       = "RestoreData"
      }
```

> `States.Format` 을 **스냅샷 키에만** 쓴다 — S3 키(`vault/vault-raft-20260715T000001Z.snap`)엔
> 따옴표·중괄호·개행이 없어 안전하다. 나머지 6개 상태는 `env` 를 안 넘긴다.

**자식 SM 의 `commands` 를 두 원소로:**
```hcl
          Parameters = {
            # env 가 있으면 [env, script], 없으면 [script]. AWS-RunShellScript 는 commands 배열을
            # 순서대로 **같은 셸**에서 실행하므로 앞 원소의 export 가 뒤 스크립트에 적용된다.
            "commands.$"         = "$.commands"
            "executionTimeout.$" = "States.Array(States.Format('{}', $.timeoutSeconds))"
          }
```
그리고 `SendCommand` 앞에 `Pass` 상태를 하나 두어 배열을 만든다(`env` 유무 분기):
```hcl
      BuildCommands = {
        Type = "Pass"
        Parameters = {
          "instanceId.$"     = "$.instanceId"
          "timeoutSeconds.$" = "$.timeoutSeconds"
          "label.$"          = "$.label"
          # env 미지정 시 States.Array 가 null 을 원소로 넣지 않도록 기본값 ""(빈 export 없는 no-op)
          "commands.$" = "States.Array($.env, $.script)"
        }
        Next = "SendCommand"
      }
```
> **`env` 기본값이 필요하다** — 6개 상태는 `env` 를 안 넘기므로 `$.env` 가 없어 `States.Runtime` 이 난다.
> **메인 SM 의 모든 자식 SM 호출에 `env = ":"` 를 기본으로 넣는다**(`:` 는 셸 no-op). [7] 만
> `"env.$" = "States.Format(...)"` 로 덮어쓴다. 이러면 분기 없이 항상 2원소 배열이 된다.

**모든 상태에 `Catch` → NotifyFailed. 롤백하지 않는다**(설계 §5.3 — 재해 중엔 부분 완성이 0보다 낫고,
자동 롤백은 사람이 손댈 발판까지 치운다).

> **⚠️ `States.ALL` 이 전부를 잡지 않는다(AWS 문서 — 적대적 검증 2026-07-15).** 초안은 "모든 상태에
> Catch → NotifyFailed" 라고만 적었는데, 그러면 **정작 우리가 낼 만한 실수에서 알림이 안 간다:**
>
> | 에러 | 언제 나나 | `States.ALL` |
> |---|---|---|
> | `States.Runtime` | **ASL JSONPath 오타·null 에 InputPath 적용** — 우리가 제일 낼 만한 실수 | ❌ **catch 불가**(문서: "A retry or catch on States.ALL won't catch States.Runtime errors") |
> | `States.DataLimitExceeded` | 페이로드 256KB 초과 — **우리가 stdout 을 S3 로 버려 막으려던 것** | ❌ 단, **명시하면 잡힌다** |
>
> → **Catch 를 두 개 둔다.** `States.DataLimitExceeded` 를 명시적으로 먼저, `States.ALL` 을 마지막에
> (States.ALL 은 단독·마지막이어야 한다):
> ```hcl
> Catch = [
>   { ErrorEquals = ["States.DataLimitExceeded"], ResultPath = "$.error", Next = "NotifyFailed" },
>   { ErrorEquals = ["States.ALL"],               ResultPath = "$.error", Next = "NotifyFailed" },
> ]
> ```
> **`States.Runtime` 은 어떤 Catch 로도 못 잡는다** — 방어는 오직 **Step 4 의 실측**뿐이다. ASL 의
> JSONPath 오류는 `terraform validate` 도 못 잡으므로, 상태를 붙일 때마다 실행해 보는 것이 유일한 검증이다.

**[13] NotifyComplete · NotifyFailed — 초안엔 이 두 상태의 정의가 아예 없었다(F5):**

> **초안은 `notify/index.py` 를 정성껏 만들어놓고 그 입력을 채우는 `Payload` 매핑을 어디에도 안 적었다.**
> 그 결과 **이 계획의 헤드라인 산출물인 "RTO 2단" 이 `감지→승인: ? / 승인→서빙: ?` 로 나온다** —
> C2 에서 공들여 고친 `_ts()` 파서가 애초에 값을 못 받는 것이다. Interfaces 에 입력 계약을 적어두고
> 그걸 채우는 책임은 아무 Task 에도 없던, 이번 3회차의 전형적 패턴이다.

```hcl
      # [10] SwitchDNS — alb 를 [13] 에 넘겨야 하므로 결과를 버리지 않는다(ResultPath=null 금지).
      SwitchDNS = {
        Type       = "Task"
        Resource   = "arn:aws:states:::lambda:invoke"
        Parameters = { FunctionName = aws_lambda_function.dr_dns_switch.arn }
        ResultSelector = { "alb.$" = "$.Payload.alb" }
        ResultPath     = "$.dns"
        Next           = "RestartApps"
      }

      NotifyComplete = {
        Type     = "Task"
        Resource = "arn:aws:states:::lambda:invoke"
        Parameters = {
          FunctionName = aws_lambda_function.dr_notify.arn
          Payload = {
            outcome = "success"
            # ⚠️ detectedAt 은 $.detail.state.timestamp(알람 이벤트)가 아니라 **$$.Execution.StartTime** 을 쓴다.
            #   (a) 실행 시작 = 알람→EventBridge→trigger→StartExecution 이라 감지와 몇 초 차이다(RTO 보고엔 충분)
            #   (b) $.detail 은 **테스트 실행(`{"mode":"test"}`)에 없어서** States.Runtime 이 난다 —
            #       그리고 States.Runtime 은 어떤 Catch 로도 못 잡는다(위 절).
            #   (c) 컨텍스트 객체는 **항상** 있다 → 실재해·드릴·테스트가 같은 경로를 탄다(C2 의 교훈).
            "detectedAt.$" = "$$.Execution.StartTime"
            "approvedAt.$" = "$.approval.approvedAt"
            "alb.$"        = "$.dns.alb"
          }
        }
        End = true
      }

      # ⚠️ NotifyFailed 는 $.approval·$.dns 를 **직접 참조하면 안 된다** — [1] 이나 [2] 에서 실패하면 그
      # 경로가 아직 없어 States.Runtime 이 나고, **실패 알림 자체가 무음으로 죽는다.**
      # $.failedStep·$.flags.dnsSwitched 는 아래 실패 경로가 **항상** 채워주므로 안전하다.
      NotifyFailed = {
        Type     = "Task"
        Resource = "arn:aws:states:::lambda:invoke"
        Parameters = {
          FunctionName = aws_lambda_function.dr_notify.arn
          Payload = {
            outcome = "failed"
            # 🔴 **`$.error.Error` 를 쓰지 않는다 — 실측으로 무용 확정(스펙 §11.18 (b)).**
            #    `.sync:2` 가 자식 Error 를 감싸 **States.TaskFailed** 가 온다. bastion 7단계뿐 아니라
            #    `.sync` 를 쓰는 모든 상태가 같은 값이다. 대신 각 Task 의 Catch 가 자기 이름을
            #    static 으로 주입한 $.failedStep 을 쓴다(아래 "실패 경로" 절).
            "failedState.$" = "$.failedStep"
            # DNS 안내는 **이름 추론이 아니라 지상 진실**로 판정한다(§11.18 (c) — allowlist 는 dead code 였다).
            "dnsSwitched.$" = "$.flags.dnsSwitched"
            # Cause 는 **날것 그대로** 넘긴다. 파싱은 notify(Python)의 몫이다.
            # 🔴 **ASL 로 파싱하면 안 된다(§11.18 (e) 재현)** — 평문 Cause 에 States.StringToJson 을 쓰면
            #    States.Runtime 이고 Pass 는 Catch 를 못 단다(스키마 거부) → **알림이 무음으로 죽는다.**
            #    Cause 가 JSON 인 건 자식 SM 실패일 때뿐이고 [2]/[2.4]/[2.5] 는 평문이다.
            "stdoutTail.$"   = "$.error.Cause"
            "executionArn.$" = "$$.Execution.Id"
          }
        }
        # 잔여 #4 — Discord 429/장애로 실패 알림까지 유실되는 것을 막는다.
        Retry = [{
          ErrorEquals     = ["States.ALL"]
          IntervalSeconds = 5
          MaxAttempts     = 3
          BackoffRate     = 2.0
        }]
        # 재시도 소진 시에도 Fail 상태엔 도달시킨다 — 그래야 실행이 의도한 Error 로 끝난다.
        Catch = [{
          ErrorEquals = ["States.ALL"]
          ResultPath  = null
          Next        = "Failed"
        }]
        Next = "Failed"
      }
      Failed = {
        Type  = "Fail"
        Error = "DrFailoverFailed"
        Cause = "페일오버 실패 — Discord 알림과 실행 이력 참조. 롤백하지 않았다(설계 §5.3)."
      }
```

**실패 경로 — `Catch` 는 NotifyFailed 로 직행하지 않는다(스펙 §11.18 (f)):**

```
<Task> --Catch(ResultPath="$.error")--> <Task>Failed (Pass) --> DnsSwitched? --> Mark{Post,Pre}Dns --> NotifyFailed
           17개 Task 각각              자기 이름을 static 주입   Choice: $.dns.alb    flags.dnsSwitched
                                        → $.failedStep           IsPresent            → notify
```

```hcl
# ── 실패 경로 ① 어느 단계인가 — 각 Task 가 자기 이름을 static 으로 주입한다 ──
# 왜 생성하나: Task 상태가 17개다. 손으로 17번 쓰면 오타가 나고, 오타는 **재해 중에만** 드러난다.
# Choice·Wait·Pass 는 대상이 아니다 — **Pass 는 Catch 를 못 단다**(스키마 거부, §11.18 (e) 실측).
locals {
  # ⚠️ 이 목록은 아래 States 맵의 Task 상태와 **정확히 일치**해야 한다. Step 3 에 대수 검증을 둔다.
  dr_failover_tasks = [
    "RequestApproval", "TerraformApply", "ClearAlbParam", "ResolveBastion",
    "CleanWarmEtcd", "ScaleNodes", "UpdateNodegroup", "WaitNodesReady",
    "InstallAddons", "CheckAddons", "BootstrapApps", "RestoreVault",
    "RestoreData", "WaitAppsReady", "SwitchDNS", "RestartApps", "VerifyServing",
  ]

  # 각 Task 에 붙일 Catch. States.ALL 은 **단독·마지막**이어야 한다(AWS 문서).
  dr_catch = { for s in local.dr_failover_tasks : s => [
    { ErrorEquals = ["States.DataLimitExceeded"], ResultPath = "$.error", Next = "${s}Failed" },
    { ErrorEquals = ["States.ALL"], ResultPath = "$.error", Next = "${s}Failed" },
  ] }

  # 이름 주입 Pass 17개.
  dr_failed_states = { for s in local.dr_failover_tasks : "${s}Failed" => {
    Type       = "Pass"
    Result     = s
    ResultPath = "$.failedStep"
    Next       = "DnsSwitched?"
  } }

  # ── 실패 경로 ② 트래픽이 어디 있나 — 이름이 아니라 페이로드 실물을 본다 ──
  dr_dns_states = {
    # SwitchDNS 가 ResultPath="$.dns" 라 **$.dns.alb 의 존재 ⟺ [10] 통과**다.
    # ⚠️ IsPresent 가드 필수 — [10] 전에 죽으면 그 경로가 아예 없다. 경로 없는 Variable 을 Choice 가
    #    어떻게 다루는지는 미확정이나 States.Runtime 이면 **어떤 Catch 로도 못 잡는다**(자식 SM 의
    #    AgentReady? 가 같은 이유로 같은 가드를 쓴다 — 선례).
    # ⚠️ **allowlist 로 되돌아가지 말 것.** failedStep 이 정확해져 이름 판정도 "작동은" 하지만,
    #    [10]↔[11] 사이에 상태가 하나 끼는 순간 **조용히** 틀린다. IsPresent 는 안 틀린다.
    "DnsSwitched?" = {
      Type    = "Choice"
      Choices = [{ Variable = "$.dns.alb", IsPresent = true, Next = "MarkPostDns" }]
      Default = "MarkPreDns"
    }
    MarkPostDns = {
      Type = "Pass", Result = { dnsSwitched = true }, ResultPath = "$.flags", Next = "NotifyFailed"
    }
    # SwitchDNS 자체 실패도 여기로 온다 — [10] 은 fail-closed 라 "온프렘"이 참이다.
    MarkPreDns = {
      Type = "Pass", Result = { dnsSwitched = false }, ResultPath = "$.flags", Next = "NotifyFailed"
    }
  }
}
```

> **`Catch` 의 `ResultPath = "$.error"` 가 매핑의 전제다** — `{Error, Cause}` 가 거기 들어간다.
> Catch 를 쓰는 모든 상태가 같은 `ResultPath` 를 써야 NotifyFailed 가 한 벌로 동작한다.
> `Pass` 의 `Result`+`ResultPath` 는 `$.error` 를 덮지 않는다(다른 경로에 쓴다).

**[3][6][7][8][9][11][12] 의 자식 SM `Failed` 를 `CausePath` 로 바꾼다(§11.18 (d) — 3~4홉 → 0홉):**

```hcl
      # 자식 SM(dr_run_on_bastion)의 Failed. 초안은 정적 Cause 라 commandId 를 못 실었고,
      # §11.13 (d) 는 그걸 "B안의 비용"으로 수용했다. **그 전제가 실측으로 깨졌다** — CausePath 가 된다.
      # label 과 commandId 는 시크릿이 아니므로 B안의 안전성(stderr 를 알림에 안 올림)은 그대로다.
      Failed = {
        Type  = "Fail"
        Error = "BastionScriptFailed"
        CausePath = "States.Format('{} 실패 — aws logs tail ${aws_cloudwatch_log_group.dr_bastion_commands.name} --log-stream-name-prefix {}', $.label, $.cmd.Command.CommandId)"
      }
```
> ⚠️ `$.cmd` 는 `SendCommand` 의 `ResultPath` 라 **SendCommand 성공 후에만** 있다. `WaitForSsmAgent`·
> `SendCommand` 자체가 실패하면 이 `Failed` 를 안 거치고 Retry 소진 후 자기 에러로 죽는다(부모의 Catch 가
> 잡는다) → `CausePath` 가 없는 경로를 참조할 일은 없다. **Step 4 에서 이 가정을 확인한다.**

- [ ] **Step 2: 트리거를 메인 SM 으로 교체**

```hcl
# failover-trigger 의 env·IAM 을 테스트 SM → 메인 SM 으로
resource "aws_lambda_function" "dr_failover_trigger" {
  environment {
    variables = {
      STATE_MACHINE_ARN = aws_sfn_state_machine.dr_failover.arn   # was: dr_approval_test
      SFN_REGION        = var.region
    }
  }
}
data "aws_iam_policy_document" "dr_failover_trigger" {
  statement {
    sid       = "StartFailover"
    actions   = ["states:StartExecution"]
    resources = [aws_sfn_state_machine.dr_failover.arn]           # was: dr_approval_test
  }
  ...
}
```

> **테스트 SM(`dr_approval_test`)은 삭제하지 않는다** — §7.2 의 과금 ~0 승인 경로 검증 하네스로 계속 쓴다.

- [ ] **Step 3: 정적 검증**

```bash
cd /home/user/Cledyu
# ⚠️ shellcheck·shfmt·ruff·terraform_docs 를 로컬 설치 여부로 건너뛰지 말 것 — 훅이 자기 것을 쓰므로
#    커밋 때 반드시 걸린다(T3 에서 "미설치 — 건너뜀" 했다가 SC2015 로 커밋 거부됐다).
#    shfmt·terraform_docs 는 **파일을 수정**하므로 먼저 돌려 확정한 뒤 add 한다.
pre-commit run --files infra/terraform/aws/dr-orchestration.tf \
  infra/terraform/aws/dr-orchestration-lambda/notify/index.py \
  infra/terraform/aws/scripts/bastion/04-wait-nodes-ready.sh infra/terraform/aws/README.md
```
✅ 2026-07-16 실측 전부 Passed (terraform fmt·validate·tflint·docs · shellcheck · shfmt · ruff ·
mixed-line-ending). `validate` 가 `Error: Cycle` 을 안 냈다 = dr_sfn_child 분리가 유효하다.

**🆕 대수 검증 — `dr_failover_tasks` 목록 == SM 의 실제 Task 집합인가.** 어긋나면 조용히 위험하다:
목록에만 있으면 고아 `<X>Failed` Pass(혼란), **SM 에만 있으면 그 상태엔 Catch 가 없어 실패가 알림 없이
실행을 죽인다**(무음 사망). 손으로 17개를 세는 건 §11.11 이 경고한 바로 그 과소 게이트다.

```bash
cd /home/user/Cledyu/infra/terraform/aws
# 목록
awk '/^  dr_failover_tasks = \[/,/^  \]/' dr-orchestration.tf |
  grep -oE '"[A-Za-z?]+"' | tr -d '"' | sort -u > /tmp/list.txt
# dr_failover 리소스 블록 안의 Type="Task" 상태 (자식 SM 의 Task 를 안 섞으려면 블록 스코프가 필수)
awk '/^resource "aws_sfn_state_machine" "dr_failover"/,0' dr-orchestration.tf | awk '
  /^      "?[A-Za-z?]+"? += \{$/ { n=$1; gsub(/"/,"",n); cur=n; next }
  /^        Type +=/ { t=$3; gsub(/"/,"",t); if (t=="Task" && cur!="") print cur; cur="" }' | sort -u > /tmp/sm.txt
# Catch 를 **의도적으로** 안 단 Task 2개. 세 번째가 생기면 그건 진짜 결함이므로 잡혀야 한다.
#   NotifyComplete : Catch 를 달면 성공한 페일오버에 "❌ 실패" 알림(C2 재현) → 금지
#   NotifyFailed   : 이미 실패 경로다. 자기 Catch 로 Failed 에 도달시킨다
printf 'NotifyComplete\nNotifyFailed\n' > /tmp/nocatch.txt
diff <(comm -23 /tmp/sm.txt /tmp/list.txt) /tmp/nocatch.txt \
  && echo "✅ Catch 없는 Task 는 의도한 2개뿐" || echo "🔴 Catch 빠진 상태 발견 — 무음 사망"
# 목록에만 있는 것(= 고아 Failed Pass). 출력이 있으면 결함이다.
comm -13 /tmp/sm.txt /tmp/list.txt > /tmp/orphan.txt
[ -s /tmp/orphan.txt ] && { echo "❌ 목록에만 있음 = 고아 Failed Pass:"; cat /tmp/orphan.txt; } \
  || echo "✅ 고아 Failed Pass 없음"
```
✅ 2026-07-16 실측: 일치(17 + 의도적 제외 2 = SM 의 Task 19). **검증기 자체도 음성 대조했다** —
목록에서 `RestartApps` 를 빼니 `+RestartApps` 로, SM 에 없는 `GhostState` 를 넣으니 고아로 각각 잡혔다.
(검증기를 안 검증하면 그게 또 하나의 과소 게이트다 — T2 가 `SUCCEEDED` 를 믿었다 로그를 잃은 것과 같다.)

**🆕 타임아웃 정합성 — `내부 대기 합 < timeoutSeconds < 자식 SM 백스톱`** (codex P2, 2026-07-16)

`timeoutSeconds` 는 SSM `executionTimeout` 으로 전달돼 명령을 **강제 종료**한다. 내부 합이 그보다 크면
각 단계가 자기 제한 안에서 **정상 진행 중인데 SSM 이 먼저 죽인다** — `[9]` 면 DNS 전환 직전에 죽어
"전부 복구됐는데 서비스가 안 돌아온다". **사람이 세면 틀린다 — 실제로 3건 틀렸다:**

| | 주장 | 실제 | 원인 |
|---|---|---|---|
| `09` | 주석 "내부 합 2400" · 선언 3000 | **5700** | `kubectl wait --timeout` 만 세고 **존재 게이트 5개(3000초)를 빠뜨림.** 계획서 표의 2400 을 베꼈는데 그 표는 존재 게이트 추가(`79e9605`, §11.16 (b)) **이전** 것이다 |
| `04` | 주석 "직렬 아님, 여유" · 선언 900 | **1200** | **직렬이 맞다**(등장 루프 → wait). 스스로 합리화하고 넘어갔다 |
| `08` | 계획서 "3000 → +600 여유" · 선언 3600 | **3600** | 여유 **0** — 최악의 경우 경계에서 죽는다 |

→ **자동 검증기**를 뒀다. 손으로 세지 말 것:
```bash
python3 infra/terraform/aws/scripts/check-timeouts.py   # exit 0 정상 / 1 결함
```
✅ 2026-07-16 정합 확인. 해소 방법은 **양쪽**이다 — 예산을 줄이고(존재 게이트 600→300, 08 의 ArgoCD
재생성 600→300) 남는 합 위로 선언을 올렸다(04: 900→1200 · 09: 3000→4800). **자식 SM 백스톱도 같이
올려야 한다**(4200→5400) — 안 그러면 그게 먼저 걸려 SSM 의 TimedOut 대신 States.Timeout 이 나고
"어느 스크립트가 왜" 가 사라진다. 검증기가 이 3중 부등식을 전부 본다(음성 대조 3방향 확인).

> ⚠️ **검증기는 주석을 걷어내고 센다.** 초안이 09 주석 속 `--timeout=900s` 를 세서 5700 을 6600 으로
> 잘못 냈다 — T3 Step 9 가 경고한 "주석의 나쁜 예를 grep 이 오탐" 을 그대로 밟은 것이다.

**ASL JSONPath 는 여기서 안 잡힌다** — `States.Runtime` 은 `terraform validate` 도 Catch 도 못 잡으므로
**Step 4 의 구간별 실행이 유일한 검증이다.**

- [ ] **Step 4: 운영자 실측 — 상태를 붙일 때마다 거기까지 도달하나**

**한 번에 13단계를 다 넣지 말고**, `Next` 를 바꿔가며 **3구간**으로 확인한다. `States.Runtime`(JSONPath
오타·없는 경로)은 **어떤 Catch 로도 못 잡고 `terraform validate` 도 못 잡으므로, 실행이 유일한 검증이다.**

| 구간 | 끝 상태 | 여기서만 볼 수 있는 것 |
|---|---|---|
| 1 | `ResolveBastion` | `[1]` 의 `$.approval` 매핑 · `[2]` CodeBuild `.sync` · **`[2.4]` 의 Catch** · `[2.5]` 의 `Reservations[0]` |
| 2 | `WaitNodesReady` | `[3]` env 계약 · `[4]` 노드그룹 런타임 조회 · **`[4.5]` 의 `WANT_NODES` env 주입**(잔여 #6) |
| 3 | `NotifyComplete` | `[7]` 의 `States.Format` 스냅샷 주입 · `[10]` 의 `$.Payload.alb` · **`[13]` 의 RTO 2단**(F5 회귀) |

```bash
cd /home/user/Cledyu/infra/terraform/aws
terraform apply -target=aws_sfn_state_machine.dr_failover
ARN=$(aws stepfunctions list-state-machines --region ap-northeast-2 \
  --query "stateMachines[?name=='cledyu-lab-dr-failover'].stateMachineArn" --output text)
aws stepfunctions start-execution --region ap-northeast-2 --state-machine-arn "$ARN" --input '{"mode":"test"}'
# → Discord 승인 → 각 상태 통과 확인
aws stepfunctions get-execution-history --region ap-northeast-2 --execution-arn <ARN> \
  --query 'events[?type==`TaskStateEntered`].stateEnteredEventDetails.name' --output text
```
Expected(구간 3): `RequestApproval → TerraformApply → ClearAlbParam → ResolveBastion → CleanWarmEtcd →
ScaleNodes → UpdateNodegroup → WaitNodesReady → InstallAddons → WaitAddons → CheckAddons → ...` 순서대로.
(초안의 `WaitNodes`/`CheckNodes`/`NodesActive?` 는 **삭제**됐다 — 스펙 §11.14 로 무효 판정)

> ⚠️ **첫 회차는 hot 이 없다**(2026-07-16 실측: bastion 없음·노드 desired 0) → `[2]` 가 NAT·bastion 을
> **실제로 만든다**(~3분). 2회차부터는 terraform 멱등이라 빠르다. 계획서 초안의 "hot 이 이미 떠 있으므로
> [2] 는 no-op" 은 T4 드릴 직후를 전제한 서술이었다.

**✅ `ClearAlbParam` 의 에러명은 착수 전 탐침으로 확정됐다 — `Ssm.ParameterNotFoundException`**
(스펙 §11.18 (a)). 계획서의 유추가 **맞았다.** SFN 의 SDK 통합은 와이어 코드(`ParameterNotFound`)가 아니라
**SDK 예외 클래스명**을 에러명으로 쓴다(와이어 코드는 `Cause` 에만). → 코드 변경 불필요. **다만 드릴에서
회귀 확인은 한다** — 첫 실행은 파라미터가 없으므로 `[2.4]` 가 Catch 를 타고 `ResolveBastion` 으로 넘어가야
한다(안 넘어가면 여기서 실행이 죽는다):

```bash
aws stepfunctions get-execution-history --region ap-northeast-2 --execution-arn <ARN> \
  --query 'events[?type==`TaskFailed`].taskFailedEventDetails.error' --output text
# → Ssm.ParameterNotFoundException 하나만 나오고 실행은 계속됐으면 정상(Catch 가 삼킨 것)
```

> **🔴 실패하면 `redrive` 가 아니라 `start-execution` 으로 `[1]` 부터다** (2026-07-16 실측, 스펙 §11.18 (j)).
> `redrive-execution` 은 **이 SM 에선 아무것도 못 한다** — "unsuccessful step 부터 재개"인데, 우리 설계는
> **모든 Task 의 실패를 `Catch` 가 성공적으로 처리**해 `NotifyFailed`(TaskSucceeded)까지 흘려보내므로
> 실행이 실제로 실패한 지점은 **종착지 `Fail` 상태**다. redrive 는 그 `Fail` 만 다시 밟고 **1초 만에 끝난다**
> (실측: `ExecutionRedriven` → 같은 초 `ExecutionFailed`, 대상 Lambda 로그 0건).
> `describeExecution` 이 `redriveStatus: REDRIVABLE` 이라고 답하지만 그건 **"호출이 거부되지 않는다"** 는
> 뜻이지 "원하는 지점부터 간다"가 아니다 — 에이전트가 그걸 보고 오판했다.
> **알림(Catch)과 redrive 는 양립하지 않고, 설계 §5.3 상 알림이 옳다.** 대신 재개 비용을 알고 있어야 한다:
> 승인 재클릭 + `[3]` 이 Vault PVC 를 비우고 `[7]`·`[8]` 이 **S3 에서 재복원** = 20~30분. hot 은 그대로라
> `[2]`~`[6]` 은 빠르다. **런북(T6)에 명시할 것.**
>
> ⚠️ **redrive 가 통하는 유일한 경우:** 수정이 **SM 정의 밖**(Lambda 코드·IAM)이고 실행이 **Catch 없는
> 지점**에서 죽었을 때. 우리 SM 엔 그런 Task 가 `NotifyComplete` 뿐이다.

> **🔴 재실행 전 필수 점검 — bastion 스크립트를 고쳤으면 `terraform apply` 를 해야 반영된다**(스펙 §11.18 (l)).
> 스크립트는 `file()` 로 **apply 시점에 SM 정의 안에 구워지는 복사본**이다 → `git commit` 만으론 AWS 가
> 모른다. **`[2]` 의 CodeBuild 도 자가 수리 못 한다**(SM 은 `-target` 18개에 없다).
> 2026-07-16 실측: `[3]` 을 고치고 재실행 직전에 확인하니 **배포된 SM 엔 구 게이트가 그대로**였다.
> ```bash
> terraform apply -target=aws_sfn_state_machine.dr_failover
> aws stepfunctions describe-state-machine --state-machine-arn <메인SM> --query definition --output text \
>   | python3 -c "import json,sys;print(json.load(sys.stdin)['States']['CleanWarmEtcd']['Parameters']['Input']['script'])" \
>   | grep -c endpointslice     # 0 이면 미반영 — 이대로 돌리면 [3] 에서 또 죽는다
> ```


**실패 경로도 한 번은 밟아본다** — 성공 경로만 보면 `$.failedStep`·`$.flags.dnsSwitched` 매핑이 틀려도
모른다. 구간 1에서 `[2.4]` 의 `Catch` 를 잠시 떼면 거기서 실패하므로, `ClearAlbParamFailed → DnsSwitched? →
MarkPreDns → NotifyFailed` 를 타고 Discord 에 **`실패 단계: ClearAlbParam`**(`States.TaskFailed` 가 아니라)과
"DNS 는 아직 온프렘" 이 뜨는지 본다. 확인 후 `Catch` 를 되돌린다.

**그리고 [13] 의 RTO 가 `?` 가 아닌지 본다** — `?` 면 `_ts()` 가 아니라 **Payload 매핑**이 틀린 것이다
(F5 회귀). `$$.Execution.StartTime`·`$.approval.approvedAt` 이 실제로 실렸는지 확인:
```bash
aws stepfunctions get-execution-history --region ap-northeast-2 --execution-arn <ARN> \
  --query 'events[?type==`TaskScheduled`].taskScheduledEventDetails.parameters' --output text | grep -o 'detectedAt[^,]*'
```

- [ ] **Step 5: Commit** (사용자가 실행)

```bash
cd /home/user/Cledyu
git add infra/terraform/aws/dr-orchestration.tf infra/terraform/aws/README.md
git commit -m "feat(dr): 페일오버 메인 상태 머신 13단계 + 트리거 배선 교체"
```

---

### Task 6: 런북 3개 반영

> 자동화만 고치면 수동 경로가 깨진 채 남는다. **P1c 는 런북에 아예 없다** — 자동화가 먼저 알아낸 것을
> 사람 경로에도 돌려준다.

**Files:**
- Modify: `docs/RUNBOOK/dr-eks-bootstrap.md`
- Modify: `docs/RUNBOOK/dr-detection.md`
- Modify: `docs/RUNBOOK/dr-failback.md`

- [ ] **Step 1: `dr-eks-bootstrap.md`**
  - **CleanWarmEtcd 스텝 신설**(P1c) — `[P1b]` CNPG 가드와 같은 성격·같은 자리
  - **P1d**(stale hostAlias — 7/14 드릴 발견) 원인·조치 기록
  - **WAF 확인을 필수 게이트로**(현재는 "확인한다"로만)
  - api 로그 게이트에 **부분매칭 주의** 추가("db 연결 실패"가 "db 연결"을 포함)
  - **§Phase 1 의 `-target` 목록을 17 → 18 로**(`aws_iam_role_policy.eks_dr_bastion_ssm_param` 추가,
    T3 Step 8). 안 하면 **수동 경로로 페일오버한 운영자만 `09-` 의 put-parameter 에서 AccessDenied** 를
    맞는다 — 자동화는 고쳐졌는데 사람 경로가 깨진 채 남는, 이 Task 가 존재하는 이유 그 자체다
  - **sync-wave 주석 정정(H6)** — `:296` 이 "(cert-manager -10 → pki -8 → ... → api/web 0)"이라 적었으나
    실제 `gitops/argocd/apps-eks/service-api.yaml:12` 는 **`sync-wave: "2"`** 다. 이 오해가 H1 을
    키웠다(api 가 wave 0 이면 "곧 뜨겠지"로 보이지만 실제론 마지막 wave 라 한참 뒤다)
  - **§apps-eks 부트스트랩 끝의 확인 2줄에 "폴링해서 볼 것" 명시(H1)** — `kubectl get clusterissuer` ·
    `kubectl -n api get configmap cledyu-root-ca-bundle` 은 그 시점에 **없는 게 정상**이다.
    사람이 읽어도 "없으면 실패"로 오해할 수 있게 적혀 있다
  - **`git clone` 을 재실행 안전 형태로(H2)** — 런북 독자도 두 번째 시도에서 같은 걸 밟는다

- [ ] **Step 2: `dr-detection.md`**
  - 상단 경고를 **"무장 가능"** 으로 갱신(Plan 2 완료 = 하류가 붙음)
  - 승인 갈래 다이어그램에 [2]~[13] 반영
  - **🆕 "승인 전 `terraform` 을 만지지 말 것"(T1-2, 스펙 §11.9)** — state 락은 하나라서 운영자의
    `terraform plan/apply` 가 [2] 를 죽인다. **재해 중엔 상태를 확인하려고 terraform 을 칠 확률이
    오히려 높다.** 확인이 필요하면 `terraform` 대신 `aws` CLI 읽기 명령을 쓴다
  - **🆕 "미커밋 로컬 수정은 재해 중 무시된다"(T1-3, 스펙 §11.9)** — [2] 는 **GitHub main** 을 clone 한다.
    운영자 디스크의 미커밋 수정은 **조용히 롤백된다.** DR 직전에 급히 고쳤다면 **반드시 커밋·푸시·머지**해야
    [2] 가 그걸 본다. (T1 실측 중 실제로 이 사고가 났다 — 로컬 수정을 apply 해놓고 빌드를 돌려 옛 코드가
    되돌렸다)

- [ ] **Step 3: `dr-failback.md`**
  - **step 0 게이트 신설** — `backupEnabled=true` 확인. false 면 여기서 flip·커밋·sync 후 진행.
    **step 1(서비스 quiesce) 앞**이라 누락 시 서비스가 살아 있는 동안 발견된다(설계 §8.1)

- [ ] **Step 4: Commit** (사용자가 실행)

```bash
cd /home/user/Cledyu
git add docs/RUNBOOK/
git commit -m "docs(dr): 런북에 CleanWarmEtcd(P1c)·P1d·WAF 게이트·failback step0 반영"
```

---

### Task 7: 전체 드릴 + RTO 실측 + 파괴

> T1~T5 에서 구간별로 검증했으므로 여기선 **클린 실행 1회**로 RTO 를 측정한다.

- [ ] **Step 1: 클린 상태로 되돌리기**

점진적 드릴에서 만든 상태를 지우고 **진짜 재해와 같은 조건**에서 시작한다.

```bash
# 런북 §destroy 의 고아 방지 순서를 따른다(클러스터 먼저 부수면 ALB·ENI·EBS 가 고아)
# → 런북 :498-573 참조. 노드만 0 으로 내리는 게 아니라 hot 리소스를 전부 내린다.
```

- [ ] **Step 1.5: 표식 주입 — 🔴 이 드릴에서 유일하게 stale 복원을 잡을 수 있는 장치**

> **왜 필요한가:** `08-restore-data.sh:24` 의 미확정(**`kubectl delete cluster` 가 PVC 도 지우나**)이
> T3 Step 10 을 그냥 지나갔다. 안 지워지면 **CNPG 가 옛 PVC 의 PGDATA 를 재사용해 뜨고
> `bootstrap.recovery` 가 아예 안 돈다** — S3 복원본이 아니라 **지난 드릴 데이터로 서비스가 올라간다.**
>
> **그리고 이 드릴은 그걸 절대 못 잡는다.** `09` 는 "파드 Ready?", `12` 는 "`users` 에 row 가 있나?" 만
> 본다 — **옛날 데이터에도 row 는 있다.** 온프렘과 DR 데이터가 어차피 같으니 stale/fresh 를 구분할
> 방법이 **원리적으로 없다.** → **드릴이 초록불로 끝나도 "복구됐다"가 아니라 "뭔가 떴다"일 뿐이다.**
> 표식은 그 구분을 만드는 유일한 수단이다: **스냅샷 이후에 넣은 값이 복원본에 보이면 fresh, 없으면 stale.**

```bash
# ⚠️ 알람 발동(Step 2) **전에** 넣는다 — 재해가 시작되면 온프렘 쓰기가 멈춘다.
# ⚠️ 레포 선례대로 -U 없이(H3). 온프렘 컨텍스트 고정 — DR 에 넣으면 시험 자체가 무의미하다.
MARK="drill-$(date -u +%Y%m%dT%H%M%SZ)"; echo "표식: $MARK"

# 전용 테이블 — users 등 실제 스키마를 안 건드린다(드릴이 프로덕션 데이터를 오염시키면 안 된다).
kubectl --context onprem -n postgres exec cledyu-pg-1 -- psql -d cledyu -c \
  "CREATE TABLE IF NOT EXISTS dr_drill_marker (id text primary key, at timestamptz default now())"
kubectl --context onprem -n postgres exec cledyu-pg-1 -- psql -d cledyu -c \
  "INSERT INTO dr_drill_marker (id) VALUES ('$MARK')"

# ⚠️ WAL 이 S3 에 닿아야 복원본에 들어온다 — 강제 스위치로 현재 세그먼트를 아카이브시킨다.
kubectl --context onprem -n postgres exec cledyu-pg-1 -- psql -d cledyu -tAc "SELECT pg_switch_wal()"

# 아카이브 확인 — 여기서 실패하면 표식이 S3 에 없으니 Step 4 판정이 **거짓 음성**이 된다.
kubectl --context onprem -n postgres exec cledyu-pg-1 -- psql -d cledyu -tAc \
  "SELECT last_archived_wal, last_failed_wal FROM pg_stat_archiver"
```
`last_failed_wal` 이 비어 있고 `last_archived_wal` 이 방금 값이어야 한다.

- [ ] **Step 2: 무장 + 알람 발동**

```bash
cd /home/user/Cledyu/infra/terraform/aws
terraform apply -var dr_orchestration_armed=true \
  -target=aws_cloudwatch_event_rule.dr_disaster \
  -target=aws_cloudwatch_event_target.dr_disaster \
  -target=aws_lambda_permission.dr_disaster

aws cloudwatch set-alarm-state --region us-east-1 --alarm-name cledyu-lab-dr-disaster \
  --state-value ALARM --state-reason "Plan 2 전체 드릴"
```

- [ ] **Step 3: 승인 → 완주 관찰**

Discord 승인 버튼 클릭(**드롭다운에서 최신이 아닌 스냅샷을 골라** [7] 의 주입 경로를 검증).

**RTO 2단 기록(설계 §5.1.5):**
| 구간 | 시각 | 소요 |
|---|---|---|
| 감지(SFN 실행 시작) | | — |
| 승인 클릭 | | (사람 지연) |
| [12] VerifyServing 통과 | | **← 자동화 RTO** |

- [ ] **Step 4: 서빙 검증**

```bash
curl -sf https://auth.cledyu.com/realms/cledyu-learn | grep -q cledyu-learn && echo "✅ realm"
curl -s -o /dev/null -w "%{http_code}\n" https://api.cledyu.com/metrics   # → 403 (WAF)
```

**🔴 표식 판정 — 이 드릴의 진짜 합격선이다(Step 1.5 참조).**

```bash
# bastion 에서(DR 은 private). 표식이 없으면 복원이 아니라 stale PVC 재사용이다.
NG_MARK=$(kubectl -n postgres exec cledyu-pg-1 -- psql -d cledyu -tAc \
  "SELECT count(*) FROM dr_drill_marker WHERE id = '$MARK'" 2>&1 | tr -d '[:space:]')
```
| 결과 | 뜻 |
|---|---|
| `1` | ✅ **fresh** — S3 복원본이 서빙된다. `08` 의 delete 가 PVC 까지 지웠다(미확정 해소) |
| `0` | ❌ **stale** — PVC 가 재사용됐다. `bootstrap.recovery` 가 안 돌았다 |
| `ERROR: relation ... does not exist` | ❌ **stale** — 표식 테이블 생성 이전 시점의 디스크다 |

> **⚠️ `0`·`ERROR` 면 다른 게이트가 전부 통과했어도 드릴은 실패다.** `12-verify-serving.sh` 의
> `SELECT count(*) FROM users` 는 이 경우에도 통과한다 — 옛 데이터에도 users 는 있다. **"뭔가 떴다"와
> "복구됐다"를 가르는 건 이 한 줄뿐이다.**
> → 실패 시 `08-restore-data.sh` 에 PVC 삭제를 추가하고(런북 §destroy step 3 의 PVC 정리 참조)
> **그 미확정 주석(`08:24`)을 실측 결과로 대체한다.**

- [ ] **Step 4.5: 표식 정리**

```bash
# 온프렘 복귀(failback) 후. 드릴 흔적을 프로덕션 DB 에 남기지 않는다.
kubectl --context onprem -n postgres exec cledyu-pg-1 -- psql -d cledyu -c \
  "DROP TABLE IF EXISTS dr_drill_marker"
```

- [ ] **Step 5: 알람 복원 + 무장 해제 + 파괴**

```bash
aws cloudwatch set-alarm-state --region us-east-1 --alarm-name cledyu-lab-dr-disaster \
  --state-value OK --state-reason "드릴 종료"
aws cloudwatch describe-alarms --region us-east-1 --alarm-names cledyu-lab-dr-disaster \
  --alarm-types CompositeAlarm --query 'CompositeAlarms[].StateValue' --output text   # → OK

# DNS 를 온프렘으로 원복 (드릴이 실 Route53 을 바꿨다)
# → 런북 §failback 또는 terraform apply -target=aws_route53_record.public

terraform apply \
  -target=aws_cloudwatch_event_rule.dr_disaster \
  -target=aws_cloudwatch_event_target.dr_disaster \
  -target=aws_lambda_permission.dr_disaster    # 무장 해제

# hot 리소스 파괴 — 런북 §destroy 의 고아 방지 순서 **절대 준수**
#
# 🔴 **순서: in-cluster 정리 → CLI 노드 축소 → terraform. 뒤바꾸면 고아가 남는다**(2026-07-16 실측, §11.17 (f)).
#   드릴 뒷정리에서 **노드를 먼저 0 으로 내렸다가** ALB 컨트롤러·EBS CSI 가 죽어 DR ALB 1·gp3 EBS 11 이
#   고아로 남았다. PV 는 etcd 에 살아있어 **EBS 만 CLI 로 지우면 PV 가 붕 떠 다음 failover 의 kafka PVC
#   마운트가 깨진다** → 노드를 도로 올려 런북 순서대로 재정리해야 했다.
# 절차(dr-eks-bootstrap.md §destroy 0~4.5 = dr-failback.md §8.1 과 동일):
#   0) argocd application-controller scale 0 (selfHeal 정지 — 안 끄면 지운 걸 되살림)
#   1) vault STS · CNPG Cluster · Kafka(`kafkas`+`kafkanodepool` 둘 다) 삭제 → 파드 빠질 때까지 대기
#   2) delete ingress → ALB/TG 소멸 대기   3) delete pvc → EBS 소멸 대기
#   4.5) 고아 ENI(aws-K8S-*, available) 삭제 — 노드 빠진 뒤
# 그 다음 CLI 노드 N→0(§11.15 — terraform -var 는 ignore_changes 로 씹힌다. 마지막 1대 ~15분은 정상,
#   kube-system PDB 꼬리. 강제 종료 안 함 — RTO 이득 0). 마지막에 terraform(hot 회수 또는 enable_eks_dr=false).
# ✅ 이 순서로 하면 ALB·EBS·ENI 전부 0 으로 소멸 실측(2026-07-16).
```

- [ ] **Step 6: 결과 기록 + Commit** (사용자가 실행)

RTO 실측치를 스펙 §5 의 "승인 이후 총 ~40-55분" 목표와 대조해 기록한다.

```bash
cd /home/user/Cledyu
git add docs/superpowers/specs/2026-07-15-dr-discord-approval-orchestration-design.md docs/RUNBOOK/
git commit -m "docs(dr): Plan 2 전체 드릴 RTO 실측 반영"
```

---

## 완료 기준

- [ ] **`terraform validate` 가 통과한다 — `Error: Cycle` 이 없다** (T2 Step 4 — F1)
- [x] CodeBuild 가 hot 리소스를 올린다 (T1 Step 4) — ✅ 2026-07-16 드릴 `Apply complete! 1 added`
      (그 1개가 F3 정책이었다, §11.17 (a)). python 3.12 는 pyenv 로 설치됨(codex P1 반박, §11.16 (f))
- [x] 자식 SM 이 성공/실패를 정확히 구분한다 (T2 Step 5 — `exit 3` → `FAILED`) — ✅ 드릴 확인(§11.17)
- [x] **자식 SM 이 `env` 를 스크립트에 주입한다** (T2 Step 5 env-test — `got=vault/test.snap`) — ✅ 드릴 확인
- [x] **명령 로그 전문이 CloudWatch 에 실제로 쌓인다** (T2 Step 5 — 스트림 개수 ≥1.
      `SUCCEEDED` 만으로는 부족하다 — 3종 다 통과하는데 유실됐던 이력, 스펙 §11.11) — ✅ 12스트림 실측
- [x] ~~**미확정 5건 확정**~~ → **3건 해소, 2건 남음** (2026-07-16 감사 — 스펙 §11.16):
  - [x] `03-` 의 webhook 이름 — 2026-07-15 warm 클러스터 직접 조회로 확정(03 주석)
  - [x] KafkaTopic 의 Ready 조건 유무 — **있다.** Strimzi 0.45.2 CRD 가 printer column
        `Ready ← .status.conditions[?(@.type=="Ready")].status` 선언(§11.16 (e)) → `09` 유지
  - [x] **`delete cluster` 가 PVC 를 지우나**(H5) — **지운다.** CNPG 0.26.1 이 PVC 에
        `ownerReferences: Cluster/<name>` 를 붙여 GC 가 연쇄 삭제(kind 실측 ~15초, §11.16 (e))
        → **`08` 에 PVC 삭제 추가 불필요**
  - [ ] **`psql -d cledyu` 인증 통과**(H3) — 여전히 미확정 (T3 Step 10 이 실측 없이 지나갔다) → **T7**
  - [x] **`ClearAlbParam` 의 에러명** — **`Ssm.ParameterNotFoundException` 확정** (2026-07-16 착수 전 탐침,
        스펙 §11.18 (a)). 계획서 유추가 맞았다 — SFN 은 와이어 코드가 아니라 SDK 예외 클래스명을 쓴다.
        **드릴 없이 버릴 SM 하나로 쟀다**(과금 ~$0) — 미확정을 드릴 밖으로 뺄 수 있으면 빼는 게 싸다
- [x] **`06-` 이 재실행에서도 `SUCCEEDED`** (T3 Step 10 — 두 번 돌린다. H2) — ✅ 2026-07-16 드릴 2회
      연속 통과, 로그에 `git clone`(1회차)→`fetch/reset`(2회차) 두 경로 확인(§11.17)
- [ ] **`08-` 재생성 후 PVC 가 stale 재사용이 아니다** — [12] 의 `count(*)` 가 **복원 시점 값과 일치**
      (H5 — Ready 통과·count>0 통과인데 데이터가 옛것일 수 있는 유일한 무음 경로)
      ⚠️ **위 H5 해소에도 이 항목은 남긴다.** kind 실측은 "지금 이 버전이 이렇게 동작한다"이고,
      CNPG 가 동작을 바꾸거나 retention 정책이 붙으면 조용히 되살아난다 — **실측이 실환경 백스톱을
      대체하지 않는다**(§11.16 (e)). T7 표식(`dr_drill_marker`)이 그 회귀를 잡는다.
- [x] **`03-` 이 Vault raft PVC 를 실제로 비운다** (🆕 2026-07-16, §11.16 (c)) — `[7]` 직전
      `vault status` 의 `initialized == false`. ✅ 드릴에서 `[7]` 이 fresh Vault 만나 init 성공(§11.17 (b))
- [x] **`07` 이 감사 로그에 시크릿을 안 남긴다** (🆕 2026-07-16, §11.16 (a)) — ✅ 드릴 실행 구간 실측:
      안전형태 18 · **유출 0** · 무관 16 (07-13~14 는 24/12/4 였다). stdin 수정이 실환경에서 확증(§11.17 (b))
- [x] **bastion 이 `put-parameter` 에 성공한다** (T3 Step 10 의 `09-` — F3) — ✅ 다만 **정책이 없어서
      드릴이 처음 apply 했다**(§11.17 (a) — 진짜 재해였으면 여기서 죽었다). `[9]` 가 `{"Version":1}` 로 실증
- [x] bastion 스크립트 **8개**가 각각 `SUCCEEDED` (T3 Step 10) — ✅ `[3]×2·[4.5]·[6]×2·[7]·[8]·[9]·[11]`
      (드릴 스코프는 `[10]`/`[12]` 제외 — DNS 전환은 T7, §11.17 도입부)
- [x] `addon-install` 이 멱등하다 — `action=start` 를 두 번 돌려도 성공 (T4) — ✅ **3회 연속** 통과
      (§11.14 (g): 2회로는 UPDATING 충돌을 못 잡는다 → 3회로 검증)
- [x] ~~`dns-switch` 가 SSM 파라미터 없으면 **실패**한다 (fail-closed)~~ — **이 체크는 거짓이었다**
      (2026-07-16 T5 드릴, 스펙 §11.18 (i)). `ParameterNotFound` 로 실패한 건 맞으나 그건 `index.py:37`
      이고, **`:47` 의 WAF 호출엔 도달조차 못 했다** → 거기 `wafv2:GetWebACL` 이 빠진 걸 못 잡았고
      T5 드릴이 `[10]` 에서 죽었다. 🔴 **fail-closed 테스트는 "실패했다" 가 아니라 "의도한 그 줄에서
      실패했다" 를 확인해야 한다** — 앞단에서 죽으면 뒷단은 시험된 적이 없다.
      (⚠️ 이 검증 위해 dns-switch 를 먼저 apply 해야 했다 — T4 순서 구멍, 계획서 T4 Step 5 반영)
- [ ] 🆕 **`[10]` 이 WAF 게이트를 통과해 실제로 Route53 을 바꾼다** (T5/T7) — `wafv2:GetWebACL` 추가 후
      재실행. `:47` 이 실행되는 건 `[9]` 가 SSM 파라미터를 채운 뒤뿐이라 **드릴 전체를 돌려야만 검증된다**
- [x] 메인 SM 이 [1]→[13] 을 완주한다 (T5 Step 4) — ✅ **2026-07-16 3회차 `SUCCEEDED`**.
      서비스가 EKS DR 에서 실제 서빙(`auth.cledyu.com` HTTP 200/0.19s, ALB=`k8s-cledyudr-...`).
      드릴이 결함 3건을 잡았다(§11.18 (g) IAM 드리프트 · (h) `wafv2:GetWebACL` · (k) `[3]` 비멱등) —
      **셋 다 라이브가 아니면 원리적으로 못 잡고, 앞의 둘은 T4 가 "검증했다"고 체크해둔 항목이었다**
- [ ] **[2.4] ClearAlbParam 이 첫 실행(파라미터 없음)에서 Catch 를 타고 넘어간다** (T5 Step 4 — F4)
- [ ] **드롭다운에서 고른 스냅샷이 [7] 에 도달한다** — 최신이 아닌 걸 골라 검증
- [ ] **NotifyFailed 가 실제로 Discord 에 뜬다** — 일부러 한 상태를 깨뜨려 확인 (F2 — 무음 실패 방어선)
- [ ] 🆕 **실패 알림이 `실패 단계: States.TaskFailed` 가 **아니다**** (T5 Step 4 — 스펙 §11.18 (b)(c) 회귀).
      진짜 상태 이름(예: `ClearAlbParam`)이 찍혀야 한다. `States.TaskFailed` 가 찍히면 `$.failedStep` 배선이
      빠진 것이고, 그건 **allowlist dead code 시절로 되돌아간 것**이다
- [ ] 🆕 **`[11]`/`[12]` 실패 시 알림이 "DNS 는 이미 EKS" 라고 말한다** (§11.18 (c) — 이 버그의 본체).
      "온프렘 — 트래픽은 안전합니다" 가 뜨면 `$.dns.alb` `IsPresent` 분기가 안 먹은 것이다 → **T7 에서 확인**
      (`[10]` 이 실제로 도는 건 T7 뿐이다)
- [ ] 🆕 **코드에 있는 것이 AWS 에도 있다** (§11.17 (a)·§11.18 (g) — terraform 은 자동 apply 가 없고
      드리프트를 **아무도 안 알려준다**. 이 계획에서 **두 번** 물렸다)
      ⚠️ **"리소스가 존재하나"만 보면 안 된다** — §11.18 (g) 는 **Lambda 는 멀쩡히 있는데 SFN 롤에 부를
      권한이 없던** 사고다. 존재 체크는 그걸 통과시킨다.
      ```bash
      # ① 존재 — SM 이 부르는 Lambda 3종
      for f in addon-install dns-switch notify; do aws lambda get-function \
        --function-name cledyu-lab-dr-$f --region ap-northeast-2 --query 'Configuration.FunctionName'; done
      # ② 권한 — **실행 주체의 롤**에 그 호출 statement 가 있나 (§11.18 (g) 가 여기서 걸린다)
      # ⚠️ grep 을 파일 전체에 걸지 말 것 — 다른 롤 정책의 sid 까지 긁어 **오탐 28건**이 나온다(실측).
      #    28번 늑대를 부르는 체크는 아무도 안 본다. dr_sfn 정책 문서 **블록으로 스코프**를 자른다.
      aws iam get-role-policy --role-name cledyu-lab-dr-sfn --policy-name cledyu-lab-dr-sfn \
        --query 'PolicyDocument.Statement[].Sid' --output text | tr '\t' '\n' | sort > /tmp/aws_sids.txt
      awk '/^data "aws_iam_policy_document" "dr_sfn" \{/,/^\}/' dr-orchestration.tf |
        grep -oE 'sid *= *"[A-Za-z]+"' | sed 's/.*"\(.*\)"/\1/' | sort > /tmp/code_sids.txt
      comm -13 /tmp/aws_sids.txt /tmp/code_sids.txt   # 출력 = 🔴 코드엔 있고 AWS 엔 없다(드리프트)
      comm -23 /tmp/aws_sids.txt /tmp/code_sids.txt   # 출력 = AWS 에만 있다(코드에서 지운 게 안 지워짐)
      # ③ bastion 롤 — SM 이 참조하지 않아 -target 이 안 딸려온다. 별도 확인 필수(§11.17 (a) 의 F3)
      aws iam list-role-policies --role-name cledyu-dr-bastion   # ssm-put-failover-param 이 있어야 한다
      ```
      - ✅ 2026-07-16: Lambda 3종 존재 · bastion 정책 4종 존재(IAM 롤은 hot 파괴 대상이 아니라 살아남는다)
      - 🔴 2026-07-16: **`InvokeFailoverLambdas` 가 AWS 에 없었다**(§11.18 (g)) — `[5]`·`[10]`·`[13]` 을
        전부 AccessDenied 로 만들고 **NotifyFailed 까지 죽여 무음**이 될 뻔했다. **T5 apply 가 수리한다**
        (SM 이 정책을 depends_on → `-target` 이 의존성을 따라간다). Step 4 후 위 ②를 **다시 돌려 공백 확인**
- [ ] 🆕 **IAM 은 "실제 principal 로" 행사해 검증한다** (§11.18 (g) 의 교훈). `aws lambda invoke` 를 사람
      자격증명으로 성공시킨 것은 **SFN 롤이 부를 수 있다는 증거가 아니다** — T4 가 그렇게 검증했다고
      적어놓고 실제론 롤 권한을 한 번도 안 건드렸다(§11.12 와 같은 뿌리: 명령은 맞는데 실행 주체가 다르다).
      → 이 항목은 **Step 4 의 SM 실행**이 채운다(SFN 이 자기 롤로 Lambda 를 부른다)
- [ ] `curl https://auth.cledyu.com/realms/cledyu-learn` 이 응답 (T7 Step 4)
- [ ] `/metrics` 가 403 (WAF 연결)
- [ ] **RTO 2단 실측** 기록 — 알림에 `?` 가 아니라 실제 분이 찍힌다 (T7 Step 3 — F5)
- [ ] 드릴 후 **알람 OK 복원 · 무장 해제 · hot 파괴** (T7 Step 5)

## 알려진 한계 (이 계획 범위 밖)

- 🆕 **07-13~14 드릴분 Vault 시크릿이 감사 로그에 남아 있다**(스펙 §11.16 (a) — 2026-07-16).
  원인은 제거됐고(87d74ce: exec 인자 → stdin) 새로 새지 않는다. 그러나 **이미 쌓인
  `/aws/eks/cledyu-dr/cluster` 의 24 / 12 / 4건은 그대로**다(보존 90일 → 2026-10-11 자연 만료).
  그중 12건은 `cledyu/vault/bootstrap` 의 **온프렘 운영 Vault recovery 키 전량**(threshold 3)이다.
  **잔여 조치(사용자):** `kube-apiserver-audit-*` 스트림 삭제. 복제 경로는 없음을 확인했다
  (구독 필터·메트릭 필터·S3 export 전부 0건) → 지우면 실제로 사라지고 **로테이션 불요**.
  악용엔 "AWS 로그 읽기 + 온프렘 망 접근"이 둘 다 필요해 즉시 위험은 아니나, **망 격리 하나로만
  버티는 상태**다. 지우지 않고 두면 드릴마다 3건씩 쌓인다.
- 🆕 **07 단독 재실행은 불가**(스펙 §11.16 (c)) — 실패 시 `[3]` 부터 파이프라인을 다시 태워야 한다.
  init~restore 의 되돌릴 수 없는 구간을 "3-peer 대기 + restore 한 줄"까지 줄여 그 창에 걸릴 확률은
  낮췄으나 없앤 건 아니다. 없애려면 init 산물을 어딘가 남겨야 하는데 그게 (a) 의 유출 경로였다 —
  **재실행성과 유출 방어가 맞바꿈 관계**이고, `[3]` 의 PVC 정리로 전자를 우회한 것이다.
- **Vault k8s auth 가 ~1시간 후 만료된다**(스펙 §8.2) — 런북이 "드릴엔 무해"로 쓴 가정을 DR 1~2일 전제가
  상속. ESO 가 조용히 refresh 불능이 된다. 후속 이슈(비만료 reviewer 토큰)
- **failback 은 수동**(스펙 §8) — 구현·런북은 있으나 실 DR 검증 이력 없음. Plan 2 드릴 직후 이어서 하면
  "EKS 켜고 → 복원 → DNS 전환 → 회귀"가 한 번에 검증된다
