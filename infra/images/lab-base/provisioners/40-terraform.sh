#!/usr/bin/env bash
# terraform 바이너리 설치(releases.hashicorp.com zip → /usr/local/bin).
set -euo pipefail
curl -fsSL -o /tmp/terraform.zip \
  https://releases.hashicorp.com/terraform/1.9.8/terraform_1.9.8_linux_amd64.zip
unzip -o /tmp/terraform.zip -d /usr/local/bin
rm -f /tmp/terraform.zip
