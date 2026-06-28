#!/usr/bin/env bash
# 베이킹된 도구가 전부 존재하는지 확인한다. 하나라도 없으면 비-0 종료로 빌드를 중단시킨다.
set -euo pipefail

test -f /etc/cledyu-baked
command -v qemu-ga
k3s --version
docker --version
code-server --version
terraform version
command -v ansible
helm version --short
# nginx 이미지 캐시 확인(k3s 임시 기동 없이 containerd 이미지 스토어 파일 존재로 갈음).
k3s ctr images ls 2> /dev/null | grep -q nginx || {
  echo "nginx image not pre-pulled" >&2
  exit 1
}
echo "smoke OK"
