# 풀서비스 EKS DR 재설계 (pilot-light + 실습 스택)

- 날짜: 2026-07-11
- 브랜치: `feat/dr-eks-overlay` (후속 플랜에서 분리 가능)
- 관련: [[project_dr_plan_b_eks_overlay]] · [[project_dr_scope_delegation]] · [[project_analytics_pipeline]] · [[project_kafka_auth_gap]]

## 1. 배경 · 문제

현행 EKS DR(Plan B)은 **Cold DR**로, 재해 시 **사이트+데이터만** 재현하고 **실습 스택(KubeVirt·Kafka·validation-engine) 전체를 제거**했다. 이는 프로젝트의 실제 목표와 어긋난다.

**실제 목표:** 온프렘 마비 시 **풀서비스(사이트·데이터·실습)를 그대로 AWS에서 제공**한다. 비용은 **pilot-light**로 최소화한다 — EKS 컨트롤플레인을 상시 띄워 두고 노드(앱)는 평시 0으로 내려, 재해 시 미리 띄워둔 EKS 위에 노드만 스케일업해 빠르게 대처한다.

**핵심 교정:** "실습하려면 KubeVirt+metal 노드가 필요해 비용이 급증한다"는 분석은 이 프로젝트엔 틀렸다. 프로젝트엔 이미 **EC2 기반 실습 백엔드**가 완성돼 있다:
- `apps/api/internal/ec2/provisioner.go` — EC2 인스턴스로 실습 VM을 띄우는 `session.Provider`.
- `apps/validation-engine/internal/executor/ec2.go` — 검증엔진이 EC2 VM에 **SSM SendCommand**로 채점(SSH·nested-virt 불필요).
- `infra/terraform/aws/image-baker.tf` + launch template `lt-03243d5c74802ddfd` — EC2 실습 이미지 파이프라인.

즉 DR 실습 = **EC2**(KubeVirt/metal 아님). 이는 아키텍처의 "EC2 버스트" 기둥 그 자체다(온프렘 KubeVirt 만석 시 AWS로 버스트 → 재해 = 온프렘 부재의 최종 버스트). 현행 DR 오버레이가 바로 이 경로를 `aws.enabled=false`로 꺼버린 것이 문제였다.

## 2. 목표 상태 (target architecture)

### 2.1 pilot-light 클러스터 모델

**상시 warm (~$73/mo):**
```
EKS 컨트롤플레인 + VPC + IRSA + 노드그룹(desired=0)
└ etcd: ArgoCD + root-app-eks + 모든 Application CR (셋업 시 1회 seed → 파드 Pending)
```
- 노드 0이라 노드 비용 0. 컨트롤플레인만 과금.
- NAT · VPC 인터페이스 엔드포인트 · bastion 은 **상시가 아니라 재해 시 생성**(비용 최적화, ~$150→~$73/mo). RTO +2~3분 감수.

**셋업(최초 1회):** 임시 bastion(SSM)으로 `helm install argocd` + `kubectl apply root-app-eks` → CR이 **etcd에 저장**(노드 0이라 파드는 Pending, 하지만 desired state 확정). `kubectl/helm apply`는 API 서버(=컨트롤플레인, 상시)에만 쓰므로 노드 불필요. seed 후 임시 bastion 파기 — etcd CR은 컨트롤플레인에 영속.

### 2.2 페일오버 (Approach A — warm etcd + terraform 스케일)

```bash
# 재해 트리거 (단일 apply — DR -target 목록 유지, tfvars 없어 전체 apply 금지)
terraform apply -var eks_dr_active=true -var eks_dr_node_desired=N <DR -target 목록>
#   → NAT + VPC 엔드포인트 + bastion + 노드 N개 생성
#   → Pending 파드 전부 스케줄 → ArgoCD 가 git(main)에서 최신 pull 해 wave 순서로 자동 수렴
#      (cert-manager → PKI → ESO → strimzi → kafka → cnpg → vault → validation-engine → api/web)
# Vault 스냅샷 복원 (수동 break-glass — 유일한 명령형 스텝, 기존 런북 절차)
# api/web rollout restart (의존성 Ready 후) + 공개 DNS 전환
```
RTO 목표 ~8~10분 (cold ~15~20분 대비 단축: 컨트롤플레인 사전 생성 + 앱 사전 seed).

