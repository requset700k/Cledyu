#!/usr/bin/env bash
# ansible-core(apt) + helm(get-helm-3) 설치.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends ansible-core
curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 -o /tmp/get-helm-3.sh
# helm 버전 핀: get-helm-3 은 기본적으로 helm3-latest-version(최신 v3)을 받아 빌드마다 버전이
# 달라진다(재현성 없음). helm 랩 검증이 helm get metadata(3.13+ 전용)에 의존하므로 명시 버전으로
# 고정한다. 버전을 올릴 땐 이 값을 바꾸고 helm 랩 검증을 재확인할 것.
DESIRED_VERSION=v3.21.2 bash /tmp/get-helm-3.sh
rm -f /tmp/get-helm-3.sh
