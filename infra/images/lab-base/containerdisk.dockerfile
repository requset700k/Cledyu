# CDI registry import 용 containerDisk 포맷 — 베이킹된 디스크를 /disk/ 에 둔다.
# (런타임 containerDisk 가 아니라 CDI 가 Longhorn base PVC 로 import 하는 소스 이미지다.)
# bake.sh 가 disk/lab-base.qcow2 를 먼저 생성해야 한다(CI 가 순서대로 실행).
FROM scratch
ADD disk/lab-base.qcow2 /disk/