**failback:** 온프렘 복구 후 `eks_dr_active=false` + `eks_dr_node_desired=0` + DNS 원복 → 노드·NAT·엔드포인트·bastion 소멸, 클러스터는 warm 유지.

### 2.3 실습 실행 흐름 (재해 시)

```
사용자 실습 요청 → api 가 launch template 로 EC2 인스턴스 생성(tailnet 조인)
   → 사용자: tailnet 터미널로 VM 접속·명령 실행
   → validation-engine: SSM SendCommand 로 VM 상태 채점 → 결과 Kafka(validation-results)
   → api: 결과를 Postgres(session_steps/progress/completions)에 영속화
```

## 3. 실습 스택 (Plan A)

### 3.1 apps-eks 에 추가할 Application (기존 gitops 앱 재사용)

| 앱 | 내용 | 데이터 복원 |
|---|---|---|
| `strimzi-operator` | Kafka/KafkaTopic/KafkaUser CRD·오퍼레이터 (wave 선행, cnpg-operator 패턴) | — |
| `kafka-cluster` | Strimzi Kafka(mTLS 9093) + 토픽 3종(validation-requests/results/dlq) | **클러스터·토픽 구조는 온프렘과 동일 배포**. 큐의 in-flight 메시지만 미복원 |
| `validation-engine` | 검증엔진 + KafkaUser + Certificate(cert-manager) | — |

**Kafka 데이터 정책 (중요 — "빈 클러스터" 아님):** 클러스터+토픽 구조는 gitops로 온프렘과 **동일하게 배포**한다. 복원하지 않는 것은 재해 순간 큐에 떠 있던 **in-flight 메시지**뿐이다. 근거: 의미 있는 검증 **결과**(진도·수료·스텝 상태)는 `session_progress`·`session_steps`·`lab_completions` 테이블에 영속화되어 **CNPG가 복원**한다(=사용자가 말한 "따로 백업"의 실체는 Postgres). 재해 순간 검증 중이던 요청은 사용자가 해당 스텝을 **재검증**하면 무손실이다. (Kafka 로그 자체의 S3 sink 는 미배포 — 비-DR 후속 항목.)

### 3.2 api `values-eks.yaml` 게이트 뒤집기

현행(fail-closed로 꺼둔 것) → 신규:
- `aws.enabled: false → true` — EC2 실습 백엔드 활성 (`launchTemplateId`·`region`·`maxActiveSessions` **상향**: 온프렘 사망 시 DR 상한을 실부하 맞게).
- `kafka.enabled: false → true` — 검증 발행이 Kafka 로 흐름.
- `kubevirt.enabled: false` **유지** — DR 백엔드는 EC2 이므로 KubeVirt 미사용. → `sessions` 는 EC2 overflow 로 non-nil(세션 API 정상), `validator` 는 validation-engine 배포로 non-nil(실검증). 따라서 §5 fail-closed 503 은 **발동하지 않고** 실습이 정상 동작.

### 3.3 ExternalSecret (ESO, Vault `cledyu/aws/*` 경로 — 드릴서 존재 확인)

현재 DR에서 disable 된 다음 ExternalSecret 을 포함:
- `cledyu-api-aws` ← `cledyu/aws/api` (AWS 키 + Tailscale authkey)
- `cledyu-validation-engine-aws` ← `cledyu/aws/validation-engine` (SSM 채점용 AWS 키)

정적 AWS 키 방식(IRSA 아님)은 기존 설계 그대로 승계.

### 3.4 CRD-missing 블로커 게이팅 (검증 스윕 A클래스)

- **CiliumNetworkPolicy** (validation-engine) — EKS 는 VPC CNI 로 Cilium CRD 가 없어 그대로 sync 하면 **블로커**. → `values-eks` 게이트로 EKS 엔 미렌더(온프렘 k3s 는 Cilium 이라 렌더). 부수효과: EKS 에선 egress 제한(ssm.* 만 허용)이 사라짐 → 필요 시 plain NetworkPolicy/SG 로 대체(DR 한시 허용 가능).
- validation-engine 차트의 다른 온프렘 종속(ServiceMonitor·Longhorn SC 등)도 렌더 실측으로 동일 점검.

## 4. pilot-light 인프라 (Plan B)

### 4.1 terraform 재구조

