# kubectl Keycloak OIDC 통합 운영 런북

5명 팀원이 자기 Keycloak 계정으로 `kubectl` 사용 — admin cert 분배 종속성 0.

## 1. 구성 요약

```
kubectl 명령
   ↓
kubectl-oidc_login (kubelogin) — 로컬 OAuth client
   ↓ 브라우저 자동 열림 (첫 호출만)
Keycloak (cledyu realm, client=kubectl, public + PKCE)
   ↓ ID token (preferred_username + groups claim)
kube-apiserver OIDC plugin (--oidc-issuer-url, --oidc-ca-file)
   ↓ User=preferred_username, Groups=team-*
ClusterRoleBinding (Group=team-platform → cluster-admin, 나머지 → view)
```

## 2. 영구화된 매니페스트

| 파일 | 역할 |
|---|---|
| `infra/terraform/keycloak/clients.tf` + `terraform.tfvars` | Keycloak `kubectl` client (PUBLIC + PKCE, redirect=`http://localhost:{8000,18000}`) |
| `ansible/roles/kube_apiserver_oidc/` | kube-apiserver OIDC flag + dnsConfig + split DNS + /etc/hosts fallback |
| `ansible/playbooks/82-kube-apiserver-oidc.yml` | Rolling apply 진입점 |
| `infra/kubernetes/rbac/keycloak-bindings.yaml` | Keycloak group → ClusterRole 매핑 (ADR-0001 동기화) |
| `infra/kubernetes/kubeconfig/cledyu-oidc.yaml` | 5명 분배용 kubeconfig (sensitive 없음) |
| `infra/kubernetes/kubeconfig/cledyu-root-ca.pem` | OIDC discovery 용 내부 CA (kubelogin `--certificate-authority`) |

## 3. 처음 배포 절차 (재구성 시)

### 3.1 Keycloak — kubectl client 등록

```bash
cd infra/terraform/keycloak
export TF_VAR_keycloak_admin_username="$(kubectl -n keycloak get secret cledyu-keycloak-initial-admin -o jsonpath='{.data.username}' | base64 -d)"
export TF_VAR_keycloak_admin_password="$(kubectl -n keycloak get secret cledyu-keycloak-initial-admin -o jsonpath='{.data.password}' | base64 -d)"
export TF_VAR_keycloak_tls_insecure_skip_verify=true
terraform apply
```

### 3.2 control plane — OIDC flag + dnsConfig + split DNS + hosts

**Rolling 권장** (한 노드씩, lock-out 최소화):

```bash
cd ansible
ansible-playbook -i inventory.yml playbooks/82-kube-apiserver-oidc.yml --limit cp01
# 검증
kubectl get nodes
# OK 면 cp02, cp03
ansible-playbook -i inventory.yml playbooks/82-kube-apiserver-oidc.yml --limit cp02:cp03
```

### 3.3 RBAC

```bash
kubectl apply -f infra/kubernetes/rbac/keycloak-bindings.yaml
```

### 3.4 5명 분배 — kubelogin 설치 + kubeconfig 사용

각 팀원:

```bash
# 1. kubelogin 설치
brew install int128/kubelogin/kubelogin       # macOS
# Linux:  https://github.com/int128/kubelogin/releases
# Windows: 동일 release 페이지

# 2. 레포 clone (안 했으면)
git clone https://github.com/requset700k/cledyu.git
cd cledyu

# 3. 내부 CA 배치 (kubelogin이 Keycloak TLS 검증에 사용)
cp infra/kubernetes/kubeconfig/cledyu-root-ca.pem ~/.kube/cledyu-root-ca.pem

# 4. *.cledyu.local 와일드카드 split-DNS 설정 (브라우저가 Keycloak 에 접근하기 위해 필요)
#    한 번만 실행하면 신규 호스트 추가 시 추가 작업 불요. macOS/Linux/Windows 자동 분기.
ansible-playbook -i ansible/inventory.yml ansible/playbooks/80-local-dns.yml --limit <hostname>

# 5. 첫 kubectl 호출 — 브라우저 자동 열림 → Keycloak 로그인
export KUBECONFIG=$(pwd)/infra/kubernetes/kubeconfig/cledyu-oidc.yaml
kubectl get nodes

# 6. 영구 export (선택)
echo "export KUBECONFIG=$(pwd)/infra/kubernetes/kubeconfig/cledyu-oidc.yaml" >> ~/.zshrc
```

