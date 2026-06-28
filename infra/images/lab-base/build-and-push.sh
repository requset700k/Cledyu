#!/usr/bin/env bash
# packer-qemu 로 qcow2 를 굽고 → containerDisk(OCI) 로 빌드 → ghcr 에 푸시한다.
# metal baker(baker-bootstrap.sh)와 로컬 디버깅이 공용으로 호출한다. 사전에 ghcr 로그인이
# 돼 있어야 한다(docker login ghcr.io). packer/qemu/docker 가 설치돼 있어야 한다.
set -euo pipefail
cd "$(dirname "$0")"

IMAGE="${IMAGE:-ghcr.io/requset700k/cledyu-lab-base}"
TAG="${TAG:-$(date -u +%Y%m%d)}"

echo ">> packer build"
packer init lab-base.pkr.hcl
packer build lab-base.pkr.hcl

echo ">> stage qcow2 for containerDisk"
mkdir -p disk
mv output/lab-base.qcow2 disk/lab-base.qcow2
rm -rf output

echo ">> build containerDisk: ${IMAGE}:${TAG} (+latest)"
docker build -f containerdisk.dockerfile -t "${IMAGE}:${TAG}" -t "${IMAGE}:latest" .

echo ">> push"
docker push "${IMAGE}:${TAG}"
docker push "${IMAGE}:latest"

echo ">> done: ${IMAGE}:${TAG}"
