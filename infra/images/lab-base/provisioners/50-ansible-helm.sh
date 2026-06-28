#!/usr/bin/env bash
# ansible-core(apt) + helm(get-helm-3) 설치.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends ansible-core
curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 -o /tmp/get-helm-3.sh
bash /tmp/get-helm-3.sh
rm -f /tmp/get-helm-3.sh
