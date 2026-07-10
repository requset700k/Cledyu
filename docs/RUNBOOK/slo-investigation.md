# SLO/SLI 런북

## 트리거

- Alertmanager가 `*SLO` 알림을 보낸다.
- PR 또는 배포 후 `.github/PULL_REQUEST_TEMPLATE.md`의 SLO 항목에 영향이 있다고 판단된다.
- 사용자 제보로 Lab 시작, Validation, AI 힌트, WebSocket, API 응답 품질 저하가 의심된다.
- 주간 운영 점검에서 error budget burn rate 또는 30일 SLO 추세를 확인한다.

## 현재 기준

| 구분 | 의미 | Cledyu에서의 사용 |
|---|---|---|
| SLI | 사용자가 체감하는 품질을 수치화한 지표 | Prometheus metric 또는 Sloth SLI query |
| SLO | 내부 운영 목표 | `gitops/apps/sloth/*.yaml`의 `objective` |

현재 저장소의 확정된 운영 기준은 **SLO**다. 외부 보상, 크레딧, 계약 위반 판단은 이 런북만으로
결정하지 않는다. SLA가 필요한 경우 제품/운영 책임자가 별도 문서에 보상 조건, 제외 조건,
측정 기간, 공지 절차를 명시해야 한다.

## 사전 조건

- `kubectl`이 Cledyu 클러스터에 인증되어 있어야 한다.
- Prometheus, Alertmanager, Sloth 리소스 조회 권한이 있어야 한다.
- Grafana `Cledyu API & Validation Tempo Bottleneck` 대시보드와 Tempo Explore 조회 권한이 있어야 한다.
- 실클러스터 직접 접속이 어려운 PR 리뷰 상황에서는 `gitops/apps/sloth/*.yaml`, Grafana 링크,
  CI 결과, 운영자가 첨부한 Prometheus 쿼리 결과를 기준으로 판단한다.

```bash
kubectl auth can-i get prometheusrules -n monitoring
kubectl auth can-i get prometheusservicelevels -n monitoring
kubectl -n monitoring get prometheusservicelevels
```

예상 출력:

```text
yes
yes
NAME             AGE
api-slo          1d
lab-slo          1d
kafka-slo        1d
kubevirt-slo     1d
traefik-slo      1d
```

## 주요 SLO/SLI

| 영역 | SLO | SLI 소스 |
|---|---|---|
| API 요청 가용성 | 99.5% | `http_requests_total{path=~"/api/v1/.*", path!="/api/v1/sessions/:id/ws", path!="/api/v1/sessions/:id/ide/*idepath", status=~"5.."}` / `http_requests_total{path=~"/api/v1/.*", path!="/api/v1/sessions/:id/ws", path!="/api/v1/sessions/:id/ide/*idepath"}` |
| API 요청 지연 | 99.5% 요청 1초 이내 | `http_request_duration_seconds_bucket{path=~"/api/v1/.*", path!="/api/v1/sessions/:id/ws", path!="/api/v1/sessions/:id/ide/*idepath", le="1"}` |
| 세션 생성 API 가용성 | 99.5% | `http_requests_total{method="POST", path="/api/v1/sessions", status=~"5.."}` / `http_requests_total{method="POST", path="/api/v1/sessions"}` |
| 온프렘 Lab 시작 | 99.0% 세션 7분 이내 Ready | `lab_start_total`, `lab_startup_duration_seconds_bucket{env="onprem", le="420"}` |
| EC2 Lab 시작 | 99.0% 세션 10분 이내 running | `lab_start_total`, `lab_startup_duration_seconds_bucket{env="ec2", le="600"}` |
| Lab 시작 성공률 | 99.0% | `lab_start_total{result!="success"}` / `lab_start_total` |
| VM 부팅 성공률 | 99.5% | `vm_boot_total{result="failed"}` / `vm_boot_total` |
| WebSocket 안정성 | 99.0% | `ws_connection_drop_total{result="error"}` / `ws_connection_established_total` |
| Validation 지연 | 99.0% 요청 10초 이내 | `validation_duration_seconds_bucket{le="10"}` |
| AI 힌트 지연 | 99.0% 요청 5초 이내 | `ai_hint_latency_seconds_bucket{le="5"}` |
| Traefik 가용성 | 99.9% | `traefik_service_requests_total{code=~"5.."}` |
| Traefik 지연 | 99.9% 요청 1.2초 이내 | `traefik_service_request_duration_seconds_bucket{le="1.2"}` |
| KubeVirt 컴포넌트 가용성 | 99.9% | `up{job="kubevirt-prometheus-metrics", container=...}` |
| Kafka 가용성 | 99.9% | `kafka_controller_offlinepartitionscount` / `kafka_controller_globalpartitioncount` |
| Kafka Produce 지연 | 99.5% | `kafka_network_requestmetrics_totaltimems_50thpercentile{request="Produce"}` |

