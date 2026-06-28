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
