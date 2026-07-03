# DR/백업 오프사이트 저장소 — 온프렘 durable 데이터(Postgres WAL/base, Vault raft 스냅샷)를
# S3 로 내보낸다. 온프렘이 죽어도 여기 사본이 남아 EKS DR 로 복원한다.
# 자격증명은 정적 IAM 키 → Vault → ESO(온프렘 클러스터라 IRSA 불가).
# (Longhorn 볼륨 백업은 매시 주기라 아래 Object Lock 30일과 충돌 → 이 버킷을 쓰지 않고 별도
#  무-락 버킷으로 분리한다. Plan A Task 6 참조.)

resource "aws_s3_bucket" "dr_backups" {
  bucket = "${var.name_prefix}-dr-backups"

  # Object Lock 은 버킷 생성 시에만 켤 수 있다(기존 버킷엔 사후 불가). 백업을 보존기간 내
  # 삭제·변조 불가로 굳혀, writer 키 유출·랜섬웨어·실수 삭제로부터 보호한다(§ object_lock_configuration).
  object_lock_enabled = true
}

resource "aws_s3_bucket_versioning" "dr_backups" {
  bucket = aws_s3_bucket.dr_backups.id
  versioning_configuration {
    status = "Enabled"
  }
}

# Object Lock 기본 보존 — GOVERNANCE 30일. writer 키가 유출되거나 랜섬웨어·실수로 삭제를 시도해도
# 30일간 백업 객체를 삭제·덮어쓸 수 없다(versioning 이 못 막는 "권한 있는 삭제"까지 차단).
# GOVERNANCE 라 s3:BypassGovernanceRetention 특별 권한으로만 예외 삭제 가능 — backup-writer 정책엔 그
# 권한을 주지 않는다(키 유출 시 우회 방지). COMPLIANCE 와 달리 break-glass 탈출구는 관리자에게 남는다.
#
# 주의: 이 30일 락은 저빈도 백업(postgres 30d retention·vault 6h·velero 6h)과는 정합적이나,
# Longhorn 매시 백업(retain 24)과는 충돌한다(720개 누적) → Longhorn 백업은 이 버킷/락 대상에서
# 제외한다(Plan A Task 6에서 별도 버킷 또는 무-락 처리). Longhorn 은 온프렘 로컬 복구용(비-DR)이라
# 락 보호의 우선순위가 낮다.
resource "aws_s3_bucket_object_lock_configuration" "dr_backups" {
  bucket = aws_s3_bucket.dr_backups.id

  rule {
    default_retention {
      mode = "GOVERNANCE"
      days = 30
    }
  }

  # object lock 은 versioning 이 켜져 있어야 유효하다 — 순서 보장.
  depends_on = [aws_s3_bucket_versioning.dr_backups]
}

# 백업엔 DB 전체(WAL)와 Vault 시크릿(raft 스냅샷)이 들어간다 — 퍼블릭 노출을 전면 차단한다.
resource "aws_s3_bucket_public_access_block" "dr_backups" {
  bucket                  = aws_s3_bucket.dr_backups.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

# 백업 암호화 키(고객 관리 KMS CMK). 이 버킷엔 Vault 시크릿·학습자 PII 가 들어가므로 자물쇠를 둘로
# 만든다 — SSE-S3(AES256)는 버킷 접근 권한만 있으면 복호화되지만, 고객 관리 KMS 키를 쓰면
# "버킷 접근(s3)"과 "복호화(kms)"가 분리된다. + CloudTrail 복호화 감사·키 비활성화 즉시 봉인 가능.
resource "aws_kms_key" "dr_backups" {
  description             = "Cledyu DR 백업(S3) 봉투 암호화 키"
  enable_key_rotation     = true # 연 1회 자동 로테이션
  deletion_window_in_days = 30   # 실수 삭제 방지 유예

  # 키 정책은 계정 root 에만 위임한다(자기 잠금 방지 — 키 관리 권한을 계정 IAM 으로 넘긴다).
  # 실제 사용 권한(GenerateDataKey/Decrypt)은 각 주체의 IAM 정책에서 부여한다:
  #   - backup-writer(아래 정책): 쓰기용 GenerateDataKey + 검증 read 용 Decrypt
  #   - DR 복원 롤(Plan C Task 5, 아직 미생성): kms:Decrypt 를 그 롤 정책에 추가 예정
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid       = "EnableIAMRoot"
      Effect    = "Allow"
      Principal = { AWS = "arn:aws:iam::${data.aws_caller_identity.current.account_id}:root" }
      Action    = "kms:*"
      Resource  = "*"
    }]
  })

  tags = { Name = "${var.name_prefix}-dr-backups" }
}

