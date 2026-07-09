SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help

# ────── 환경 변수 ──────
HOST        ?=                       # 온프레미스 호스트 IP (예: make host-prep HOST=192.168.0.50)
HOST_USER   ?= ubuntu
ANSIBLE_DIR := ansible
TF_KVM_DIR  := infra/terraform/kvm
TF_KVM_ENV  := infra/terraform/envs/onprem
K8S_DIR     := infra/kubernetes
GITOPS_DIR  := gitops
KEYCLOAK_SECRETS_FILE ?=
ANSIBLE_VAULT_PASSWORD_FILE ?=

# ────── 도움말 ──────
help: ## 사용 가능한 명령 목록
	@awk 'BEGIN{FS=":.*##"; printf "\n사용법:\n  make \033[36m<target>\033[0m\n\n"} \
		/^[a-zA-Z_0-9\-]+:.*?##/ {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# ansible 계열 target 은 모두 repo root 에서 실행한다. ansible.cfg(repo root)
# 가 단일 소스로 inventory / roles / collections 를 해결하므로 cd 가 필요 없음.

# ────── Phase 1: 호스트 / VM 프로비저닝 ──────
host-prep: ## 원격 호스트에 libvirt + cledyubr0 NAT 네트워크 설치 (HOST=<ip> 필수)
	@test -n "$(HOST)" || (echo "HOST=<ip> 를 지정하세요" && exit 1)
	ansible-playbook -i "$(HOST_USER)@$(HOST)," $(ANSIBLE_DIR)/playbooks/00-host-prep.yml

kvm-init: ## Terraform 초기화 (libvirt provider)
	cd $(TF_KVM_ENV) && terraform init

kvm-plan: ## KVM VM 프로비저닝 계획 확인
	cd $(TF_KVM_ENV) && terraform plan

kvm-apply: ## KVM VM 3대 (cp01/cp02/cp03) 프로비저닝
	cd $(TF_KVM_ENV) && terraform apply

kvm-destroy: ## KVM VM 모두 제거 (주의)
	cd $(TF_KVM_ENV) && terraform destroy

# ────── Phase 2: K8s 부트스트랩 ──────
vm-prep: ## VM에 containerd/kubeadm/kubelet 사전 설치
	ansible-playbook $(ANSIBLE_DIR)/playbooks/10-k8s-nodes.yml

k8s-bootstrap: ## kube-vip + kubeadm HA 컨트롤플레인 부트스트랩
	ansible-playbook $(ANSIBLE_DIR)/playbooks/20-k8s-bootstrap.yml

# ────── Phase 3~4: 플랫폼 스택 ──────
cilium-up: ## Cilium CNI 설치 (kube-proxy replacement)
	ansible-playbook $(ANSIBLE_DIR)/playbooks/30-cilium.yml

metallb-up: ## MetalLB L2 LoadBalancer 설치
	ansible-playbook $(ANSIBLE_DIR)/playbooks/40-metallb.yml

longhorn-up: ## Longhorn 분산 블록 스토리지 설치
	ansible-playbook $(ANSIBLE_DIR)/playbooks/41-longhorn.yml

platform-up: cilium-up metallb-up longhorn-up ## Cilium → MetalLB → Longhorn 순차 설치

# ────── Phase 5: 원격 접근 ──────
tailscale-up: ## Tailscale subnet router 설치 (최초: AUTHKEY=tskey-auth-...)
	ansible-playbook $(ANSIBLE_DIR)/playbooks/50-tailscale.yml \
		$(if $(AUTHKEY),-e tailscale_authkey=$(AUTHKEY),)

# ────── Phase 6: GitOps ──────
argocd-up: ## ArgoCD 설치 + 루트 App-of-Apps 적용
	ansible-playbook $(ANSIBLE_DIR)/playbooks/60-argocd.yml

argocd-password: ## ArgoCD 초기 admin 비밀번호 출력
	@ssh -o ExitOnForwardFailure=no -o ClearAllForwardings=yes \
		-J ykgoesdumb@192.168.45.245 -i ~/.ssh/ykgoesdumb_key ubuntu@10.10.0.11 \
		"kubectl -n argocd get secret argocd-initial-admin-secret -o jsonpath={.data.password} | base64 -d" \
		&& echo

argocd-url: ## ArgoCD LB IP 출력 (http://<ip> 로 접속)
	@ssh -o ExitOnForwardFailure=no -o ClearAllForwardings=yes \
		-J ykgoesdumb@192.168.45.245 -i ~/.ssh/ykgoesdumb_key ubuntu@10.10.0.11 \
		"kubectl -n argocd get svc argocd-server -o jsonpath={.status.loadBalancer.ingress[0].ip}" \
		| awk '{print "http://"$$0}'

cert-manager-up: ## cert-manager install + Cledyu internal CA bootstrap
	ansible-playbook $(ANSIBLE_DIR)/playbooks/31-cert-manager.yml

keycloak-foundation-up: ## keycloak operator + internal keycloak (DB는 CNPG keycloak-pg/GitOps, vault secrets required)
	@test -n "$(KEYCLOAK_SECRETS_FILE)" || (echo "KEYCLOAK_SECRETS_FILE=<vault-vars.yml> 를 지정하세요"; exit 1)
	@test -f "$(KEYCLOAK_SECRETS_FILE)" || (echo "secret vars file not found: $(KEYCLOAK_SECRETS_FILE)"; exit 1)
	@if [ -n "$(ANSIBLE_VAULT_PASSWORD_FILE)" ] && [ ! -f "$(ANSIBLE_VAULT_PASSWORD_FILE)" ]; then echo "vault password file not found: $(ANSIBLE_VAULT_PASSWORD_FILE)"; exit 1; fi
	ansible-playbook $(ANSIBLE_DIR)/playbooks/70-keycloak-foundation.yml \
		--extra-vars "@$(KEYCLOAK_SECRETS_FILE)" \
		$(if $(ANSIBLE_VAULT_PASSWORD_FILE),--vault-password-file $(ANSIBLE_VAULT_PASSWORD_FILE),--ask-vault-pass)

client-trust-up: ## install Cledyu root CA on client trust targets
	ansible-playbook $(ANSIBLE_DIR)/playbooks/81-client-trust.yml

vault-bootstrap-configure: ## configure Vault Kubernetes auth, policies, and initial secret migration
	powershell.exe -ExecutionPolicy Bypass -File scripts/vault-bootstrap-configure.ps1

.PHONY: help host-prep kvm-init kvm-plan kvm-apply kvm-destroy vm-prep k8s-bootstrap cilium-up cert-manager-up keycloak-foundation-up client-trust-up vault-bootstrap-configure metallb-up longhorn-up platform-up tailscale-up argocd-up argocd-password argocd-url
