# DR Plan B — T5 완결(B1·B2·B6·B7) + destroy 하드닝 설계

> 상위 플랜: `docs/superpowers/plans/2026-07-09-dr-eks-overlay-plan-b.md`
> 브랜치: `feat/dr-eks-overlay` (terraform base `3ca5810` 위 T5 변경 staged)
> 작성: 2026-07-10

## 배경 / 문제

T5(apps-eks + api/web values-eks + 플랫폼 애드온)는 `helm template`은 통과하지만
**런타임에 api 파드가 EKS에서 기동하지 못하는 미완결 상태**다. 적대적 코드리뷰(codex 스타일)로
확인한, plan/validate가 못 잡고 런타임에만 터지는 계약들을 이 스펙에서 닫는다.

### 적대적 리뷰 발견 (심각도 순)

- **F1 (내 최초 B1 설계 결함) — trust-manager Bundle CR 누락 → api ContainerCreating.**
  api `deployment.yaml:170`이 configMap `cledyu-root-ca-bundle`을 **비-optional** 마운트(`/etc/ssl/cledyu`).
  이 CM은 trust-manager **Bundle CR**(`gitops/apps/trust-manager/cledyu-root-ca-bundle.yaml`)이
  ns 라벨(`cledyu.io/trust-bundle: root-ca`) 기준으로 분배해 생성한다.
  온프렘 `platform-trust-manager.yaml`은 소스 3개(chart + $values + `directory:{namespace.yaml,cledyu-root-ca-bundle.yaml}`)로
  Bundle CR을 sync하지만, **DR apps-eks trust-manager는 3번째 소스를 누락**해 컨트롤러만 뜬다.
  게다가 ArgoCD `CreateNamespace=true`가 만드는 ns엔 라벨이 없어 분배 대상에서 빠진다.
  참고: 온프렘 `infra/kubernetes/monitoring/cledyu-root-ca-bundle.yaml`은 **정적 ConfigMap(하드코딩된 온프렘 CA cert)**
  이라 새 CA를 발급하는 DR엔 재사용 불가 — Bundle CR(동적 source)만이 정답.

- **F2 (B2 미구현) — kafka-certs 볼륨 비게이팅 → api ContainerCreating.**
  api `deployment.yaml:167`의 볼륨 `kafka-certs` → secret `api-kafka-client-tls`가 **optional 아님**,
  `:131`에서 비-optional 마운트. EKS엔 Strimzi/Kafka가 없어 이 secret이 없다.
  플랜 B2가 요구한 `{{ if .Values.kafka.enabled }}` 게이팅이 T5에서 누락됨.

- **F3 (내 최초 B6 설계 위험) — ACM 자동발견은 드릴에 비결정적.**
  `certificate-arn`을 빼고 aws-load-balancer-controller의 host 자동발견에 맡기면 발견 실패 시
  ALB에 443 리스너 자체가 안 생긴다. 1회 드릴엔 결정성 우선 → **와일드카드 ARN 하드코딩**으로 정정.

- **F4 (검증 필요) — cert-manager 차트 소스 레지스트리.**
  온프렘 ansible은 OCI(`oci://quay.io/jetstack/charts/cert-manager` v1.20.2). classic
  `charts.jetstack.io`에 v1.20.2가 없을 수 있음 → 구현 시 확인, 없으면 OCI 사용.

- **F5 (해소) — DR 노드그룹 taint 없음** → lean cert-manager 스케줄 문제 없음.

- **F6 (정밀화) — sync-wave는 엄격 게이팅 아님.** app-of-apps child App wave는 best-effort.
  실제 안전판은 PKI 앱의 `SkipDryRunOnMissingResource=true` + `retry`. api는 Bundle이 CM을
  채울 때까지 ContainerCreating 후 기동(최종 수렴).

- **F7 (B2 확대, 최우선) — `api/templates/kafka.yaml`이 api Application sync를 실패시킴.**
  이 파일엔 `Certificate`(api-kafka-client) + **`KafkaUser`(Strimzi CRD)**가 있는데 EKS엔 Strimzi CRD가
  없어 `no matches for kind "KafkaUser"`로 **app sync 자체가 실패**(파드 이전 블로커). deployment 볼륨만
  게이팅해선 안 되고 **kafka.yaml 전체**를 `{{ if .Values.kafka.enabled }}`로 감싸야 한다.