정식 정의는 `gitops/apps/sloth/api-slo.yaml`, `gitops/apps/sloth/lab-slo.yaml`,
`gitops/apps/sloth/traefik-slo.yaml`, `gitops/apps/sloth/kubevirt-slo.yaml`,
`gitops/apps/sloth/kafka-slo.yaml`을 우선한다.

## 주요 진단 지표

아래 지표는 Grafana 병목 대시보드와 장애 조사에서 사용하지만, 현재 단일 SLO 알림으로
정의되어 있지는 않다.

| 영역 | 확인 목적 | 지표 |
|---|---|---|
| Validation Kafka 흐름 | 검증 요청/결과/DLQ 흐름과 consumer lag 확인 | `kafka_server_brokertopicmetrics_messagesinpersec_oneminuterate{topic=~"validation-requests|validation-results|validation-requests-dlq"}`, `kafka_consumergroup_lag` |

## 관측성 대시보드 맵

SLO 알림이나 사용자 제보가 들어오면 아래 네 대시보드를 증상에 맞게 함께 본다.

| 대시보드 | 파일 | 먼저 볼 때 | 핵심 확인 |
|---|---|---|---|
| `Lab SLO Dashboard` | `infra/kubernetes/monitoring/dashboard-lab-slo.yaml` | Lab 시작, VM 부팅, WebSocket, Validation, AI 힌트 SLO가 흔들릴 때 | SLO Summary, Startup Details, Interactive Paths, Validation Latency, AI Hint Latency, Sloth Error Budgets |
| `Platform SLO Burndown` | `infra/kubernetes/monitoring/dashboard-slo-burndown.yaml` | error budget 소모 속도와 플랫폼 공통 SLO 영향을 볼 때 | Traefik, KubeVirt, Kafka의 error budget remaining, current burn rate, 7일 burndown |
| `Cilium Network Overview` | `infra/kubernetes/monitoring/dashboard-cilium-metrics.yaml` | API/VM/Kafka/validation-engine 간 통신이 느리거나 끊기는 의심이 있을 때 | 현재 드롭률, 총 드롭, 이벤트 유실, 정책 차단, reason별 drop rate, verdict별 흐름 |
| `Cledyu API & Validation Tempo Bottleneck` | `infra/kubernetes/monitoring/dashboard-bottleneck.yaml` | API 병목, Validation 지연, Kafka 검증 흐름, validation-engine trace를 이어서 볼 때 | API Overview, API Internal Operations, Kafka Validation Flow, Validation Engine Tempo |

대시보드별 역할:

