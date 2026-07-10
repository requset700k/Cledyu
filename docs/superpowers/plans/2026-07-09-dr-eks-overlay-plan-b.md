# DR Plan B — EKS Cold DR 오버레이 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 온프렘 상실 시 과금 최소경로(계정·수료·진도)를 임시 EKS에서 백업으로 재현하고, 로컬 테스트유저 로그인으로 복원 데이터 서빙을 검증하는 Cold DR 기반을 구축한다.

**Architecture:** `enable_eks_dr` 게이트 Terraform이 전용 임시 VPC+EKS+IRSA를 세우고, EKS 자체 ArgoCD가 `apps-eks/` app-of-apps로 과금경로 앱을 sync한다. Vault는 IRSA로 AWS KMS unseal 후 raft 스냅샷 복원, CNPG는 별도 DR recovery 매니페스트로 S3에서 PITR 복원(자격=IRSA). 드릴은 apply→부트스트랩→복원→검증→destroy 1회 완주.

**Tech Stack:** Terraform(`terraform-aws-modules/eks`,`/vpc`), AWS(EKS/IRSA/KMS/S3/VPC endpoints), ArgoCD app-of-apps, Helm(first-party 차트 + `values-eks.yaml`), CloudNativePG(bootstrap.recovery), HashiCorp Vault(awskms seal, raft), Keycloak Operator v26.6.1.

## Global Constraints

- **전제(§0 스펙)**: A-2가 main에 랜딩된 상태 전제. **keycloak-pg 관련 태스크(T7-b, T8 런타임)는 A-2 머지 전 착수 금지.** 나머지는 지금 진행 가능.
- **리전/계정**: `ap-northeast-2` / account `504284203153` (고정값, verbatim 사용).
- **KMS unseal 키**: `alias/cledyu-vault-unseal` = `arn:aws:kms:ap-northeast-2:504284203153:key/e29e3ec2-f5e0-4308-af6f-5b576cc99f52` — **DR-durable, 삭제 금지**.
- **백업 버킷**: `s3://cledyu-lab-dr-backups` (프리픽스: `postgres/`, `keycloak/`, `vault/`, `velero/`).
- **TF backend = S3** `cledyu-tf-state` (AWS 단독, GCP 자격 불필요).
- **정적 키 금지**: 복원 자격은 전부 IRSA. 부트스트랩에 심는 정적 시크릿은 없다(유일 명령형 스텝 = Vault raft 스냅샷 복원).
- **비용 게이트**: EKS는 평시 `enable_eks_dr=false`(0 리소스). 드릴(T10)에서만 apply→destroy. 그 외 모든 태스크는 **정적 검증만**(apply 금지).
- **오버레이 원칙**: 비즈값은 공유 `values.yaml`, `values-eks.yaml`엔 인프라 델타만. **예외 CNPG = 별도 DR 차트 경로**.
- **CNPG barman in-tree**: 오퍼레이터 1.25.x 기준 `spec.backup.barmanObjectStore` 정상(≥1.26 deprecated). DR 차트도 in-tree 유지.

---

## File Structure

**Phase 1 — Terraform (`infra/terraform/aws/`):**
- Create `eks-dr.tf` — VPC 모듈 + EKS 모듈 + node group + 애드온(EBS CSI, ALB Controller) + VPC 엔드포인트, `enable_eks_dr` count 게이트.
- Create `eks-dr-irsa.tf` — vault-unseal / cnpg-restore IRSA 롤·정책.
- Modify `variables.tf` — `enable_eks_dr`, `eks_dr_*` 변수 추가.
- Modify `outputs.tf` — 롤 ARN·OIDC·클러스터명 출력(gitops values-eks가 참조).

**Phase 2 — GitOps 오버레이 (`gitops/`):**
- Create `gitops/argocd/root-app-eks.yaml` — apps-eks/ 를 recurse 하는 EKS 전용 root-app(수동 적용).
- Create `gitops/argocd/apps-eks/*.yaml` — 과금경로 앱 Application(각 `valueFiles:[values.yaml,values-eks.yaml]`, destination in-cluster).
- Create `gitops/apps/{vault,api,web,external-secrets,cnpg-operator,argocd,trust-manager}/values-eks.yaml` — 델타.
- Create `gitops/apps/postgres-cnpg-dr/` (Chart.yaml+templates) — CNPG 복원 전용.
- Create `gitops/apps/keycloak-pg-dr/` (Chart.yaml+templates) — **A-2 머지 후**.
- Create `gitops/apps/keycloak/` (Chart.yaml+templates) — 오퍼레이터 v26.6.1 + Keycloak CR + 테마/네이버-SPI ConfigMap.

**Phase 3 — 부트스트랩 + 드릴:**
- Create `docs/RUNBOOK/dr-eks-bootstrap.md` — 수동 부트스트랩·복원·검증·destroy 절차.
- 드릴 실행(T10) = 위 전부를 apply로 한 번 완주.

---

# Phase 1 — Terraform 기반 (정적 검증만, apply 금지)

### Task 1: EKS VPC·클러스터·노드그룹 + `enable_eks_dr` 게이트

**Files:**
- Create: `infra/terraform/aws/eks-dr.tf`
- Modify: `infra/terraform/aws/variables.tf`
- Modify: `infra/terraform/aws/outputs.tf`

**Interfaces:**
- Produces: `module.eks_dr` (count 게이트), outputs `eks_dr_cluster_name`, `eks_dr_oidc_provider_arn`, `eks_dr_oidc_provider` (T2·T3 IRSA가 소비).

- [ ] **Step 1: 변수 추가**

`infra/terraform/aws/variables.tf` 에 추가:
```hcl
variable "enable_eks_dr" {
  description = "DR 드릴/재해 시에만 true. 평시 false 로 EKS 리소스 0."
  type        = bool
  default     = false
}

variable "eks_dr_cluster_version" {
  type    = string
  default = "1.31"
}

variable "eks_dr_node_instance_type" {
  # CNPG(postgres+keycloak-pg) + Vault + api/web/keycloak/argocd/eso 수용. 드릴 신뢰성 위해 on-demand.
  type    = string
  default = "m6i.xlarge"
}

variable "eks_dr_node_desired" {
  type    = number
  default = 3
}
```