토큰은 `~/.kube/cache/oidc-login/` 에 cache → 다음 호출부터 브라우저 X (refresh token TTL 8h).

## 4. RBAC 매트릭스

| Keycloak Group | k8s ClusterRole | 의미 |
|---|---|---|
| `team-platform` | `cluster-admin` | 김용균 — 전체 운영 |
| `team-security` | `view` | 윤승호 |
| `team-observability` | `view` | 조승연 |
| `team-lab-data` | `view` | 김찬영 |
| `team-ai` | `view` | 양성호 |
| `team-service` | `view` | 한정현 |

도메인별 admin 권한이 필요하면 namespace-scoped Role + RoleBinding 으로 별도 부여 (ADR-0001 후속).

## 5. 트러블슈팅

| 증상 | 원인 | 해결 |
|---|---|---|
| `interactiveMode must be specified` | kubeconfig exec spec 에 `interactiveMode` 누락 | `interactiveMode: IfAvailable` 추가 |
| `tls: failed to verify certificate: x509: certificate signed by unknown authority` | kubeconfig 의 `certificate-authority-data` 가 실제 cluster CA 불일치 | `kubectl --kubeconfig=admin.conf config view --raw -o jsonpath='{.clusters[0].cluster.certificate-authority-data}'` 로 진짜 CA 추출 후 갱신 |
| `oidc authenticator: ... lookup keycloak.cledyu.local on 1.1.1.1:53: no such host` | kube-apiserver 가 노드의 default DNS (1.1.1.1) 사용 → cledyu.local 모름 | role 의 `dnsConfig` (CoreDNS 단독) 적용 확인 — `kubectl -n kube-system get pod kube-apiserver-cp01 -o jsonpath='{.spec.dnsPolicy}'` → `None` |
| `forbidden: User ... cannot get nodes` | ClusterRoleBinding 의 group claim 매칭 실패 | JWT decode 해서 `groups` claim 값 확인 (`kubectl-oidc_login get-token --oidc-issuer-url=... --oidc-client-id=kubectl \| jq -r .status.token \| cut -d. -f2 \| base64 -d \| jq .groups`) |
| `error: interactiveMode must be specified for keycloak to use exec authentication plugin` | kubectl >= 1.26 의 exec credential 새 요건 | kubeconfig 의 user.exec 에 `interactiveMode: IfAvailable` |
| Pod 가 새 manifest 인식 안 함 | kubelet 의 manifest 변경 감지 누락 | `sudo touch /etc/kubernetes/manifests/kube-apiserver.yaml` |
| `kubectl-oidc_login: command not found` | kubelogin 미설치 | `brew install int128/kubelogin/kubelogin` |
| `authcode-browser error: context deadline exceeded` | `*.cledyu.local` 와일드카드 split-DNS 미적용 → 브라우저가 Keycloak 에 접근 못 해 콜백 타임아웃 | `ansible-playbook -i ansible/inventory.yml ansible/playbooks/80-local-dns.yml --limit <hostname>` (또는 macOS: `dscacheutil -q host -a name keycloak.cledyu.local` 로 resolve 확인) |
| `x509: certificate signed by unknown authority` (kubelogin) | `~/.kube/cledyu-root-ca.pem` 없음 | `cp infra/kubernetes/kubeconfig/cledyu-root-ca.pem ~/.kube/cledyu-root-ca.pem` |

## 6. 롤백

```bash
# 1. 가장 최신 백업 manifest 로 복원
ssh cp01 'sudo cp /etc/kubernetes/kube-apiserver.yaml.bak-<TS> /etc/kubernetes/manifests/kube-apiserver.yaml'
# cp02, cp03 동일

# 2. ClusterRoleBinding 삭제
kubectl delete -f infra/kubernetes/rbac/keycloak-bindings.yaml

# 3. 5명에게 admin.conf fallback 분배 (긴급 시)
```

## 7. 후속 작업

- [ ] 도메인별 admin 권한 namespace-scoped Role + RoleBinding (ADR-0001 매트릭스 의 admin/observer 차등 정밀화)
- [ ] kubelogin auto-update mechanism
- [ ] CoreDNS hosts plugin 의 Ansible role 자동화 (현재 manifest 직접 apply)
- [ ] Audit log → Kafka `security-logs` (Strimzi 의존)
