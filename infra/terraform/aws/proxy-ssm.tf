# 프록시에 SSM Session Manager 접근을 부여한다. 이번 인시던트에서 SSH 키도
# SSM 역할도 없어 셸 진입이 전혀 안 됐던 문제를 해소한다(계정 default host
# management 설정과 무관하게 인스턴스 프로파일로 동작).
data "aws_iam_policy_document" "proxy_assume" {
  count = local.pub
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "proxy_ssm" {
  count              = local.pub
  name_prefix        = "${var.name_prefix}-kc-proxy-ssm-"
  assume_role_policy = data.aws_iam_policy_document.proxy_assume[0].json
  tags               = { Name = "${var.name_prefix}-kc-proxy-ssm" }
}

resource "aws_iam_role_policy_attachment" "proxy_ssm_core" {
  count      = local.pub
  role       = aws_iam_role.proxy_ssm[0].name
  policy_arn = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

resource "aws_iam_instance_profile" "proxy_ssm" {
  count       = local.pub
  name_prefix = "${var.name_prefix}-kc-proxy-ssm-"
  role        = aws_iam_role.proxy_ssm[0].name
}