- **F8 (스코프 정정) — T5 단독으론 api가 Running 못 감.** api의 `CLEDYU_KEYCLOAK_CLIENT_SECRET`
  (`deployment.yaml:71`)은 **비-optional** secretKeyRef(secret `cledyu-api-oidc`, key `client_secret`).
  이 secret은 `infra/kubernetes/external-secrets/cledyu-web-oidc-externalsecret.yaml`의 **ExternalSecret이
  Vault에서** 만든다 → ClusterSecretStore(**T6**) + Vault 복원(**T6**) 필요. 이 ES는 DR 오버레이에 **부재**.
  ∴ T5 완결로도 api는 secret 부재 시 `CreateContainerConfigError`. **api Running 증명은 T6 이후.**

- **F9 (정정, 축소) — trust-bundle ns 라벨은 이미 처리됨.** api `templates/namespace.yaml`이
  `cledyu.io/trust-bundle: root-ca`를 self-label(chart 렌더)한다. web/vault는 T5에서 bundle 미소비.
  ∴ 수동 ns 라벨링 불요 — 남은 B1 갭은 **Bundle CR sync 하나뿐**.

- **F10 (B7 미적용, 하드 블로커) — api AWS 크레드 비-optional.** `deployment.yaml`의
  `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`는 `{{ if .Values.aws.enabled }}`(base 기본 **true**) 블록 안의
  **비-optional** secretKeyRef(secret `cledyu-api-aws`, Vault→ESO). staged api values-eks가 `aws.enabled`를
  안 꺼서 EKS에서 `CreateContainerConfigError`. **api values-eks에 `aws.enabled: false` 필수.**
  (앞선 "B7 자체해결" 판단은 오판이었음 — 정정.) 비-optional env 전수: keycloak clientSecret(F8·T6) + AWS(F10·여기).
  둘 다 처리하면 T5+B7 후 api의 유일 잔여 블로커는 keycloak secret(T6)뿐.

- **F11 (검증·클리어) — ghcr 이미지 pull.** `ghcr.io/requset700k/{api,web}` — imagePullSecrets·dockerconfigjson·
  node registries·docker login이 repo/ansible **어디에도 없고** 온프렘 파드가 정상 동작 → **이미지 public** 결론.
  EKS 노드가 NAT egress로 pull. **ImagePullBackOff 아님.**

- **F12 (검증·클리어) — PSA / web 백엔드 도달성.** api ns엔 `pod-security.../enforce` 라벨 **없음**(privileged 기본,
  거부 없음), web ns `enforce: baseline`는 파드 securityContext(runAsNonRoot·65532)로 준수.
  web `CLEDYU_BACKEND_URL = http://api.api.svc.cluster.local`(in-cluster svc DNS) → EKS 정상. 둘 다 블로커 아님.

### 확정된 사실(구현 근거)

- PKI 체인(온프렘 `infra/kubernetes/cert-manager/pki-bootstrap.yaml`, 5개 리소스):
  `selfsigned-root`(ClusterIssuer) → `cledyu-root-ca`(CA Cert, secret) → `cledyu-root-ca`(ClusterIssuer)
  → `cledyu-ca`(CA Cert) → `cledyu-ca`(ClusterIssuer). api/web/vault Certificate가 `cledyu-ca`를 issuerRef로 사용.
- cert-manager 버전 = **v1.20.2**(온프렘 ansible과 동일 핀).
- DR 노드 = **3개** m6i.xlarge, taint 없음.
- ACM = **와일드카드 `*.cledyu.com`** (`data.aws_acm_certificate.wildcard`, ISSUED). `api.cledyu.com`·`app.cledyu.com` 매칭.
  stale 참조 주의: T5 주석의 `aws_acm_certificate_validation.auth`는 2026-07-09 state 제거됨 → 폐기.
- 소스 스타일: 멀티소스 `repoURL(chart) + $values ref(git)`, targetRevision `feat/dr-eks-overlay`(드릴 후 main).

## 범위

staged T5 위에 얹는다. 전부 **오프라인 검증만**(helm template + kubeconform), apply 없음. 커밋은 사용자 직접.

### B1 (확장) — cert-manager + PKI + Bundle 분배

