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

# 임시 기동 때 bake VM 이 자기 hostname 으로 노드 등록한 흔적이 server 데이터스토어에 남는다.
# 이대로 클론하면 모든 세션 k3s 에 유령 노드(bake VM)가 보인다. nginx 이미지 캐시는
# agent/containerd 에 있으므로 server/db 만 지우면, 클론 첫 부팅에 단일 노드로 새로 부트스트랩되고
# 캐시는 유지된다(CA/tls 는 남겨 재사용).
rm -rf /var/lib/rancher/k3s/server/db
# bake 기동 때 생성된 노드 패스워드도 제거(클론에서 새 노드로 등록되게).
rm -f /etc/rancher/node/password
