# DR Plan B — pilot-light 인프라 재구조 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> **Revised 2026-07-13 (2차 — 적대적 리뷰 반영, 설계 B):** **etcd seed 폐기.** node=0 에선 ArgoCD 가 뜰 컴퓨트가 없어 seed 가 원리적으로 불가(root-app 객체만 써지고 자식 앱·워크로드 미생성). → **warm = 컨트롤플레인만**, 앱은 매 failover 마다 기존 부트스트랩 본체로 처음부터 sync. pilot-light 이득 = 컨트롤플레인 상시 warm(프로비저닝 ~10-15분 제거). 반영: P1(노드 스케일 = CLI `update-nodegroup-config`, 모듈이 `desired_size` ignore) · P1b(failover 의 CNPG 섹션에서 복원 전 구 CR 삭제 → 최신 S3 재복원) · P2(failover root-app apply 전 git-source 브랜치핀만 검사, chart-version 오탐 금지) · C4(매 failover `helm upgrade --install argocd` 재실행 → controller 항상 replicas=1, "스킵" 문구 금지) · Imp1(failback DNS 원복 먼저).

**Goal:** 현행 완전 Cold DR(평시 리소스 0, 재해 시 클러스터부터 통째 생성 ~15-20분)을 pilot-light(컨트롤플레인 상시 ON·노드 0, 재해 시 노드/NAT/엔드포인트/bastion 만 스케일 ~8-10분)로 바꾼다.

**Architecture:** count-gate `local.eks_dr_enabled`(=enable_eks_dr) 는 **warm 스택**(VPC·EKS·IRSA·SG·bastion 롤/프로필/정책·노드그룹)을 상시 존재시키는 게이트로 유지(운영상 true 고정). 새 `var.eks_dr_active` 로 **hot 리소스**(NAT·VPC 엔드포인트·bastion **인스턴스**)만 재해 시 생성한다. **노드 스케일(0↔N)은 terraform 이 아니라 AWS CLI `aws eks update-nodegroup-config`** 로 한다 — 관리형 노드그룹 모듈이 `scaling_config[0].desired_size` 를 `ignore_changes` 하므로 terraform 으로는 안 바뀐다(§Global). 리소스 정의·드릴 수정은 전부 재사용 — 게이팅 리팩터.

**Tech Stack:** terraform(count-gate, terraform-aws-modules/{vpc,eks,vpc-endpoints}), EKS managed node group(min=0), AWS CLI 노드 스케일, ArgoCD(매 failover 설치·sync — seed 없음).

## Global Constraints

