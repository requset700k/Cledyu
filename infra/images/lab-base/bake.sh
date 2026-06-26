#!/usr/bin/env bash
# Ubuntu 22.04 클라우드 이미지에 공통 패키지·qemu-guest-agent 를 미리 굽는다(virt-customize, 부팅 불필요).
# 세션 cloud-init 의 apt update/install 시간을 줄이고(측정상 ~63s 의 큰 몫), qemu-guest-agent 로
# 게스트 ready 신호를 확보한다(현재 base 는 agent 미탑재라 AgentConnected 가 항상 blank).
#
# KVM + 실네트워크가 있는 호스트(또는 self-hosted runner)에서 실행한다. GitHub 호스티드 러너는
# libguestfs appliance 네트워크(passt/slirp)가 불안정해 베이크가 실패하므로 쓰지 않는다.
#
# 결과 qcow2 는 containerdisk.dockerfile 이 containerDisk 포맷으로 감싸 ghcr 에 푸시하고, CDI 가
# 그 이미지를 Longhorn base PVC(ubuntu-2204-base)로 import 한다(런타임 containerDisk 아님).
set -euo pipefail

SRC_URL="${SRC_URL:-https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img}"
OUT="${OUT:-disk/lab-base.qcow2}"

mkdir -p "$(dirname "$OUT")"
echo ">> download base cloud image: $SRC_URL"
curl -fsSL -o "$OUT" "$SRC_URL"

echo ">> bake: apt update + 공통 패키지 + qemu-guest-agent"
virt-customize -a "$OUT" \
  --update \
  --install qemu-guest-agent,curl,jq,unzip,ca-certificates,gnupg,apt-transport-https \
  --run-command 'systemctl enable qemu-guest-agent' \
  --run-command 'cloud-init clean --logs --seed || true' \
  --delete '/etc/ssh/ssh_host_*' \
  --truncate '/etc/machine-id'

echo ">> baked: $OUT ($(du -h "$OUT" | cut -f1))"
