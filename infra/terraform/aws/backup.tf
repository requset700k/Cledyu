# DR/백업 오프사이트 저장소 — 온프렘 durable 데이터(Postgres WAL/base, Vault raft 스냅샷,
# Longhorn 볼륨 백업)를 S3 로 내보낸다. 온프렘이 죽어도 여기 사본이 남아 EKS DR 로 복원한다.
# 자격증명은 정적 IAM 키 → Vault → ESO(온프렘 클러스터라 IRSA 불가).

resource "aws_s3_bucket" "dr_backups" {
  bucket = "${var.name_prefix}-dr-backups"
}

resource "aws_s3_bucket_versioning" "dr_backups" {
  bucket = aws_s3_bucket.dr_backups.id
  versioning_configuration {
    status = "Enabled"
  }
}

# 백업엔 DB 전체(WAL)와 Vault 시크릿(raft 스냅샷)이 들어간다 — 퍼블릭 노출을 전면 차단한다.
resource "aws_s3_bucket_public_access_block" "dr_backups" {
  bucket                  = aws_s3_bucket.dr_backups.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

# 저장 시 암호화(SSE-S3). 시크릿이 평문으로 S3 에 눕지 않도록 명시한다.
resource "aws_s3_bucket_server_side_encryption_configuration" "dr_backups" {
  bucket = aws_s3_bucket.dr_backups.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

# 수명주기 — 프리픽스별로 보관 책임이 다르다.
#  postgres/ : 실제 retention 은 CNPG barman(retentionPolicy)이 관리(고유 키 WAL 삭제). 여기 규칙은
#              versioning 잔재(non-current) 정리 backstop 일 뿐이다.
#  vault/    : 6시간마다 고유 키 .snap 이 쌓이며 삭제 주체가 없다 → 여기서 현재 버전까지 만료시킨다.
#  longhorn/ : Longhorn RecurringJob(retain)이 자체 삭제하므로 별도 규칙을 두지 않는다.
resource "aws_s3_bucket_lifecycle_configuration" "dr_backups" {
  bucket = aws_s3_bucket.dr_backups.id

  rule {
    id     = "backstop-noncurrent-postgres"
    status = "Enabled"
    filter {
      prefix = "postgres/"
    }
    noncurrent_version_expiration {
      noncurrent_days = 30
    }
  }

  rule {
    id     = "expire-vault-snapshots"
    status = "Enabled"
    filter {
      prefix = "vault/"
    }
    expiration {
      days = 90
    }
    noncurrent_version_expiration {
      noncurrent_days = 7
    }
  }
}

# 백업 전용 쓰기 IAM 사용자 — 액세스 키는 apply 후 콘솔/CLI 로 수동 발급해 Vault(cledyu/aws/backup)에
# 보관하고 ESO 가 클러스터로 뿌린다.
resource "aws_iam_user" "backup" {
  name = "${var.name_prefix}-backup-writer"
}

data "aws_iam_policy_document" "backup" {
  statement {
    actions = [
      "s3:PutObject",
      "s3:GetObject",
      "s3:DeleteObject",
      "s3:ListBucket",
      "s3:GetBucketLocation",
    ]
    resources = [
      aws_s3_bucket.dr_backups.arn,
      "${aws_s3_bucket.dr_backups.arn}/*",
    ]
  }
}

resource "aws_iam_user_policy" "backup" {
  name   = "${var.name_prefix}-backup-s3"
  user   = aws_iam_user.backup.name
  policy = data.aws_iam_policy_document.backup.json
}
