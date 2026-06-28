#!/usr/bin/env bash
# 공통 CLI 와 qemu-guest-agent 를 설치한다. 서비스는 켜지 않는다(베이킹 규율).
# cloud-init clean 으로 베이스 이미지를 first-boot 가능한 상태로 되돌린다(머신ID/seed 제거).
set -euo pipefail

export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends \
  qemu-guest-agent curl unzip jq ca-certificates gnupg apt-transport-https

# 서비스 비활성(랩 cloud-init 이 필요 시 enable). agent 는 KubeVirt/EC2 부팅 시 별도 기동됨.
systemctl disable qemu-guest-agent || true

echo "baked at $(date -u +%Y-%m-%dT%H:%M:%SZ)" > /etc/cledyu-baked
