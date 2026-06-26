#!/usr/bin/env bash
# Ubuntu 22.04 클라우드 이미지에 공통 패키지·qemu-guest-agent 를 미리 굽는다(virt-customize, 부팅 불필요).
# 세션 cloud-init 의 apt update/install 시간을 줄이고(측정상 ~63s 의 큰 몫), qemu-guest-agent 로
# 게스트 ready 신호를 확보한다(현재 base 는 agent 미탑재라 AgentConnected 가 항상 blank).
#
# 네트워크 전략: virt-customize 의 appliance 네트워크(passt/slirp)는 여러 환경에서 DNS 가 깨져
# 불안정하다. 그래서 패키지는 호스트(네트워크 정상)에서 docker 로 .deb 의존성 클로저를 받아두고,
# virt-customize 는 OFFLINE(--copy-in + dpkg)으로만 설치한다 → appliance 네트워크 불필요.
#
# 요구: docker, virt-customize, qemu-img (setup-host.sh 가 설치). KVM 권장.
# 결과 qcow2 는 containerdisk.dockerfile 이 감싸 ghcr 에 푸시하고 CDI 가 base PVC 로 import 한다.
set -euo pipefail

SRC_URL="${SRC_URL:-https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img}"
OUT="${OUT:-disk/lab-base.qcow2}"
PKGS="${PKGS:-qemu-guest-agent curl jq unzip ca-certificates gnupg apt-transport-https}"

OUTDIR="$(dirname "$OUT")"
DEBS="$OUTDIR/debs"
mkdir -p "$OUTDIR"

echo ">> download base cloud image: $SRC_URL"
curl -fsSL -o "$OUT" "$SRC_URL"

echo ">> resolve + download .deb closure via docker (host network): $PKGS"
rm -rf "$DEBS"; mkdir -p "$DEBS"
# jammy 컨테이너에서 클로저를 받는다. cloud 이미지는 minimal 컨테이너보다 패키지가 많으므로,
# minimal 에서 받은 deb 집합은 cloud 이미지에 필요한 의존성의 상위집합이라 offline dpkg 로 충족된다.
docker run --rm -v "$(cd "$DEBS" && pwd):/debs" ubuntu:22.04 bash -c "
  set -e
  apt-get update -qq
  apt-get install -y --download-only --no-install-recommends $PKGS
  cp /var/cache/apt/archives/*.deb /debs/
  chmod -R a+rw /debs
"
echo ">>   downloaded $(find "$DEBS" -name '*.deb' | wc -l) debs"

echo ">> bake OFFLINE (virt-customize --copy-in + dpkg, no appliance network)"
virt-customize -a "$OUT" \
  --copy-in "$DEBS:/tmp" \
  --run-command 'dpkg -i /tmp/debs/*.deb || dpkg -i /tmp/debs/*.deb || dpkg --configure -a' \
  --run-command 'systemctl enable qemu-guest-agent' \
  --run-command 'rm -rf /tmp/debs' \
  --run-command 'cloud-init clean --logs --seed || true' \
  --delete '/etc/ssh/ssh_host_*' \
  --truncate '/etc/machine-id'

rm -rf "$DEBS"
echo ">> baked: $OUT ($(du -h "$OUT" | cut -f1))"
