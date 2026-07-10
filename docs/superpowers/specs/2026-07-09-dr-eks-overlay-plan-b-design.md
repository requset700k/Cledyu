# DR Plan B — EKS Cold DR 오버레이 Design

- 상위 설계: `docs/superpowers/specs/2026-07-01-aws-dr-backup-design.md`
- 선행: Plan A(백업 계층, `2026-07-01-dr-backup-plan-a-backup-layer.md`), Plan A-2(Keycloak DB→CNPG, `2026-07-09-dr-keycloak-db-cnpg-migration.md`)
- 후행: Plan C(오케스트레이션, `2026-07-03-dr-backup-plan-c-orchestration.md`)
- 작성일: 2026-07-09

---

## 0. 전제조건

Plan B는 다음이 **main에 랜딩된 상태**를 전제로 한다:

- **A-2 완료**: keycloak-pg(CNPG) 차트·앱이 main에 존재, Keycloak `db.host` cutover 완료, 구 Bitnami Keycloak DB 폐기(A-2 Task 6).
- **구 cledyu postgres 폐기**(#267)로 cledyu-pg CNPG 단일화.

**주의 — 실제 선행조건은 "구DB 폐기"가 아니라 "A-2가 main에 있음"이다.** EKS 복원 경로는 라이브 구DB(`postgres.postgres.svc` / `keycloak-db-postgresql.svc`)를 참조하지 않고 **S3 백업에서만** 복원하므로(§5.1), 구DB가 살아있든 폐기됐든 EKS엔 영향이 없다. 유일한 선행조건은 keycloak-pg **차트**가 main에 존재하는 것(그래야 EKS recovery 매니페스트를 만든다). 구현계획에서 **keycloak-pg 관련 태스크만 A-2 머지 뒤로** 순서를 두고, 나머지는 지금 진행 가능.

---

## 1. 목표 · 성공 기준

Plan B는 **"백업이 존재할 때, 온프렘 없이 EKS에서 과금 경로를 선언적으로 재현할 수 있는 기반"**을 만든다.
Plan C(감지·오케스트레이션)가 자동으로 호출할 대상이지만, **Plan B 자체는 수동으로 end-to-end 실행 가능**해야 한다 — 그것이 "검증된 DR 드릴"의 정의다.

**성공 기준:** 임시 EKS를 `terraform apply` → ArgoCD 부트스트랩 → 백업에서 복원 →
**Keycloak 로컬 테스트유저로 로그인해, 복원된 특정 학습자의 수료/진도 데이터가 api를 통해 실제로 서빙되면** 통과 → `terraform destroy`.
(소셜 로그인 end-to-end는 라이브 DNS 전환이 필요해 Plan C 통합드릴로 이월 — §8 F3.)

DR 전략은 상위 설계대로 **Cold**(평시 EKS 미기동, 재해 시에만 임시 기동). 검증 중에만 기동한다.

### 재현 범위 = 과금 최소경로

| 분류 | 앱 |
|---|---|
| 필수(과금 직결) | argocd, external-secrets, cnpg-operator, postgres-cnpg, keycloak-pg, vault, api, web, **keycloak(신설)**, ingress(ALB) |
| 제외(최소경로) | kubevirt, longhorn/snapshot-controller, metallb, kafka, redis, airflow, ai-tutor, 관측(kube-prometheus/loki/tempo/alloy/sloth), kyverno, traefik |

세션 VM은 재현하지 않는다(상위 설계: 신규 세션은 EC2 버스트, KubeVirt 제외 확정).

---

## 2. 아키텍처 개요

```
[terraform apply] ─> 전용 임시 VPC + EKS + 노드그룹 + 애드온(EBS CSI, ALB Controller, VPC 엔드포인트)
      │
      └─> 부트스트랩: ArgoCD 설치 ─> root-app(apps-eks/) ─> 과금경로 앱 sync
                                          │
            [Vault raft 스냅샷 복원(명령형)]─┤  (GitOps로 표현 불가한 유일 스텝)
                                          │
            Vault(EBS) → IRSA로 AWS KMS unseal → 스냅샷 복원 → ESO 온라인
                                          │
            CNPG postgres-cnpg / keycloak-pg → bootstrap.recovery(S3, targetTime=최신)
                                          │
            Keycloak(오퍼레이터+CR) → api → web → 각자 ALB 생성
      │
[Plan C가 DNS 전환] auth/api/web.cledyu.com ─> EKS ALB   (Plan B는 ALB 생성까지, 전환은 Plan C)
```

**핵심 구조:** EKS는 **자기 ArgoCD**를 부트스트랩해 **자기 클러스터만** 관리한다(한 ArgoCD가 두 클러스터를 관리하는 구조가 아니다).
따라서 EKS 오버레이 = "같은 Helm 차트 + EKS용 values 델타 + DR 필수 앱만 담은 별도 app-of-apps".

온프렘 GitOps 부트스트랩 체인(현행)은 다음과 같고, EKS는 이 체인의 EKS 변형을 사용한다:

```
Ansible → ArgoCD 설치 + root-apps(app-of-apps)
  → gitops/argocd/apps/ 디렉터리 recurse(*.yaml)
    → 각 Application → gitops/apps/<name> (first-party Helm 차트 + values.yaml) → destination: in-cluster
```

---

## 3. 컴포넌트 (신설/수정 산출물)

| # | 산출물 | 위치 | 역할 |
|---|---|---|---|
| C1 | EKS Terraform | `infra/terraform/aws/eks-dr.tf` (+ VPC 모듈) | 전용 VPC·EKS·노드그룹·OIDC(IRSA)·애드온(EBS CSI, ALB Controller). S3/KMS/STS는 VPC 엔드포인트, **github/ghcr 도달용 NAT 게이트웨이 또는 퍼블릭 서브넷**(레포·이미지가 ghcr/github 공개라 egress 필요 — ECR 아님, F5). `enable_eks_dr` 변수 게이트 → 평시 미적용, 드릴 때만 apply/destroy |
| C2 | IRSA 롤 | 동 TF | ① vault-unseal(KMS Decrypt on `alias/cledyu-vault-unseal`) ② cnpg-restore(백업 S3 read) ③ ALB Controller ④ EBS CSI. **정적 키 없음** |
| C3 | EKS app-of-apps | `gitops/argocd/apps-eks/` | 과금경로 앱만 큐레이션한 Application 목록. 각 Application이 `helm.valueFiles: [values.yaml, values-eks.yaml]` |
| C4 | 앱별 EKS 델타 | `gitops/apps/<name>/values-eks.yaml` | 인프라 델타만(§5) |
| C5 | Keycloak GitOps 앱 | `gitops/apps/keycloak/` (신설) | 공식 Keycloak Operator v26.6.1 + Keycloak CR + 테마/네이버-SPI ConfigMap. Ansible 역할(`keycloak_operator`/`keycloak_foundation`)에서 그대로 이식 |
| C6 | 부트스트랩 런북 | `docs/RUNBOOK/dr-eks-bootstrap.md` (+ 스크립트) | ArgoCD 설치 → root-app-eks 적용 → Vault 스냅샷 복원(명령형) 순서(§6) |
| C7 | CNPG EKS recovery 매니페스트 | `gitops/apps/postgres-cnpg`·`keycloak-pg`의 EKS 경로(별도) | S3 barmanObjectStore recovery 전용(§5.1). 운영 import 템플릿과 분리 |

---

## 4. 자격증명 — AWS 단독 (실측 반영)

상위 설계 초안은 "GCS backend + GCP KMS auto-unseal → 복원 롤에 GCP KMS 필요"였으나, 2026-07-04 이후 현실은 다르다(실측):

| 항목 | 구 설계 | 현재 코드 |
|---|---|---|
| TF backend | GCS (AWS+GCP 동시) | **S3** `cledyu-tf-state` (AWS 단독) |
| Vault unseal | GCP KMS | **AWS KMS** `awskms` (`alias/cledyu-vault-unseal`, 마이그레이션 완료·3노드 unsealed) |

**함의:** EKS 복원 경로는 **GCP 자격증명 불필요**. 복원 주체가 챙길 건 딱 둘 — ① 백업 S3 버킷 read, ② KMS `alias/cledyu-vault-unseal`(`arn:aws:kms:ap-northeast-2:504284203153:key/e29e3ec2-f5e0-4308-af6f-5b576cc99f52`)에 `kms:Decrypt`. 이 키는 **DR-durable, 삭제 금지**(복원된 Vault가 이 키로 스스로 unseal). 둘 다 **IRSA 롤**로 부여(정적 키 없음).

---

## 5. 앱별 오버레이 델타 (`values-eks.yaml`)

| 앱 | 델타 |
|---|---|
| postgres-cnpg / keycloak-pg | **values-eks 토글이 아니라 별도 EKS recovery 매니페스트**(§5.1). ① storageClass → `gp3`(EBS CSI) ② bootstrap.recovery(externalCluster=S3 백업, serverName=원본, targetTime=최신) ③ s3Credentials → **IRSA**(inheritFromIAMRole, recovery-read·backup-write 공통) ④ **backup serverName = `-dr` 접미**(원본 아카이브 덮어쓰기 방지) |
| vault | ① storageClass → `gp3` (**`dataStorage`+`auditStorage` 둘 다** — 온프렘 base가 longhorn) ② unseal → **IRSA**: SA에 role-arn annotation **+ `vault-aws-kms-creds` env 주입 블록 제거**(env가 있으면 web identity 토큰을 덮음 — 어노테이션만으론 부족). HashiCorp 차트 지원 확인됨 ③ 데이터는 raft 스냅샷 복원(§6, 명령형) |
| api / web | ① ingressClassName → `alb` + ALB 어노테이션(internet-facing, ACM ARN, group) ② replicas 최소화. 이미지태그·OIDC url 등 비즈값은 **공유 values.yaml 그대로**(드리프트 불가) |
| keycloak(신설) | ingress ALB, hostname `auth.cledyu.com`, db.host `keycloak-pg-rw` |
| external-secrets / cnpg-operator / argocd / trust-manager | 클러스터 무관 → 델타 최소(리소스 sizing 정도) |
| traefik | EKS 미배포(ALB Controller 대체) → apps-eks에서 제외 |

**오버레이 원칙:** 비즈니스 설정(이미지·env·차트 로직)은 공유 `values.yaml`에 남기고, `values-eks.yaml`에는 **인프라 델타(storageClass, ingressClass, IRSA)만** 둔다. 그래야 드리프트가 인프라 축으로만 제한된다.
**예외 = CNPG 클러스터:** bootstrap/externalClusters/자격증명/serverName이 운영본과 **네 곳 모두** 달라 values 토글로는 조건문 범벅이 되므로 **별도 EKS recovery 매니페스트**로 뺀다(§5.1). 접근안 A(공유 차트 + values-eks)는 stateless(api/web/keycloak)·storageClass·ingress 델타엔 유효.

### 5.1 CNPG는 별도 EKS recovery 매니페스트

실측(`postgres-cnpg/templates/cluster.yaml`): 운영 템플릿은 bootstrap `initdb.import`(source=`old-postgres` svc)와 `s3Credentials` **secret 참조가 하드코딩**돼 있고 recovery 분기가 없다. EKS recovery 변형은 다음 **네 곳이 모두** 달라진다:

1. bootstrap: `recovery` vs `initdb.import`
2. externalClusters: **S3 barmanObjectStore(백업)** vs 라이브 구DB svc
3. s3Credentials: **IRSA(inheritFromIAMRole)** vs secret
4. backup serverName: **`-dr` 접미**(원본 아카이브 보호) vs 원본

하나의 템플릿에 values 토글로 우겨넣으면 `{{if}}` 범벅이 되고 운영 sync 오염 위험이 있다. 따라서 CNPG는 **EKS 전용 recovery 매니페스트**를 별도로 둔다. 이 매니페스트는 **라이브 구DB svc를 참조하지 않고 S3에서만** 복원하므로 §0 전제(구DB 폐기)와 무관하게 성립한다. 메모리 `cnpg-bootstrap-failsafe`의 "recovery=별도 매니페스트, 운영본은 import fail-safe 유지" 방침과 일치.

---

## 6. 부트스트랩 시크릿 순서 — 치킨에그 해소

의존 사이클: ESO는 Vault가 살아야, CNPG recovery는 S3 자격이 있어야, Vault는 unseal 자격이 있어야 동작한다. 온프렘은 정적 시크릿(ESO→Vault)으로 풀지만, **EKS에선 복원 시점에 Vault가 비어 있어** 순환이 생긴다.

**해법 = IRSA로 순환을 끊는다** (부트스트랩에 미리 심을 정적 시크릿 최소화):

```
1. terraform apply → VPC·EKS·IRSA 롤 준비
2. ArgoCD 설치 → root-app-eks 적용
3. ArgoCD sync: cnpg-operator, external-secrets(CRD), vault
     └ Vault 기동 → IRSA로 AWS KMS unseal → 단, 데이터 비어있음(fresh EBS)
4. [명령형] S3에서 최신 vault raft 스냅샷 fetch → `vault operator raft snapshot restore`
     └ Vault 데이터 채워짐 → ESO가 Vault에서 시크릿 서빙 시작
5. CNPG postgres-cnpg / keycloak-pg → bootstrap.recovery (S3 자격=IRSA, ESO 불필요) → DB 복원
6. Keycloak(오퍼레이터+CR) → keycloak-pg-rw 연결, admin 자격=ESO
7. api → web → 각자 ALB 생성
8. 검증: 로그인 + 수료증/진도
```

**포인트:** Vault unseal·CNPG S3 read를 IRSA로 돌리면 부트스트랩 정적 시크릿이 거의 사라진다.
GitOps로 표현 불가한 유일한 명령형 스텝은 **4번 Vault raft 스냅샷 복원** — 이것이 Plan C의 복원 Lambda가 자동화할 지점이자, 드릴에선 런북 수동 스텝이다.

---

## 7. 복원 통합 — Plan A/C와의 경계

- **Plan A** = 백업 생산 (S3 `postgres/` `keycloak/` `vault/` `velero/`)
- **Plan B** = 복원-부트스트랩 오버레이 + Vault 복원 절차 + IRSA 롤 (선언/스크립트 산출물)
- **Plan C** = 오케스트레이션(Step Functions가 §6 순서를 자동 실행) + DNS 전환
- **드릴** = Plan B를 손으로 §6 실행 → 나중에 Plan C가 그대로 자동화. Plan B는 Plan C 없이 완결 검증 가능

**Velero 복원은 최소경로에선 선택적:** 과금경로 앱(api/web/keycloak/cnpg/vault)은 전부 GitOps 선언이라 git sync로 재현된다. Velero가 필요했던 `lab-sessions`(동적 생성) 네임스페이스는 과금경로 밖 → **최소 드릴에선 Velero 복원 생략**, 범위 확장 시 재검토.

---

## 8. 검증 드릴 (성공 기준 실측)

런북 `docs/RUNBOOK/dr-eks-bootstrap.md` 한 번 완주:

```
terraform apply(enable_eks_dr=true)
  → ArgoCD 부트스트랩 → root-app-eks
  → Vault raft 스냅샷 복원(§6)
  → CNPG DB 복원 후 **원본 대비 row 수 대조**(postgres=수료/진도, keycloak-pg=계정)
  → Keycloak **로컬 테스트유저**(id/pw)로 로그인 → 토큰 발급 → api 호출
  → **복원된 특정 학습자의 실제 수료/진도 값이 서빙되는지 대조**
  → RTO 구간별 실측 기록
  → terraform destroy
```

**통과 조건 = 과금 기능(수료증·진도·계정) 정상 동작.** 세션 초기화는 허용 손실.

**F2 (거짓통과 방지):** api는 DB Secret 미주입 시 **in-memory 폴백**으로도 200을 반환하므로, "페이지 뜸"은 통과 근거가 못 된다. 반드시 **알려진 학습자의 복원된 수료/진도 값 일치**로 확정한다(api가 DB 모드로 붙었는지 포함).
**F3 (드릴 경계):** 소셜 로그인(Google/Naver → `auth.cledyu.com` redirect)은 라이브 DNS를 EKS로 돌려야 완주하므로 **본 드릴 범위 밖**이다. 계정 복원 정합성이 아니라 외부 IdP↔DNS 배선 문제라 A-2 완료로도 사라지지 않는다. 본 드릴은 **로컬 테스트유저**로 복원→서빙 체인을 검증하고, 소셜 IdP redirect 검증은 **Plan C 통합드릴(실 DNS 전환)**로 이월한다.

---

## 9. 명시적 범위 밖 · 미결 · 후속 추적

### 범위 밖
- kubevirt·kafka·redis·airflow·ai-tutor·관측·kyverno (최소경로 제외)
- **온프렘 Keycloak Ansible→GitOps 이관 = A-3 후속** (Plan B는 EKS 배포용 GitOps 앱만 신설; 온프렘은 Ansible 유지 → 온프렘/EKS 두 소스 드리프트는 A-3에서 수렴)
- **소셜 로그인 end-to-end 검증**(Google/Naver→auth.cledyu.com) = Plan C 통합드릴(실 DNS 전환)로 이월. 본 드릴은 로컬유저 축소검증(§8 F3)
- Velero 동적 리소스 복원 (범위 확장 시)
- failback / 스플릿브레인 (Plan C Task 6)
- Warm/Pilot standby (비용상 Cold 확정)

### 미결(구현 착수 시 확정)
1. **CNPG recovery 매니페스트 형태**: 별도 파일(`recovery-eks.yaml`) vs 별도 차트 경로 — 운영 sync 오염 없이 apps-eks에서만 잡히게 (§5.1)
2. **노드 sizing**: CNPG+Vault PVC IOPS/메모리 기준 인스턴스 타입
3. **VPC 엔드포인트 vs NAT**: 임시 클러스터 비용/복잡도 트레이드오프 (S3/KMS/STS는 엔드포인트, github/ghcr는 NAT 필요 §3)
4. **Vault IRSA 실기동 검증**: HashiCorp 차트에서 SA annotation + env 제거로 awskms seal이 web identity로 unseal되는지 드릴에서 실측 (설계상 지원 확인, 실행 미검증)
5. **RTO 단축**: 필요 시 EKS 컨트롤플레인 warm 유지 검토 (현재 Cold, 기동 ~30분 상위 설계 근거)

### 검증 완료(리뷰에서 해소)
- **이미지/레포 egress(구 F5)**: 레포·ghcr 이미지 모두 **공개 확인**(Ansible argocd 역할에 repo 자격 없음, api/web에 pull secret 없음) → 자격 시드 불필요. 노드가 github/ghcr에 **egress(NAT/퍼블릭 서브넷)**만 되면 됨(§3 C1)
- **A-2 의존(구 F6)**: 블로커가 아니라 §0 전제조건으로 정리 — 실 선행조건은 "keycloak-pg 차트가 main에 있음"뿐
