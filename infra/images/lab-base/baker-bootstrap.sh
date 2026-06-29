#!/usr/bin/env bash
# metal user-data 가 실행하는 전체 베이크 오케스트레이션. 실패해도 마지막에 sentinel 과
# self-terminate 를 보장한다(orphan metal 방지). 산출물: ghcr 태그 + (옵션)AMI.
set -euo pipefail

REGION="${REGION:-ap-northeast-2}"
IMAGE="${IMAGE:-ghcr.io/requset700k/cledyu-lab-base}"
TAG="${TAG:?TAG required}"
BAKER_BUCKET="${BAKER_BUCKET:?BAKER_BUCKET required}"
IMPORT_AMI="${IMPORT_AMI:-true}"
GHCR_USER="${GHCR_USER:-ykgoesdumb}"
REF="${REF:-main}"

# 전체 출력을 로그로 캡처한다. metal 은 실패 시 self-terminate 되어 사후 콘솔 접근이 안 되므로,
# finish() 가 이 로그를 sentinel 과 함께 S3 에 올려 원인을 진단할 수 있게 한다. set -x 로 명령 추적.
exec > >(tee -a /var/log/cledyu-baker.log) 2>&1
set -x

STATUS="failed"
AMI_ID=""
WORK=/opt/cledyu
log() { echo "[baker] $*"; }

finish() {
  printf '{"status":"%s","tag":"%s","ami":"%s"}\n' "$STATUS" "$TAG" "$AMI_ID" > /tmp/done.json
  aws s3 cp /tmp/done.json "s3://$BAKER_BUCKET/builds/$TAG/done.json" --region "$REGION" || true
  aws s3 cp /var/log/cledyu-baker.log "s3://$BAKER_BUCKET/builds/$TAG/baker.log" --region "$REGION" || true
  TOKEN=$(curl -sX PUT "http://169.254.169.254/latest/api/token" \
    -H "X-aws-ec2-metadata-token-ttl-seconds: 60") || true
  IID=$(curl -s -H "X-aws-ec2-metadata-token: $TOKEN" \
    http://169.254.169.254/latest/meta-data/instance-id) || true
  aws ec2 terminate-instances --instance-ids "$IID" --region "$REGION" || true
}
trap finish EXIT

# 도구 설치(metal 은 Amazon Linux 2023 가정). awscli 는 AL2023 에 v2 가 기본 설치돼 있어
# dnf 패키지로 잡으면 "No match for argument: awscli" 로 트랜잭션 전체가 실패하므로 제외한다.
if ! dnf install -y git docker qemu-kvm qemu-img unzip; then
  yum install -y git docker qemu-kvm qemu-img unzip
fi
# packer-qemu 는 기본적으로 qemu-system-x86_64 바이너리를 찾는다. AL2023 의 qemu-kvm 은
# /usr/libexec/qemu-kvm 로 깔리므로, 그 이름으로 PATH 에 링크해 packer 가 찾게 한다.
if ! command -v qemu-system-x86_64 > /dev/null 2>&1; then
  for cand in /usr/libexec/qemu-kvm /usr/bin/qemu-kvm; do
    if [ -x "$cand" ]; then
      ln -sf "$cand" /usr/local/bin/qemu-system-x86_64
      break
    fi
  done
fi
systemctl enable --now docker
curl -fsSL -o /tmp/packer.zip \
  https://releases.hashicorp.com/packer/1.11.2/packer_1.11.2_linux_amd64.zip
unzip -o /tmp/packer.zip -d /usr/local/bin

git clone https://github.com/requset700k/cledyu.git "$WORK"
git -C "$WORK" checkout "$REF"
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
  ST=""
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
  if [ "$ST" != "completed" ]; then
    log "AMI import timed out after 60 polls"
    exit 1
  fi
  AMI_ID=$(aws ec2 describe-import-image-tasks --import-task-ids "$TASK" \
    --region "$REGION" --query 'ImportImageTasks[0].ImageId' --output text)
  aws ec2 create-tags --resources "$AMI_ID" --region "$REGION" \
    --tags "Key=Name,Value=cledyu-lab-base-$TAG" "Key=cledyu-role,Value=lab-session-ami"
  log "AMI $AMI_ID"
fi

STATUS="ok"
