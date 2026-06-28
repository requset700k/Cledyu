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
  printf '{"status":"%s","tag":"%s","ami":"%s"}\n' "$STATUS" "$TAG" "$AMI_ID" > /tmp/done.json
  aws s3 cp /tmp/done.json "s3://$BAKER_BUCKET/builds/$TAG/done.json" --region "$REGION" || true
  TOKEN=$(curl -sX PUT "http://169.254.169.254/latest/api/token" \
    -H "X-aws-ec2-metadata-token-ttl-seconds: 60")
  IID=$(curl -s -H "X-aws-ec2-metadata-token: $TOKEN" \
    http://169.254.169.254/latest/meta-data/instance-id)
  aws ec2 terminate-instances --instance-ids "$IID" --region "$REGION" || true
}
trap finish EXIT

# 도구 설치(metal 은 Amazon Linux 2023 가정 — packer/qemu/docker/awscli).
if ! dnf install -y git docker qemu-kvm awscli unzip; then
  yum install -y git docker qemu-kvm awscli unzip
fi
systemctl enable --now docker
curl -fsSL -o /tmp/packer.zip \
  https://releases.hashicorp.com/packer/1.11.2/packer_1.11.2_linux_amd64.zip
unzip -o /tmp/packer.zip -d /usr/local/bin

git clone --depth 1 https://github.com/requset700k/cledyu.git "$WORK"
cd "$WORK/infra/images/lab-base" || exit 1

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
  cat > /tmp/containers.json << JSON
[{"Description":"cledyu-lab-base","Format":"raw","UserBucket":{"S3Bucket":"$BAKER_BUCKET","S3Key":"import/$TAG/lab-base.raw"}}]
JSON
  TASK=$(aws ec2 import-image --disk-containers file:///tmp/containers.json \
    --region "$REGION" --query ImportTaskId --output text)
  log "import task $TASK"
  for _i in $(seq 1 60); do
    ST=$(aws ec2 describe-import-image-tasks --import-task-ids "$TASK" \
      --region "$REGION" --query 'ImportImageTasks[0].Status' --output text)
    if [ "$ST" = "completed" ]; then
      break
    fi
    if [ "$ST" = "deleted" ]; then
      log "import failed"
      exit 1
    fi
    sleep 30
  done
  AMI_ID=$(aws ec2 describe-import-image-tasks --import-task-ids "$TASK" \
    --region "$REGION" --query 'ImportImageTasks[0].ImageId' --output text)
  aws ec2 create-tags --resources "$AMI_ID" --region "$REGION" \
    --tags "Key=Name,Value=cledyu-lab-base-$TAG" "Key=cledyu-role,Value=lab-session-ami"
  log "AMI $AMI_ID"
fi

STATUS="ok"