- **커플링 주의**: EKS 클러스터(warm)가 `aws_iam_role.eks_dr_bastion[0]`(access_entries)·`aws_security_group.eks_dr_bastion[0]`(cluster SG rule)을 참조한다 → bastion **롤·SG·프로필·정책은 warm 유지**(local.eks_dr_enabled). **인스턴스만 hot**(local.eks_dr_active). 안 그러면 warm 클러스터의 `[0]` 참조가 깨진다(index out of range).
- **-target 규율**(tfvars 없음): 셋업/재해/failback 모두 DR `-target` 목록만(1차 드릴 런북과 동일). 전체 apply 금지.
- 이미지 egress 는 NAT 경유(ECR 엔드포인트 없음 — eks-dr.tf L179). NAT 는 hot 이므로 재해 apply 에서 노드보다 먼저·같이 생성돼 노드 부팅 시 이미지 pull 가능.
- warm 상태(노드 0)에서 애드온(coredns·ebs-csi Deployment, vpc-cni·kube-proxy DaemonSet)은 Pending/0 — 정상. 스케일업 시 스케줄.
- `enable_eks_dr` 는 pilot-light 에서 **true 고정**(warm 스택 존재). false 는 완전 폐기 시만.
- terraform 변수 추가/변경 커밋엔 재생성된 README.md 를 함께 add(pre-commit terraform_docs 훅 — [[feedback_terraform_docs]]).
- **[P1] 노드 스케일은 terraform 아닌 AWS CLI**: `eks-managed-node-group` 모듈이 `scaling_config[0].desired_size` 를 무조건 `ignore_changes`(`.terraform/modules/eks_dr/modules/eks-managed-node-group/main.tf`). 그래서 `terraform apply -var eks_dr_node_desired=N` 은 **무시**돼 노드가 안 뜬다(페일오버 dead-end). terraform 의 `desired_size` 는 노드그룹 **최초 생성 시 0** 만 세팅하고, 실제 0↔N 스케일은 재해/failback 에서 `aws eks update-nodegroup-config --scaling-config minSize=0,maxSize=<max>,desiredSize=N` 로 한다. (min_size 로 우회 불가 — ignore 된 desired 가 옛값에 머물러 `min>desired` 로 EKS API 가 거부.)
- **[P1b] CNPG 는 bootstrap-recovery 1회 → failover 에서 구 CR 선삭제**: `bootstrap.recovery` 는 Cluster CR 생성 시 1회만 실행. warm 은 컨트롤플레인(etcd)이 상시라, 이전 failover 가 만든 CNPG Cluster CR(`postgres`ns `cledyu-pg`·`keycloak`ns `keycloak-pg`)이 **failback 을 넘어 etcd 에 잔존**한다. failback 후 온프렘이 primary 로 전진해 그 데이터는 **stale** → **재-failover 는 최신 S3 에서 재복원**해야 한다. 보장 지점 = **Phase 1(failover)의 CNPG 섹션에서 복원 전 구 CR 을 삭제**(Task2 Step4) → ArgoCD 가 새로 만들어 recovery 재실행. bastion kubectl 컨텍스트에서 실행(Phase 1 최상단 아님 — 거긴 클러스터 경로 미확보). 단발 failover 는 CR 이 없어 no-op.
- **[P2] failover 시 git-source revision = main 검사(가드 오탐 금지)**: 매 failover 가 root-app 을 apply 하므로(seed 안 함), apply 직전 `root-app-eks.yaml`·apps-eks git-source Application 이 `main` 인지 확인한다. **주의: `grep -v main` 은 chart-version(0.32.0·v1.20.2 등)을 오탐하고, `feat/…` prefix 화이트리스트는 `test/…`·prefix 없는 이름을 놓친다 — "main 도 chart-version(`v?[0-9]+\.[0-9]`)도 아닌 값"을 브랜치핀으로 검출**(Task2 Step3). 현 main 에 `feat/dr-eks-overlay-cnpg-keycloak` 핀 잔존(#290 revert 누락) — 첫 드릴 전 정리 필요.

---

### Task 1: pilot-light 게이팅 리팩터 (변수·locals·NAT/엔드포인트/bastion·노드 사이징)

`eks_dr_active` 도입, hot 리소스 게이팅, 노드 min=0·desired 기본 0. terraform validate + 타깃 plan 으로 검증.

**Files:**
- Modify: `infra/terraform/aws/variables.tf`(eks_dr_active 추가, eks_dr_node_desired 기본 0, eks_dr_node_max 추가)
- Modify: `infra/terraform/aws/eks-dr.tf`(locals eks_dr_active, VPC NAT 게이트, endpoints count, 노드 min/max/desired)
- Modify: `infra/terraform/aws/eks-dr-bastion.tf`(aws_instance.eks_dr_bastion count 만 hot)
- Modify: `infra/terraform/aws/README.md`(terraform-docs 재생성)

**Interfaces:**
- Produces: `var.eks_dr_active`(bool, 기본 false), `local.eks_dr_active`(0/1), `var.eks_dr_node_max`(number, 기본 6). Task 2 런북 트리거가 `eks_dr_active` 를 set(hot on/off). **노드 스케일은 변수가 아니라 CLI** `aws eks update-nodegroup-config` 로(desired_size ignore_changes — §Global P1). `eks_dr_node_desired` 는 노드그룹 최초 생성값(0)만 결정.

- [ ] **Step 1: 변수 추가·변경 (variables.tf)**

`variable "eks_dr_node_desired"` 의 default 를 `3 → 0` 으로 바꾸고 description 갱신, 그리고 두 변수 추가:
```hcl
variable "eks_dr_node_desired" {
  description = "DR EKS 워커 노드 개수. pilot-light: 평시 0(비용 0), 재해 시 N 으로 스케일. min=0, max=eks_dr_node_max."
  type        = number
  default     = 0
}

variable "eks_dr_node_max" {
  description = "DR EKS 노드그룹 max_size. 재해 시 eks_dr_node_desired 를 이 상한 내에서 올린다."
  type        = number
  default     = 6
}

variable "eks_dr_active" {
  description = <<-EOT
    pilot-light hot 리소스 스위치. 평시 false — NAT·VPC 인터페이스 엔드포인트·bastion 인스턴스 미생성(비용은
    컨트롤플레인만). 재해 시 true — 이들 생성(+ eks_dr_node_desired 로 노드 스케일). warm 스택(VPC·EKS·IRSA·SG·
    bastion 롤)은 enable_eks_dr 로 상시 유지되고 이 값과 무관.
  EOT
  type        = bool
  default     = false
}
```
(`enable_eks_dr` description 도 갱신: "pilot-light: 평시 true 로 warm 스택(컨트롤플레인·노드그룹 0) 상시. false 는 완전 폐기 시만." — default 는 false 유지, 운영상 항상 true 로 apply.)

- [ ] **Step 2: locals 에 eks_dr_active 추가 (eks-dr.tf L2-6)**

```hcl
locals {
  eks_dr_enabled = var.enable_eks_dr ? 1 : 0
  eks_dr_active  = var.eks_dr_active ? 1 : 0   # pilot-light hot(NAT·엔드포인트·bastion 인스턴스)
  eks_dr_name    = "cledyu-dr"
  eks_dr_tags    = { Project = "cledyu", Purpose = "dr", ManagedBy = "terraform" }
}
```

- [ ] **Step 3: VPC NAT 를 hot 으로 (eks-dr.tf L20)**

`module "eks_dr_vpc"` 의 NAT 플래그만 변경(모듈 count·서브넷 등은 그대로 warm):
```hcl
  enable_nat_gateway = var.eks_dr_active   # pilot-light: 평시 미생성(노드 0이라 egress 불요), 재해 시 생성
  single_nat_gateway = true
```

- [ ] **Step 4: 노드그룹 min=0·max·desired (eks-dr.tf L67-75)**

```hcl
  eks_managed_node_groups = {
    dr = {
      instance_types = [var.eks_dr_node_instance_type]
      capacity_type  = "ON_DEMAND"
      desired_size   = var.eks_dr_node_desired   # 최초 생성값(기본 0). 모듈이 이후 desired 변경을 ignore → 실제 스케일은 CLI(§Global P1)
      min_size       = 0
      max_size       = var.eks_dr_node_max
    }
  }
```
> ⚠️ **[P1]** 이 `desired_size` 는 노드그룹 최초 생성 시 0 을 박는 용도일 뿐, 이후 `terraform apply -var eks_dr_node_desired=N` 으로는 **안 바뀐다**(모듈 `ignore_changes = [scaling_config[0].desired_size]`). 재해/failback 의 노드 스케일은 Task 2 의 `aws eks update-nodegroup-config` 로만 한다. 노드그룹 이름은 고정이 아니므로 CLI 에서 `aws eks list-nodegroups --cluster-name cledyu-dr` 로 취득한다.

- [ ] **Step 5: VPC 엔드포인트 모듈을 hot 으로 (eks-dr.tf L183)**

`module "eks_dr_endpoints"` 의 count 만 변경:
```hcl
  count   = local.eks_dr_active
```
(SG `aws_security_group.eks_dr_endpoints` 는 warm 유지 — L143 `count = local.eks_dr_enabled` 그대로. 엔드포인트 모듈이 이 SG[0] 를 참조하는데 warm 이라 항상 존재.)

- [ ] **Step 6: bastion 인스턴스만 hot (eks-dr-bastion.tf)**

`aws_instance "eks_dr_bastion"` 의 count 만 변경(롤·프로필·정책·SG·데이터소스는 warm 유지 — 전부 `local.eks_dr_enabled` 그대로):
```hcl
resource "aws_instance" "eks_dr_bastion" {
  count = local.eks_dr_active   # pilot-light: 재해 시만. 롤/프로필/SG 는 warm(클러스터 access_entries 참조).
```
(주: bastion AMI data source `data.aws_ssm_parameter.eks_dr_bastion_ami` 도 warm 유지 — 인스턴스가 `[0]` 로 참조하나 data source 는 count=eks_dr_enabled 라 warm 이면 항상 존재.)

- [ ] **Step 7: terraform validate**

Run:
```bash
cd infra/terraform/aws && terraform init -reconfigure -input=false >/dev/null 2>&1; terraform validate
```
Expected: `Success! The configuration is valid.`

- [ ] **Step 8: warm plan 검증 (hot 리소스 미포함, 노드 0)**

Run(읽기전용, DR -target 목록 + warm vars):
```bash
cd infra/terraform/aws
terraform plan -input=false -var enable_eks_dr=true -var eks_dr_active=false -var eks_dr_node_desired=0 \
  -target=module.eks_dr_vpc -target=module.eks_dr -target=module.eks_dr_ebs_csi_irsa \
  -target=module.eks_dr_alb_irsa -target=aws_security_group.eks_dr_endpoints \
  -target=aws_security_group.eks_dr_bastion -target=module.eks_dr_endpoints \
  -target=module.eks_dr_vault_unseal_irsa -target=aws_iam_policy.eks_dr_vault_unseal \
  -target=aws_iam_policy.eks_dr_cnpg_restore -target=module.eks_dr_cnpg_restore_irsa \
  -target=aws_iam_role.eks_dr_bastion -target=aws_iam_role_policy_attachment.eks_dr_bastion_ssm \
  -target=aws_iam_role_policy.eks_dr_bastion_describe -target=aws_iam_role_policy.eks_dr_bastion_vault_restore \
  -target=aws_iam_instance_profile.eks_dr_bastion -target=aws_instance.eks_dr_bastion \
  2>&1 | grep -E 'nat_gateway|eks_dr_endpoints|eks_dr_bastion\b|will be created|Plan:' | head -40
```
Expected: NAT gateway·module.eks_dr_endpoints·aws_instance.eks_dr_bastion 이 **"will be created" 에 없어야** 함(active=false). VPC·EKS·IRSA·SG·bastion 롤/프로필 은 create. 노드그룹 desired 0. (현재 상태가 이미 warm 이면 "No changes" 도 정상 — 핵심은 hot 3종이 plan 에 create 로 안 뜨는 것.)

- [ ] **Step 9: README 재생성 + Commit**

```bash
cd infra/terraform/aws && terraform-docs markdown table --output-file README.md --output-mode inject . >/dev/null 2>&1 || terraform-docs markdown . > README.md
cd - >/dev/null
git add infra/terraform/aws/variables.tf infra/terraform/aws/eks-dr.tf infra/terraform/aws/eks-dr-bastion.tf infra/terraform/aws/README.md
git commit -m "feat(dr): pilot-light 게이팅 — eks_dr_active(NAT·엔드포인트·bastion 인스턴스)+노드 min=0·desired 기본 0"
```

---

### Task 2: 런북 — pilot-light warm 셋업·페일오버·failback (seed 없음)

Phase 0(warm terraform apply, seed 없음) + Phase 1(hot terraform + CLI 노드 스케일, 이후 기존 부트스트랩 본체 재사용) + revision 가드(C2) + CNPG 재복원 가드(P1b) + failback(DNS-first)을 런북에 반영. **기존 부트스트랩 본체(bastion→ArgoCD 설치→Vault→CNPG→DNS)는 매 failover 마다 그대로 실행**(seed 안 하므로).

**Files:**
- Modify: `docs/RUNBOOK/dr-eks-bootstrap.md`(Phase 0 warm 셋업 + Phase 1 트리거 교체 + revision/CNPG 가드 삽입 + failback + 배너 정합)

**Interfaces:**
- Consumes: Task 1 의 `eks_dr_active` 변수. 노드 스케일은 CLI(`aws eks update-nodegroup-config`).

- [ ] **Step 1: Phase 0 — warm 스택 셋업 섹션 추가 (최초 1회, seed 없음)**

> **설계(B):** node=0 에선 ArgoCD 가 뜰 컴퓨트가 없어 "etcd seed" 는 불가능하다(root-app 객체만 써질 뿐 자식 앱·워크로드는 생성 안 됨). 그래서 **앱을 seed 하지 않는다.** warm = 컨트롤플레인·VPC·IRSA·SG·bastion 롤·노드그룹(desired 0) 뿐. 앱·bastion 인스턴스·NAT 는 Phase 1(재해)에서만. 매 failover 가 기존 부트스트랩 본체(ArgoCD 설치→root-app→Vault→CNPG→DNS)로 앱을 처음부터 sync 한다. pilot-light 이득 = 컨트롤플레인 상시 warm(프로비저닝 ~10-15분 제거). failback 이 in-cluster 앱상태를 정리하므로 **매 failover 는 처음부터 재빌드 — 동일·멱등**(2회차라고 빨라지지 않음).

런북 상단 실행순서 배너 다음에 추가:
```markdown
### Phase 0 — warm 스택 셋업 (최초 1회)

컨트롤플레인·VPC·IRSA·노드그룹(desired 0)만 상시 존재시킨다. NAT·엔드포인트·bastion 인스턴스·노드·앱은 이 단계서 만들지 않는다.

```bash
cd infra/terraform/aws && terraform init -reconfigure -input=false
terraform apply -var enable_eks_dr=true -var eks_dr_active=false -var eks_dr_node_desired=0 <DR -target 목록>
#   → EKS 컨트롤플레인·VPC·IRSA·SG·bastion 롤/프로필/정책·노드그룹(0) 생성. hot(NAT·엔드포인트·bastion 인스턴스)·노드 없음.
#   → 평시 과금 = 컨트롤플레인만 ~$73/mo. 이 상태로 상시 유지(재해 대기). 검증: aws eks describe-cluster --name cledyu-dr → ACTIVE.
```
```

- [ ] **Step 2: Phase 1(재해 트리거) 교체 — hot + 노드 스케일만**

기존 "Phase 1 — terraform apply" 의 `-var enable_eks_dr=true` 전체-apply 명령을 아래로 교체. warm 은 이미 존재하니 hot·노드만 올리고, 그 다음은 **기존 부트스트랩 본체를 그대로** 탄다:
```markdown
### Phase 1 — 재해 페일오버 트리거 (pilot-light)

```bash
# (1) hot 리소스(NAT·엔드포인트·bastion) — terraform. 노드 desired 는 모듈이 ignore 하므로 여기선 안 오른다.
cd infra/terraform/aws
terraform apply -var enable_eks_dr=true -var eks_dr_active=true -var eks_dr_node_desired=0 <DR -target 목록>
#   → NAT·엔드포인트·bastion 생성(~2-3분). NAT 를 노드보다 먼저 세워 이미지 pull 경로 확보.
# (2) [P1] 노드 스케일 0→N — CLI(terraform desired_size ignore_changes 회피, §Global).
NG=$(aws eks list-nodegroups --cluster-name cledyu-dr --region ap-northeast-2 --query 'nodegroups[0]' --output text)
aws eks update-nodegroup-config --cluster-name cledyu-dr --region ap-northeast-2 \
  --nodegroup-name "$NG" --scaling-config minSize=0,maxSize=6,desiredSize=3
#   → 노드 3 생성(~3-5분).
```

이후 **아래 "apps-eks 부트스트랩"부터 기존 절차를 매 failover 동일하게 수행**한다(seed 안 했으므로 여기서 처음부터 설치·sync):
bastion 진입 → `helm upgrade --install argocd`(멱등) → root-app apply → Vault 복원 → CNPG → api/web restart → DNS 전환.
```
> **[C4]** 매 failover 가 `helm upgrade --install argocd` 를 재실행하므로 `argocd-application-controller` 는 항상 replicas=1 로 (재)생성된다 — failback 이 0 으로 내렸든 무관. **"재해 시 helm/root-app 재실행 불요" 같은 스킵 문구를 절대 넣지 말 것**(그게 교차사이클 dead-end를 만든다).

- [ ] **Step 3: [C2] root-app apply 앞에 git-source revision 가드 삽입**

기존 "apps-eks 부트스트랩"의 `kubectl apply -f gitops/argocd/root-app-eks.yaml` **직전**에 삽입. ⚠️ chart-version `targetRevision`(예: `0.32.0`·`v1.20.2`·`7.7.10`)을 오탐하면 정상상태서도 항상 막힌다. 브랜치핀 이름 규칙(feat/…)을 화이트리스트하는 것도 `test/…`·prefix 없는 이름을 놓친다 → **"main 도 아니고 chart-version 도 아닌 값"을 전부 브랜치핀으로 간주**한다:
```bash
# [P2] git-source Application 의 targetRevision 이 main 도 chart-version(vX.Y / X.Y)도 아니면 = 브랜치핀 → 중단.
# ^[[:space:]]*targetRevision: 로 앵커 — 주석줄(# targetRevision: …)은 제외(value-only revert 후 오탐 방지).
if grep -REn '^[[:space:]]*targetRevision:' gitops/argocd/root-app-eks.yaml gitops/argocd/apps-eks/ \
   | grep -vE 'targetRevision:[[:space:]]*(main|v?[0-9]+\.[0-9])'; then
  echo "❌ git-source 가 main 아닌 revision(브랜치핀) — main 으로 되돌린 뒤 진행"; exit 1
fi
```
> 참고: 현재 main 의 `root-app-eks.yaml`·일부 apps-eks 에 `feat/dr-eks-overlay-cnpg-keycloak` 핀이 잔존(#290 머지 전 revert 누락)한다 — 이 가드가 첫 드릴에서 그걸 잡을 것이므로, 사전에 main 을 정리해 둘 것(별도 프로덕션 이슈).

- [ ] **Step 4: [P1b] CNPG 재복원 가드 — 기존 CNPG 복원 스텝 앞(bastion kubectl 컨텍스트)에 삽입**

failback 후 온프렘이 primary 로 전진하므로 warm etcd 에 잔존하는 이전 사이클 CNPG CR 데이터는 stale. bastion 진입·kubeconfig 취득이 끝난 **CNPG 섹션 안**(경로 확보됨 — Phase 1 최상단 아님)에서, 복원 전에 구 CR 을 지워 ArgoCD 가 새로 만들어 최신 S3 로 recovery 를 재실행하게 한다:
```bash
# [P1b] 재-failover 시 잔존 CNPG CR 제거 → ArgoCD 재생성 → bootstrap.recovery 최신 S3. 단발 failover 는 CR 이 없어 no-op.
kubectl -n postgres delete cluster cledyu-pg --ignore-not-found
kubectl -n keycloak delete cluster keycloak-pg --ignore-not-found
```

- [ ] **Step 5: failback 섹션 추가 (destroy 앞) — DNS 먼저**

기존 `### destroy (고아 방지)` 앞에 추가. [Imp1] 서빙 스택을 부수기 전에 **DNS 를 먼저 온프렘으로** 돌려 사용자 outage 창을 없앤다:
```markdown
### failback (온프렘 복구 후 — 클러스터는 warm 유지, 노드만 회수)

warm(컨트롤플레인·VPC·IRSA·노드그룹0)은 유지하고 hot·노드·in-cluster 앱상태는 회수한다(EBS/ALB 반납). 완전 폐기는 아래 destroy.

```bash
# 1) [Imp1] 공개 DNS 를 온프렘으로 먼저 원복 — 서빙 파괴 전에 트래픽부터 돌린다(운영자 머신, route53 권한).
#    failover 가 route53 CLI UPSERT 로 EKS ALB 를 덮었으므로 terraform 관리값(온프렘 프록시 ALB alias)으로 되돌린다.
#    ⚠️ 반드시 -var enable_public_ingress=true + -target — 안 그러면 count=0(기본 false)이라 terraform 이 이 레코드를
#       DELETE 하거나 공개 스택을 오-destroy 한다(이 문서 -target 경고 참조). 온프렘 Healthy 확인 후 실행.
cd infra/terraform/aws && terraform apply -var enable_public_ingress=true -target=aws_route53_record.public
# 2) 고아 방지 in-cluster 정리(노드·컨트롤러 살아있을 때): 아래 destroy 의 **step 0)~4.5) 전체** 수행
#    (⚠️ terraform destroy 는 제외). 특히 step 1)(statefulset/CNPG/kafka CR unmount)을 빼먹으면 step 3) PVC 삭제가
#    pvc-protection finalizer 로 Terminating 에 걸린다. Ingress→ALB/TG, PVC→EBS 정리 완료까지 대기.
# 3) [P1] 노드 N→0 (CLI). 위 in-cluster 정리 끝난 뒤 실행.
NG=$(aws eks list-nodegroups --cluster-name cledyu-dr --region ap-northeast-2 --query 'nodegroups[0]' --output text)
aws eks update-nodegroup-config --cluster-name cledyu-dr --region ap-northeast-2 --nodegroup-name "$NG" --scaling-config minSize=0,maxSize=6,desiredSize=0
# 4) hot 회수: eks_dr_active=false → NAT·엔드포인트·bastion 소멸. 컨트롤플레인·VPC·IRSA·노드그룹(0) 유지.
cd infra/terraform/aws && terraform apply -var enable_eks_dr=true -var eks_dr_active=false -var eks_dr_node_desired=0 <DR -target 목록>
```
> failback 이 in-cluster 앱상태(CNPG CR 포함)를 정리하므로 재-failover 는 처음부터 재빌드(매 failover 동일·멱등). Phase 1 Step 4 의 CNPG CR 선삭제는 정리 누락 대비 idempotent 안전망. 정상 복귀는 이 failback(warm 유지), **완전 폐기는 아래 destroy + `enable_eks_dr=false`**(과금 0, 단 다음 재해는 Phase 0 부터).
```
그리고 상단 실행순서 배너의 "⑩ destroy" 옆에 "정상 복귀=failback(warm 유지) / destroy=완전 폐기" 를 한 줄 병기(Minor 배너 정합).

- [ ] **Step 6: 문서 정합 검증 (커밋 안 함)**

런북의 코드펜스(백틱 3연속) 개수가 짝수(균형)인지 grep 으로 확인. Phase 0 → Phase 1 → (기존 부트스트랩 본체) → failback → destroy 흐름과 상호참조(failback 이 destroy 스텝 참조 등)가 실제 섹션으로 해소되는지 확인. **git commit 금지 — 워킹트리만.**

---

## Self-Review (작성자 체크)

**Spec 커버리지(§2.1·§2.2·§4):** pilot-light 게이팅(Task1 eks_dr_active·NAT/엔드포인트/bastion·노드 min0)·warm 셋업(Task2 Phase0, **seed 없음** — 설계 B)·페일오버 트리거(Task2 Phase1 hot+CLI)·failback(Task2 Step5)·비용 ~$73(컨트롤플레인만)·-target 규율(Global) 커버. **적대적 리뷰 발견 반영**: C1(seed 불가 → warm=컨트롤플레인만, 앱은 매 failover sync) · P1(노드 스케일 CLI, 모듈 `ignore_changes`) · P1b(failover 의 CNPG 섹션서 복원 전 구 CR 삭제) · C2/P2(git-source 브랜치핀만 검사, chart-version 오탐 금지) · C3(kubectl 은 bastion 진입 후) · C4(매 failover `helm --install` 재실행 → controller=1) · Imp1(failback DNS 먼저).

**플레이스홀더 스캔:** `<DR -target 목록>` 은 런북 문맥의 참조(Task1 Step8 에 전체 목록 명시, 런북 Phase1 에도 존재) — 실제 목록 있음. 그 외 실제 명령·hcl. TBD/TODO 없음.

**타입/이름 일관성:** `var.eks_dr_active`(Task1 정의 → Task2 트리거 사용), `local.eks_dr_active`(Task1 Step2 → Step5/6 count), `var.eks_dr_node_desired`(기존, 기본 0으로), `var.eks_dr_node_max`(Task1 → 노드 max). bastion 롤/SG warm·인스턴스 hot 분리 일관(Global + Step6).

**미해결(플랜 밖·후속):**
- 애드온 warm(0노드) 시 EKS 콘솔 DEGRADED 표시 가능(코스메틱, 스케일업 시 해소) — 드릴서 확인.
- **매 failover 가 앱을 처음부터 설치·sync**(seed 없음, failback 이 in-cluster 정리) → RTO 는 컨트롤플레인 프로비저닝(~10-15분)만 절감, 스펙의 ~8-10분보다 상향 가능(그래도 Cold 15-20분보다 빠름). 2회차라고 빨라지지 않음(동일·멱등). 실측은 드릴.
- 실습 스택(A1~A3)·Vault 복원·Keycloak(T8) 은 이 클러스터 위에서 동작 — pilot-light 와 대체로 직교, 라이브 드릴서 RTO 실측.
- **CNPG(T7)는 직교 아님**: warm etcd 가 CNPG Cluster CR 을 failback 넘어 잔존시켜, 재-failover 시 stale. **Phase 1(failover)의 CNPG 섹션에서 복원 전 구 CR 삭제로 보장**(Task2 Step4·§Global P1b). 반복 재해 드릴로 실증 필요.
- **main 정리 선행**: `root-app-eks`·apps-eks 의 `feat/dr-eks-overlay-cnpg-keycloak` 핀(#290 revert 누락)을 첫 드릴 전 main 에서 main 으로 원복(별도 프로덕션 이슈, P2 가드가 첫 드릴서 잡음).
- 노드그룹 이름은 CLI 에서 `aws eks list-nodegroups` 로 취득(모듈 네이밍 고정 아님). 드릴서 실제 이름 확인.
- **자원 사이징 재검토**: Plan A(실습 스택)가 kafka(브로커3×2Gi)·validation-engine 을 추가 — `eks_dr_node_instance_type` 설명은 이를 누락(Cold 기준). 명시 요청 합계(~2.5CPU/8Gi)는 3×m6i.xlarge 여유이나, CNPG·keycloak 에 requests 없음(best-effort) → 드릴서 노드 headroom 실측 후 `eks_dr_node_desired`(failover 시 CLI desiredSize) 확정.
- RTO(~8-10분 목표) 실측은 드릴.