- [ ] **Step 2: VPC + EKS 모듈 작성**

`infra/terraform/aws/eks-dr.tf`:
```hcl
# EKS Cold DR 기반. enable_eks_dr=false 면 count=0 → 평시 리소스 없음(비용 0).
locals {
  eks_dr_enabled = var.enable_eks_dr ? 1 : 0
  eks_dr_name    = "cledyu-dr"
  eks_dr_tags    = { Project = "cledyu", Purpose = "dr", ManagedBy = "terraform" }
}

module "eks_dr_vpc" {
  source  = "terraform-aws-modules/vpc/aws"
  version = "~> 5.13"
  count   = local.eks_dr_enabled

  name = "${local.eks_dr_name}-vpc"
  cidr = "10.90.0.0/16"

  azs             = ["ap-northeast-2a", "ap-northeast-2b", "ap-northeast-2c"]
  private_subnets = ["10.90.1.0/24", "10.90.2.0/24", "10.90.3.0/24"]
  public_subnets  = ["10.90.101.0/24", "10.90.102.0/24", "10.90.103.0/24"]

  enable_nat_gateway = true
  single_nat_gateway = true # 드릴 비용 절감(가용성 요구 낮음)

  # ALB Controller / EKS 서브넷 자동 발견 태그
  public_subnet_tags  = { "kubernetes.io/role/elb" = "1" }
  private_subnet_tags = { "kubernetes.io/role/internal-elb" = "1" }
  tags                = local.eks_dr_tags
}

module "eks_dr" {
  source  = "terraform-aws-modules/eks/aws"
  version = "~> 20.24"
  count   = local.eks_dr_enabled

  cluster_name    = local.eks_dr_name
  cluster_version = var.eks_dr_cluster_version

  cluster_endpoint_public_access = true # 부트스트랩/드릴 운영자 kubectl 접근
  enable_irsa                    = true

  vpc_id     = module.eks_dr_vpc[0].vpc_id
  subnet_ids = module.eks_dr_vpc[0].private_subnets

  eks_managed_node_groups = {
    dr = {
      instance_types = [var.eks_dr_node_instance_type]
      capacity_type  = "ON_DEMAND"
      desired_size   = var.eks_dr_node_desired
      min_size       = var.eks_dr_node_desired
      max_size       = var.eks_dr_node_desired
    }
  }

  # 부트스트랩 운영자가 관리자로 접근
  enable_cluster_creator_admin_permissions = true
  tags                                     = local.eks_dr_tags
}
```

- [ ] **Step 3: outputs 추가**

`infra/terraform/aws/outputs.tf` 에 추가:
```hcl
output "eks_dr_cluster_name" {
  value = var.enable_eks_dr ? module.eks_dr[0].cluster_name : null
}
output "eks_dr_oidc_provider_arn" {
  value = var.enable_eks_dr ? module.eks_dr[0].oidc_provider_arn : null
}
output "eks_dr_oidc_provider" {
  value = var.enable_eks_dr ? module.eks_dr[0].oidc_provider : null
}
```

- [ ] **Step 4: 평시(게이트 off) 검증 — 0 리소스**

Run:
```bash
cd infra/terraform/aws && terraform init && terraform fmt -check && terraform validate
terraform plan | tail -5
```
Expected: `validate` = Success. `plan`(default `enable_eks_dr=false`) = **No changes**(count=0).

- [ ] **Step 5: 게이트 on 검증 — VPC+EKS 계획됨 (apply 금지)**

Run: `terraform plan -var enable_eks_dr=true | grep -E "will be created|Plan:" | tail -20`
Expected: VPC(subnets/NAT/IGW), EKS 클러스터, managed node group 등 다수 create. **apply 하지 않는다.**

- [ ] **Step 6: Commit**
```bash
git add infra/terraform/aws/eks-dr.tf infra/terraform/aws/variables.tf infra/terraform/aws/outputs.tf
git commit -m "feat(dr): EKS DR VPC·클러스터·노드그룹 (enable_eks_dr 게이트)"
```

---

### Task 2: EKS 애드온 — EBS CSI + AWS Load Balancer Controller (IRSA 포함)

**Files:**
- Modify: `infra/terraform/aws/eks-dr.tf`

**Interfaces:**
- Consumes: `module.eks_dr` (T1).
- Produces: gp3 StorageClass(클러스터 내), ALB Controller(ingressClass `alb`). CNPG/Vault(gp3)·api/web(alb ingress)가 런타임 소비.

- [ ] **Step 1: EBS CSI 애드온 + IRSA를 EKS 모듈에 추가**

`eks-dr.tf` `module "eks_dr"` 블록 안에 추가:
```hcl
  cluster_addons = {
    aws-ebs-csi-driver = {
      service_account_role_arn = module.eks_dr_ebs_csi_irsa[0].iam_role_arn
    }
    coredns    = {}
    kube-proxy = {}
    vpc-cni    = {}
  }
```
그리고 모듈 블록 밖에 IRSA:
```hcl
module "eks_dr_ebs_csi_irsa" {
  source  = "terraform-aws-modules/iam/aws//modules/iam-role-for-service-accounts-eks"
  version = "~> 5.44"
  count   = local.eks_dr_enabled

  role_name             = "${local.eks_dr_name}-ebs-csi"
  attach_ebs_csi_policy = true
  oidc_providers = {
    main = {
      provider_arn               = module.eks_dr[0].oidc_provider_arn
      namespace_service_accounts = ["kube-system:ebs-csi-controller-sa"]
    }
  }
  tags = local.eks_dr_tags
}
```

- [ ] **Step 2: ALB Controller IRSA 롤 추가**

