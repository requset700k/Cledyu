#!/usr/bin/env bash
# amazon-ssm-agent 를 deb 로 설치한다(서비스 disabled). EC2 오버플로우 세션의 채점(SendCommand)
# 경로에 필요하다. 베이크해 두면 EC2 cloud-init 이 런타임 snap 설치(~1-2분) 없이 기동만 하면 돼
# 오버플로우 부팅이 크게 빨라진다. 온프렘(KubeVirt)에선 disabled 라 미사용(qemu-guest-agent 사용).
set -euo pipefail

curl -fsSL -o /tmp/amazon-ssm-agent.deb \
  https://s3.ap-northeast-2.amazonaws.com/amazon-ssm-ap-northeast-2/latest/debian_amd64/amazon-ssm-agent.deb
dpkg -i /tmp/amazon-ssm-agent.deb
rm -f /tmp/amazon-ssm-agent.deb
systemctl disable amazon-ssm-agent || true
