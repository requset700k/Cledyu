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
# nginx 이미지 캐시 확인 — 이미지는 10-k3s.sh 가 crictl(k8s.io ns)로 받았으므로 동일하게
# crictl 로 확인한다. containerd 가 꺼져 있어 잠깐 기동→확인→정지한다.
systemctl start k3s
ok=0
for _i in $(seq 1 12); do
  if k3s crictl images 2> /dev/null | grep -q nginx; then
    ok=1
    break
  fi
  sleep 5
done
systemctl stop k3s
if [ "$ok" -ne 1 ]; then
  echo "nginx image not pre-pulled" >&2
  exit 1
fi
echo "smoke OK"
