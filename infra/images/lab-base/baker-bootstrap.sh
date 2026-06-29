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

# cloud-init user-data 는 root 로 실행되지만 HOME 이 비어 있을 수 있다. packer 는 설정 디렉터리
# 결정에 HOME 을 요구하므로(없으면 "No $HOME environment variable found") 명시한다.
export HOME=/root

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

# 도구 설치(metal 은 Ubuntu 22.04). AL2023 는 qemu-system 패키지가 없어 packer-qemu 에 부적합해
# Ubuntu 로 베이크한다. qemu-system-x86 이 /usr/bin/qemu-system-x86_64 를 제공한다.
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends \
  git docker.io qemu-system-x86 qemu-utils curl unzip xorriso
# aws CLI v2 설치(Ubuntu 엔 미포함). finish() 의 sentinel/log 업로드와 SSM/EC2 호출에 필요해 일찍 깐다.
curl -fsSL "https://awscli.amazonaws.com/awscli-exe-linux-x86_64.zip" -o /tmp/awscliv2.zip
unzip -q /tmp/awscliv2.zip -d /tmp
/tmp/aws/install
systemctl enable --now docker
curl -fsSL -o /tmp/packer.zip \
  https://releases.hashicorp.com/packer/1.11.2/packer_1.11.2_linux_amd64.zip
unzip -o /tmp/packer.zip -d /usr/local/bin

git clone https://github.com/requset700k/cledyu.git "$WORK"
git -C "$WORK" checkout "$REF"
cd "$WORK/infra/images/lab-base" || exit 1

# ghcr 로그인(PAT 는 SSM SecureString). set -x 로그에 PAT 가 평문으로 찍히지 않게 이 구간만 추적 끔.
set +x
GHCR_PAT=$(aws ssm get-parameter --name /cledyu/baker/ghcr_pat --with-decryption \
  --region "$REGION" --query Parameter.Value --output text)
echo "$GHCR_PAT" | docker login ghcr.io -u "$GHCR_USER" --password-stdin
unset GHCR_PAT
set -x

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

  # 스냅샷 태깅(prune 의 DeleteSnapshot 가 cledyu-role 태그로 스코프됨).
  SNAP=$(aws ec2 describe-images --image-ids "$AMI_ID" --region "$REGION" \
    --query 'Images[0].BlockDeviceMappings[0].Ebs.SnapshotId' --output text)
  if [ -n "$SNAP" ] && [ "$SNAP" != "None" ]; then
    aws ec2 create-tags --resources "$SNAP" --region "$REGION" \
      --tags "Key=cledyu-role,Value=lab-session-ami-snap" || true
  fi

  # prune-on-bake: lab-session-ami 태그 AMI 중 최신 KEEP 개만 남기고 옛것 deregister + 스냅샷 삭제.
  # 베이크가 AMI 를 만드는 유일한 주체라 만들 때 정리한다(스냅샷 누적 비용 방지). 방금 만든 건 보호.
  KEEP=3
  # 배포는 수동이라 현재 Launch Template 이 참조하는 AMI 가 최신 KEEP 밖일 수 있다. 그 AMI 는
  # 운영 중이므로 prune 에서 반드시 제외한다(방금 만든 AMI 도 제외).
  LT_ID=$(aws ec2 describe-launch-templates --region "$REGION" \
    --query "LaunchTemplates[?starts_with(LaunchTemplateName,'cledyu-lab-session')].LaunchTemplateId | [0]" \
    --output text 2>/dev/null || echo "None")
  LT_AMI="None"
  if [ -n "$LT_ID" ] && [ "$LT_ID" != "None" ]; then
    # '$Default' 는 AWS Launch Template 의 기본 버전 별칭 리터럴이라 셸 확장하면 안 된다(단일따옴표 유지).
    # shellcheck disable=SC2016
    LT_AMI=$(aws ec2 describe-launch-template-versions --launch-template-id "$LT_ID" --region "$REGION" \
      --versions '$Default' --query 'LaunchTemplateVersions[0].LaunchTemplateData.ImageId' --output text 2>/dev/null || echo "None")
  fi
  log "prune keeps newest $KEEP + in-use LT AMI $LT_AMI"
  OLD=$(aws ec2 describe-images --owners self --region "$REGION" \
    --filters "Name=tag:cledyu-role,Values=lab-session-ami" \
    --query "sort_by(Images,&CreationDate)[:-${KEEP}].ImageId" --output text)
  for old in $OLD; do
    if [ "$old" = "$AMI_ID" ] || [ "$old" = "$LT_AMI" ]; then continue; fi
    osnap=$(aws ec2 describe-images --image-ids "$old" --region "$REGION" \
      --query 'Images[0].BlockDeviceMappings[0].Ebs.SnapshotId' --output text)
    aws ec2 deregister-image --image-id "$old" --region "$REGION" || true
    if [ -n "$osnap" ] && [ "$osnap" != "None" ]; then
      # 이전 베이크 스냅샷엔 태그가 없을 수 있어, 삭제 전 태그해 tag-scoped DeleteSnapshot 가 통과되게 한다.
      aws ec2 create-tags --resources "$osnap" --region "$REGION" \
        --tags "Key=cledyu-role,Value=lab-session-ami-snap" || true
      aws ec2 delete-snapshot --snapshot-id "$osnap" --region "$REGION" || true
    fi
    log "pruned old AMI $old (snap $osnap)"
  done
fi

STATUS="ok"