- `Lab SLO Dashboard`: 사용자 체감 SLO의 현재 상태를 먼저 본다. Lab 시작/VM 부팅/WebSocket/Validation/AI 힌트가 어느 축에서 깨지는지 분리한다.
- `Platform SLO Burndown`: 특정 순간값보다 error budget 소모 추세가 중요한 경우 본다. Traefik, KubeVirt, Kafka 중 어떤 플랫폼 계층이 SLO budget을 태우는지 확인한다.
- `Cilium Network Overview`: 서비스 자체 로그가 깨끗한데 timeout, reconnect, Kafka lag, VM 접근 실패가 같이 보이면 본다. 정책 차단과 Hubble 이벤트 유실을 구분한다.
- `Cledyu API & Validation Tempo Bottleneck`: API 요청량/지연, validation 왕복 지연, Kafka 요청/결과/DLQ 흐름, Tempo trace를 한 화면에서 연결해 본다.

## 절차

1. 어떤 SLO가 타고 있는지 확인한다.

```bash
kubectl -n monitoring get prometheusrules | grep -E 'sloth|slo'
kubectl -n monitoring get prometheusservicelevels
kubectl -n monitoring get prometheusservicelevels -o yaml | sed -n '1,260p'
```

예상 출력:

```text
api-slo
lab-slo
kafka-slo
kubevirt-slo
traefik-slo
```

2. 현재 firing 알림을 확인한다.

```bash
kubectl -n monitoring port-forward svc/kps-alertmanager 9093:9093 >/tmp/cledyu-alertmanager-pf.log 2>&1 &
AM_PF_PID=$!
trap 'kill $AM_PF_PID 2>/dev/null || true' EXIT
until curl -sf http://127.0.0.1:9093/-/ready >/dev/null; do sleep 1; done

curl -s http://127.0.0.1:9093/api/v2/alerts \
  | jq -r '.[] | select(.status.state=="active") | [.labels.alertname,.labels.severity,.labels.service,.labels.slo] | @tsv'

kill $AM_PF_PID 2>/dev/null || true
trap - EXIT
```

예상 출력:

```text
LabStartupOnpremSLO	critical	lab	startup-onprem
```

3. Grafana 대시보드에서 범위를 먼저 좁힌다.

| 알림/증상 | 먼저 볼 대시보드 | 이어서 볼 대시보드 |
|---|---|---|
| `APIAvailabilitySLO`, `APILatencySLO`, `APISessionCreationSLO` | `Cledyu API & Validation Tempo Bottleneck` | `Lab SLO Dashboard`, API 로그 |
| `LabStartupOnpremSLO`, `LabStartupEC2SLO`, `LabStartSuccessRateSLO`, `LabVMBootSuccessRateSLO` | `Lab SLO Dashboard` | `Cilium Network Overview`, KubeVirt 로그 |
| `LabValidationLatencySLO` | `Lab SLO Dashboard` | `Cledyu API & Validation Tempo Bottleneck`, `Platform SLO Burndown`의 Kafka |
| `LabAIHintLatencySLO` | `Lab SLO Dashboard` | `Cledyu API & Validation Tempo Bottleneck`, ai-tutor 로그 |
| `LabWebSocketStabilitySLO` | `Lab SLO Dashboard` | `Cilium Network Overview`, Traefik 로그 |
| `Kafka*SLO` | `Platform SLO Burndown` | `Cledyu API & Validation Tempo Bottleneck`의 Kafka Validation Flow |
| `Traefik*SLO` | `Platform SLO Burndown` | `Cilium Network Overview`, Ingress/backend readiness |
| 네트워크 drop, 정책 차단, pod 간 timeout | `Cilium Network Overview` | 해당 서비스 로그와 NetworkPolicy |

4. Prometheus에서 SLI 원시값을 확인한다.

```bash
kubectl -n monitoring port-forward svc/kps-prometheus 9090:9090 >/tmp/cledyu-prometheus-pf.log 2>&1 &
PROM_PF_PID=$!
trap 'kill $PROM_PF_PID 2>/dev/null || true' EXIT
until curl -sf http://127.0.0.1:9090/-/ready >/dev/null; do sleep 1; done
```

API 5xx 비율:

```bash
curl -G -s http://127.0.0.1:9090/api/v1/query \
  --data-urlencode 'query=sum(rate(http_requests_total{path=~"/api/v1/.*",path!="/api/v1/sessions/:id/ws",path!="/api/v1/sessions/:id/ide/*idepath",status=~"5.."}[30m])) / sum(rate(http_requests_total{path=~"/api/v1/.*",path!="/api/v1/sessions/:id/ws",path!="/api/v1/sessions/:id/ide/*idepath"}[30m]))' \
  | jq '.data.result'
```

Lab 시작 실패율:

```bash
curl -G -s http://127.0.0.1:9090/api/v1/query \
  --data-urlencode 'query=(sum(rate(lab_start_total{result!="success"}[30m])) or vector(0)) / sum(rate(lab_start_total[30m]))' \
  | jq '.data.result'
```

Validation 10초 초과율:

```bash
curl -G -s http://127.0.0.1:9090/api/v1/query \
  --data-urlencode 'query=(sum(rate(validation_duration_seconds_count[30m])) - sum(rate(validation_duration_seconds_bucket{le="10"}[30m]))) / sum(rate(validation_duration_seconds_count[30m]))' \
  | jq '.data.result'
```

Validation Kafka 토픽 유입:

```bash
curl -G -s http://127.0.0.1:9090/api/v1/query \
  --data-urlencode 'query=sum by (topic) (kafka_server_brokertopicmetrics_messagesinpersec_oneminuterate{topic=~"validation-requests|validation-results|validation-requests-dlq"})' \
  | jq '.data.result'
```

Validation Kafka consumer lag:

```bash
curl -G -s http://127.0.0.1:9090/api/v1/query \
  --data-urlencode 'query=sum(kafka_consumergroup_lag{topic="validation-requests",consumergroup="validation-engine"})' \
  | jq '.data.result'

curl -G -s http://127.0.0.1:9090/api/v1/query \
  --data-urlencode 'query=sum(kafka_consumergroup_lag{topic="validation-results",consumergroup="cledyu-api-validation-results"})' \
  | jq '.data.result'
```

주의:

- `validation-requests`, `validation-results`, `validation-requests-dlq` 토픽 유입 패널은 no data를 `0`으로 보정하지 않는다.
- 값이 `0`이면 해당 시점에 유입이 없는 것이고, no data면 Kafka JMX scrape, ServiceMonitor, metricsConfig, 토픽 MBean 생성 여부를 먼저 확인한다.

Cilium 네트워크 드롭:

```bash
curl -G -s http://127.0.0.1:9090/api/v1/query \
  --data-urlencode 'query=sum(rate(hubble_drop_total[5m])) by (reason)' \
  | jq '.data.result'
```

Hubble 이벤트 유실:

```bash
curl -G -s http://127.0.0.1:9090/api/v1/query \
  --data-urlencode 'query=sum(increase(hubble_lost_events_total[1h]))' \
  | jq '.data.result'
```

5. 영향 범위를 분리한다.

```bash
kubectl -n api get deploy,pods
kubectl -n api logs deploy/api --since=30m --tail=300
kubectl -n kubevirt get pods
kubectl -n kafka get kafka,pods
kubectl -n traefik get pods
kubectl -n kube-system get pods -l k8s-app=cilium
```

판단 기준:

- `APIAvailabilitySLO`, `APILatencySLO`, `APISessionCreationSLO`: `api` 로그, readiness, API request/latency 패널, 최근 배포를 확인한다.
- `LabStartupOnpremSLO`, `LabStartupEC2SLO`: KubeVirt/EC2 overflow, PVC/DataVolume, 이미지 pull, namespace quota를 확인한다.
- `LabStartSuccessRateSLO`, `LabVMBootSuccessRateSLO`: `api` 로그, VM 생성/부팅 이벤트, provider별 실패 라벨, 최근 배포를 확인한다.
- `LabValidationLatencySLO`: `validation-engine`, Kafka lag, VM SSH/exec 실패를 확인한다.
- `LabAIHintLatencySLO`: `ai-tutor`, Gemini quota/rate limit, ChromaDB/RAG 검색 지연을 확인한다.
- `LabWebSocketStabilitySLO`: Traefik idle timeout, browser reconnect, VM SSH 연결, provider별 drop 라벨을 확인한다.
- `Kafka*SLO`: Strimzi Kafka broker, offline partition, disk pressure, ISR shrink를 확인한다.
- `KubeVirt*SLO`: `virt-api`, `virt-controller`, `virt-handler`, 노드 상태를 확인한다.
- `Traefik*SLO`: upstream별 5xx, Ingress 경로, TLS/cert, backend readiness를 확인한다.
- 네트워크 이상: Cilium drop reason, policy denied, Hubble lost events를 확인한다.

