# DR Plan B — pilot-light 인프라 재구조 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 현행 완전 Cold DR(평시 리소스 0, 재해 시 클러스터부터 통째 생성 ~15-20분)을 pilot-light(컨트롤플레인 상시 ON·노드 0, 재해 시 노드/NAT/엔드포인트/bastion 만 스케일 ~8-10분)로 바꾼다.

**Architecture:** count-gate `local.eks_dr_enabled`(=enable_eks_dr) 는 **warm 스택**(VPC·EKS·IRSA·SG·bastion 롤/프로필/정책·노드그룹)을 상시 존재시키는 게이트로 유지(운영상 true 고정). 새 `var.eks_dr_active` 로 **hot 리소스**(NAT·VPC 엔드포인트·bastion **인스턴스**)만 재해 시 생성한다. 노드는 `var.eks_dr_node_desired`(기본 0)로 스케일. 리소스 정의·드릴 수정은 전부 재사용 — 게이팅 리팩터.

**Tech Stack:** terraform(count-gate, terraform-aws-modules/{vpc,eks,vpc-endpoints}), ArgoCD warm etcd seed, EKS managed node group(min=0).

## Global Constraints

- **커플링 주의**: EKS 클러스터(warm)가 `aws_iam_role.eks_dr_bastion[0]`(access_entries)·`aws_security_group.eks_dr_bastion[0]`(cluster SG rule)을 참조한다 → bastion **롤·SG·프로필·정책은 warm 유지**(local.eks_dr_enabled). **인스턴스만 hot**(local.eks_dr_active). 안 그러면 warm 클러스터의 `[0]` 참조가 깨진다(index out of range).
- **-target 규율**(tfvars 없음): 셋업/재해/failback 모두 DR `-target` 목록만(1차 드릴 런북과 동일). 전체 apply 금지.
- 이미지 egress 는 NAT 경유(ECR 엔드포인트 없음 — eks-dr.tf L179). NAT 는 hot 이므로 재해 apply 에서 노드보다 먼저·같이 생성돼 노드 부팅 시 이미지 pull 가능.
- warm 상태(노드 0)에서 애드온(coredns·ebs-csi Deployment, vpc-cni·kube-proxy DaemonSet)은 Pending/0 — 정상. 스케일업 시 스케줄.
- `enable_eks_dr` 는 pilot-light 에서 **true 고정**(warm 스택 존재). false 는 완전 폐기 시만.
- terraform 변수 추가/변경 커밋엔 재생성된 README.md 를 함께 add(pre-commit terraform_docs 훅 — [[feedback_terraform_docs]]).

---

### Task 1: pilot-light 게이팅 리팩터 (변수·locals·NAT/엔드포인트/bastion·노드 사이징)

`eks_dr_active` 도입, hot 리소스 게이팅, 노드 min=0·desired 기본 0. terraform validate + 타깃 plan 으로 검증.

**Files:**
- Modify: `infra/terraform/aws/variables.tf`(eks_dr_active 추가, eks_dr_node_desired 기본 0, eks_dr_node_max 추가)
- Modify: `infra/terraform/aws/eks-dr.tf`(locals eks_dr_active, VPC NAT 게이트, endpoints count, 노드 min/max/desired)
- Modify: `infra/terraform/aws/eks-dr-bastion.tf`(aws_instance.eks_dr_bastion count 만 hot)
- Modify: `infra/terraform/aws/README.md`(terraform-docs 재생성)

**Interfaces:**
- Produces: `var.eks_dr_active`(bool, 기본 false), `local.eks_dr_active`(0/1), `var.eks_dr_node_max`(number, 기본 6). Task 2 런북 트리거가 이 변수들을 set.

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
      desired_size   = var.eks_dr_node_desired   # 평시 0, 재해 N
      min_size       = 0
      max_size       = var.eks_dr_node_max
    }
  }
```

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

### Task 2: 런북 — pilot-light 셋업·페일오버·failback

warm 셋업(1회) + 재해 트리거(eks_dr_active+node_desired) + failback 을 런북에 반영. 부트스트랩 본체(bastion→ArgoCD→Vault 복원)는 그대로.

**Files:**
- Modify: `docs/RUNBOOK/dr-eks-bootstrap.md`(pilot-light 셋업 섹션 + Phase 1 트리거 교체 + failback)

**Interfaces:**
- Consumes: Task 1 의 `eks_dr_active`·`eks_dr_node_desired` 변수.

- [ ] **Step 1: pilot-light 셋업 섹션 추가(1회, warm etcd seed)**

런북 상단 실행순서 배너 다음에 "Phase 0 — pilot-light 셋업(최초 1회)" 추가:
```markdown
### Phase 0 — pilot-light 셋업 (최초 1회, warm 스택 + etcd seed)

