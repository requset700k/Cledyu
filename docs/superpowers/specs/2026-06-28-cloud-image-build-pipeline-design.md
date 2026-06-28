# 클라우드 네이티브 세션 이미지 빌드 파이프라인 설계

- 작성일: 2026-06-28
- 작성자: 김용균 (Platform Architect / Infra Lead)
- 상태: 설계 승인됨 (구현 계획 대기)
- 관련: 이미지 베이킹 Phase 1(PR #193), base-datavolume Force(PR #205), 랩 cloud-init apt 스킵(PR #206), EC2 오버플로우(PR #149/#153/#162/#166/#177)

## 1. 배경 / 문제

온프렘 KubeVirt 세션 프로비저닝을 최적화하던 중, 실측(2026-06-28)으로 병목을 정밀 분해했다.

- 클론(snapshot CoW, longhorn-r2 storageprofile cloneStrategy=snapshot + snapshot-controller PR #192): ~21s, 이미지 크기와 무관.
- 부팅(systemd kernel 9.4s + userspace): 고정.
- cloud-init: 총 26.6s 중 `modules-final/config-scripts_user`(=runcmd)가 24.5s. `config-apt_configure`는 0.039s로 PR #206이 apt 단계를 완전히 제거했음을 확인.

즉 경량 랩의 남은 비용은 거의 전부 **runcmd의 도구 다운로드/설치**다. 도구별 in-guest 실측:

| 도구 | 설치 시간 | 사용 랩 |
| --- | --- | --- |
| docker.io (apt update+install) | ~44s | docker-basics |
| code-server | ~36s | terraform-basics(IDE) |
| k3s + nginx crictl pull | ~24.5s | k8s-basics, helm-advanced |
| terraform zip+unzip | ~12s | terraform-basics |
| ansible-core (apt) | 수십초(추정) | ansible-basics |

이 도구들을 베이스 이미지에 미리 구우면(베이킹) runcmd 비용이 사라진다. 그러나 온프렘 베이킹은 **호스트의 libguestfs appliance 네트워크(passt/slirp)가 깨져** 정상 동작하지 않아, 현재는 offline-deb 핵(`infra/images/lab-base/bake.sh`)으로 우회 중이다. 이 우회는 취약하고 확장(무거운 도구·서비스 설치)이 어렵다.

추가로, EC2 오버플로우 세션은 stock Ubuntu AMI + cloud-init으로 부팅하므로 위 다운로드 비용을 **EC2에서도 그대로** 낸다. 결과적으로 온프렘이 꽉 차 클라우드로 넘어간 사용자가 더 느린 경험을 한다(본말전도).

## 2. 목표 / 비목표

목표:

- 온프렘에서 물리적으로 불가능한 이미지 빌드를 **클라우드 네이티브 빌드(packer-qemu on EC2 metal)** 로 전환한다.
- **단일 소스(설치 스크립트 한 벌)에서 qcow2와 AMI를 모두 산출**해, 온프렘(KubeVirt)과 EC2(오버플로우)가 동일한 베이킹 이미지를 쓰게 한다(드리프트 0).
- 무거운 랩 도구를 베이스에 구워, heavy 랩의 runcmd를 install→start로 바꿔 부팅 대기를 수초 수준으로 낮춘다.
- self-hosted KVM 러너 의존을 제거한다.

비목표(YAGNI):

- 자동 트리거(콘텐츠 변경 시 자동 베이크) — 수동 게이트 유지.
- per-lab 이미지 / 멀티리전 AMI 복제 / GPU 랩 / arm64 멀티아치 — 이번 범위 밖.

## 3. 아키텍처 개요

핵심 전환: 온프렘 libguestfs 우회를 버리고 **일회성 클라우드 metal baker**로 대체. 한 번의 packer-qemu 빌드가 1개 qcow2를 만들고, 그것을 두 타깃으로 배달한다.

```
GitHub Action (hosted runner, AWS OIDC)
  └─launch─> EC2 *.metal (transient, user-data baker)
                ├─ packer-qemu: Ubuntu 22.04 cloud img → 도구 설치(서비스 disabled) → lab-base.qcow2
                ├─ in-image smoke test (실패 시 abort)
                ├─ containerDisk build → ghcr push       ──> [온프렘] CDI import → base PVC
                ├─ qcow2 → S3 → ec2 import-image → AMI    ──> [EC2] launch template
                └─ sentinel(S3) 기록 → self-terminate
```

아티팩트 이식성 차이가 설계의 중심이다:

- **qcow2 → containerDisk(ghcr)**: 범용·이식 가능. KubeVirt가 도는 곳이면 어디서든 CDI가 같은 이미지를 import.
- **AMI**: AWS raw-EC2 전용. EC2 오버플로우 경로에서만 사용. qcow2 빌드 아래의 **모듈식 다운스트림 한 스텝**으로 분리.

## 4. 구성요소

1. `infra/images/lab-base/lab-base.pkr.hcl` (신규) — packer **qemu** 빌더 + 공유 provisioner. Ubuntu 22.04 클라우드 이미지를 부팅해 도구 설치.
2. provisioner 스크립트 (신규, 멱등) — `install, don't auto-enable` 규율: qemu-guest-agent, 공통 CLI(curl·unzip·jq·ca-certificates·gnupg·apt-transport-https), k3s(`INSTALL_K3S_SKIP_ENABLE=true` + nginx 사전 pull), docker.io, code-server, terraform, ansible-core, helm. 전부 설치만, 서비스 disabled.
3. metal baker user-data (신규) — transient EC2 `*.metal`이 부팅하며 스스로: repo clone → packer-qemu 빌드 → containerDisk push(ghcr) → qcow2 S3 업로드 후 `import-image`로 AMI 등록 → sentinel 기록 → self-terminate. IAM instance profile로 S3/EC2-import 권한, ghcr PAT는 SSM Parameter Store(SecureString)에서.
4. `.github/workflows/build-lab-base-image.yml` (개편) — `runs-on: ubuntu-latest`. AWS OIDC로 `cledyu` 계정 role assume → metal launch → sentinel polling → 산출물(ghcr 태그, AMI id) 출력. 입력: `tag`(기본 날짜), `import_ami`(bool, 기본 true). self-hosted 러너 의존 제거.
5. 기존 재사용/폐기 — `containerdisk.dockerfile`·`build-and-push.sh`는 packer 산출 qcow2를 소비하도록 소폭 조정. `bake.sh`·`setup-host.sh`는 폐기.

설계 규율: 두 배달 레그(containerDisk push / AMI import)는 빌드 스크립트에서 **서로 의존 없이 독립**한다. `import_ami` 플래그로 AMI 레그를 켜고 끌 수 있어야 한다(DR 대비, 10절).

## 5. 빌드 파이프라인 흐름

1. 메인테이너가 `workflow_dispatch`로 트리거(태그·import_ami 입력).
2. GitHub Action(hosted)이 AWS OIDC로 인증, transient `*.metal` 인스턴스를 launch(baker user-data 주입).
3. metal이 packer-qemu로 Ubuntu 클라우드 이미지를 부팅→provisioner 설치(서비스 disabled)→`lab-base.qcow2` 산출.
4. provisioner 끝에서 in-image smoke test 실행. 실패 시 빌드 abort, 아티팩트 미배포.
5. containerDisk(OCI) 빌드 후 ghcr push(`ghcr.io/requset700k/cledyu-lab-base:<tag>`).
6. `import_ami=true`면 qcow2를 S3 업로드 → `ec2 import-image` → AMI 등록·태깅.
7. metal이 S3에 완료 sentinel 기록 후 self-terminate.
8. Action이 sentinel polling(최대 ~25분 타임아웃). 실패/타임아웃 시 metal 강제 종료(orphan 방지). 산출물 id 출력.

## 6. 콘텐츠/런타임 변경 (install → start)

베이스가 도구를 이미 갖고 있으니, 랩 `init.runcmd`를 "설치"에서 "기동/설정"으로 교체한다. 같은 `session.BootInit`이 KubeVirt·EC2 양쪽에 흐르므로(PR #206에서 확인) 한 번 고치면 둘 다 빨라진다. 런타임 Go 코드는 무수정 — 콘텐츠 YAML만 바뀐다.

| 랩 | 변경 후 runcmd | 절감 |
| --- | --- | --- |
| k8s-basics | `systemctl enable --now k3s` (nginx 캐시됨) | ~24.5s→수초 |
| helm-advanced | k3s start (helm 베이크됨) | ~24.5s→수초 |
| terraform-basics | code-server 설정/start (terraform·code-server 베이크됨) | ~48s→수초 |
| docker-basics | `systemctl enable --now docker` + 그룹 | ~44s→수초 |
| ansible-basics | runcmd 비움(ansible-core 베이크됨) | 수십초→0 |
| linux-basics | 변경 없음 | - |

규율: 베이스는 설치만 하고 서비스는 disabled. 랩이 자기 도구만 켜므로 부팅이 가볍게 유지된다(linux-basics 세션에서 k3s/docker가 안 뜸 → 부팅·RAM 절약).

## 7. 아티팩트 배달

- 온프렘(qcow2→containerDisk): 새 ghcr 태그 → `gitops/apps/kubevirt/base-datavolume.yaml`의 registry url 태그 bump(PR) → ArgoCD sync → PR #205의 `Force=true`가 immutable DataVolume을 delete+recreate → CDI가 새 base PVC import. 이미 검증된 경로.
- EC2(qcow2→AMI): 새 AMI 등록 후 launch template 갱신. 권장: `infra/terraform/aws`의 `aws_launch_template.image_id`를 새 AMI로 → terraform apply(+ `update_default_version=true`, PR #177 미적용분 함께 반영). api가 런타임에 LT 기본 버전을 쓰므로 신규 세션이 자동으로 새 AMI 사용. 빌드가 직접 LT 버전을 만드는 방식은 state 드리프트를 유발하므로 비권장.

드리프트 0의 핵심: 온프렘 base PVC와 EC2 AMI가 같은 qcow2에서 나온다. "온프렘에서 되는 랩은 EC2에서도 동일하게 동작"이 구조적으로 보장된다.

## 8. 트리거 / 보안

- 트리거: `workflow_dispatch`(메인테이너 수동) 유지. 베이크는 드물고 비용이 있어 자동 트리거는 보류.
- 인증: GitHub hosted runner + AWS OIDC로 `cledyu` 계정 role assume(장기 키 없음).
- 시크릿: ghcr push PAT·(필요시) tailscale authkey는 SSM Parameter Store(SecureString)에서 metal이 instance profile로 읽음 → GitHub secret/로그 노출 최소화.
- IAM 최소권한: S3(특정 버킷), `ec2:ImportImage`/`RegisterImage`, 자기 태그 한정 `ec2:TerminateInstances`.
- public repo fork-PR RCE: 빌드가 격리 클라우드 계정 인스턴스에서만 돌고 self-terminate → 온프렘 무접촉. workflow_dispatch라 fork PR이 트리거 불가.
- interim classic write:packages PAT은 이 전환과 함께 revoke → SSM의 fine-grained 토큰으로 대체.

## 9. 비용

- metal 온디맨드(spot 금지 — 빌드 중 중단 회피). 서울 리전 최소 metal(예: `m5.metal`/`c6i.metal`) ~$4-5/hr × ~15분 ≈ 빌드당 $1-1.5. 베이크 빈도가 낮아 무시 가능.
- 기존 budget 알람(terraform `budget_limit_usd`) 범위 내. S3·AMI 스토리지 비용 미미.

## 10. DR / 이식성

이 설계는 향후 EKS 기반 DR과 충돌하지 않으며 오히려 이식성을 공짜로 제공한다.

- **qcow2/containerDisk는 범용 아티팩트**다. EKS DR이 "KubeVirt-on-EKS"(metal 노드그룹에서 `/dev/kvm`) 형태라면 같은 ghcr 이미지를 그대로 재사용한다. 바뀌는 것은 `base-datavolume.yaml`의 `storageClassName`(longhorn-r2 → ebs-csi)뿐이며, 이는 env 오버레이지 이미지 문제가 아니다.
- **AMI는 모듈식 다운스트림**이다. DR이 "KubeVirt everywhere"로 가 raw-EC2 provider가 불필요해지면 `import_ami=false`로 AMI 레그만 끄면 된다. qcow2/containerDisk는 무수정으로 넘어간다.
- 이 파이프라인이 dual-path(kubevirt vs ec2 provider)를 만드는 것이 아니다. provider 추상화는 PR #149에서 이미 존재하고, 파이프라인은 둘에 같은 소스의 이미지를 먹일 뿐이다.

지금 지킬 원칙(DR-ready 유지비용 ≈ 0):

1. 두 배달 레그를 독립 유지(`import_ami` 플래그로 분리) — 나중에 AMI를 추가/제거하기 쉽게.
2. `base-datavolume`의 storageClass를 하드코딩하지 말고 env 오버레이로 — 온프렘/EKS가 갈리는 유일한 지점.

DR 본연의 결정사항(이 설계와 무관, DR 스펙에서 다룰 것): 아키텍처(Graviton/arm64면 빌드 매트릭스 확장), clone strategy(ebs-csi 스냅샷 — CDI smart-clone 동일 동작), DR 범위(세션까지 EKS로 vs 앱 컨트롤플레인만).

## 11. 검증 전략

1. 빌드 단계: packer provisioner 끝에 in-image smoke(`k3s --version`, `docker --version`, `code-server --version`, `terraform version`, `command -v ansible qemu-ga`). 하나라도 실패 시 abort.
2. 온프렘 e2e: 베이크 후 측정 VM(2026-06-28 harness 재사용: base clone + lab-small + virtctl ssh + `cloud-init analyze`)으로 k8s-basics·terraform-basics 부팅 → runcmd가 install→start로 줄었는지 실측(목표: heavy 랩 runcmd <5s).
3. EC2 e2e: 새 AMI로 오버플로우 세션 1건 → tailnet join + SSM grading + 라이브 터미널 동작 확인(기존 e2e 경로).
4. CI: 변경되는 Go 코드 없음(콘텐츠 YAML만) → 기존 content loader 테스트 + yamllint로 충분.

## 12. 측정 근거 (실측 데이터)

2026-06-28, PR #206 배포 후. 측정 VM(실제 세션과 동일: base snapshot-clone + lab-small + ubuntu preference + masquerade)에 k8s-basics 실제 runcmd, virtctl ssh + `cloud-init analyze`.

- 클론(SmartClone snapshot): apply→DV Succeeded ~21s(기존 host-assisted copy ~35s에서 단축).
- 부팅: systemd kernel 9.4s + userspace 41.8s = 51.2s. DV→VMI Running ~33s.
- cloud-init: total 26.6s, `config-scripts_user`(runcmd) 24.5s, `config-apt_configure` 0.039s(PR #206 검증).
- 도구별 설치 시간: 4절 표 참조.
- AgentConnected는 베이크된 qemu-guest-agent가 부팅 초기 enable되어 cloud-init 완료 신호로 쓸 수 없음 → 측정은 virtctl ssh + cloud-init analyze 사용.