신규/수정 파일:
- `gitops/apps/cert-manager/values.yaml` — jetstack chart 값, **lean**(replica 1 / webhook 1 / cainjector 1, PDB 없음; 콜드DR 1회 드릴이라 온프렘 HA값 대신).
- `gitops/argocd/apps-eks/platform-cert-manager.yaml` — Application, chart v1.20.2, `crds.enabled=true`, **wave -10**.
- `gitops/argocd/apps-eks/platform-pki.yaml` — Application, 단일 directory 소스 path `infra/kubernetes/cert-manager`,
  `directory.include: pki-bootstrap.yaml`(온프렘 매니페스트 재사용, 드리프트 0, DR에 자체 CA 신규 발급),
  **wave -8**, `SkipDryRunOnMissingResource=true`.
- **`gitops/argocd/apps-eks/platform-trust-manager.yaml` 수정** — 온프렘처럼 3번째 directory 소스
  `directory.include:"{namespace.yaml,cledyu-root-ca-bundle.yaml}"` 추가(Bundle CR sync). **이것이 B1의 유일한 실질 갭.**
- **네임스페이스 라벨은 불요**(F9): api `templates/namespace.yaml`이 `cledyu.io/trust-bundle: root-ca`를
  self-label하므로 api ns는 자동 분배 대상. web/vault는 T5에서 bundle 미소비(마운트 없음) → 라벨 불필요.

순서: cert-manager(-10) → PKI/`cledyu-ca`(-8) → argocd(-5)/alb(-1)/trust(0)/api·web(0). consumer Certificate가 CA 이후 발급.

### B2 (신규 편입) — Kafka 커플링 게이팅 (F7: app-sync 블로커)

- **`gitops/apps/api/templates/kafka.yaml` 전체**를 `{{ if .Values.kafka.enabled }}`로 감싼다 —
  `KafkaUser`(Strimzi CRD)가 EKS에 CRD 없어 app sync를 실패시키므로 최우선. `Certificate`(api-kafka-client)도 함께 게이팅.
- `gitops/apps/api/templates/deployment.yaml` — `kafka-certs` volume + volumeMount도
  `{{ if .Values.kafka.enabled }}`로 감싼다(**기존 주석 보존, Edit로 수술**).
- `gitops/apps/api/values-eks.yaml` — `kafka.enabled: false` 추가.
- 회귀 방지: 온프렘 렌더(`kafka.enabled=true` 기본)에서 kafka.yaml·kafka-certs가 그대로 남는지 helm template로 확인.

### B7 (신규 편입) — 온프렘 전용 기능 플래그 off (F10: 비-optional 크레드 차단)

- `gitops/apps/api/values-eks.yaml` — **`aws.enabled: false`** 추가(EC2 오버플로우 미사용).
  → 비-optional `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`(secret `cledyu-api-aws`) 주입 자체가 사라져 boot 블로커 제거.
- `analytics.enabled`는 base 기본 false라 별도 불요(확인만). keycloak clientSecret은 F8(T6 의존)이라 여기서 못 끔.

### B6 — host 오버라이드 + ACM 하드코딩

- `gitops/apps/api/values-eks.yaml` → `ingress.hosts: [api.cledyu.com]` (`.local` 드롭).
- `gitops/apps/web/values-eks.yaml` → `ingress.hosts: [app.cledyu.com]`.
- `certificate-arn` 어노테이션 = **와일드카드 `*.cledyu.com` ACM ARN 하드코딩**(자동발견 폐기, verbatim 상수).
  실제 ARN은 구현 시 `terraform output`/AWS로 1회 확정해 박는다(오프라인이라 값 미확정 시 명시 플레이스홀더 + 런북 1줄 치환).

### T9 — 런북 보강 + destroy 하드닝

