# 클라우드 네이티브 세션 이미지 빌드 파이프라인 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 온프렘 libguestfs 우회를 클라우드 packer-qemu(EC2 metal) 빌드로 대체하고, 단일 qcow2에서 containerDisk(온프렘)+AMI(EC2)를 산출해 세션 부팅 대기를 수초로 낮춘다.

**Architecture:** 일회성 EC2 `*.metal`이 packer-qemu로 Ubuntu 22.04를 부팅·도구설치(서비스 disabled)해 qcow2를 만들고, containerDisk로 ghcr push + qcow2→S3→import-image로 AMI 등록 후 self-terminate. GitHub Action(hosted, OIDC)이 launch·poll만 담당. 랩 콘텐츠는 install→start로 교체(런타임 Go 무수정).

**Tech Stack:** packer(qemu builder), QEMU/KVM, AWS(EC2 metal·S3·VM Import/Export·IAM OIDC), Terraform, GitHub Actions, KubeVirt/CDI, ArgoCD.

## Global Constraints

- 리전: `ap-northeast-2`(서울). 계정: cledyu `504284203153`(프로파일 `cledyu`).
- ghcr 이미지: `ghcr.io/requset700k/cledyu-lab-base:<tag>` (public). org.opencontainers.image.source 라벨 유지.
- 커밋: Conventional Commits, type∈{feat,fix,refactor,perf,docs,test,chore,ci,build,revert,security}, scope∈{infra,k8s,terraform,ansible,gitops,api,web,...}, subject 소문자 시작(`^(?![A-Z]).+$`), header≤100자, body 줄당≤120자, 이모지 금지.
- 문서·주석 한국어, 코드 식별자·CLI·키는 영어.
- 셸 스크립트: shellcheck 통과 + shfmt `-i 2 -ci -sr`. `&&/||` 체인 대신 if-then-else.
- yamllint: document-start 경고는 기존 관례상 허용. `**/vendor/` 무시.
- image-scan.yml은 `**/Dockerfile`·`**/Dockerfile.*` 글롭을 스캔 → qcow2 없이 빌드 불가한 containerDisk는 `*.dockerfile` 확장자로 글롭 회피(기존 `containerdisk.dockerfile` 관례 유지).
- 베이킹 규율: 도구는 **설치만, 서비스 auto-enable 금지**(`systemctl disable`/`INSTALL_K3S_SKIP_ENABLE=true`). 기동은 랩 cloud-init이 담당.
- 두 배달 레그(containerDisk push / AMI import)는 독립. `import_ami` 플래그로 AMI 레그 on/off(DR 대비).

---

## File Structure

빌드(Phase A):
- Create `infra/images/lab-base/lab-base.pkr.hcl` — packer qemu 템플릿(Ubuntu cloud img → qcow2).
- Create `infra/images/lab-base/provisioners/00-common.sh` — qemu-guest-agent + 공통 CLI.
- Create `infra/images/lab-base/provisioners/10-k3s.sh` — k3s(skip-enable) + nginx 사전 pull.
- Create `infra/images/lab-base/provisioners/20-docker.sh` — docker.io(disable).
- Create `infra/images/lab-base/provisioners/30-code-server.sh` — code-server.
- Create `infra/images/lab-base/provisioners/40-terraform.sh` — terraform 바이너리.
- Create `infra/images/lab-base/provisioners/50-ansible-helm.sh` — ansible-core + helm.
- Create `infra/images/lab-base/provisioners/99-smoke.sh` — in-image smoke test.
- Modify `infra/images/lab-base/build-and-push.sh` — packer 산출 qcow2 소비.
- Delete `infra/images/lab-base/bake.sh`, `infra/images/lab-base/setup-host.sh`.

오케스트레이션(Phase B):
- Create `infra/terraform/aws/image-baker.tf` — OIDC provider+role(GH Action), baker IAM role/instance-profile, S3 버킷.
- Modify `infra/terraform/aws/variables.tf` — baker 변수(metal instance type, gh repo).
- Modify `infra/terraform/aws/outputs.tf` — baker role arn, S3 버킷명, AMI 조회용 출력.
- Create `infra/images/lab-base/baker-bootstrap.sh` — metal user-data가 실행하는 전체 베이크 오케스트레이션.
- Modify `.github/workflows/build-lab-base-image.yml` — hosted+OIDC, metal launch, sentinel poll.