`eks-dr.tf` 에 추가:
```hcl
module "eks_dr_alb_irsa" {
  source  = "terraform-aws-modules/iam/aws//modules/iam-role-for-service-accounts-eks"
  version = "~> 5.44"
  count   = local.eks_dr_enabled

  role_name                              = "${local.eks_dr_name}-alb-controller"
  attach_load_balancer_controller_policy = true
  oidc_providers = {
    main = {
      provider_arn               = module.eks_dr[0].oidc_provider_arn
      namespace_service_accounts = ["kube-system:aws-load-balancer-controller"]
    }
  }
  tags = local.eks_dr_tags
}

output "eks_dr_alb_controller_role_arn" {
  value = var.enable_eks_dr ? module.eks_dr_alb_irsa[0].iam_role_arn : null
}
```

> 참고: ALB Controller **Helm 차트 자체는 Phase 2에서 `apps-eks`의 한 앱**으로 배포하고, 그 SA 에 위 롤 ARN 을 annotation 한다(gp3 StorageClass 도 apps-eks 의 작은 매니페스트로 생성). Terraform 은 롤만 만든다.

- [ ] **Step 3: 검증**

Run: `cd infra/terraform/aws && terraform validate && terraform plan -var enable_eks_dr=true | grep -E "ebs-csi|alb-controller|Plan:"`
Expected: validate Success, EBS/ALB IRSA 롤 create 표시.

- [ ] **Step 4: Commit**
```bash
git add infra/terraform/aws/eks-dr.tf infra/terraform/aws/outputs.tf
git commit -m "feat(dr): EKS DR 애드온 EBS CSI + ALB Controller IRSA"
```

---

### Task 3: 복원 IRSA 롤 — Vault unseal(KMS) + CNPG restore(S3)

**Files:**
- Create: `infra/terraform/aws/eks-dr-irsa.tf`
- Modify: `infra/terraform/aws/outputs.tf`

**Interfaces:**
- Consumes: `module.eks_dr` OIDC (T1).
- Produces: outputs `eks_dr_vault_unseal_role_arn`, `eks_dr_cnpg_restore_role_arn` — Phase 2 vault/cnpg-dr values-eks 가 SA annotation 으로 소비.

- [ ] **Step 1: Vault unseal 롤(KMS Decrypt)**

`infra/terraform/aws/eks-dr-irsa.tf`:
```hcl
# Vault SA(vault:vault)가 awskms seal 로 unseal — 정적 키 없이 IRSA.
data "aws_iam_policy_document" "eks_dr_vault_unseal" {
  count = local.eks_dr_enabled
  statement {
    actions   = ["kms:Encrypt", "kms:Decrypt", "kms:DescribeKey"]
    resources = ["arn:aws:kms:ap-northeast-2:504284203153:key/e29e3ec2-f5e0-4308-af6f-5b576cc99f52"]
  }
}

module "eks_dr_vault_unseal_irsa" {
  source  = "terraform-aws-modules/iam/aws//modules/iam-role-for-service-accounts-eks"
  version = "~> 5.44"
  count   = local.eks_dr_enabled

  role_name = "${local.eks_dr_name}-vault-unseal"
  role_policy_arns = { unseal = aws_iam_policy.eks_dr_vault_unseal[0].arn }
  oidc_providers = {
    main = {
      provider_arn               = module.eks_dr[0].oidc_provider_arn
      namespace_service_accounts = ["vault:vault"]
    }
  }
  tags = local.eks_dr_tags
}

resource "aws_iam_policy" "eks_dr_vault_unseal" {
  count  = local.eks_dr_enabled
  name   = "${local.eks_dr_name}-vault-unseal"
  policy = data.aws_iam_policy_document.eks_dr_vault_unseal[0].json
}
```

- [ ] **Step 2: CNPG restore 롤(S3 read + `-dr` write)**

`eks-dr-irsa.tf` 에 추가:
```hcl
# CNPG DR 클러스터 SA 가 barman inheritFromIAMRole 로 S3 접근.
# recovery = 원본 프리픽스 read, backup = -dr 프리픽스 write. 원본 삭제 권한 없음.
data "aws_iam_policy_document" "eks_dr_cnpg_restore" {
  count = local.eks_dr_enabled
  statement {
    sid       = "ListBucket"
    actions   = ["s3:ListBucket", "s3:GetBucketLocation"]
    resources = ["arn:aws:s3:::cledyu-lab-dr-backups"]
  }
  statement {
    sid       = "ReadSource"
    actions   = ["s3:GetObject"]
    resources = [
      "arn:aws:s3:::cledyu-lab-dr-backups/postgres/*",
      "arn:aws:s3:::cledyu-lab-dr-backups/keycloak/*",
    ]
  }
  statement {
    sid       = "WriteDrPrefix"
    actions   = ["s3:PutObject", "s3:GetObject"]
    resources = [
      "arn:aws:s3:::cledyu-lab-dr-backups/postgres-dr/*",
      "arn:aws:s3:::cledyu-lab-dr-backups/keycloak-dr/*",
    ]
  }
}

resource "aws_iam_policy" "eks_dr_cnpg_restore" {
  count  = local.eks_dr_enabled
  name   = "${local.eks_dr_name}-cnpg-restore"
  policy = data.aws_iam_policy_document.eks_dr_cnpg_restore[0].json
}

# CNPG 클러스터별 SA: postgres 네임스페이스의 cledyu-pg, keycloak 의 keycloak-pg.
module "eks_dr_cnpg_restore_irsa" {
  source  = "terraform-aws-modules/iam/aws//modules/iam-role-for-service-accounts-eks"
  version = "~> 5.44"
  count   = local.eks_dr_enabled

  role_name        = "${local.eks_dr_name}-cnpg-restore"
  role_policy_arns = { restore = aws_iam_policy.eks_dr_cnpg_restore[0].arn }
  oidc_providers = {
    main = {
      provider_arn = module.eks_dr[0].oidc_provider_arn
      namespace_service_accounts = [
        "postgres:cledyu-pg",
        "keycloak:keycloak-pg",
      ]
    }
  }
  tags = local.eks_dr_tags
}
```

> **serverName 프리픽스 주의:** DR 백업 write 를 원본과 다른 프리픽스(`postgres-dr/`,`keycloak-dr/`)로 완전 분리한다(§5.1 의 "-dr 접미"를 프리픽스로 구현 → IAM 으로도 원본 write 차단). recovery 는 원본 프리픽스에서 read.

