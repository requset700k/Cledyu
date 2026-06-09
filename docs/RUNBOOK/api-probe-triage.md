# API Probe Triage

## 트리거

- `api` Pod가 `Ready=False` 또는 반복 재시작 상태가 됨
- `app.cledyu.local`에서 API 호출이 502/503으로 실패함
- ArgoCD sync 이후 `api` Deployment rollout이 완료되지 않음

## 사전 조건

- `kubectl`이 Cledyu 클러스터에 인증되어 있어야 한다.
- `api.cledyu.local` DNS가 클러스터 Ingress로 해석되어야 한다.

```bash
kubectl auth can-i get pods -n api
kubectl -n api get deploy api
```

예상 출력:

```text
yes
NAME   READY   UP-TO-DATE   AVAILABLE
api    1/1     1            1
```

## 절차

1. Pod 상태와 재시작 횟수를 확인한다.

```bash
kubectl -n api get pods -l app=api
kubectl -n api describe pod -l app=api | grep -A8 -E 'Liveness|Readiness|Events'
```

예상 출력:

```text
Liveness:  http-get http://:8080/health
Readiness: http-get http://:8080/ready
```

2. 외부 경로에서 liveness/readiness 응답을 확인한다.

```bash
curl -k -i https://api.cledyu.local/health
curl -k -i https://api.cledyu.local/ready
```

예상 출력:

```text
HTTP/2 200
{"status":"ok","version":"0.1.0"}
```

`/ready`는 `checks`에 Keycloak/KubeVirt/Kafka 상태를 표시한다. 외부 의존성이
`degraded`여도 Lab DSL 콘텐츠가 로드되어 있으면 Pod는 ready 상태일 수 있다.

3. API 로그에서 probe 실패 원인을 확인한다.

```bash
kubectl -n api logs deploy/api --tail=100
```

예상 출력:

```text
server started {"addr":":8080"}
request {"method":"GET","path":"/health","status":200}
request {"method":"GET","path":"/ready","status":200}
```

## 검증

```bash
kubectl -n api rollout status deploy/api
kubectl -n api get deploy api
curl -k -s https://api.cledyu.local/ready
```

성공 기준:

- Deployment rollout이 완료된다.
- `READY`가 `1/1`이다.
- `/ready`가 `status: ok`를 반환한다.

## 롤백

probe 변경 후 rollout이 실패하면 직전 정상 revision으로 되돌린다.

```bash
kubectl -n api rollout history deploy/api
kubectl -n api rollout undo deploy/api
kubectl -n api rollout status deploy/api
```

GitOps 기준으로는 문제가 된 PR을 revert한 뒤 ArgoCD sync를 다시 수행한다.

## 참고

- `/health`: Kubernetes livenessProbe. 외부 의존성을 확인하지 않는다.
- `/ready`: Kubernetes readinessProbe. Lab DSL 로드 실패만 트래픽 차단 조건으로 사용하고, Keycloak/KubeVirt/Kafka 상태는 `checks`에만 표시한다.