롤아웃·콘텐츠(Phase C):
- Modify `apps/api/internal/content/labs/lab-k8s-basics.yaml`, `lab-helm-advanced.yaml`, `lab-terraform-basics.yaml`, `lab-docker-basics.yaml`, `lab-ansible-basics.yaml` — install→start.
- Modify `gitops/apps/kubevirt/base-datavolume.yaml` — registry 태그 bump(빌드 산출 후).
- Modify `infra/terraform/aws/main.tf` / `variables.tf` — `ami_id` 신규 AMI 반영 + `update_default_version=true`(PR #177 미적용분).

검증(Phase D): 신규 파일 없음 — 명령 기반 e2e.

---

## Phase A — 빌드 파이프라인 (deliverable: 로컬/metal에서 packer로 baked qcow2 생성, smoke 통과)

### Task A1: provisioner 스크립트 — 공통 CLI + qemu-guest-agent

**Files:**
- Create: `infra/images/lab-base/provisioners/00-common.sh`

**Interfaces:**
- Produces: `/etc/cledyu-baked` 마커 파일(베이킹 이미지 식별). 후속 스크립트·smoke가 참조.

- [ ] **Step 1: 스크립트 작성**

```bash
#!/usr/bin/env bash
# 공통 CLI 와 qemu-guest-agent 를 설치한다. 서비스는 켜지 않는다(베이킹 규율).
# cloud-init clean 으로 베이스 이미지를 first-boot 가능한 상태로 되돌린다(머신ID/seed 제거).
set -euo pipefail

export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends \
  qemu-guest-agent curl unzip jq ca-certificates gnupg apt-transport-https

# 서비스 비활성(랩 cloud-init 이 필요 시 enable). agent 는 KubeVirt/EC2 부팅 시 별도 기동됨.
systemctl disable qemu-guest-agent || true

echo "baked at $(date -u +%Y-%m-%dT%H:%M:%SZ)" >/etc/cledyu-baked
```

- [ ] **Step 2: shellcheck/shfmt 검증**

Run: `shellcheck infra/images/lab-base/provisioners/00-common.sh && shfmt -i 2 -ci -sr -d infra/images/lab-base/provisioners/00-common.sh`
Expected: 출력 없음(통과). 차이 있으면 `shfmt -w`로 정렬.

- [ ] **Step 3: 실행권한 + 커밋**

```bash
chmod +x infra/images/lab-base/provisioners/00-common.sh
git add infra/images/lab-base/provisioners/00-common.sh
git commit -m "build(infra): add common cli provisioner for lab base image"
```

### Task A2: provisioner — k3s(skip-enable) + nginx 사전 pull

**Files:**
- Create: `infra/images/lab-base/provisioners/10-k3s.sh`

**Interfaces:**
- Consumes: 00-common(curl).
- Produces: `/usr/local/bin/k3s`(설치, 서비스 disabled), k3s containerd 이미지 스토어에 `docker.io/library/nginx:1.27-alpine` 캐시.

- [ ] **Step 1: 스크립트 작성**

```bash
#!/usr/bin/env bash
# k3s 를 설치하되 부팅 시 자동기동하지 않는다(INSTALL_K3S_SKIP_ENABLE=true).
# 랩 cloud-init 이 `systemctl enable --now k3s` 로 켠다. nginx 이미지를 k3s 의 containerd
# (CRI, k8s.io namespace)에 미리 받아 둬 step 검증의 pull 지연을 없앤다.
set -euo pipefail

curl -sfL https://get.k3s.io -o /tmp/k3s-install.sh
INSTALL_K3S_SKIP_ENABLE=true \
  INSTALL_K3S_EXEC="--write-kubeconfig-mode 644 --cluster-cidr=10.244.0.0/16 --service-cidr=10.245.0.0/16" \
  sh /tmp/k3s-install.sh
rm -f /tmp/k3s-install.sh

# 이미지 사전 pull 은 k3s 가 잠깐 떠 있어야 가능하다. 임시 기동→pull→정지.
systemctl start k3s
for i in $(seq 1 24); do
  if k3s crictl pull docker.io/library/nginx:1.27-alpine; then break; fi
  sleep 5
done
systemctl stop k3s
systemctl disable k3s || true
```

- [ ] **Step 2: shellcheck/shfmt 검증**

Run: `shellcheck infra/images/lab-base/provisioners/10-k3s.sh && shfmt -i 2 -ci -sr -d infra/images/lab-base/provisioners/10-k3s.sh`
Expected: 통과.

- [ ] **Step 3: 실행권한 + 커밋**

```bash
chmod +x infra/images/lab-base/provisioners/10-k3s.sh
git add infra/images/lab-base/provisioners/10-k3s.sh
git commit -m "build(infra): add k3s provisioner with skip-enable and nginx prepull"
```

### Task A3: provisioner — docker.io / code-server / terraform / ansible-helm

**Files:**
- Create: `infra/images/lab-base/provisioners/20-docker.sh`
- Create: `infra/images/lab-base/provisioners/30-code-server.sh`
- Create: `infra/images/lab-base/provisioners/40-terraform.sh`
- Create: `infra/images/lab-base/provisioners/50-ansible-helm.sh`

- [ ] **Step 1: 20-docker.sh**

```bash
#!/usr/bin/env bash
# docker.io 설치, 서비스는 disabled(랩 cloud-init 이 enable + lab 그룹/소켓 처리).
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends docker.io
systemctl disable docker || true
```

- [ ] **Step 2: 30-code-server.sh**

```bash
#!/usr/bin/env bash
# code-server 설치(바이너리만). 랩 cloud-init 이 config 작성 + code-server@lab 기동.
set -euo pipefail
curl -fsSL https://code-server.dev/install.sh -o /tmp/code-server-install.sh
sh /tmp/code-server-install.sh
rm -f /tmp/code-server-install.sh
systemctl disable code-server@lab || true
```

- [ ] **Step 3: 40-terraform.sh**

```bash
#!/usr/bin/env bash
# terraform 바이너리 설치(releases.hashicorp.com zip → /usr/local/bin).
set -euo pipefail
curl -fsSL -o /tmp/terraform.zip \
  https://releases.hashicorp.com/terraform/1.9.8/terraform_1.9.8_linux_amd64.zip
unzip -o /tmp/terraform.zip -d /usr/local/bin
rm -f /tmp/terraform.zip
```

- [ ] **Step 4: 50-ansible-helm.sh**

```bash
#!/usr/bin/env bash
# ansible-core(apt) + helm(get-helm-3) 설치.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends ansible-core
curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 -o /tmp/get-helm-3.sh
bash /tmp/get-helm-3.sh
rm -f /tmp/get-helm-3.sh
```

- [ ] **Step 5: shellcheck/shfmt 전체 검증**

Run: `for f in infra/images/lab-base/provisioners/{20-docker,30-code-server,40-terraform,50-ansible-helm}.sh; do shellcheck "$f" && shfmt -i 2 -ci -sr -d "$f"; done`
Expected: 통과.

- [ ] **Step 6: 실행권한 + 커밋**

```bash
chmod +x infra/images/lab-base/provisioners/{20-docker,30-code-server,40-terraform,50-ansible-helm}.sh
git add infra/images/lab-base/provisioners/
git commit -m "build(infra): add docker code-server terraform ansible helm provisioners"
```

### Task A4: in-image smoke test

**Files:**
- Create: `infra/images/lab-base/provisioners/99-smoke.sh`

**Interfaces:**
- Consumes: A1–A3 산출(설치된 도구).
- Produces: 비-0 종료 시 packer 빌드 abort → 아티팩트 미배포.

- [ ] **Step 1: 스크립트 작성**

```bash
#!/usr/bin/env bash
# 베이킹된 도구가 전부 존재하는지 확인한다. 하나라도 없으면 비-0 종료로 빌드를 중단시킨다.
set -euo pipefail

test -f /etc/cledyu-baked
command -v qemu-ga
k3s --version
docker --version
code-server --version
terraform version
command -v ansible
helm version --short
# nginx 이미지 캐시 확인 — 이미지는 10-k3s.sh 가 crictl(k8s.io ns)로 받았으므로 동일하게
# crictl 로 확인한다. containerd 가 꺼져 있어 잠깐 기동→확인→정지한다.
systemctl start k3s
ok=0
for _i in $(seq 1 12); do
  if k3s crictl images 2> /dev/null | grep -q nginx; then
    ok=1
    break
  fi
  sleep 5
done
systemctl stop k3s
if [ "$ok" -ne 1 ]; then
  echo "nginx image not pre-pulled" >&2
  exit 1
fi
echo "smoke OK"
```

- [ ] **Step 2: shellcheck/shfmt + 커밋**

Run: `shellcheck infra/images/lab-base/provisioners/99-smoke.sh && shfmt -i 2 -ci -sr -d infra/images/lab-base/provisioners/99-smoke.sh`
Expected: 통과.

```bash
chmod +x infra/images/lab-base/provisioners/99-smoke.sh
git add infra/images/lab-base/provisioners/99-smoke.sh
git commit -m "build(infra): add in-image smoke test provisioner"
```

### Task A5: packer qemu 템플릿

**Files:**
- Create: `infra/images/lab-base/lab-base.pkr.hcl`

**Interfaces:**
- Consumes: provisioners/*.sh.
- Produces: `output/lab-base.qcow2`(빌드 산출 디스크).

- [ ] **Step 1: HCL 작성**

```hcl
# Ubuntu 22.04 클라우드 이미지를 부팅해 도구를 베이킹하고 qcow2 를 산출한다.
# qemu 빌더는 KVM 호스트(EC2 *.metal)에서 실행된다. user-mode networking 으로
# 아웃바운드만 쓰므로(libguestfs appliance 와 달리) 정상 동작한다.
packer {
  required_plugins {
    qemu = {
      source  = "github.com/hashicorp/qemu"
      version = "~> 1.1"
    }
  }
}

variable "iso_url" {
  type    = string
  default = "https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img"
}

variable "iso_checksum" {
  type    = string
  default = "file:https://cloud-images.ubuntu.com/jammy/current/SHA256SUMS"
}

source "qemu" "lab_base" {
  iso_url          = var.iso_url
  iso_checksum     = var.iso_checksum
  disk_image       = true
  disk_size        = "10G"
  format           = "qcow2"
  accelerator      = "kvm"
  cpus             = 4
  memory           = 4096
  headless         = true
  ssh_username     = "ubuntu"
  ssh_password     = "ubuntu"
  ssh_timeout      = "10m"
  output_directory = "output"
  vm_name          = "lab-base.qcow2"
  # cloud-init seed(NoCloud) 로 ubuntu/ubuntu 로그인 가능하게 한다.
  cd_label = "cidata"
  cd_content = {
    "user-data" = <<-EOF
      #cloud-config
      password: ubuntu
      chpasswd: { expire: false }
      ssh_pwauth: true
      EOF
    "meta-data" = ""
  }
  shutdown_command = "sudo cloud-init clean --logs --seed && sudo shutdown -P now"
}

build {
  sources = ["source.qemu.lab_base"]

  provisioner "shell" {
    execute_command = "sudo -E bash '{{ .Path }}'"
    scripts = [
      "provisioners/00-common.sh",
      "provisioners/10-k3s.sh",
      "provisioners/20-docker.sh",
      "provisioners/30-code-server.sh",
      "provisioners/40-terraform.sh",
      "provisioners/50-ansible-helm.sh",
      "provisioners/99-smoke.sh",
    ]
  }
}
```

- [ ] **Step 2: 포맷/검증(로컬은 fmt·validate만; 실제 빌드는 metal에서)**

Run: `cd infra/images/lab-base && packer fmt -check lab-base.pkr.hcl && packer validate lab-base.pkr.hcl`
Expected: fmt 차이 없음 + `The configuration is valid.` (packer 미설치 시 이 검증은 Phase B의 metal에서 수행 — 그 경우 이 step은 metal 빌드 로그로 갈음).

- [ ] **Step 3: 커밋**

```bash
git add infra/images/lab-base/lab-base.pkr.hcl
git commit -m "build(infra): add packer qemu template for lab base image"
```

### Task A6: build-and-push.sh를 packer 산출물 소비로 전환 + 레거시 삭제

**Files:**
- Modify: `infra/images/lab-base/build-and-push.sh`
- Delete: `infra/images/lab-base/bake.sh`, `infra/images/lab-base/setup-host.sh`

**Interfaces:**
- Consumes: `lab-base.pkr.hcl`, `containerdisk.dockerfile`.
- Produces: ghcr push(`${IMAGE}:${TAG}` + `:latest`), 로컬 `disk/lab-base.qcow2`(containerDisk ADD 소스).

- [ ] **Step 1: build-and-push.sh 재작성**

```bash
#!/usr/bin/env bash
# packer-qemu 로 qcow2 를 굽고 → containerDisk(OCI) 로 빌드 → ghcr 에 푸시한다.
# metal baker(baker-bootstrap.sh)와 로컬 디버깅이 공용으로 호출한다. 사전에 ghcr 로그인이
# 돼 있어야 한다(docker login ghcr.io). packer/qemu/docker 가 설치돼 있어야 한다.
set -euo pipefail
cd "$(dirname "$0")"

IMAGE="${IMAGE:-ghcr.io/requset700k/cledyu-lab-base}"
TAG="${TAG:-$(date -u +%Y%m%d)}"

echo ">> packer build"
packer init lab-base.pkr.hcl
packer build lab-base.pkr.hcl

echo ">> stage qcow2 for containerDisk"
mkdir -p disk
mv output/lab-base.qcow2 disk/lab-base.qcow2
rm -rf output

echo ">> build containerDisk: ${IMAGE}:${TAG} (+latest)"
docker build -f containerdisk.dockerfile -t "${IMAGE}:${TAG}" -t "${IMAGE}:latest" .

echo ">> push"
docker push "${IMAGE}:${TAG}"
docker push "${IMAGE}:latest"

echo ">> done: ${IMAGE}:${TAG}"
```

- [ ] **Step 2: 레거시 삭제**

```bash
git rm infra/images/lab-base/bake.sh infra/images/lab-base/setup-host.sh
```

- [ ] **Step 3: shellcheck/shfmt + 커밋**

Run: `shellcheck infra/images/lab-base/build-and-push.sh && shfmt -i 2 -ci -sr -d infra/images/lab-base/build-and-push.sh`
Expected: 통과.

```bash
git add infra/images/lab-base/build-and-push.sh
git commit -m "build(infra): switch image build to packer qemu, drop libguestfs bake"
```

---

## Phase B — 클라우드 오케스트레이션 (deliverable: workflow_dispatch가 metal을 띄워 ghcr 태그+AMI 산출)

### Task B1: terraform — OIDC provider + GitHub Action role

**Files:**
- Create: `infra/terraform/aws/image-baker.tf`
- Modify: `infra/terraform/aws/variables.tf`

**Interfaces:**
- Produces: `aws_iam_role.gha_baker`(GH Action이 assume), 출력 `gha_baker_role_arn`.

- [ ] **Step 1: 변수 추가(variables.tf 끝에)**

```hcl
variable "github_repo" {
  description = "베이커 워크플로를 실행하는 GitHub 레포(owner/name). OIDC sub 제한에 사용."
  type        = string
  default     = "requset700k/cledyu"
}

variable "baker_metal_instance_type" {
  description = "이미지 베이커 metal 인스턴스 타입(중첩가상화 필요 → .metal). 빌드 중 spot 금지."
  type        = string
  default     = "m5.metal"
}
```

- [ ] **Step 2: image-baker.tf 작성(OIDC provider + role)**

```hcl
# GitHub Actions OIDC provider — 장기 키 없이 hosted runner 가 role 을 assume 한다.
resource "aws_iam_openid_connect_provider" "github" {
  url             = "https://token.actions.githubusercontent.com"
  client_id_list  = ["sts.amazonaws.com"]
  thumbprint_list = ["6938fd4d98bab03faadb97b34396831e3780aea1"]
}

data "aws_iam_policy_document" "gha_baker_assume" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]
    principals {
      type        = "Federated"
      identifiers = [aws_iam_openid_connect_provider.github.arn]
    }
    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:aud"
      values   = ["sts.amazonaws.com"]
    }
    condition {
      test     = "StringLike"
      variable = "token.actions.githubusercontent.com:sub"
      values   = ["repo:${var.github_repo}:*"]
    }
  }
}

resource "aws_iam_role" "gha_baker" {
  name               = "${var.name_prefix}-gha-baker"
  assume_role_policy = data.aws_iam_policy_document.gha_baker_assume.json
}

# GH Action 은 metal launch/terminate, instance profile 전달, sentinel 조회만 한다.
data "aws_iam_policy_document" "gha_baker" {
  statement {
    actions   = ["ec2:RunInstances", "ec2:CreateTags"]
    resources = ["*"]
  }
  statement {
    actions   = ["ec2:DescribeInstances", "ec2:DescribeImages"]
    resources = ["*"]
  }
  statement {
    actions   = ["ec2:TerminateInstances"]
    resources = ["*"]
    condition {
      test     = "StringEquals"
      variable = "ec2:ResourceTag/cledyu-role"
      values   = ["image-baker"]
    }
  }
  statement {
    actions   = ["iam:PassRole"]
    resources = [aws_iam_role.baker_instance.arn]
  }
  statement {
    actions   = ["s3:GetObject", "s3:ListBucket"]
    resources = [aws_s3_bucket.baker.arn, "${aws_s3_bucket.baker.arn}/*"]
  }
}

resource "aws_iam_role_policy" "gha_baker" {
  name   = "gha-baker"
  role   = aws_iam_role.gha_baker.id
  policy = data.aws_iam_policy_document.gha_baker.json
}
```

- [ ] **Step 3: terraform fmt/validate**

Run: `cd infra/terraform/aws && terraform fmt && terraform validate`
Expected: `Success! The configuration is valid.` (validate가 자격증명 없이 통과. provider init 필요 시 `terraform init -backend=false`).

- [ ] **Step 4: 커밋**

```bash
git add infra/terraform/aws/image-baker.tf infra/terraform/aws/variables.tf
git commit -m "feat(terraform): add github oidc role for image baker"
```

### Task B2: terraform — baker instance role + S3 bucket + vmimport role

**Files:**
- Modify: `infra/terraform/aws/image-baker.tf`
- Modify: `infra/terraform/aws/outputs.tf`

**Interfaces:**
- Produces: `aws_iam_instance_profile.baker_instance`, `aws_s3_bucket.baker`, `vmimport` 서비스 role. 출력 `baker_bucket`, `baker_instance_profile`, `gha_baker_role_arn`.

- [ ] **Step 1: image-baker.tf에 추가**

```hcl
resource "aws_s3_bucket" "baker" {
  bucket        = "${var.name_prefix}-image-baker"
  force_destroy = true
}

# metal 인스턴스 role — packer/import 산출물 S3 입출력, import-image, 자기 종료, SSM 파라미터(ghcr PAT) 읽기.
data "aws_iam_policy_document" "baker_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "baker_instance" {
  name               = "${var.name_prefix}-baker-instance"
  assume_role_policy = data.aws_iam_policy_document.baker_assume.json
}

data "aws_iam_policy_document" "baker_instance" {
  statement {
    actions   = ["s3:GetObject", "s3:PutObject", "s3:ListBucket"]
    resources = [aws_s3_bucket.baker.arn, "${aws_s3_bucket.baker.arn}/*"]
  }
  statement {
    actions = [
      "ec2:ImportImage", "ec2:DescribeImportImageTasks",
      "ec2:RegisterImage", "ec2:DescribeImages", "ec2:CreateTags",
    ]
    resources = ["*"]
  }
  statement {
    actions   = ["ec2:TerminateInstances"]
    resources = ["*"]
    condition {
      test     = "StringEquals"
      variable = "ec2:ResourceTag/cledyu-role"
      values   = ["image-baker"]
    }
  }
  statement {
    actions   = ["ssm:GetParameter"]
    resources = ["arn:aws:ssm:${var.region}:${data.aws_caller_identity.current.account_id}:parameter/cledyu/baker/*"]
  }
}

resource "aws_iam_role_policy" "baker_instance" {
  name   = "baker-instance"
  role   = aws_iam_role.baker_instance.id
  policy = data.aws_iam_policy_document.baker_instance.json
}

resource "aws_iam_instance_profile" "baker_instance" {
  name = "${var.name_prefix}-baker-instance"
  role = aws_iam_role.baker_instance.name
}

# VM Import/Export 서비스 role(import-image 가 S3 에서 디스크를 읽어 스냅샷 생성).
data "aws_iam_policy_document" "vmimport_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["vmie.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "sts:ExternalId"
      values   = ["vmimport"]
    }
  }
}

resource "aws_iam_role" "vmimport" {
  name               = "vmimport"
  assume_role_policy = data.aws_iam_policy_document.vmimport_assume.json
}

data "aws_iam_policy_document" "vmimport" {
  statement {
    actions   = ["s3:GetObject", "s3:ListBucket", "s3:GetBucketLocation"]
    resources = [aws_s3_bucket.baker.arn, "${aws_s3_bucket.baker.arn}/*"]
  }
  statement {
    actions = [
      "ec2:ModifySnapshotAttribute", "ec2:CopySnapshot",
      "ec2:RegisterImage", "ec2:Describe*",
    ]
    resources = ["*"]
  }
}

resource "aws_iam_role_policy" "vmimport" {
  name   = "vmimport"
  role   = aws_iam_role.vmimport.id
  policy = data.aws_iam_policy_document.vmimport.json
}
```

- [ ] **Step 2: outputs.tf에 추가**

```hcl
output "gha_baker_role_arn" {
  description = "GitHub Action 이 assume 할 베이커 role ARN."
  value       = aws_iam_role.gha_baker.arn
}

output "baker_bucket" {
  description = "이미지 베이커 S3 버킷명."
  value       = aws_s3_bucket.baker.bucket
}

output "baker_instance_profile" {
  description = "metal 베이커 인스턴스 프로파일명."
  value       = aws_iam_instance_profile.baker_instance.name
}
```

- [ ] **Step 3: terraform fmt/validate + 커밋**

Run: `cd infra/terraform/aws && terraform fmt && terraform validate`
Expected: valid.

```bash
git add infra/terraform/aws/image-baker.tf infra/terraform/aws/outputs.tf
git commit -m "feat(terraform): add baker instance profile s3 bucket and vmimport role"
```

### Task B3: metal baker bootstrap 스크립트

**Files:**
- Create: `infra/images/lab-base/baker-bootstrap.sh`

**Interfaces:**
- Consumes: 환경변수 `IMAGE`, `TAG`, `BAKER_BUCKET`, `IMPORT_AMI`, `GHCR_USER`, `REGION`; SSM 파라미터 `/cledyu/baker/ghcr_pat`.
- Produces: ghcr push, (옵션) AMI, S3 sentinel `s3://$BAKER_BUCKET/builds/$TAG/done.json`.

- [ ] **Step 1: 스크립트 작성**

```bash
#!/usr/bin/env bash
# metal user-data 가 실행하는 전체 베이크 오케스트레이션. 실패해도 마지막에 sentinel 과
# self-terminate 를 보장한다(orphan metal 방지). 산출물: ghcr 태그 + (옵션)AMI.
set -uo pipefail

REGION="${REGION:-ap-northeast-2}"
IMAGE="${IMAGE:-ghcr.io/requset700k/cledyu-lab-base}"
TAG="${TAG:?TAG required}"
BAKER_BUCKET="${BAKER_BUCKET:?BAKER_BUCKET required}"
IMPORT_AMI="${IMPORT_AMI:-true}"
GHCR_USER="${GHCR_USER:-ykgoesdumb}"

STATUS="failed"
AMI_ID=""
WORK=/opt/cledyu
log() { echo "[baker] $*"; }

finish() {
  printf '{"status":"%s","tag":"%s","ami":"%s"}\n' "$STATUS" "$TAG" "$AMI_ID" >/tmp/done.json
  aws s3 cp /tmp/done.json "s3://$BAKER_BUCKET/builds/$TAG/done.json" --region "$REGION" || true
  TOKEN=$(curl -sX PUT "http://169.254.169.254/latest/api/token" \
    -H "X-aws-ec2-metadata-token-ttl-seconds: 60")
  IID=$(curl -s -H "X-aws-ec2-metadata-token: $TOKEN" \
    http://169.254.169.254/latest/meta-data/instance-id)
  aws ec2 terminate-instances --instance-ids "$IID" --region "$REGION" || true
}
trap finish EXIT

# 도구 설치(metal 은 Amazon Linux 2023 가정 — packer/qemu/docker/awscli).
dnf install -y git docker qemu-kvm awscli unzip || yum install -y git docker qemu-kvm awscli unzip
systemctl enable --now docker
curl -fsSL -o /tmp/packer.zip \
  https://releases.hashicorp.com/packer/1.11.2/packer_1.11.2_linux_amd64.zip
unzip -o /tmp/packer.zip -d /usr/local/bin

git clone --depth 1 https://github.com/requset700k/cledyu.git "$WORK"
cd "$WORK/infra/images/lab-base"

# ghcr 로그인(PAT 는 SSM SecureString).
GHCR_PAT=$(aws ssm get-parameter --name /cledyu/baker/ghcr_pat --with-decryption \
  --region "$REGION" --query Parameter.Value --output text)
echo "$GHCR_PAT" | docker login ghcr.io -u "$GHCR_USER" --password-stdin

# 빌드 + ghcr push(온프렘 레그).
IMAGE="$IMAGE" TAG="$TAG" bash build-and-push.sh

# AMI 레그(옵션).
if [ "$IMPORT_AMI" = "true" ]; then
  log "convert qcow2 -> raw and upload"
  qemu-img convert -O raw disk/lab-base.qcow2 /tmp/lab-base.raw
  aws s3 cp /tmp/lab-base.raw "s3://$BAKER_BUCKET/import/$TAG/lab-base.raw" --region "$REGION"
  cat >/tmp/containers.json <<JSON
[{"Description":"cledyu-lab-base","Format":"raw","UserBucket":{"S3Bucket":"$BAKER_BUCKET","S3Key":"import/$TAG/lab-base.raw"}}]
JSON
  TASK=$(aws ec2 import-image --disk-containers file:///tmp/containers.json \
    --region "$REGION" --query ImportTaskId --output text)
  log "import task $TASK"
  for i in $(seq 1 60); do
    ST=$(aws ec2 describe-import-image-tasks --import-task-ids "$TASK" \
      --region "$REGION" --query 'ImportImageTasks[0].Status' --output text)
    [ "$ST" = "completed" ] && break
    [ "$ST" = "deleted" ] && { log "import failed"; exit 1; }
    sleep 30
  done
  AMI_ID=$(aws ec2 describe-import-image-tasks --import-task-ids "$TASK" \
    --region "$REGION" --query 'ImportImageTasks[0].ImageId' --output text)
  aws ec2 create-tags --resources "$AMI_ID" --region "$REGION" \
    --tags "Key=Name,Value=cledyu-lab-base-$TAG" "Key=cledyu-role,Value=lab-session-ami"
  log "AMI $AMI_ID"
fi

STATUS="ok"
```

- [ ] **Step 2: shellcheck/shfmt 검증**

Run: `shellcheck infra/images/lab-base/baker-bootstrap.sh && shfmt -i 2 -ci -sr -d infra/images/lab-base/baker-bootstrap.sh`
Expected: 통과(불가피한 경고는 `# shellcheck disable`로 명시).

- [ ] **Step 3: 실행권한 + 커밋**

```bash
chmod +x infra/images/lab-base/baker-bootstrap.sh
git add infra/images/lab-base/baker-bootstrap.sh
git commit -m "build(infra): add metal baker bootstrap orchestration"
```

### Task B4: GitHub workflow 개편(hosted + OIDC + metal launch/poll)

**Files:**
- Modify: `.github/workflows/build-lab-base-image.yml`

**Interfaces:**
- Consumes: terraform 출력(`gha_baker_role_arn`, `baker_bucket`, `baker_instance_profile`)을 워크플로 입력/변수로.
- Produces: ghcr 태그 + AMI id(job summary).

- [ ] **Step 1: 워크플로 재작성**

```yaml
---
# 세션 VM 베이스 이미지 베이킹 — hosted runner 가 AWS OIDC 로 인증해 일회성 EC2 *.metal 을
# 띄우고, metal 이 packer-qemu 로 qcow2 를 구워 ghcr push + (옵션)AMI import 후 self-terminate.
# self-hosted KVM 러너 불필요. workflow_dispatch 전용(public repo fork-PR RCE 방지).
name: build lab base image

on:
  workflow_dispatch:
    inputs:
      tag:
        description: "이미지 태그(미입력 시 날짜)"
        required: false
        default: ""
      import_ami:
        description: "EC2 AMI 도 함께 import"
        type: boolean
        default: true

permissions:
  id-token: write
  contents: read

env:
  AWS_REGION: ap-northeast-2
  BAKER_BUCKET: cledyu-lab-image-baker
  BAKER_INSTANCE_PROFILE: cledyu-lab-baker-instance
  BAKER_ROLE_ARN: arn:aws:iam::504284203153:role/cledyu-lab-gha-baker
  METAL_TYPE: m5.metal
  UBUNTU_AL2023_SSM: /aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-x86_64

concurrency:
  group: build-lab-base-image
  cancel-in-progress: true

jobs:
  bake:
    name: launch metal baker
    runs-on: ubuntu-latest
    steps:
      - name: Resolve tag
        id: t
        run: echo "tag=${{ inputs.tag != '' && inputs.tag || format('{0}', github.run_id) }}" >> "$GITHUB_OUTPUT"

      - name: Configure AWS credentials (OIDC)
        uses: aws-actions/configure-aws-credentials@v4
        with:
          role-to-assume: ${{ env.BAKER_ROLE_ARN }}
          aws-region: ${{ env.AWS_REGION }}

      - name: Resolve metal AMI (Amazon Linux 2023)
        id: ami
        run: |
          AMI=$(aws ssm get-parameter --name "$UBUNTU_AL2023_SSM" \
            --query Parameter.Value --output text)
          echo "ami=$AMI" >> "$GITHUB_OUTPUT"

      - name: Launch metal baker
        id: launch
        run: |
          TAG="${{ steps.t.outputs.tag }}"
          USERDATA=$(cat <<EOF
          #!/usr/bin/env bash
          export REGION=$AWS_REGION TAG=$TAG BAKER_BUCKET=$BAKER_BUCKET IMPORT_AMI=${{ inputs.import_ami }}
          curl -fsSL https://raw.githubusercontent.com/requset700k/cledyu/main/infra/images/lab-base/baker-bootstrap.sh -o /tmp/b.sh
          bash /tmp/b.sh
          EOF
          )
          IID=$(aws ec2 run-instances --image-id "${{ steps.ami.outputs.ami }}" \
            --instance-type "$METAL_TYPE" \
            --iam-instance-profile "Name=$BAKER_INSTANCE_PROFILE" \
            --user-data "$USERDATA" \
            --tag-specifications 'ResourceType=instance,Tags=[{Key=cledyu-role,Value=image-baker}]' \
            --instance-initiated-shutdown-behavior terminate \
            --query 'Instances[0].InstanceId' --output text)
          echo "iid=$IID" >> "$GITHUB_OUTPUT"
          echo "launched $IID"

      - name: Poll for completion sentinel
        run: |
          TAG="${{ steps.t.outputs.tag }}"
          for i in $(seq 1 50); do
            if aws s3 cp "s3://$BAKER_BUCKET/builds/$TAG/done.json" /tmp/done.json 2>/dev/null; then
              cat /tmp/done.json >> "$GITHUB_STEP_SUMMARY"
              grep -q '"status":"ok"' /tmp/done.json && exit 0
              echo "baker reported failure" && exit 1
            fi
            sleep 30
          done
          echo "timeout waiting for baker" >&2
          aws ec2 terminate-instances --instance-ids "${{ steps.launch.outputs.iid }}" || true
          exit 1
```

- [ ] **Step 2: actionlint/yamllint 검증**

Run: `yamllint .github/workflows/build-lab-base-image.yml`
Expected: 신규 error 없음(document-start/line-length 경고만 허용).

- [ ] **Step 3: 커밋**

```bash
git add .github/workflows/build-lab-base-image.yml
git commit -m "ci: bake lab base image via oidc and transient ec2 metal"
```

### Task B5: 수동 사전 준비 + 첫 베이크 실행(사용자 협조 필요)

**Files:** 없음(운영 단계).

- [ ] **Step 1: SSM 파라미터에 ghcr PAT 등록**

Run: `aws ssm put-parameter --profile cledyu --region ap-northeast-2 --name /cledyu/baker/ghcr_pat --type SecureString --value '<write:packages PAT>'`
Expected: `Version: 1`.

- [ ] **Step 2: terraform apply(baker 리소스)**

Run: `cd infra/terraform/aws && terraform plan -out tfplan && terraform apply tfplan`
Expected: OIDC provider·role·S3·instance-profile·vmimport 생성. 출력값 확인(`terraform output gha_baker_role_arn` 등). 워크플로 env의 ARN/버킷명이 출력과 일치하는지 대조(다르면 워크플로 env 수정 후 재커밋).

- [ ] **Step 3: 워크플로 실행 + 결과 확인**

Run: `gh workflow run build-lab-base-image.yml -f import_ami=true && sleep 20 && gh run watch`
Expected: job summary에 `{"status":"ok","tag":"...","ami":"ami-..."}`. ghcr에 `cledyu-lab-base:<tag>` 존재(`gh api /orgs/requset700k/packages/container/cledyu-lab-base/versions` 또는 패키지 페이지). 실패 시 baker 인스턴스 콘솔로그/SSM session으로 디버그.

---

## Phase C — 롤아웃 & 콘텐츠 (deliverable: 세션이 양 provider에서 빠르게 부팅)

### Task C1: 랩 콘텐츠 install→start (KubeVirt+EC2 공통)

**Files:**
- Modify: `apps/api/internal/content/labs/lab-k8s-basics.yaml`
- Modify: `apps/api/internal/content/labs/lab-helm-advanced.yaml`
- Modify: `apps/api/internal/content/labs/lab-terraform-basics.yaml`
- Modify: `apps/api/internal/content/labs/lab-docker-basics.yaml`
- Modify: `apps/api/internal/content/labs/lab-ansible-basics.yaml`
- Test: `apps/api/internal/content/loader_test.go`(기존, 변경 없음)

**Interfaces:**
- Consumes: 베이크 베이스(k3s·docker·code-server·terraform·ansible·helm 설치됨, nginx 캐시됨, 서비스 disabled).
- Produces: 각 랩의 `init.runcmd`가 기동/설정만 수행.

- [ ] **Step 1: lab-k8s-basics.yaml의 runcmd 교체**

`init.runcmd` 전체를 아래로 교체(k3s는 베이크됨, kubeconfig 환경변수만 추가하고 기동):

```yaml
  runcmd:
    # k3s 는 베이스 이미지에 설치돼 있고 nginx 이미지도 캐시돼 있다(서비스만 기동).
    - systemctl enable --now k3s
    - echo KUBECONFIG=/etc/rancher/k3s/k3s.yaml >> /etc/environment
    - "printf 'export KUBECONFIG=/etc/rancher/k3s/k3s.yaml\\n' > /etc/profile.d/k3s-kubeconfig.sh"
```

- [ ] **Step 2: lab-helm-advanced.yaml의 runcmd 교체**

```yaml
  runcmd:
    # k3s·helm 은 베이스에 설치됨. k3s 기동 + kubeconfig 환경변수만.
    - systemctl enable --now k3s
    - echo KUBECONFIG=/etc/rancher/k3s/k3s.yaml >> /etc/environment
    - "printf 'export KUBECONFIG=/etc/rancher/k3s/k3s.yaml\\n' > /etc/profile.d/k3s-kubeconfig.sh"
```

- [ ] **Step 3: lab-terraform-basics.yaml의 runcmd 교체**

terraform·code-server는 베이크됨 → 설치 명령 제거, code-server 설정/기동만:

```yaml
  runcmd:
    # terraform·code-server 는 베이스에 설치됨. code-server 설정 후 기동만 한다.
    - mkdir -p /home/lab/workspace /home/lab/.config/code-server
    - "printf 'bind-addr: 0.0.0.0:13337\\nauth: none\\ncert: false\\n' > /home/lab/.config/code-server/config.yaml"
    - chown -R lab:lab /home/lab/workspace /home/lab/.config
    - systemctl enable --now code-server@lab
```

- [ ] **Step 4: lab-docker-basics.yaml의 runcmd 교체**

docker.io는 베이크됨 → apt 제거, 기동 + 그룹/소켓만:

```yaml
  runcmd:
    # docker 는 베이스에 설치됨(서비스 disabled). 기동 후 lab 그룹/소켓 설정.
    - systemctl enable --now docker
    - usermod -aG docker lab
    - chmod 666 /var/run/docker.sock
```

`init.packages` 블록(docker.io)도 제거(베이크됨).

- [ ] **Step 5: lab-ansible-basics.yaml의 packages 제거**

ansible-core가 베이크됐으므로 `init.packages` 블록 제거. runcmd가 없으면 `init`에 빈 필드가 남지 않도록 정리(스키마상 init 자체가 옵셔널이면 init 블록 삭제, 아니면 주석만).

- [ ] **Step 6: 검증(content loader + yamllint)**

Run: `cd apps/api && go test ./internal/content/... && cd ../.. && yamllint apps/api/internal/content/labs/`
Expected: content 테스트 ok, yamllint 신규 error 없음.

- [ ] **Step 7: 커밋**

```bash
git add apps/api/internal/content/labs/
git commit -m "perf(api): switch lab runcmd from install to start using baked base"
```

### Task C2: gitops base-datavolume 태그 bump + EC2 launch template AMI 반영

**Files:**
- Modify: `gitops/apps/kubevirt/base-datavolume.yaml`
- Modify: `infra/terraform/aws/terraform.tfvars`(또는 `variables.tf`의 `ami_id` 경로)

**Interfaces:**
- Consumes: Task B5 산출(ghcr `<tag>`, `ami-...`).
- Produces: 온프렘 base PVC 재import + EC2 신규 세션이 새 AMI 사용.

- [ ] **Step 1: base-datavolume.yaml registry url 태그 bump**

`registry.url`을 B5의 새 태그로 변경:

```yaml
    registry:
      url: "docker://ghcr.io/requset700k/cledyu-lab-base:<NEW_TAG>"
```

- [ ] **Step 2: EC2 ami_id 설정**

`infra/terraform/aws/terraform.tfvars`에 B5의 AMI id 설정:

```hcl
ami_id = "ami-<NEW>"
```

(launch template은 PR #177의 `update_default_version=true`로 새 버전을 기본값으로 승격. 미적용 상태면 main.tf의 `aws_launch_template.lab_session`에 `update_default_version = true` 추가.)

- [ ] **Step 3: 검증(yamllint + terraform plan)**

Run: `yamllint gitops/apps/kubevirt/base-datavolume.yaml && cd infra/terraform/aws && terraform plan`
Expected: yamllint 통과. plan에 launch template 새 버전 1건만(파괴적 변경 없음 — public-ingress 리소스 destroy가 보이면 tfvars의 enable_public_ingress 확인).

- [ ] **Step 4: 커밋**

```bash
git add gitops/apps/kubevirt/base-datavolume.yaml infra/terraform/aws/terraform.tfvars
git commit -m "gitops: pin lab base image to baked tag and update ec2 ami"
```

---

## Phase D — E2E 검증 (deliverable: 실측으로 install→start 효과 확인)

### Task D1: 온프렘 e2e — 베이크 베이스로 heavy 랩 부팅 실측

**Files:** 없음(검증).

- [ ] **Step 1: base PVC 재import 확인**

ArgoCD가 base-datavolume 변경을 sync → Force=true가 DataVolume 재생성:

Run: `kubectl get datavolume ubuntu-2204-base -n kubevirt -o jsonpath='{.status.phase}'; echo`
Expected: 최종 `Succeeded`(import 진행 중이면 ImportInProgress→Succeeded 폴링).

- [ ] **Step 2: 측정 VM으로 k8s-basics runcmd 실측**

2026-06-28 harness 재사용(measure-vm.yaml의 runcmd를 C1의 새 k8s-basics runcmd로 교체, base는 새 태그). 부팅 후:

Run: `virtctl ssh --username lab vmi/measure-vm -n measure-prov --identity-file=<key> -c "sudo cloud-init analyze blame | head -5; sudo k3s kubectl get nodes"`
Expected: `config-scripts_user`(runcmd) < 5s, k3s 노드 Ready. 측정 후 `kubectl delete ns measure-prov`.

### Task D2: EC2 e2e — 새 AMI 오버플로우 세션

**Files:** 없음(검증).

- [ ] **Step 1: 오버플로우 세션 1건 강제 생성 후 동작 확인**

기존 EC2 e2e 경로 사용(api가 새 LT 기본 버전 = 새 AMI 사용). tailnet join + SSM grading + 라이브 터미널 확인:

Run: (메모리 project_ec2_overflow_e2e의 절차) 인스턴스 부팅 → `aws ssm send-command`로 grading 스모크 → tailnet hostname SSH 터미널.
Expected: 세션 runcmd가 install 없이 빠르게 ready, grading·터미널 정상.

- [ ] **Step 2: 정리**

Run: 테스트 인스턴스 종료 확인(`aws ec2 describe-instances --filters Name=tag:cledyu-role,Values=lab-session`).
Expected: 잔여 인스턴스 없음.

---

## Self-Review

**Spec coverage:**
- 1 배경/문제 → Phase A(빌드 대체), C(install→start). OK.
- 2 목표 → A(클라우드 빌드·단일소스), B(self-hosted 제거), C(install→start). OK.
- 3 아키텍처 → A5(packer), B3/B4(metal baker·workflow). OK.
- 4 구성요소 → A1–A6, B1–B4. OK.
- 5 빌드 흐름 → B3(bootstrap), B4(launch/poll). OK.
- 6 콘텐츠 → C1. OK.
- 7 배달 → C2(gitops bump·AMI). OK.
- 8 트리거/보안 → B1(OIDC), B2(IAM 최소권한·vmimport), B4(workflow_dispatch), B5(SSM PAT). PAT revoke는 B5 이후 운영 정리로 남김.
- 9 비용 → Global Constraints/Task B 주석에 metal 온디맨드. OK.
- 10 DR → 설계 규율(import_ami 분리)이 B3/B4에 반영. OK.
- 11 검증 → A4(smoke), D1/D2(e2e), C1 step6(loader/yamllint). OK.
- 12 측정근거 → D1이 harness 재사용. OK.

**Placeholder scan:** `<NEW_TAG>`/`<NEW>`/`<key>`/`<write:packages PAT>`는 런타임 산출값(빌드가 만든 태그·AMI·로컬 키 경로·시크릿)으로, 플레이스홀더가 아니라 실행 시점 주입값임. 그 외 TBD/TODO 없음.

**Type consistency:** baker 환경변수(IMAGE/TAG/BAKER_BUCKET/IMPORT_AMI/GHCR_USER/REGION)가 B3 스크립트·B4 워크플로·B5 명령에서 일관. terraform 출력명(gha_baker_role_arn/baker_bucket/baker_instance_profile)이 B2 outputs·B4 env에서 일관. name_prefix 기본값 `request700k`(레포 네이밍 규칙) 가정 — B5 step2에서 실제 출력과 대조하도록 명시함.

**알려진 주의:** metal AMI는 Amazon Linux 2023(dnf) 가정 — baker-bootstrap이 dnf/yum 폴백 처리. m5.metal은 amd64 — arm64(Graviton) DR은 별도 빌드 매트릭스(스펙 10절).