- [ ] **Step 3: outputs**

`outputs.tf` 에 추가:
```hcl
output "eks_dr_vault_unseal_role_arn" {
  value = var.enable_eks_dr ? module.eks_dr_vault_unseal_irsa[0].iam_role_arn : null
}
output "eks_dr_cnpg_restore_role_arn" {
  value = var.enable_eks_dr ? module.eks_dr_cnpg_restore_irsa[0].iam_role_arn : null
}
```

- [ ] **Step 4: 검증**

Run: `cd infra/terraform/aws && terraform validate && terraform plan -var enable_eks_dr=true | grep -E "vault-unseal|cnpg-restore|Plan:"`
Expected: validate Success. 두 정책·롤 create.

- [ ] **Step 5: Commit**
```bash
git add infra/terraform/aws/eks-dr-irsa.tf infra/terraform/aws/outputs.tf
git commit -m "feat(dr): 복원 IRSA — vault unseal KMS + CNPG restore S3"
```

---

### Task 4: VPC 엔드포인트(S3/KMS/STS) + 문서 egress 확인

**Files:**
- Modify: `infra/terraform/aws/eks-dr.tf`

**Interfaces:**
- Consumes: `module.eks_dr_vpc` (T1).
- Produces: 프라이빗 서브넷에서 S3/KMS/STS 도달(엔드포인트). github/ghcr 는 NAT(T1 `enable_nat_gateway`)로 egress.

- [ ] **Step 1: 엔드포인트 추가**

`eks-dr.tf` 에 추가:
```hcl
module "eks_dr_endpoints" {
  source  = "terraform-aws-modules/vpc/aws//modules/vpc-endpoints"
  version = "~> 5.13"
  count   = local.eks_dr_enabled

  vpc_id = module.eks_dr_vpc[0].vpc_id
  endpoints = {
    s3 = {
      service      = "s3"
      service_type = "Gateway"
      route_table_ids = module.eks_dr_vpc[0].private_route_table_ids
    }
    kms = {
      service             = "kms"
      private_dns_enabled = true
      subnet_ids          = module.eks_dr_vpc[0].private_subnets
      security_group_ids  = [module.eks_dr[0].node_security_group_id]
    }
    sts = {
      service             = "sts"
      private_dns_enabled = true
      subnet_ids          = module.eks_dr_vpc[0].private_subnets
      security_group_ids  = [module.eks_dr[0].node_security_group_id]
    }
  }
  tags = local.eks_dr_tags
}
```

- [ ] **Step 2: 검증 + egress 근거 확인**

Run:
```bash
cd infra/terraform/aws && terraform validate && terraform plan -var enable_eks_dr=true | grep -E "endpoint|Plan:"
# github/ghcr 공개 egress 근거: repo/이미지에 자격 없음(Plan B 스펙 F5)
grep -rL "imagePullSecret" gitops/apps/api gitops/apps/web >/dev/null && echo "api/web pull secret 없음 = ghcr 공개 전제 OK"
```
Expected: validate Success, S3(Gateway)/KMS/STS 엔드포인트 create.

- [ ] **Step 3: Commit**
```bash
git add infra/terraform/aws/eks-dr.tf
git commit -m "feat(dr): EKS DR VPC 엔드포인트 S3/KMS/STS"
```

---

# Phase 2 — GitOps 오버레이 (정적 검증: helm template + kubeconform)

### Task 5: apps-eks app-of-apps + stateless values-eks + 플랫폼 애드온 앱

**Files:**
- Create: `gitops/argocd/root-app-eks.yaml`
- Create: `gitops/argocd/apps-eks/{platform-argocd,platform-external-secrets,data-cnpg-operator,platform-trust-manager,platform-alb-controller,platform-storage,service-api,service-web}.yaml`
- Create: `gitops/apps/{api,web,external-secrets,cnpg-operator,argocd,trust-manager}/values-eks.yaml`
- Create: `gitops/apps/alb-controller/` (신설 앱), `gitops/apps/storage/` (gp3 StorageClass)

**Interfaces:**
- Consumes: Task 2 outputs(`eks_dr_alb_controller_role_arn`), ACM ARN(`aws_acm_certificate_validation.auth`).
- Produces: EKS root-app-eks 가 sync 하는 과금경로 앱 집합. destination in-cluster.

- [ ] **Step 1: api/web 차트의 ingress className 파라미터화 여부 확인**

Run: `grep -n "ingressClassName\|className\|kubernetes.io/ingress.class\|traefik" gitops/apps/api/templates/*.yaml gitops/apps/web/templates/*.yaml`
- className 이 values 로 파라미터화돼 있으면 values-eks 로 override.
- traefik 이 하드코딩돼 있으면 템플릿을 `ingressClassName: {{ .Values.ingress.className | default "traefik" }}` 로 수정(이 Step 에 포함).

- [ ] **Step 2: gp3 StorageClass 앱 작성**

`gitops/apps/storage/storageclass-gp3.yaml`:
```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: gp3
  annotations:
    storageclass.kubernetes.io/is-default-class: "true"
provisioner: ebs.csi.aws.com
volumeBindingMode: WaitForFirstConsumer
allowVolumeExpansion: true
parameters:
  type: gp3
```
(디렉터리 앱이라 Chart 불필요 — Application 이 directory 로 sync)

- [ ] **Step 3: ALB Controller 앱 values**

`gitops/apps/alb-controller/values.yaml`(Helm chart `aws-load-balancer-controller` 래핑; Chart.yaml dependency 로 `eks-charts` 사용) — 핵심:
```yaml
clusterName: cledyu-dr
serviceAccount:
  create: true
  name: aws-load-balancer-controller
  annotations:
    eks.amazonaws.com/role-arn: "<<T2 eks_dr_alb_controller_role_arn>>"
region: ap-northeast-2
vpcId: "<<T1 vpc id — 부트스트랩 시 주입>>"
```
> `<<...>>` 는 부트스트랩(T9 런북)에서 `terraform output` 값으로 치환. 런북에 치환 절차 명시.