### Validation/Kafka 병목 대시보드 확인

Grafana의 `Cledyu API & Validation Tempo Bottleneck` 대시보드에서 아래 순서로 확인한다.

1. `API Internal Operations`
   - `Validation latency`: API가 검증 요청을 발행한 뒤 `validation-results`를 받아 Redis start time과 매칭한 왕복 지연이다.
   - `AI hint latency`: API가 AI 힌트를 요청하고 응답을 받은 지연이다.
   - 실제 API 경로를 탄 요청이 없으면 latency 패널에 표시할 샘플도 없다.

2. `Kafka Validation Flow`
   - `validation-requests 유입`: Session API가 Kafka에 검증 요청을 넣는지 확인한다.
   - `validation-results 유입`: validation-engine이 검증 결과를 Kafka에 넣는지 확인한다.
   - `validation DLQ 유입`: 처리 실패 메시지가 `validation-requests-dlq`로 빠지는지 확인한다. 0이 정상이며, 증가하면 handler 오류나 메시지 계약 문제를 본다.
   - `검증 consumer lag`: `validation-engine`의 `validation-requests` 소비와 API의 `validation-results` 소비가 밀리는지 확인한다.

3. `Validation Engine Tempo`
   - `최근 validation-engine trace`에서 `validation-engine` span을 확인한다.
   - TraceQL에서 직접 볼 때는 `{ resource.service.name = "validation-engine" }` 또는 `{ name = "validation_engine.handle" }`를 사용한다.
   - Kafka에 직접 넣은 수동 테스트 메시지는 API root span이 없어 `<root span not yet received>`로 보일 수 있다. 실제 앱 트래픽은 API가 주입한 `traceparent`로 `cledyu-api` span과 `validation_engine.handle` span이 같은 trace에 연결되어야 한다.

판단 기준:

| 증상 | 우선 확인 |
|---|---|
| `Validation latency` 상승, `validation-requests` 유입 정상, `validation-results` 유입 지연 | `validation-engine` 로그, VM SSH/exec, consumer lag |
| `validation-requests` 유입 없음 | Session API publish 경로, Kafka 인증서, `api.validation.publish_kafka` trace |
| `validation-results` 유입 없음 | validation-engine 처리 오류, result producer, DLQ 유입 |
| DLQ 유입 증가 | 메시지 JSON 계약, 빈 `checks`, handler 오류 로그 |
| Kafka topic 패널 no data | Kafka JMX scrape, `kafka-metrics` ServiceMonitor, metricsConfig, 토픽 MBean 생성 여부 |
| consumer lag 증가 | validation-engine/API 레플리카 상태, Kafka broker 부하, consumer group 상태 |

6. 최근 변경과 상관관계를 확인한다.

```bash
kubectl -n argocd get applications
kubectl -n argocd get applications -o json \
  | jq -r '.items[] | [.metadata.name,.status.sync.status,.status.health.status,.status.operationState.finishedAt] | @tsv'
git log --oneline --since='24 hours ago' -- gitops apps infra docs/RUNBOOK
```

예상 출력:

```text
service-api	Synced	Healthy	2026-07-08T10:20:11Z
```

7. 사용자 영향과 error budget 상태를 기록한다.

