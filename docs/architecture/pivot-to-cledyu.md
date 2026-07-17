# WikiPulse → Cledyu 마이그레이션 노트

이 레포는 원래 **WikiPulse** (Wikipedia 편집 폭증 기반 실시간 이슈 감지 + AI 여론 분석
플랫폼) 였으나, **Cledyu / KT Tech-Up Labs** (KodeKloud 스타일 Hands-on 교육 플랫폼 +
AI 학습 도우미 + 자동 채점 엔진) 로 프로젝트가 pivot되었습니다.

KT Tech-Up Labs 계획서에 **"WikiPulse 프로젝트와 동일 인프라 베이스라인으로 팀 학습
곡선 최소화"** 라고 명시되어 있어, Phase 0~7에서 구축한 쿠버네티스 HA 플랫폼은 그대로
계승합니다.

## 바뀐 것 (프로젝트 identity)

- 레포명: `wikipulse` → `cledyu` (GitHub 레포 이름 이미 변경됨)
- git remote URL: `https://github.com/requset700k/wikipulse.git` → `https://github.com/requset700k/cledyu.git`
- 로컬 경로: `/Users/kylekim1223/request700k/wikipulse/` → `/Users/kylekim1223/request700k/cledyu/`
- README, CONTRIBUTING, SECURITY, PR 템플릿, ISSUE 템플릿 identity 교체
- Kafka 토픽: `wiki-edits·reddit-comments·sentiment-results·briefings` → `learning-events·validation-requests/results·security-logs`
- 앱 스택: `FastAPI+Next.js 대시보드` → `Go/Gin Session API · xterm.js · Validation Engine · AI BFF`
- AI: `DistilBERT + Gemini 2.0 Flash` → `Gemini 3 Pro (Google AI Pro 크레딧) + Flash fallback · RAG`
- 데모 시나리오: `편집 폭증 감지` → `Lab 프로비저닝 + AI 힌트 + Validation`

## 운영 식별자 통일 (Phase 12 투입 전 완료)

초기에는 Terraform state · libvirt pool · Cilium 클러스터 이름이 `wikipulse`로
유지되어 있었으나 (이름만 바꾸면 VM 재생성으로 클러스터 파괴), Phase 12(KubeVirt)
투입 전 팀 다운타임 창구를 활용하여 **모든 식별자를 `cledyu` / `cledyubr0` 로
통일**했습니다. 재배포(`terraform destroy` + `apply`) 시 CA 재발급으로 어차피
kubeconfig 일괄 재배포가 필수였기 때문에 rename 추가 비용은 context 이름 변경뿐.

| 위치 | 과거 값 → 현재 값 |
|---|---|
| `infra/terraform/kvm/variables.tf` → `cluster_name` | `wikipulse` → `cledyu` |
| `infra/terraform/kvm/variables.tf` → `network_name` | `wpbr0` → `cledyubr0` |
| `infra/terraform/kvm/variables.tf` → `images_pool_path` | `/var/lib/libvirt/images/wikipulse` → `/var/lib/libvirt/images/cledyu` |
| `ansible/roles/cilium/defaults/main.yml` → `cilium_cluster_name` | `wikipulse` → `cledyu` |
| `ansible/roles/kubeadm_bootstrap/defaults/main.yml` → `cluster_name` | `wikipulse` → `cledyu` |
| `ansible/roles/libvirt_host/tasks/main.yml` → 이미지 서브경로 | `wikipulse` → `cledyu` |
| `ansible/playbooks/00-host-prep.yml` → `wpbr_*` 변수들 | `wpbr_*` → `br_*`, bridge `wpbr0` → `cledyubr0` |

## 재배포 절차 (rename 반영 시 실행)

```bash
# 1) 클러스터 tear-down
cd infra/terraform/envs/onprem && terraform destroy

# 2) 호스트에서 기존 libvirt 풀/네트워크 정리
ssh hv01 "sudo virsh pool-destroy wikipulse-pool  && sudo virsh pool-undefine wikipulse-pool"
ssh hv01 "sudo virsh net-destroy  wpbr0           && sudo virsh net-undefine  wpbr0"
ssh hv01 "sudo rm -rf /var/lib/libvirt/images/wikipulse"

# 3) 코드 main 머지 (식별자 cledyu로 교체됨)

# 4) 처음부터 재배포
make host-prep HOST=<호스트-IP>     # cledyubr0 + /var/lib/libvirt/images/cledyu 생성
make kvm-apply                      # cledyu-cp01/02/03 VM 3대
make vm-prep
make k8s-bootstrap                  # clusterName=cledyu, kubeconfig context=kubernetes-admin@cledyu
make platform-up                    # Cilium(cluster.name=cledyu) + MetalLB + Longhorn
make tailscale-up AUTHKEY=...
make argocd-up

# 5) 팀원에게 새 kubeconfig 배포 (CA 교체로 기존 kubeconfig 전부 무효)
```

## 문서 참고

- KT Tech-Up Labs 계획서: `/Users/kylekim1223/request700k/KT_Tech_Up_Labs_기획서_v4.docx`
- WikiPulse 원본 계획서 (참조용): `/Users/kylekim1223/request700k/wikipulse-project-plan-final.docx`