- [ ] **Step 4: stateless values-eks 작성**

`gitops/apps/api/values-eks.yaml`:
```yaml
replicaCount: 1
ingress:
  className: alb
  annotations:
    alb.ingress.kubernetes.io/scheme: internet-facing
    alb.ingress.kubernetes.io/target-type: ip
    alb.ingress.kubernetes.io/group.name: cledyu-dr
    alb.ingress.kubernetes.io/certificate-arn: "<<ACM auth cert ARN>>"
```
`gitops/apps/web/values-eks.yaml` — 동일 패턴(className alb, group cledyu-dr, replicaCount 1).
`gitops/apps/{external-secrets,cnpg-operator,argocd,trust-manager}/values-eks.yaml` — 최소(빈 파일 또는 리소스 sizing만). 클러스터 무관.

- [ ] **Step 5: apps-eks Application + root-app-eks 작성**

`gitops/argocd/apps-eks/service-api.yaml`(나머지 앱도 동일 패턴, path/namespace만 교체):
```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: eks-service-api
  namespace: argocd
  finalizers: [resources-finalizer.argocd.argoproj.io]
spec:
  project: default
  source:
    repoURL: https://github.com/requset700k/Cledyu.git
    targetRevision: feat/dr-eks-overlay   # 드릴 검증 후 main
    path: gitops/apps/api
    helm:
      releaseName: api
      valueFiles: [values.yaml, values-eks.yaml]
  destination: { server: https://kubernetes.default.svc, namespace: api }
  syncPolicy:
    automated: { prune: true, selfHeal: true }
    syncOptions: [CreateNamespace=true, ServerSideApply=true]
```
`gitops/argocd/root-app-eks.yaml`:
```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: root-apps-eks
  namespace: argocd
  finalizers: [resources-finalizer.argocd.argoproj.io]
spec:
  project: default
  source:
    repoURL: https://github.com/requset700k/Cledyu.git
    targetRevision: feat/dr-eks-overlay
    path: gitops/argocd/apps-eks
    directory: { recurse: true, include: "*.yaml" }
  destination: { server: https://kubernetes.default.svc, namespace: argocd }
  syncPolicy:
    automated: { prune: true, selfHeal: true }
    syncOptions: [CreateNamespace=true]
```

- [ ] **Step 6: 정적 렌더 검증**

Run:
```bash
helm template api gitops/apps/api -f gitops/apps/api/values.yaml -f gitops/apps/api/values-eks.yaml | grep -E "ingressClassName|alb.ingress" 
helm template web gitops/apps/web -f gitops/apps/web/values.yaml -f gitops/apps/web/values-eks.yaml | grep ingressClassName
# 매니페스트 스키마 검증
for a in api web; do helm template $a gitops/apps/$a -f gitops/apps/$a/values.yaml -f gitops/apps/$a/values-eks.yaml | kubeconform -strict -ignore-missing-schemas; done
```
Expected: `ingressClassName: alb` 렌더, kubeconform 통과.

- [ ] **Step 7: Commit**
```bash
git add gitops/argocd/root-app-eks.yaml gitops/argocd/apps-eks/ gitops/apps/api/values-eks.yaml gitops/apps/web/values-eks.yaml gitops/apps/external-secrets/values-eks.yaml gitops/apps/cnpg-operator/values-eks.yaml gitops/apps/argocd/values-eks.yaml gitops/apps/trust-manager/values-eks.yaml gitops/apps/alb-controller/ gitops/apps/storage/
git commit -m "feat(dr): apps-eks app-of-apps + stateless values-eks + ALB/gp3"
```

---

### Task 6: Vault values-eks — gp3(2개) + IRSA unseal(env 제거)

**Files:**
- Create: `gitops/apps/vault/values-eks.yaml`
- Create: `gitops/argocd/apps-eks/platform-vault.yaml`

**Interfaces:**
- Consumes: Task 3 `eks_dr_vault_unseal_role_arn`.
- Produces: Vault(단일 raft, gp3, IRSA unseal) — T10 스냅샷 복원 대상.

- [ ] **Step 1: Vault 차트의 seal/creds 주입 구조 확인**

Run: `sed -n '18,30p' gitops/apps/vault/values-awskms.yaml; grep -n "extraSecretEnvironmentVars\|serviceAccount\|seal " gitops/apps/vault/values*.yaml`
확인점: (a) 온프렘은 `extraSecretEnvironmentVars`(AWS_ACCESS_KEY_ID/SECRET)로 seal creds 주입, (b) seal 스탠자는 raft config 내 `seal "awskms"`.

- [ ] **Step 2: values-eks 작성 (env 주입 제거 + SA annotation + gp3 + 단일노드)**

`gitops/apps/vault/values-eks.yaml`:
```yaml
server:
  # IRSA: SA 에 role-arn. extraSecretEnvironmentVars 는 넣지 않는다(env 있으면 web identity 덮음).
  serviceAccount:
    annotations:
      eks.amazonaws.com/role-arn: "<<T3 eks_dr_vault_unseal_role_arn>>"
  extraSecretEnvironmentVars: []   # 온프렘 vault-aws-kms-creds 주입 무효화
  dataStorage:
    storageClass: gp3
  auditStorage:
    storageClass: gp3
  ha:
    replicas: 1   # 드릴: 단일노드 raft(복원 단순·비용↓)
    raft:
      enabled: true
      setNodeId: true
      config: |
        ui = true
        disable_mlock = true
        listener "tcp" {
          address = "[::]:8200"
          cluster_address = "[::]:8201"
          tls_disable = 0
          tls_cert_file = "/vault/tls/tls.crt"
          tls_key_file  = "/vault/tls/tls.key"
          tls_client_ca_file = "/vault/tls/ca.crt"
        }
        storage "raft" { path = "/vault/data" }
        seal "awskms" {
          region     = "ap-northeast-2"
          kms_key_id = "arn:aws:kms:ap-northeast-2:504284203153:key/e29e3ec2-f5e0-4308-af6f-5b576cc99f52"
        }
        service_registration "kubernetes" {}
```
> **TLS 주의:** vault-tls Secret(파드 내부 TLS)은 EKS 에도 필요. trust-manager 는 CA 배포만 하고 발급을 못 하므로, 드릴에선 **부트스트랩(T9)에서 self-signed vault-tls 를 수동 생성**(`openssl` → `kubectl create secret tls`)한다(런북에 명령 명시). api/web 은 **ALB 가 ACM 으로 TLS 종단**하므로 백엔드 파드는 HTTP 로 두면 되어 별도 인증서 불필요(values-eks 에서 tlsSecret 비활성). 단일노드 Vault 라 raft retry_join 불필요.

