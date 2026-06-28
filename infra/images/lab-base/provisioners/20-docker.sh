#!/usr/bin/env bash
# docker.io 설치, 서비스는 disabled(랩 cloud-init 이 enable + lab 그룹/소켓 처리).
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends docker.io
systemctl disable docker || true
