#!/usr/bin/env bash
# bake.sh 로 베이스 이미지를 굽고 → containerDisk(OCI)로 빌드 → ghcr 에 푸시한다.
# 호스트 수동 실행과 self-hosted runner 가 공용으로 호출한다. 사전에 ghcr 로그인이 돼 있어야 한다
# (docker login ghcr.io, write:packages PAT). setup-host.sh 로 도구가 설치돼 있어야 한다.
set -euo pipefail
cd "$(dirname "$0")"

IMAGE="${IMAGE:-ghcr.io/requset700k/cledyu-lab-base}"
TAG="${TAG:-$(date -u +%Y%m%d)}" # 수동 실행 기본은 날짜 태그. CI 는 sha-<short> 를 주입한다.

echo ">> bake (virt-customize)"
OUT="disk/lab-base.qcow2" bash bake.sh

echo ">> build containerDisk: ${IMAGE}:${TAG} (+latest)"
docker build -f containerdisk.dockerfile -t "${IMAGE}:${TAG}" -t "${IMAGE}:latest" .

echo ">> push"
docker push "${IMAGE}:${TAG}"
docker push "${IMAGE}:latest"

echo ">> done: ${IMAGE}:${TAG}"
echo ">> 다음: gitops base-datavolume.yaml 의 registry source 를 ${IMAGE}:${TAG} 로 pin"