- [ ] **Step 3: apps-eks Application(platform-vault) 작성** — Task 5 패턴, path `gitops/apps/vault`, namespace `vault`, `valueFiles:[values.yaml, values-eks.yaml]`.

- [ ] **Step 4: 정적 렌더 검증**

Run:
```bash
helm template vault gitops/apps/vault -f gitops/apps/vault/values.yaml -f gitops/apps/vault/values-eks.yaml \
  | grep -E "role-arn|storageClass|seal \"awskms\"|AWS_ACCESS_KEY_ID"
```
Expected: `role-arn` annotation 존재, `storageClass: gp3`(data+audit), `seal "awskms"` 렌더, **`AWS_ACCESS_KEY_ID` env 미존재**(제거 확인).

- [ ] **Step 5: Commit**
```bash
git add gitops/apps/vault/values-eks.yaml gitops/argocd/apps-eks/platform-vault.yaml
git commit -m "feat(dr): Vault values-eks — IRSA unseal + gp3, 정적 키 env 제거"
```

---

### Task 7: CNPG DR 복원 차트 (postgres-cnpg-dr / keycloak-pg-dr)

**Files:**
- Create: `gitops/apps/postgres-cnpg-dr/{Chart.yaml,values.yaml,templates/cluster.yaml}`
- Create: `gitops/apps/keycloak-pg-dr/{Chart.yaml,values.yaml,templates/cluster.yaml}` — **A-2 머지 후**
- Create: `gitops/argocd/apps-eks/{data-postgres-cnpg-dr,data-keycloak-pg-dr}.yaml`

**Interfaces:**
- Consumes: Task 3 `eks_dr_cnpg_restore_role_arn`(SA), S3 원본 백업(`postgres/`,`keycloak/`).
- Produces: 서비스 `cledyu-pg-rw.postgres.svc`, `keycloak-pg-rw.keycloak.svc`(복원 완료 후). api/keycloak 가 소비.

- [ ] **Step 1 (T7-a): postgres-cnpg-dr recovery Cluster 작성**

`gitops/apps/postgres-cnpg-dr/templates/cluster.yaml`:
```yaml
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: cledyu-pg
  namespace: {{ .Release.Namespace }}
spec:
  instances: 1
  imageName: "ghcr.io/cloudnative-pg/postgresql:16.4@sha256:99be063781d171d3971089b49c992706bdab9ccbd2b57cdf126c7542773aedfe"
  storage:
    size: {{ .Values.storage.size }}
    storageClass: gp3
  # IRSA: 이 SA 에 barman inheritFromIAMRole 이 assume 할 롤을 annotation.
  serviceAccountTemplate:
    metadata:
      annotations:
        eks.amazonaws.com/role-arn: "{{ .Values.restoreRoleArn }}"
  # 복원: 원본(serverName=cledyu-pg, postgres/ 프리픽스)에서 최신까지 PITR.
  bootstrap:
    recovery:
      source: cledyu-pg-origin
  externalClusters:
    - name: cledyu-pg-origin
      barmanObjectStore:
        destinationPath: "s3://cledyu-lab-dr-backups/postgres"
        serverName: cledyu-pg
        endpointURL: "https://s3.ap-northeast-2.amazonaws.com"
        s3Credentials:
          inheritFromIAMRole: true
        wal: { compression: gzip }
  # 새 백업은 원본과 분리된 프리픽스로(-dr) — 원본 아카이브 보호.
  backup:
    barmanObjectStore:
      destinationPath: "s3://cledyu-lab-dr-backups/postgres-dr"
      serverName: cledyu-pg-dr
      endpointURL: "https://s3.ap-northeast-2.amazonaws.com"
      s3Credentials:
        inheritFromIAMRole: true
      wal: { compression: gzip }
```
`values.yaml`: `storage: {size: 10Gi}`, `restoreRoleArn: ""`(부트스트랩서 주입). `Chart.yaml`: name `postgres-cnpg-dr`, version 0.1.0.

> 운영 `postgres-cnpg` 템플릿은 **건드리지 않는다**(import fail-safe 유지). 이 차트는 완전 별개 경로.

- [ ] **Step 2: T7-a 정적 검증**

Run:
```bash
helm template pgdr gitops/apps/postgres-cnpg-dr -f gitops/apps/postgres-cnpg-dr/values.yaml --set restoreRoleArn=arn:aws:iam::504284203153:role/cledyu-dr-cnpg-restore \
  | grep -E "bootstrap:|recovery:|inheritFromIAMRole|serverName|role-arn|storageClass"
helm template pgdr gitops/apps/postgres-cnpg-dr -f gitops/apps/postgres-cnpg-dr/values.yaml --set restoreRoleArn=x | kubeconform -strict -ignore-missing-schemas
```
Expected: `recovery.source`, 두 곳 `inheritFromIAMRole: true`, backup `serverName: cledyu-pg-dr`, recovery `serverName: cledyu-pg`, SA role-arn, gp3.

- [ ] **Step 3 (T7-b, A-2 머지 후): keycloak-pg-dr 작성**

A-2 의 `gitops/apps/keycloak-pg/templates/*.yaml`(main)을 기준으로, 위 postgres-cnpg-dr 와 동일 패턴의 recovery Cluster 를 `keycloak-pg-dr` 로 작성:
- name `keycloak-pg`, namespace `keycloak`
- recovery source serverName = A-2 백업 serverName(원본), 프리픽스 `s3://cledyu-lab-dr-backups/keycloak`
- backup 프리픽스 `keycloak-dr`, serverName `keycloak-pg-dr`
- imageName = A-2 keycloak-pg 의 PG major digest(main 값 확인 후 복사)
- serviceAccountTemplate role-arn = `eks_dr_cnpg_restore_role_arn`

