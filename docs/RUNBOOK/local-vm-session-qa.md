# Local Session API + VM QA Runbook

로컬에서 `apps/api`와 `apps/web`을 실행해 Lab 세션 생성, KubeVirt VM 프로비저닝, xterm 터미널 접속을 확인한다.

## 트리거

- Session API 변경 후 로컬에서 실제 KubeVirt VM 세션 생성까지 확인해야 할 때
- Web의 `실습 시작` 버튼이 실제 API/VM 경로와 연결되는지 확인해야 할 때
- `kubevirt manager init failed`, `kubevirt not configured`, `session_exists` 같은 로컬 QA 문제를 분리해야 할 때

## 사전 조건

- Cledyu 클러스터에 접근 가능한 kubeconfig 보유
- `kubectl` 설치 및 클러스터 접근 권한 보유
- `kubectl-oidc_login` 설치 및 `~/.kube/cledyu-root-ca.pem` 배치 완료
- KubeVirt CRD, base PVC, VM InstanceType/Preference 준비 완료

사전 리소스는 클러스터에 접근 가능한 터미널에서 확인한다.

```bash
kubectl get nodes
kubectl get crd virtualmachines.kubevirt.io virtualmachineinstances.kubevirt.io
kubectl -n kubevirt get pvc ubuntu-2204-base
kubectl get virtualmachineclusterinstancetype lab-small
kubectl get virtualmachineclusterpreference ubuntu
```

## 절차

### 1. OIDC kubeconfig 준비

레포에서 제공하는 OIDC kubeconfig는 `kubectl-oidc_login`과 내부 root CA 파일을 사용한다. 처음 설정하는 팀원은 Cledyu 프로젝트 루트에서 root CA를 `~/.kube`에 복사한다.

```bash
mkdir -p ~/.kube
cp infra/kubernetes/kubeconfig/cledyu-root-ca.pem ~/.kube/cledyu-root-ca.pem
```

각자 로컬에서 사용하는 Cledyu kubeconfig 경로를 `KUBECONFIG`에 지정한다.

```bash
export KUBECONFIG="$(pwd)/infra/kubernetes/kubeconfig/cledyu-oidc.yaml"
echo "$KUBECONFIG"
kubectl config current-context
kubectl get nodes
```

`apps/api` 같은 하위 디렉터리에서 위 명령을 실행하면 `pwd` 기준 경로가 달라지므로, 먼저 프로젝트 루트로 이동한다. `kubectl-oidc_login`이 없으면 macOS 기준 아래 명령으로 설치한다.

```bash
brew install int128/kubelogin/kubelogin
```

다른 위치의 kubeconfig를 쓰는 경우에는 본인 파일 경로로 바꾼다. 개인별 절대경로는 커밋하지 않는다.

### 2. API 서버 실행

```bash
cd apps/api

CLEDYU_SERVER_MODE=debug \
CLEDYU_KEYCLOAK_URL=http://127.0.0.1:65535 \
go run ./cmd/server
```

예상 로그:

```text
listening
```

`CLEDYU_KEYCLOAK_URL`은 로컬 QA에서 Keycloak discovery를 일부러 실패시켜 debug fallback 신원(`dev-user`)으로 보호 API를 호출하기 위한 값이다.

### 3. Web 서버 실행

새 터미널에서 실행한다.

```bash
cd apps/web

CLEDYU_BACKEND_URL=http://localhost:8080 \
NEXT_PUBLIC_WS_URL=ws://localhost:8080 \
AUTH_ENABLED=false \
pnpm dev
```

예상 출력:

```text
Local: http://localhost:3000
```

### 4. API 기본 확인

새 터미널에서 실행한다.

```bash
curl -s http://localhost:8080/health | jq
curl -s http://localhost:8080/ready | jq
curl -s http://localhost:8080/api/v1/labs | jq
```

예상 출력:

```json
{
  "status": "ok",
  "version": "0.1.0"
}
```

Lab 목록 응답에는 `lab-linux-basics`가 포함되어야 한다.
`/ready` 응답의 `checks.kubevirt.detail`이 `configured`이면 API가 KubeVirt manager를 초기화한 상태다.

### 5. 세션 생성 확인

터미널 연결만 확인할 때는 `lab-linux-basics`를 우선 사용한다.

```bash
curl -i \
  -H 'Content-Type: application/json' \
  -d '{"lab_id":"lab-linux-basics"}' \
  http://localhost:8080/api/v1/sessions
```

예상 출력:

```json
{
  "id": "<session_id>",
  "lab_id": "lab-linux-basics",
  "status": "provisioning",
  "user_id": "dev-user",
  "vm_provider": "kubevirt"
}
```

### 6. KubeVirt 리소스 확인

```bash
kubectl get vm,vmi,pod -n lab-<session_id>
```

예상 출력:

```text
NAME                         AGE   STATUS    READY
virtualmachine/session-vm    ...   Running   True

NAME                                 AGE   PHASE     IP
virtualmachineinstance/session-vm    ...   Running   ...
```

### 7. Web에서 터미널 확인

브라우저에서 접속한다.

```text
http://localhost:3000/labs/lab-linux-basics
```

`실습 시작`을 누른 뒤 xterm 터미널이 표시되면 VM 로그인은 다음 계정을 사용한다.

```text
username: lab
password: lab
```

비밀번호 입력 중에는 화면에 문자가 표시되지 않는 것이 정상이다.

## 검증

성공 기준:

- `GET /health`가 `200 OK`와 `{"status":"ok"}`를 반환한다.
- `GET /ready`가 `checks`와 함께 readiness 상태를 반환한다.
- `GET /api/v1/labs`가 Lab 목록을 반환한다.
- `POST /api/v1/sessions`가 새 session id를 반환한다.
- `kubectl get vm,vmi -n lab-<session_id>`에서 VM/VMI가 Running이 된다.
- Web xterm 터미널에서 `lab/lab` 로그인에 성공한다.

## 롤백 / 정리

로컬 QA가 끝나면 세션 namespace를 삭제한다.

```bash
curl -i -X DELETE http://localhost:8080/api/v1/sessions/<session_id>
```

삭제가 API로 실패하면 namespace를 직접 확인한 뒤 정리한다.

```bash
kubectl get ns -l cledyu.io/managed-by=cledyu-session
kubectl delete ns lab-<session_id>
```

## 자주 발생하는 문제

### 409 session_exists

로컬 debug 모드에서는 모든 요청이 `dev-user`로 처리된다. 기존 활성 세션이 남아 있으면 새 세션 생성이 막힌다.

```json
{
  "code": "session_exists",
  "error": "active session already exists",
  "session_id": "<session_id>"
}
```

기존 세션을 삭제한 뒤 다시 시작한다.

```bash
curl -i -X DELETE http://localhost:8080/api/v1/sessions/<session_id>
```

### kubevirt not configured

API 서버 실행 시 `KUBECONFIG`가 지정되지 않았거나 잘못된 kubeconfig를 가리킨다.

```bash
echo "$KUBECONFIG"
kubectl config current-context
kubectl get nodes
```

### /ready에서 kubevirt가 optional_in_debug로 표시됨

```bash
curl -s http://localhost:8080/ready | jq '.checks.kubevirt'
```

로컬 debug 모드에서는 KubeVirt manager가 없어도 readiness 자체는 `ok`로 응답할 수 있다. 세션 VM QA를 하려면 `detail`이 `configured`인지 확인한다. `optional_in_debug`이면 API 서버 실행 시 `KUBECONFIG`가 지정되지 않았거나 잘못된 kubeconfig를 가리키는지 확인한다.

### 터미널에서 Login incorrect

로그인 프롬프트에는 명령어가 아니라 먼저 VM 계정을 입력해야 한다.

```text
username: lab
password: lab
```

### VM 내부 DNS 문제

VM은 뜨지만 cloud-init 중 apt/k3s 설치가 DNS 문제로 실패할 수 있다.

```text
Temporary failure resolving 'security.ubuntu.com'
```

API가 생성하는 cloud-init `bootcmd`에서 public DNS fallback을 설정해
패키지 설치와 k3s 다운로드 전에 이름 해석이 가능하도록 보정한다.

### 터미널이 설치 로그를 먼저 보여줌

랩별 `init.runcmd`가 끝나기 전에 serial getty가 먼저 재시작되면 학생 터미널에
cloud-init 설치 로그나 준비 전 프롬프트가 보일 수 있다. API가 생성하는 cloud-init은
부팅 초기에 `serial-getty@ttyS0`를 멈추고, 랩별 init이 끝난 뒤 autologin getty를
다시 시작한다.

세션 상태 조회는 VM이 `Running`이 되면 `ready`로 전환한다. Web은 이후 최대 2분간
부팅 카드를 유지해 랩별 init 완료와 autologin getty 재시작을 기다린다. 반대로 기본
프로비저닝 제한 시간(10분, CDI/Longhorn clone이 정상적으로 7분까지 걸리는 케이스를
감안한 값)을 넘겼는데도 VM이 `Running`이 아니면 `failed`로 표시한다. 이 namespace는
백그라운드 reaper가 주기적으로 회수하지만, 사용자가 그 전에 같은 lab을 다시 시작하면
`CreateSession`이 reaper를 기다리지 않고 즉시 정리한 뒤 새 세션을 만든다.

## 참고

- `apps/api/internal/config/config.go`: `KUBECONFIG`를 `kubevirt.kubeconfig` fallback으로 사용
- `apps/api/internal/kubevirt/session.go`: KubeVirt 세션 namespace/VM 생성
- `apps/api/internal/api/handlers/session.go`: 세션 생성, 삭제, 유저당 활성 세션 1개 제한
