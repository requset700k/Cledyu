# Kafka 운영 런북

Strimzi KRaft 모드 Kafka 클러스터(브로커 3대) 운영 절차.

## 선행 조건

- `kubectl get pods -n kafka` 에서 브로커 3대 Running 확인
- ArgoCD `data-kafka-cluster` Application Synced 확인

```bash
kubectl get pods -n kafka
kubectl get kafka cledyu-kafka -n kafka
kubectl get kafkatopic -n kafka
```

---

## 1. 토픽 추가

### 절차

1. `gitops/apps/kafka-cluster/topics/` 에 yaml 파일 생성:

   ```yaml
   apiVersion: kafka.strimzi.io/v1beta2
   kind: KafkaTopic
   metadata:
     name: <토픽-이름>
     namespace: kafka
     labels:
       strimzi.io/cluster: cledyu-kafka
   spec:
     partitions: 6        # 컨슈머 수에 맞게 조정 (lab-events는 12, 나머지는 6)
     replicas: 3
     config:
       retention.ms: "604800000"    # 7일
       min.insync.replicas: "2"
       cleanup.policy: delete
   ```

2. Git push → ArgoCD 자동 sync:

   ```bash
   git add gitops/apps/kafka-cluster/topics/<토픽-이름>.yaml
   git commit -m "feat(data): add <토픽-이름> topic"
   git push
   ```

### 검증

```bash
kubectl get kafkatopic <토픽-이름> -n kafka
```

기대 출력:

```
NAME           CLUSTER         PARTITIONS   REPLICATION FACTOR   READY
<토픽-이름>   cledyu-kafka    6            3                    True
```

### 롤백

```bash
# yaml 파일 삭제 후 push → ArgoCD prune으로 자동 제거
git rm gitops/apps/kafka-cluster/topics/<토픽-이름>.yaml
git commit -m "revert(data): remove <토픽-이름> topic"
git push
```

---

## 2. 토픽 제거

토픽 yaml 파일 삭제 후 push. ArgoCD `prune: true` 설정으로 자동 삭제됨.

> ⚠️ 토픽 삭제 시 데이터도 함께 삭제됨. 컨슈머 연결 여부 먼저 확인.

```bash
# 컨슈머 그룹 확인
kubectl run kcat-check --rm --attach --restart=Never -n kafka \
  --image=edenhill/kcat:1.7.1 \
  --overrides='{"spec":{"securityContext":{"runAsNonRoot":true,"runAsUser":1000,"seccompProfile":{"type":"RuntimeDefault"}},"containers":[{"name":"kcat-check","image":"edenhill/kcat:1.7.1","command":["sh","-c","kcat -b cledyu-kafka-kafka-bootstrap.kafka.svc:9092 -L | grep <토픽-이름>"],"securityContext":{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]}}}]}}'
```

---

## 3. 브로커 Scale

### 스케일 업 (예: 3 → 5대)

`gitops/apps/kafka-cluster/kafka-nodepool.yaml` 의 `replicas` 값 수정:

```yaml
spec:
  replicas: 5    # 기존 3에서 변경
```

Git push → ArgoCD sync → Strimzi가 새 브로커 Pod 자동 생성.

### 검증

```bash
kubectl get pods -n kafka -l app.kubernetes.io/name=kafka
kubectl get kafkanodepool -n kafka
```

### 주의사항

- 스케일 다운 시 해당 브로커의 파티션이 다른 브로커로 재분배된 뒤 Pod 종료됨
- 재분배 완료 전 강제 삭제 금지

---

## 4. CA 인증서 갱신 (CA Rotate)

`ca-sync-watcher` Deployment가 ConfigMap 해시를 60초마다 체크해 변경 감지 시 자동으로 Strimzi Secret에 새 체인을 반영한다. 수동 개입 불필요.

### 자동 갱신 흐름

```
cert-manager CA 갱신
  → trust-manager Bundle이 cledyu-root-ca-bundle ConfigMap 업데이트
  → ca-sync-watcher가 해시 변경 감지 (최대 60초 지연)
  → Strimzi Secret 자동 재동기화
  → Strimzi rolling restart
```

### 검증

```bash
# watcher 동작 확인
kubectl logs -n kafka deploy/kafka-ca-sync-watcher

# 브로커 재시작 확인
kubectl get pods -n kafka -w
```