> **A-2 머지 전 이 Step 착수 금지.** main 에 keycloak-pg 차트가 있어야 원본 serverName/이미지 digest 를 정확히 복사한다.

- [ ] **Step 4: apps-eks Application 2개 작성** — `data-postgres-cnpg-dr.yaml`(ns postgres), `data-keycloak-pg-dr.yaml`(ns keycloak). Task 5 패턴, path 각 DR 차트.

- [ ] **Step 5: Commit** (T7-b 는 별도 커밋, A-2 머지 후)
```bash
git add gitops/apps/postgres-cnpg-dr/ gitops/argocd/apps-eks/data-postgres-cnpg-dr.yaml
git commit -m "feat(dr): postgres-cnpg-dr recovery 차트 (IRSA, -dr 프리픽스)"
```

---

### Task 8: Keycloak GitOps 앱 (오퍼레이터 v26.6.1 + CR + ConfigMap)

**Files:**
- Create: `gitops/apps/keycloak-operator/` (Chart: CRD+deployment upstream 적용)
- Create: `gitops/apps/keycloak/{Chart.yaml,values.yaml,templates/{keycloak.yaml,theme-cm.yaml,naver-spi-cm.yaml}}`
- Create: `gitops/argocd/apps-eks/{platform-keycloak-operator,service-keycloak}.yaml`

**Interfaces:**
- Consumes: `keycloak-pg-rw.keycloak.svc`(T7-b), Vault→ESO 시크릿(admin/db 자격), 테마/SPI 소스(`keycloak/naver-idp`).
- Produces: Keycloak(auth.cledyu.com hostname, ALB ingress). api 가 OIDC 소비.

- [ ] **Step 1: Ansible 역할에서 오퍼레이터 설치 방식 복사**

Run: `sed -n '1,90p' ansible/roles/keycloak_operator/tasks/main.yml; cat ansible/roles/keycloak_operator/defaults/main.yml`
확인: v26.6.1 CRD/realmimport/deployment URL. 이를 ArgoCD 앱으로 재현 — `keycloak-operator` Application 이 upstream manifest URL 들을 sync(Application `source.directory` 대신 여러 URL → Chart 로 감싸 `kubectl apply` 대신 선언). 가장 단순: Chart `templates/` 에 오퍼레이터 deployment YAML 을 vendoring(v26.6.1 고정), CRD 는 별도 Application(sync-wave -1).

- [ ] **Step 2: Keycloak CR 템플릿 (Ansible j2 → Helm)**

`gitops/apps/keycloak/templates/keycloak.yaml` — `ansible/roles/keycloak_foundation/templates/keycloak.yaml.j2` 를 Helm values 로 치환:
```yaml
apiVersion: k8s.keycloak.org/v2alpha1
kind: Keycloak
metadata:
  name: cledyu-keycloak
  namespace: {{ .Release.Namespace }}
spec:
  instances: {{ .Values.instances }}
  db:
    vendor: postgres
    host: keycloak-pg-rw          # T7-b 복원 서비스
    usernameSecret: { name: {{ .Values.dbSecret }}, key: username }
    passwordSecret: { name: {{ .Values.dbSecret }}, key: password }
  http: { httpEnabled: true }
  ingress: { enabled: false }      # ALB 는 별도 Ingress(아래)로
  proxy: { headers: xforwarded }
  hostname:
    hostname: {{ .Values.hostname }}
    strict: {{ .Values.hostnameStrict }}
  unsupported:
    podTemplate:
      spec:
        volumes:
          - { name: cledyu-theme, configMap: { name: cledyu-theme } }
          - { name: naver-spi, configMap: { name: naver-spi } }
        containers:
          - name: keycloak
            volumeMounts:
              - { name: cledyu-theme, mountPath: /opt/keycloak/themes/cledyu, readOnly: true }
              - { name: naver-spi, mountPath: /opt/keycloak/providers/naver-idp.jar, subPath: naver-idp.jar, readOnly: true }
```
`values.yaml`: `instances: 1`, `dbSecret: keycloak-db-credentials`(A-2 ESO 시크릿명 main 확인 후 일치), `hostname: auth.cledyu.com`, `hostnameStrict: false`.

- [ ] **Step 3: 테마/SPI ConfigMap 템플릿** — `theme-cm.yaml`(theme.properties/cledyu.css/messages_ko.properties), `naver-spi-cm.yaml`(binaryData: naver-idp.jar). 소스는 `keycloak/naver-idp/dist/naver-idp.jar` + 테마 파일(Ansible 역할 files/ 에서 복사).

- [ ] **Step 4: ALB Ingress 추가** — `templates/ingress.yaml`: className alb, host auth.cledyu.com, group cledyu-dr, ACM ARN, backend = keycloak service(오퍼레이터 생성 `cledyu-keycloak-service:8080`).

- [ ] **Step 5: 정적 검증**

Run:
```bash
helm template keycloak gitops/apps/keycloak -f gitops/apps/keycloak/values.yaml \
  | grep -E "kind: Keycloak|host: keycloak-pg-rw|hostname: auth.cledyu.com|kind: ConfigMap|ingressClassName"
helm template keycloak gitops/apps/keycloak -f gitops/apps/keycloak/values.yaml | kubeconform -strict -ignore-missing-schemas
```
Expected: Keycloak CR(db.host=keycloak-pg-rw, hostname auth.cledyu.com), 2 ConfigMap, ALB Ingress 렌더.

- [ ] **Step 6: Commit** (A-2 머지 후: keycloak 는 keycloak-pg-rw 에 런타임 의존)
```bash
git add gitops/apps/keycloak-operator/ gitops/apps/keycloak/ gitops/argocd/apps-eks/platform-keycloak-operator.yaml gitops/argocd/apps-eks/service-keycloak.yaml
git commit -m "feat(dr): Keycloak GitOps 앱 (오퍼레이터 v26.6.1 + CR + 테마/SPI)"
```