- 부트스트랩: cert-manager/PKI/Bundle Ready 대기 → `kubectl apply -f gitops/argocd/root-app-eks.yaml`(bastion) 명령 명시.
- **destroy 하드닝(고아 방지)** — 현재 "enable_eks_dr=false apply + 잔존 0" 한 줄은 불충분.
  DR ALB(aws-load-balancer-controller가 Ingress 보고 out-of-band 생성)와 gp3 EBS(reclaim=Delete)는
  terraform 밖이라, 클러스터를 먼저 부수면 ALB·TG·`k8s-*` SG·ENI·EBS가 고아로 남고 ENI가 VPC 삭제를 막는다.
  올바른 순서를 런북에 박는다:
  1. `kubectl delete ingress -A` → ALB/TG/SG 정리 대기(`aws elbv2 describe-load-balancers` 0 확인)
  2. `kubectl delete pvc -A`(또는 ns 삭제) → EBS CSI가 gp3 볼륨 삭제 대기
  3. (LoadBalancer svc 없음 — traefik은 DR 앱셋에 미포함, skip)
  4. `terraform ... -var enable_eks_dr=false apply`
  5. 고아 검증: elbv2 / `ec2 describe-volumes` / `describe-network-interfaces` / `k8s-*`·`eks-cluster-sg-*` SG / VPC 실삭제
- ACM 치환원 정정: `aws_acm_certificate_validation.auth` → 하드코딩(§B6) 또는 `data.aws_acm_certificate.wildcard`.

## T5 성공 기준 (F8 반영, 재정의)

이 스펙 완료로 얻는 것은 **"apps-eks가 sync 클린 + 파드가 이미지풀·볼륨마운트까지 진행"**이다.
web은 이 스펙만으로 Running 도달(이미지 public·백엔드 in-cluster DNS·PSA 준수 확인됨).
api는 B7(`aws.enabled=false`)로 AWS 크레드 블로커를 제거해도, `CLEDYU_KEYCLOAK_CLIENT_SECRET`(비-optional)
→ `cledyu-api-oidc`(ExternalSecret→Vault) 때문에 **T6(Vault+ClusterSecretStore) 없이는 Running 못 감**
(`CreateContainerConfigError`, 정상 예상 상태). **B7 처리 후 api의 유일 잔여 블로커 = keycloak secret(T6).**
**api 실제 Running·서빙 증명은 T6+T8 이후.** 이 스펙에서 api Running을 성공기준으로 잡지 않는다.

## 범위 밖 (별도 트랙)

- **T6(Vault) / T7(CNPG DR) / T8(Keycloak) / T10(드릴)** — 기존 순서 유지, 이 스펙 이후.
- **`cledyu-api-oidc` ExternalSecret**(`infra/kubernetes/external-secrets/cledyu-web-oidc-externalsecret.yaml`)을
  DR 오버레이로 가져오기 = **T6 스코프**(ClusterSecretStore·Vault 복원과 한 묶음). 이 스펙 밖.
- **active-active / 스플릿브레인 / failback / 외부 Route53 헬스 아비터** — Plan C급 자세 변경. 별도 브레인스토밍.

### Future direction (기록용 — 지금 설계 안 함)

콜드 DR이 실제 완주(T5~T10)로 증명된 뒤, 다음으로 확장한다:
- **외부 제3자 헬스 아비터**: 현재 없음(`proxy-alarms.tf`는 프록시 단일 리부트용 CW 알람뿐, Route53 health check/Lambda/canary 부재).
  Route53 health check + failover 라우팅(primary=온프렘, secondary=EKS)이 외부 판정 + DNS 단일대상 라우팅으로 **스플릿브레인 방지**를 겸한다.
- **스플릿브레인**은 k8s(선언상 양쪽 유지)가 아니라 **DNS/트래픽 레이어**에서 차단. 온프렘 생존 시 EKS로의 신호 차단·재라우팅.
- **failback(역동기화)**: EKS→온프렘 데이터 재동기화. 지난 단방향(온프렘→EKS) 대비 **양방향**이 차별점.
- **자세**: cold(현재, `enable_eks_dr=false`=0리소스) vs warm(클러스터 상시·노드 0). 콜드 드릴 검증 후 정보에 근거해 결정.

## 검증

- `helm template gitops/apps/api -f .../values.yaml -f .../values-eks.yaml` → **KafkaUser·Certificate(kafka.yaml)와
  kafka-certs 볼륨이 EKS 렌더에서 모두 사라지고** 온프렘 렌더엔 남는지, ingress host가 `.com` 단일인지 확인.
- `helm template gitops/apps/cert-manager` + `kubeconform` → cert-manager 매니페스트 유효성.
- PKI는 `pki-bootstrap.yaml` 원본 재사용이라 별도 검증 불요.
- apply/plan 없음(실 AWS는 T10 드릴에서 사용자 직접).