```bash
# 1) warm 스택 생성(컨트롤플레인·VPC·IRSA·노드그룹 0). + seed 위해 bastion 을 잠시 올린다.
cd infra/terraform/aws && terraform init -reconfigure -input=false
terraform apply -var enable_eks_dr=true -var eks_dr_active=true -var eks_dr_node_desired=0 <DR -target 목록>
#    (eks_dr_active=true 로 bastion/NAT/엔드포인트 임시 생성 — seed 에 bastion 필요. 노드는 0 유지.)

# 2) bastion 에서 ArgoCD seed + root-app 적용 → etcd 에 CR 영속(노드 0이라 파드 Pending).
#    ⚠️ --wait 금지: 노드 0이라 파드가 Ready 안 돼 타임아웃 난다. 매니페스트만 적용한다.
helm upgrade --install argocd argo/argo-cd --version 7.7.10 \
  -f gitops/apps/argocd/values.yaml -f gitops/apps/argocd/values-eks.yaml -n argocd --create-namespace
kubectl apply -f gitops/argocd/root-app-eks.yaml
kubectl -n argocd get applications   # 앱들이 생성됨(파드는 Pending — 정상)

# 3) seed 후 hot 리소스 회수 → 평시 warm(컨트롤플레인만 과금 ~$73/mo).
terraform apply -var enable_eks_dr=true -var eks_dr_active=false -var eks_dr_node_desired=0 <DR -target 목록>
#    → NAT·엔드포인트·bastion 인스턴스 소멸. etcd 의 ArgoCD/앱 CR 은 컨트롤플레인에 영속.
```
```

- [ ] **Step 2: Phase 1(재해 트리거) 교체**

기존 "Phase 1 — terraform apply" 의 `-var enable_eks_dr=true` 명령을 pilot-light 트리거로 교체:
```markdown
### Phase 1 — 재해 페일오버 트리거 (pilot-light)

```bash
# warm 스택은 이미 존재. hot(NAT·엔드포인트·bastion)+노드 N 만 올린다.
cd infra/terraform/aws
terraform apply -var enable_eks_dr=true -var eks_dr_active=true -var eks_dr_node_desired=3 <DR -target 목록>
#   → NAT·엔드포인트·bastion·노드 3 생성(~3-5분) → 노드 뜨면 Pending 파드(사전 seed된 ArgoCD/앱) 스케줄
#   → ArgoCD 가 git(main)에서 최신 pull 해 wave 순서로 자동 수렴
# 이후 부트스트랩 본체(아래)는 동일: bastion 진입 → Vault 복원 → CNPG → api/web restart → DNS 전환.
# (Phase 0 에서 이미 seed 했으므로 helm seed·root-app apply 는 재해 시 재실행 불요 — 이미 etcd 에 있음.)
```
```

- [ ] **Step 3: failback 섹션 추가(teardown 대체 — 클러스터는 warm 유지)**

teardown 섹션 앞에 pilot-light failback 추가:
```markdown
### failback (온프렘 복구 후 — 클러스터는 warm 유지, 노드만 회수)

pilot-light 는 완전 destroy 가 아니라 hot 리소스만 회수한다(warm 스택·etcd seed 유지 → 다음 재해 시 다시 빠르게).

```bash
# 0) 고아 방지: 먼저 in-cluster 정리(ArgoCD selfHeal 정지 + Ingress/PVC 삭제 → ALB/EBS 회수).
#    아래 "teardown(고아 방지)" 의 selfHeal 정지·Ingress·PVC·ENI 스텝을 그대로 수행(단 terraform 은 destroy 아님).
# 1) hot 회수: eks_dr_active=false, node 0.
cd infra/terraform/aws
terraform apply -var enable_eks_dr=true -var eks_dr_active=false -var eks_dr_node_desired=0 <DR -target 목록>
#   → 노드·NAT·엔드포인트·bastion 소멸. 컨트롤플레인·VPC·IRSA·노드그룹(0)·etcd seed 유지.
# 2) 공개 DNS 원복(운영자 머신): api/app alias 를 온프렘 프록시 ALB 로 UPSERT(terraform aws_route53_record.public).
```

완전 폐기(warm 스택까지 제거)는 `enable_eks_dr=false` 로 전체 DR -target destroy(과금 완전 0, 단 다음 재해 시 Phase 0 셋업부터 재수행).
```

- [ ] **Step 4: Commit**

```bash
git add docs/RUNBOOK/dr-eks-bootstrap.md
git commit -m "docs(dr): 런북 pilot-light 셋업(warm seed)·페일오버 트리거·failback 반영"
```

---

## Self-Review (작성자 체크)

**Spec 커버리지(§2.1·§2.2·§4):** pilot-light 게이팅(Task1 eks_dr_active·NAT/엔드포인트/bastion·노드 min0)·warm etcd seed(Task2 Phase0 --wait 금지)·페일오버 트리거(Task2 Phase1)·failback(Task2)·비용 최적화 ~$73(hot 회수)·-target 규율(Global) 모두 커버.

**플레이스홀더 스캔:** `<DR -target 목록>` 은 런북 문맥의 참조(Task1 Step8 에 전체 목록 명시, 런북 Phase1 에도 존재) — 실제 목록 있음. 그 외 실제 명령·hcl. TBD/TODO 없음.

**타입/이름 일관성:** `var.eks_dr_active`(Task1 정의 → Task2 트리거 사용), `local.eks_dr_active`(Task1 Step2 → Step5/6 count), `var.eks_dr_node_desired`(기존, 기본 0으로), `var.eks_dr_node_max`(Task1 → 노드 max). bastion 롤/SG warm·인스턴스 hot 분리 일관(Global + Step6).

**미해결(플랜 밖·후속):**
- 애드온 warm(0노드) 시 EKS 콘솔 DEGRADED 표시 가능(코스메틱, 스케일업 시 해소) — 드릴서 확인.
- warm etcd seed 의 ArgoCD 설치버전은 seed 시점 고정 → 주기적 재-seed(helm upgrade)로 ArgoCD 자체 갱신(앱은 ArgoCD 가 git 에서 최신 pull 하므로 무관).
- 실습 스택(A1~A3)·Vault 복원·CNPG(T7)·Keycloak(T8) 은 이 클러스터 위에서 동작 — pilot-light 와 직교. 라이브 통합 드릴에서 RTO 실측.
- RTO(~8-10분 목표) 실측은 드릴.