```bash
curl -G -s http://127.0.0.1:9090/api/v1/query \
  --data-urlencode 'query=slo:period_error_budget_remaining:ratio' \
  | jq -r '.data.result[] | [.metric.sloth_service,.metric.sloth_slo,.value[1]] | @tsv'
```

조사가 끝나면 Prometheus port-forward를 정리한다.

```bash
kill $PROM_PF_PID 2>/dev/null || true
trap - EXIT
```

기록할 항목:

- 알림명, 시작 시각, 종료 시각
- 영향 기능과 추정 사용자 수
- SLI 원시값, SLO 목표, error budget remaining
- 최근 배포/설정 변경
- 완화 조치와 재발 방지 항목

## 검증

아래를 모두 만족하면 일단 완화 완료로 본다.

```bash
kubectl -n monitoring port-forward svc/kps-alertmanager 9093:9093 >/tmp/cledyu-alertmanager-pf.log 2>&1 &
AM_PF_PID=$!
kubectl -n monitoring port-forward svc/kps-prometheus 9090:9090 >/tmp/cledyu-prometheus-pf.log 2>&1 &
PROM_PF_PID=$!
trap 'kill $AM_PF_PID $PROM_PF_PID 2>/dev/null || true' EXIT
until curl -sf http://127.0.0.1:9093/-/ready >/dev/null; do sleep 1; done
until curl -sf http://127.0.0.1:9090/-/ready >/dev/null; do sleep 1; done

curl -s http://127.0.0.1:9093/api/v2/alerts \
  | jq -r '.[] | select(.status.state=="active") | .labels.alertname' \
  | grep -E 'SLO' || true

curl -G -s http://127.0.0.1:9090/api/v1/query \
  --data-urlencode 'query=up{job=~"api|kubevirt-prometheus-metrics|kafka.*|traefik.*"}' \
  | jq '.data.result'

kill $AM_PF_PID $PROM_PF_PID 2>/dev/null || true
trap - EXIT
```

성공 기준:

- 동일 `*SLO` 알림이 더 이상 active 상태가 아니다.
- 해당 서비스의 `up` 메트릭이 정상이다.
- 영향 기능을 실제 경로로 재검증했다.
- 30분 이상 같은 증상으로 재발하지 않는다.

기능별 재검증:

```bash
curl -k -i https://api.cledyu.local/ready
kubectl -n api rollout status deploy/api
kubectl -n kubevirt get vmi -A
kubectl -n kafka get kafka cledyu-kafka
kubectl -n validation-engine rollout status deploy/validation-engine
```

## 롤백

최근 GitOps 변경이 원인일 가능성이 높으면 문제가 된 PR을 revert하고 ArgoCD sync를 수행한다.

```bash
git revert <commit-sha>
git push
argocd app sync <app-name>
argocd app wait <app-name> --health --sync
```

서비스 배포만 문제면 직전 정상 revision으로 되돌린다.

```bash
kubectl -n api rollout history deploy/api
kubectl -n api rollout undo deploy/api
kubectl -n api rollout status deploy/api
```

Sloth SLO 정의 변경 자체가 잘못되어 오탐을 만든 경우에는 `gitops/apps/sloth/*.yaml`을 revert한다.
단, 실제 사용자 영향이 있는 알림을 단순히 임계값 완화로 숨기지 않는다.

## 참고

- SLO 정의: `gitops/apps/sloth/*.yaml`
- Prometheus/Grafana 스택: `gitops/apps/kube-prometheus-stack/values.yaml`
- API metrics scrape: `gitops/apps/api/templates/servicemonitor.yaml`
- 병목 대시보드: `infra/kubernetes/monitoring/dashboard-bottleneck.yaml`
- PR SLO 체크리스트: `.github/PULL_REQUEST_TEMPLATE.md`
- API probe triage: `docs/RUNBOOK/api-probe-triage.md`
- EC2 overflow: `docs/RUNBOOK/ec2-overflow.md`
- Kafka 운영: `docs/RUNBOOK/kafka.md`
