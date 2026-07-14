# ── DR 재해 알림: SNS → Lambda → Discord (us-east-1) ──
data "archive_file" "dr_alert" {
  type        = "zip"
  source_file = "${path.module}/dr-alert-lambda/index.py"
  output_path = "${path.module}/dr-alert-lambda/dr-alert.zip"
}

# Discord 웹훅 URL. 값은 TF 밖에서 넣는다(평문 state 회피):
#   aws secretsmanager put-secret-value --region us-east-1 \
#     --secret-id cledyu-lab-dr-discord-webhook \
#     --secret-string '{"url":"https://discord.com/api/webhooks/XXX/YYY"}'
resource "aws_secretsmanager_secret" "discord_webhook" {
  provider = aws.use1
  name     = "${var.name_prefix}-dr-discord-webhook"
}

data "aws_iam_policy_document" "dr_alert_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "dr_alert" {
  name               = "${var.name_prefix}-dr-alert-lambda"
  assume_role_policy = data.aws_iam_policy_document.dr_alert_assume.json
}

data "aws_iam_policy_document" "dr_alert" {
  statement {
    sid       = "ReadWebhook"
    actions   = ["secretsmanager:GetSecretValue"]
    resources = [aws_secretsmanager_secret.discord_webhook.arn]
  }
  statement {
    sid     = "Logs"
    actions = ["logs:CreateLogGroup", "logs:CreateLogStream", "logs:PutLogEvents"]
    # 이 함수 전용 로그그룹으로 한정(계정 전체 로그 대신). CreateLogGroup=그룹 ARN,
    # CreateLogStream/PutLogEvents=스트림(:*) ARN 둘 다 커버.
    resources = [
      "arn:aws:logs:us-east-1:*:log-group:/aws/lambda/${var.name_prefix}-dr-alert",
      "arn:aws:logs:us-east-1:*:log-group:/aws/lambda/${var.name_prefix}-dr-alert:*",
    ]
  }
}

resource "aws_iam_role_policy" "dr_alert" {
  name   = "${var.name_prefix}-dr-alert-lambda"
  role   = aws_iam_role.dr_alert.id
  policy = data.aws_iam_policy_document.dr_alert.json
}

resource "aws_lambda_function" "dr_alert" {
  provider         = aws.use1
  function_name    = "${var.name_prefix}-dr-alert"
  filename         = data.archive_file.dr_alert.output_path
  source_code_hash = data.archive_file.dr_alert.output_base64sha256
  handler          = "index.handler"
  runtime          = "python3.12"
  role             = aws_iam_role.dr_alert.arn
  timeout          = 15
  environment {
    variables = {
      WEBHOOK_SECRET_ARN = aws_secretsmanager_secret.discord_webhook.arn
    }
  }
}

resource "aws_lambda_permission" "sns" {
  provider      = aws.use1
  statement_id  = "AllowSNSInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.dr_alert.function_name
  principal     = "sns.amazonaws.com"
  source_arn    = aws_sns_topic.dr_alert.arn
}

resource "aws_sns_topic_subscription" "dr_alert" {
  provider  = aws.use1
  topic_arn = aws_sns_topic.dr_alert.arn
  protocol  = "lambda"
  endpoint  = aws_lambda_function.dr_alert.arn
}