resource "aws_kms_alias" "dr_backups" {
  name          = "alias/${var.name_prefix}-dr-backups"
  target_key_id = aws_kms_key.dr_backups.key_id
}

# 저장 시 암호화(SSE-KMS, 위 CMK). bucket_key_enabled 로 객체별 KMS 호출을 버킷 단위 키로 묶어
# KMS API 호출·비용을 대폭 절감한다(WAL 등 소객체 다량 PUT 대비).
resource "aws_s3_bucket_server_side_encryption_configuration" "dr_backups" {
  bucket = aws_s3_bucket.dr_backups.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm     = "aws:kms"
      kms_master_key_id = aws_kms_key.dr_backups.arn
    }
    bucket_key_enabled = true
  }
}

# 수명주기 — versioning 이 켜져 있어 "삭제"는 delete marker 를 남기고 데이터는 non-current 버전으로
# 보존된다. 따라서 삭제 주체가 있는 프리픽스도 non-current 정리 규칙이 필요하다.
#  postgres/ : base/WAL retention 은 CNPG barman(retentionPolicy)이 관리. 여기선 그 삭제로 남은
#              non-current 버전을 정리하는 backstop.
#  vault/    : 6시간마다 고유 키 .snap 이 쌓이며 삭제 주체가 없다 → 현재 버전까지 만료시킨다.
#              non-current 정리는 Object Lock(30일)에 막혀 그 전엔 삭제 불가하므로 30일로 맞춘다
#              (7일로 두면 락에 걸려 실질 30일이 되어 의도와 실제가 어긋난다).
#  전체      : 실패한 multipart 업로드의 미완료 part 를 자동 중단해 과금 누수를 막는다.
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
      noncurrent_days = 30
    }
  }

  rule {
    id     = "abort-incomplete-multipart"
    status = "Enabled"
    filter {}
    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }
  }

  # noncurrent_version_expiration 규칙은 versioned bucket 전제다. object_lock_enabled 가 생성 시
  # versioning 을 자동 활성화하지만, 순서를 명시해 첫 apply 를 안정화하고 object_lock_configuration 과
  # 일관성을 맞춘다.
  depends_on = [aws_s3_bucket_versioning.dr_backups]
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
      # 큰 객체(Postgres base backup)는 multipart 업로드를 타므로, 실패한 업로드를
      # 중단·조회할 수 있어야 미완료 part 과금 누수를 막는다.
      "s3:AbortMultipartUpload",
      "s3:ListMultipartUploadParts",
      "s3:ListBucketMultipartUploads",
    ]
    resources = [
      aws_s3_bucket.dr_backups.arn,
      "${aws_s3_bucket.dr_backups.arn}/*",
    ]
  }

  # SSE-KMS 버킷이므로, 객체를 쓰려면 봉투 키 생성(GenerateDataKey)·검증 read 시 복호화(Decrypt)
  # 권한이 키에 대해 필요하다. 이 키(dr_backups CMK)에만 한정한다.
  statement {
    sid = "UseBackupKmsKey"
    actions = [
      "kms:GenerateDataKey",
      "kms:Decrypt",
      "kms:DescribeKey",
    ]
    resources = [aws_kms_key.dr_backups.arn]
  }
}

resource "aws_iam_user_policy" "backup" {
  name   = "${var.name_prefix}-backup-s3"
  user   = aws_iam_user.backup.name
  policy = data.aws_iam_policy_document.backup.json
}
