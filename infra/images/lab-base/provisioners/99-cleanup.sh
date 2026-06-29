#!/usr/bin/env bash
# 이미지 캡처 전 정리. 베이스가 모든 세션 VM(KubeVirt)·EC2 AMI 로 복제되므로:
# (1) packer 접속용 ubuntu 계정을 잠가 sudo 가능한 ubuntu:ubuntu 가 남지 않게 한다.
# (2) machine-id 와 SSH host key 를 비워 클론마다 새로 생성되게 한다(공유 금지).
set -euo pipefail

# (1) ubuntu 비밀번호 잠금. ubuntu 엔 authorized_keys 가 없으니 이로써 로그인 불가가 된다.
passwd -l ubuntu

# (2) 머신 식별자·호스트키 제거 → 클론 첫 부팅에 systemd/ssh 가 새로 생성.
truncate -s 0 /etc/machine-id
rm -f /var/lib/dbus/machine-id
rm -f /etc/ssh/ssh_host_*

# (3) k3s 노드 상태 리셋(맨 마지막, smoke 의 k3s 재기동 이후). bake 동안 등록된 노드가 server
# 데이터스토어에 남으면 모든 클론에 유령 노드로 보인다. db 만 지우면 nginx 이미지 캐시
# (agent/containerd)·CA(tls)는 유지되고, 클론 첫 부팅에 단일 노드로 새로 부트스트랩된다.
systemctl stop k3s 2> /dev/null || true
rm -rf /var/lib/rancher/k3s/server/db
rm -f /etc/rancher/node/password
