data "aws_caller_identity" "current" {}

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
  statement {
    # AWS 공개 파라미터(빈 account 세그먼트)에서 metal 베이스 AMI 를 조회한다.
    # Ubuntu(canonical/...) 와 Amazon Linux(ami-amazon-linux-latest/...) 모두 /aws/service/* 아래에 있다.
    actions   = ["ssm:GetParameter"]
    resources = ["arn:aws:ssm:${var.region}::parameter/aws/service/*"]
  }
}

resource "aws_iam_role_policy" "gha_baker" {
  name   = "gha-baker"
  role   = aws_iam_role.gha_baker.id
  policy = data.aws_iam_policy_document.gha_baker.json
}

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
    actions   = ["s3:GetObject", "s3:PutObject", "s3:ListBucket", "s3:GetBucketLocation"]
    resources = [aws_s3_bucket.baker.arn, "${aws_s3_bucket.baker.arn}/*"]
  }
  statement {
    actions = [
      "ec2:ImportImage", "ec2:DescribeImportImageTasks",
      "ec2:CreateTags",
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
  statement {
    actions   = ["iam:PassRole"]
    resources = [aws_iam_role.vmimport.arn]
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
