#!/usr/bin/env bash
# Ubuntu 22.04 클라우드 이미지에 공통 패키지·qemu-guest-agent 를 미리 굽는다(virt-customize, 부팅 불필요).
# 세션 cloud-init 의 apt update/install 시간을 줄이고(측정상 ~63s 의 큰 몫), qemu-guest-agent 로
# 게스트 ready 신호를 확보한다(현재 base 는 agent 미탑재라 AgentConnected 가 항상 blank).
#
# 결과 qcow2 는 Dockerfile 이 containerDisk 포맷으로 감싸 ghcr 에 푸시하고, CDI 가 그 이미지를
# Longhorn base PVC(ubuntu-2204-base)로 import 한다. 런타임 containerDisk 가 아니라 import 소스다.
set -euo pipefail

SRC_URL="${SRC_URL:-https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img}"
OUT="${OUT:-disk/lab-base.qcow2}"

mkdir -p "$(dirname "$OUT")"
echo ">> download base cloud image: $SRC_URL"
curl -fsSL -o "$OUT" "$SRC_URL"

# KVM 없는 CI(GitHub hosted runner)에서도 동작하도록 소프트웨어 가상화(TCG) 백엔드 사용.
export LIBGUESTFS_BACKEND="${LIBGUESTFS_BACKEND:-direct}"

echo ">> bake: apt update + 공통 패키지 + qemu-guest-agent"
virt-customize -a "$OUT" \
  --update \
  --install qemu-guest-agent,curl,jq,unzip,ca-certificates,gnupg,apt-transport-https \
  --run-command 'systemctl enable qemu-guest-agent' \
  --run-command 'cloud-init clean --logs --seed || true' \
  --delete '/etc/ssh/ssh_host_*' \
  --truncate '/etc/machine-id'

echo ">> baked: $OUT ($(du -h "$OUT" | cut -f1))"