---

## 5. 동작 검증 (produce → consume 왕복)

kafka 네임스페이스가 PodSecurity `restricted` 정책이므로 반드시 `-n kafka` 와 `--overrides` 를 함께 사용해야 한다.

**produce:**

```bash
kubectl run kcat-prod --rm --attach --restart=Never -n kafka \
  --image=edenhill/kcat:1.7.1 \
  --overrides='{"spec":{"securityContext":{"runAsNonRoot":true,"runAsUser":1000,"seccompProfile":{"type":"RuntimeDefault"}},"containers":[{"name":"kcat-prod","image":"edenhill/kcat:1.7.1","command":["sh","-c","echo test-message | kcat -b cledyu-kafka-kafka-bootstrap.kafka.svc:9092 -t lab-events -P -e"],"securityContext":{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]}}}]}}'
```

**consume:**

```bash
kubectl run kcat-cons --rm --attach --restart=Never -n kafka \
  --image=edenhill/kcat:1.7.1 \
  --overrides='{"spec":{"securityContext":{"runAsNonRoot":true,"runAsUser":1000,"seccompProfile":{"type":"RuntimeDefault"}},"containers":[{"name":"kcat-cons","image":"edenhill/kcat:1.7.1","command":["sh","-c","kcat -b cledyu-kafka-kafka-bootstrap.kafka.svc:9092 -t lab-events -C -e -o beginning -c 1"],"securityContext":{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]}}}]}}'
```

`test-message` 출력되면 정상. `--rm` 옵션으로 pod는 자동 삭제됨.

---

## 6. 트러블슈팅

### 브로커 Pod가 Pending 상태

```bash
kubectl describe pod <pod-name> -n kafka
```

원인별 해결:

| 원인 | 해결 |
|---|---|
| PVC 미생성 | `kubectl get pvc -n kafka` 확인, Longhorn 상태 점검 |
| anti-affinity 충돌 | 노드 수 부족. 브로커 수 줄이거나 노드 추가 |
| 리소스 부족 | `kubectl describe node \| grep -A 5 "Allocated resources"` 로 가용 리소스 확인 |

### 브로커 Pod가 CrashLoopBackOff

```bash
kubectl logs <pod-name> -n kafka --previous
```

원인별 해결:

| 원인 | 해결 |
|---|---|
| CA Secret 없음 또는 라벨 누락 | `kubectl logs -n kafka deploy/kafka-ca-sync-watcher` 로 watcher 동작 확인. 실패 시 watcher pod 재시작 |
| PVC 권한 오류 | `fsGroup: 1000` 설정 확인, PVC 재생성 필요할 수 있음 |
| JVM 메모리 부족 | `jvmOptions` `-Xmx` 값 조정 |

### KafkaTopic이 Ready가 안 됨

```bash
kubectl describe kafkatopic <토픽-이름> -n kafka
```

entity-operator (Topic Operator) 로그 확인:

```bash
kubectl logs -n kafka -l app.kubernetes.io/name=entity-operator -c topic-operator
```

### ArgoCD OutOfSync 노이즈

`data-kafka-cluster` Application에 `ignoreDifferences`가 적용돼 있어 Kafka / KafkaTopic CR의 `.status` drift는 무시됨. OutOfSync가 표시되면 실제 변경사항이 있는 리소스를 확인할 것.

```bash
kubectl get application data-kafka-cluster -n argocd -o json \
  | python3 -c "import json,sys; [print(r['kind']+'/'+r['name']) for r in json.load(sys.stdin)['status']['resources'] if r.get('status')=='OutOfSync']"
```

---

## 참고

- Strimzi 공식 문서: https://strimzi.io/docs/operators/latest/
- 관련 파일:
  - `gitops/apps/kafka-cluster/` — Kafka 클러스터 매니페스트
  - `gitops/apps/kafka-cluster/topics/` — 토픽 yaml
  - `gitops/apps/trust-manager/` — CA 공개키 분배
  - `gitops/apps/kafka-cluster/ca-secret-sync.yaml` — CA Secret 변환 Job (최초 배포 시)
  - `gitops/apps/kafka-cluster/ca-sync-watcher.yaml` — CA 자동 갱신 감지 Deployment
