#!/usr/bin/env bash
# 세션 베이스 이미지 베이킹 호스트(KVM 보유)의 멱등 셋업. 수동 실행 및 self-hosted runner 가
# 공용으로 호출한다. virt-customize(libguestfs) + docker(containerDisk 빌드/푸시)가 필요하다.
set -euo pipefail

echo ">> apt: libguestfs-tools, qemu-utils 설치(멱등)"
sudo apt-get update -qq
sudo DEBIAN_FRONTEND=noninteractive apt-get install -y libguestfs-tools qemu-utils

echo ">> verify"
for t in virt-customize qemu-img docker; do
  printf '   %s: ' "$t"
  command -v "$t" || {
    echo "MISSING"
    exit 1
  }
done
if [ -e /dev/kvm ]; then
  echo "   /dev/kvm: present"
else
  echo "   /dev/kvm: MISSING (KVM 필요)"
  exit 1
fi

echo ">> done. ghcr 푸시는 별도 인증 필요: docker login ghcr.io -u <user> -p <write:packages PAT>"