- `enable_eks_dr = true` 를 **상시 유지**(warm 스택 존재 여부 토글, 폐기 시만 false).
- 노드그룹: `desired_size = var.eks_dr_node_desired`(기본 0), `min_size = 0`, `max_size = N`.
- 새 게이트 `var.eks_dr_active`(기본 false): **NAT · VPC 인터페이스 엔드포인트 · bastion · 노드 스케일**을 이 값으로 gate → 평시 미생성(비용 0), 재해 시 true.
- 상시 warm = 컨트롤플레인 + VPC/서브넷/RT(무료) + IRSA(무료) + 노드그룹 정의(desired 0, 무료) + SG(무료).

### 4.2 warm etcd seed (셋업 1회)

셋업 시 `eks_dr_active=true` 로 잠시 bastion(+NAT/엔드포인트)을 올려 ArgoCD seed + root-app 적용 → etcd 에 CR 영속 → `eks_dr_active=false` 로 되돌리면 bastion/NAT/엔드포인트는 소멸하고 etcd CR(컨트롤플레인)만 남는다. 이후 ArgoCD 는 재해 시 노드가 뜨면 git `main` 에서 최신을 pull 하므로 seed 시점 staleness 없음(ArgoCD 설치 버전만 주기적 재-seed 로 갱신).

### 4.3 `-target` 규율

tfvars 부재로 전체 apply 는 프로덕션 게이트 리소스를 오-destroy 한다. 재해 트리거·failback 모두 DR `-target` 목록만 사용(1차 드릴 런북과 동일 목록).

## 5. fail-closed 코드와의 정합 (기존 수정 유지)

1차 드릴 후 넣은 fail-closed 코드는 **그대로 둔다**(방어적으로 옳음):
- `session.go`: `validator==nil && server.mode==release` → 503 (mock-pass 금지).
- `config`/`main.go`/차트: `kubevirt.enabled` 게이트 → 실행 백엔드 없으면 세션 API 503.

풀서비스 DR 에선 validation-engine·EC2 백엔드가 배포되어 `validator`·`sessions` 가 non-nil 이 되므로 이 503 들은 **발동하지 않는다**. 코드는 "백엔드가 진짜 없을 때"의 안전망으로 유지.

## 6. 리스크 · 미결 (플랜/드릴서 해소)

1. **실습 fidelity** — 온프렘(KubeVirt) 랩이 DR(EC2) 백엔드에서도 동일하게 통과하는가. 체크 로직은 백엔드 무관이나 cloud-init/VM 셋업 차이 → **드릴서 실검증**(대표 랩 6종).
2. **EC2 도달성** — validation-engine→SSM(재해 NAT/엔드포인트 경유), api·EC2→tailnet. EC2 인스턴스 SSM agent + instance profile(SendCommand 대상) 권한 확인.
3. **CiliumNetworkPolicy 대체** — EKS egress 통제 방식 결정(무통제 허용 vs NetworkPolicy/SG).
4. **maxActiveSessions 사이징** — DR EC2 상한(온프렘 부재 반영).
5. **Kafka authz 미설정** — KafkaUser ACL 미강제([[project_kafka_auth_gap]]) DR 승계, 비-DR 이슈.
6. **정적 AWS 키 수명** — 장기키 회전은 기존 이슈로 승계.

## 7. 구현 순서 (하나의 스펙 → 플랜 2개)

- **Plan A — 실습 스택 (먼저, 핵심 가치)**: strimzi-operator + kafka-cluster + validation-engine 을 apps-eks 에 추가 · api `values-eks` 게이트 뒤집기 · ExternalSecret 2종 추가 · CiliumNetworkPolicy(및 기타 CRD-missing) 게이트 · SSM/도달성 배선 · **실습 fidelity 드릴 검증**. 현 cold 모델 위에서도 검증 가능.
- **Plan B — pilot-light (그다음, RTO 최적화)**: terraform 재구조(컨트롤플레인 상시 · 노드 `desired_size` var · `eks_dr_active` 게이트로 NAT/엔드포인트/bastion) · 1회 warm seed · 페일오버 트리거 · 런북·RTO 실측.

## 8. 비목표 (YAGNI)

- KubeVirt/metal 노드를 EKS 에 올리지 않는다(EC2 로 충분).
- Kafka 메시지 로그의 S3 백업/복원(결과는 Postgres 로 영속) — 비-DR 후속.
- 관측 스택(prometheus-operator) DR 배포 — 비-DR.
