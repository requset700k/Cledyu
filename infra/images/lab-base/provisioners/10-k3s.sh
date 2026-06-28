#!/usr/bin/env bash
# k3s 를 설치하되 부팅 시 자동기동하지 않는다(INSTALL_K3S_SKIP_ENABLE=true).
# 랩 cloud-init 이 `systemctl enable --now k3s` 로 켠다. nginx 이미지를 k3s 의 containerd
# (CRI, k8s.io namespace)에 미리 받아 둬 step 검증의 pull 지연을 없앤다.
set -euo pipefail

curl -sfL https://get.k3s.io -o /tmp/k3s-install.sh
INSTALL_K3S_SKIP_ENABLE=true \
  INSTALL_K3S_EXEC="--write-kubeconfig-mode 644 --cluster-cidr=10.244.0.0/16 --service-cidr=10.245.0.0/16" \
  sh /tmp/k3s-install.sh
rm -f /tmp/k3s-install.sh

# 이미지 사전 pull 은 k3s 가 잠깐 떠 있어야 가능하다. 임시 기동→pull→정지.
systemctl start k3s
for _i in $(seq 1 24); do
  if k3s crictl pull docker.io/library/nginx:1.27-alpine; then break; fi
  sleep 5
done
systemctl stop k3s
systemctl disable k3s || true
