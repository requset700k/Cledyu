#!/usr/bin/env bash
# code-server 설치(바이너리만). 랩 cloud-init 이 config 작성 + code-server@lab 기동.
set -euo pipefail
curl -fsSL https://code-server.dev/install.sh -o /tmp/code-server-install.sh
sh /tmp/code-server-install.sh
rm -f /tmp/code-server-install.sh
systemctl disable code-server@lab || true