---

# Phase 3 — 부트스트랩 런북 + 드릴

### Task 9: 부트스트랩 런북 작성

**Files:**
- Create: `docs/RUNBOOK/dr-eks-bootstrap.md`

**Interfaces:**
- Consumes: Phase 1·2 산출물 전부.
- Produces: 사람이 §6 순서를 손으로 완주하는 절차(= T10 드릴 대본, Plan C 자동화의 청사진).

- [ ] **Step 1: 런북 작성 — 아래 순서를 명령까지 포함해 문서화**

`docs/RUNBOOK/dr-eks-bootstrap.md` 필수 섹션:
```
1. 사전: A-2 main 머지 확인, aws sts get-caller-identity, terraform output(정적값 확보)
2. terraform apply -var enable_eks_dr=true  → aws eks update-kubeconfig --name cledyu-dr
3. terraform output 로 얻은 role ARN/vpc id/ACM ARN 을
   - alb-controller/values, vault/keycloak/cnpg-dr values-eks 의 <<...>> 치환(또는 ArgoCD parameter override)
4. ArgoCD 설치(helm) + root-app-eks 적용
5. sync-wave: storage(gp3)→cnpg-operator/eso(CRD)→alb-controller→vault
6. Vault: SA IRSA unseal 확인(vault status = unsealed, seal_type awskms), 단 데이터 empty
7. [명령형] aws s3 cp 최신 vault/ 스냅샷 → kubectl exec vault-0 -- vault operator raft snapshot restore
   → ESO 온라인(ExternalSecret Synced) 확인
8. CNPG DR: postgres-cnpg-dr / keycloak-pg-dr sync → recovery 완료 대기(kubectl cnpg status)
9. keycloak-operator → keycloak(CR Ready) → api → web
10. 검증(§드릴)  11. terraform destroy -var enable_eks_dr=true
```
각 스텝에 실제 명령/예상출력 포함(Vault 스냅샷 복원 명령, cnpg status 확인 등).

- [ ] **Step 2: 검증(문서 리뷰)**

Run: `grep -c "^" docs/RUNBOOK/dr-eks-bootstrap.md; markdownlint docs/RUNBOOK/dr-eks-bootstrap.md`
Expected: 11개 스텝 전부 명령 포함, 린트 통과. `<<...>>` 치환 지점이 T5/T6/T7 values 와 1:1 대응.

- [ ] **Step 3: Commit**
```bash
git add docs/RUNBOOK/dr-eks-bootstrap.md
git commit -m "docs(dr): EKS DR 부트스트랩·복원 런북"
```

---

### Task 10: 검증 드릴 (실제 apply — 비용 발생, 1회 완주 후 destroy)

**Files:** (코드 변경 없음 — 실행·기록)
- Modify: `docs/RUNBOOK/dr-eks-bootstrap.md` (드릴 결과·RTO 실측 표 추가)

**Interfaces:**
- Consumes: T1~T9 전부 + 살아있는 S3 백업.
- Produces: "검증된 DR 드릴" 증거 + Plan C 착수 근거.

> **선행조건: A-2 머지 완료 + T7-b/T8 완료.** 이 태스크는 `enable_eks_dr=true` apply 로 **과금**이 발생한다. 완주 즉시 destroy.

- [ ] **Step 1: 런북 완주 (T9 1~9)** — apply → 부트스트랩 → Vault 스냅샷 복원 → CNPG recovery → keycloak/api/web Ready.

- [ ] **Step 2: 복원 정합성 검증 (F2 — 거짓통과 방지)**

Run(예시):
```bash
# postgres 복원 row 수 대조(원본 기록값과 비교)
kubectl -n postgres exec cledyu-pg-1 -- psql -U cledyu -d cledyu -c "select count(*) from <수료 테이블>;"
# api 가 DB 모드인지(in-memory 폴백 아님) — 헬스/로그로 확인
kubectl -n api logs deploy/api | grep -iE "db mode|dsn|in-memory"
```
Expected: row 수 = 원본 기준, api = DB 모드. **in-memory 면 실패로 간주.**

- [ ] **Step 3: 로컬 테스트유저 로그인 → 서빙 검증 (F3 축소검증)**

```
- Keycloak admin 으로 realm 에 로컬 테스트유저(id/pw) 생성
- 그 유저로 토큰 발급(직접 토큰 엔드포인트 or 포트포워드)
- 토큰으로 api 호출 → 복원된 특정 학습자의 수료/진도 값이 반환되는지 대조
```
Expected: 알려진 학습자 데이터가 정확히 서빙됨. (소셜로그인은 범위 밖 — Plan C 이월)

- [ ] **Step 4: RTO 구간별 실측 기록** — apply/부트스트랩/복원/검증 각 소요시간을 런북 표에 기입.

- [ ] **Step 5: Destroy + 잔존 확인**

Run:
```bash
cd infra/terraform/aws && terraform destroy -var enable_eks_dr=true
terraform plan | tail -3   # No changes 확인(게이트 off 복귀)
aws elbv2 describe-load-balancers --query "LoadBalancers[?contains(LoadBalancerName,'cledyu-dr')]" # ALB 잔존 없나
```
Expected: destroy 완료, 게이트 off 로 0 리소스, ALB Controller 가 만든 ALB 도 정리됨(없으면 수동 삭제).

- [ ] **Step 6: Commit (드릴 결과)**
```bash
git add docs/RUNBOOK/dr-eks-bootstrap.md
git commit -m "docs(dr): Plan B 검증 드릴 완주 결과·RTO 실측"
```

---

## 완료 기준

- Phase 1·2 정적 검증 전부 통과(평시 게이트 off = 0 리소스)
- T10 드릴: **로컬 테스트유저가 복원된 학습자 수료/진도를 실제로 서빙받음** + RTO 실측 기록 + destroy 후 잔존 0
- 소셜로그인 end-to-end·failback·관측·kafka 등은 범위 밖(스펙 §9) → Plan C / A-3 로 이월
