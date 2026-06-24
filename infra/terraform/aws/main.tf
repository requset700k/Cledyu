# Cledyu Lab EC2 오버플로우 인프라.
# 온프렘 KubeVirt 풀이 가득 찼을 때 학습 세션을 버스트할 EC2 베이스 리소스를 만든다:
#   - 네트워크: 인바운드 0(SSM·Tailscale 아웃바운드만), egress 허용
#   - IAM: 인스턴스 프로파일(SSM Core) + api/검증엔진용 최소권한 정책
#   - Launch Template: api(internal/ec2)가 RunInstances 에서 참조하는 베이스 템플릿
#
# 세션 인스턴스 자체는 terraform 이 만들지 않는다 — apps/api 가 세션마다 동적으로 띄우고 회수한다.

data "aws_vpc" "selected" {
  id      = var.vpc_id != "" ? var.vpc_id : null
  default = var.vpc_id == "" ? true : null
}

data "aws_subnets" "selected" {
  filter {
    name   = "vpc-id"
    values = [data.aws_vpc.selected.id]
  }
}

# Canonical Ubuntu 22.04 LTS (amd64) — ami_id 미지정 시 폴백.
data "aws_ami" "ubuntu" {
  count       = var.ami_id == "" ? 1 : 0
  most_recent = true
  owners      = ["099720109477"] # Canonical

  filter {
    name   = "name"
    values = ["ubuntu/images/hvm-ssd/ubuntu-jammy-22.04-amd64-server-*"]
  }
  filter {
    name   = "virtualization-type"
    values = ["hvm"]
  }
}

locals {
  ami_id    = var.ami_id != "" ? var.ami_id : data.aws_ami.ubuntu[0].id
  subnet_id = var.subnet_id != "" ? var.subnet_id : data.aws_subnets.selected.ids[0]
}

# --- 네트워크: 인바운드 없음 ---
# 채점(SSM)·라이브 터미널(Tailscale) 모두 아웃바운드만 쓰므로 인그레스 규칙을 두지 않는다.
resource "aws_security_group" "lab_session" {
  name_prefix = "${var.name_prefix}-session-"
  description = "Cledyu lab session instances - egress only (SSM + Tailscale)"
  vpc_id      = data.aws_vpc.selected.id

  egress {
    description = "All outbound (SSM HTTPS, Tailscale, apt)"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = {
    Name = "${var.name_prefix}-session"
  }

  lifecycle {
    create_before_destroy = true
  }
}

# --- IAM: 인스턴스 프로파일(SSM) ---
data "aws_iam_policy_document" "ec2_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "lab_instance" {
  name_prefix        = "${var.name_prefix}-instance-"
  assume_role_policy = data.aws_iam_policy_document.ec2_assume.json
}

# 세션 인스턴스가 SSM 으로 채점 명령을 받을 수 있게 한다(SSH 키 불필요).
resource "aws_iam_role_policy_attachment" "ssm_core" {
  role       = aws_iam_role.lab_instance.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

resource "aws_iam_instance_profile" "lab_instance" {
  name_prefix = "${var.name_prefix}-instance-"
  role        = aws_iam_role.lab_instance.name
}

# --- IAM: apps/api 용 최소권한 정책(EC2 세션 수명주기) ---
# 액세스 키 자체는 terraform 으로 만들지 않는다(시크릿이 state 에 남는 것을 피한다).
# 운영자가 콘솔/CLI 로 이 사용자의 키를 발급해 Vault 에 보관하고 External Secrets 로 주입한다(README).
data "aws_iam_policy_document" "api_ec2" {
  statement {
    sid       = "RunSessionInstances"
    actions   = ["ec2:RunInstances"]
    resources = ["*"]
  }
  statement {
    sid       = "ManageSessionInstances"
    actions   = ["ec2:TerminateInstances", "ec2:CreateTags", "ec2:DescribeInstances", "ec2:DescribeInstanceStatus"]
    resources = ["*"]
  }
  statement {
    # RunInstances 가 인스턴스 프로파일을 부여하려면 PassRole 이 필요하다(SSM Core 역할 한정).
    sid       = "PassInstanceProfileRole"
    actions   = ["iam:PassRole"]
    resources = [aws_iam_role.lab_instance.arn]
    condition {
      test     = "StringEquals"
      variable = "iam:PassedToService"
      values   = ["ec2.amazonaws.com"]
    }
  }
}

resource "aws_iam_user" "api" {
  name = "${var.name_prefix}-api"
}

resource "aws_iam_user_policy" "api_ec2" {
  name   = "ec2-session-lifecycle"
  user   = aws_iam_user.api.name
  policy = data.aws_iam_policy_document.api_ec2.json
}

# --- IAM: validation-engine 용 최소권한 정책(SSM 채점) ---
data "aws_iam_policy_document" "engine_ssm" {
  statement {
    sid       = "RunShellOnSessionInstances"
    actions   = ["ssm:SendCommand", "ssm:GetCommandInvocation", "ssm:ListCommandInvocations"]
    resources = ["*"]
  }
}

resource "aws_iam_user" "engine" {
  name = "${var.name_prefix}-validation-engine"
}

resource "aws_iam_user_policy" "engine_ssm" {
  name   = "ssm-grading"
  user   = aws_iam_user.engine.name
  policy = data.aws_iam_policy_document.engine_ssm.json
}

# --- Launch Template ---
# apps/api(internal/ec2)가 이 템플릿 ID 로 RunInstances 하고, 세션별 cloud-init user-data 를 덮어쓴다.
# 따라서 여기 user_data 는 직접 부팅(수동 테스트) 폴백용 베이스다 — 운영 세션은 api 가 주입한다.
resource "aws_launch_template" "lab_session" {
  name_prefix   = "${var.name_prefix}-session-"
  image_id      = local.ami_id
  instance_type = var.instance_type

  iam_instance_profile {
    arn = aws_iam_instance_profile.lab_instance.arn
  }

  # 세션 인스턴스를 선택된 subnet 에 띄우고 egress-only SG 를 붙인다. network_interfaces 로
  # subnet 을 지정하면 top-level vpc_security_group_ids 와 공존할 수 없어 SG 도 여기로 옮긴다.
  network_interfaces {
    subnet_id       = local.subnet_id
    security_groups = [aws_security_group.lab_session.id]
    # network_interfaces 명시 시 subnet 의 MapPublicIpOnLaunch 가 무시되므로 여기서 명시한다.
    # 미설정이면 퍼블릭 IP 미할당 → 인터넷 egress 불가(tailscale·SSM·설치 실패).
    associate_public_ip_address = var.assign_public_ip
  }

  # IMDSv2 강제(SSRF 완화).
  metadata_options {
    http_tokens   = "required"
    http_endpoint = "enabled"
  }

  block_device_mappings {
    device_name = "/dev/sda1"
    ebs {
      volume_size           = var.root_volume_gb
      volume_type           = "gp3"
      delete_on_termination = true
      encrypted             = true
    }
  }

  user_data = base64encode(templatefile("${path.module}/cloud-init/user-data.yaml.tftpl", {}))

  tag_specifications {
    resource_type = "instance"
    tags = {
      Name = "${var.name_prefix}-session-base"
    }
  }

  lifecycle {
    create_before_destroy = true
  }
}
