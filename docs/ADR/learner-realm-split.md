# ADR: 학습자 realm 분리 (cledyu-learn) 와 외부 자가가입·소셜 로그인

- **상태:** Accepted
- **날짜:** 2026-06-05
- **제안자:** 김용균 / Platform
- **의사결정자:** 김용균 / 윤승호
- **관련:** ADR `keycloak-rbac.md`(옵션 A) 의 학습자 부분을 일부 supersede, RUNBOOK `learner-auth.md`

## 1. 컨텍스트

`keycloak-rbac.md`(ADR, 옵션 A)는 6인 내부 프로젝트에 맞춰 **단일 `cledyu` realm** 을 채택하고, 학습자 self-registration·소셜 IdP 연동을 명시적 **비-목표**로 두었다. 그 결과 회원가입/로그인이 "내부 팀원 6명 전용 + mock stub" 상태로 남았다:

- `cledyu` realm 의 `registration_allowed = false` → 외부 학습자가 스스로 가입할 경로 없음.
- 소셜 IdP(구글/카카오/네이버) 전무.
- 백엔드(`apps/api`)는 `access_token=mock-token` 쿠키만 세팅하는 stub, JWT 검증도 stub.

교육 플랫폼의 실사용자(학습자)는 이메일/소셜로 자가가입할 수 있어야 한다. 그러나 `cledyu` realm 은 동시에 운영자 SSO realm 이기도 하다 — kube-apiserver OIDC, ArgoCD, Grafana, Kafka-UI, Vault 가 모두 이 realm 의 issuer(`…/realms/cledyu`)를 신뢰한다.

## 2. 문제 / 목표

목표:

- 외부 학습자가 **이메일 자가가입 + 소셜(구글/카카오/네이버)** 로 가입·로그인.
- 학습자 신원을 운영자 권한(클러스터/ArgoCD/Grafana)과 **격리** — 가입만으로 운영 권한을 얻지 못하게.
- 백엔드 mock 제거 → 실 OIDC(authorization code + PKCE) 연동.

비-목표:

- 기업 고객(B2B) 테넌트 분리 — 후속.
- 학습자 realm 의 LDAP/SAML 연동.

## 3. 고려한 대안

### 대안 A — 단일 `cledyu` realm 유지 + 권한 격리만 강화
- 장점: realm 1개로 운영 단순.
- 단점: 자가가입/소셜을 켜면 외부 미신뢰 사용자가 운영자 realm 에 혼재. group default/client scope 오설정 한 번에 학습자가 클러스터 권한 획득 가능 — blast radius 가 크다.

### 대안 B — 기존 realm 을 `cledyu-internal` 로 rename + `cledyu-external` 신설
- 장점: 명칭상 의도가 명확.
- 단점: issuer URL `…/realms/cledyu` 를 참조하는 운영자 소비자 5종(kube-apiserver/ArgoCD/Grafana/Kafka-UI/Vault)이 **동시에** 깨진다. 토큰 재발급·설정 동시 변경 필요 — 운영 리스크 큼.

### 대안 C — 기존 `cledyu` realm 보존(=내부) + `cledyu-learn` realm 신설(=외부)
- 장점: 운영자 realm 이름·issuer 무변경 → SSO 소비자 영향 0. 학습자/운영자 realm 수준 격리. ADR `keycloak-rbac.md` 의 옵션 C(internal/external 분리) 정신을 마이그레이션 리스크 없이 달성.
- 단점: realm 2개 운영(IdP·SMTP 등 일부 설정 중복).

## 4. 결정

**대안 C 채택.**

- `cledyu` realm = **내부/운영자** realm 으로 이름·issuer 그대로 유지. `registration_allowed=false`. `admin`/`observer`/`kafka-admin`/`kafka-viewer` 역할, `argocd`/`grafana`/`kubectl`/`kafka-ui` 클라이언트 유지. `student`/`instructor` 역할과 `students-cohort-0`/`instructors` 그룹은 **제거**(learn 으로 이동).
- `cledyu-learn` realm = **외부/학습자** realm 신설. `registration_allowed=true`, 이메일 인증(SMTP 설정 후), 구글/카카오/네이버 IdP, `student`/`instructor` 역할, `web`(confidential, BFF)/`api`/`tutor` 클라이언트.
- 신규 가입자는 realm **default group=`students`** 에만 자동 편입 → `student` 역할만. `instructor` 는 운영자가 수동 승격.
- 백엔드(`apps/api`)는 `cledyu-learn` issuer 로 OIDC authorization code(PKCE) 흐름을 수행(BFF). `web` client secret 은 Vault→ESO 로 주입.

## 5. 결과 (Consequences)

- **긍정:** 운영자 SSO 무중단. 학습자/운영 권한 realm 격리. 소셜 로그인은 Keycloak IdP 브로커링으로 앱은 단일 OIDC provider 만 바라봄.
- **부정 / 트레이드오프:** realm 2개 운영. 소셜 IdP 는 실 client 발급 전까지 `enabled_social_idps=[]` 로 비활성(provider 별 단계 활성화). 이메일 인증은 SMTP 설정 전까지 `learn_verify_email=false`.
- **후속 액션:**
  - [ ] 구글/카카오/네이버 개발자 콘솔에서 OAuth client 발급 → secret 주입 → `enabled_social_idps` 에 alias 추가(구글부터). 공개 노출·절차는 RUNBOOK `learner-auth.md` §4.
  - [ ] 학습자 realm SMTP 설정 → `learn_verify_email=true`
  - [ ] `web` client secret 생성 → Vault `secret/oidc/cledyu-web:client_secret`

## 6. 검증 기준

- ArgoCD/Grafana/Kafka-UI 로그인이 여전히 `cledyu` realm 으로 정상(issuer 무변경).
- app.cledyu.local → 로그인 → Keycloak 회원가입 폼 + 소셜 버튼 → 가입 시 `cledyu-learn` 에 `student` 역할로 생성.
- 학습자 토큰으로 ArgoCD/kube-apiserver 접근 **거부**(realm 격리 확인).
- 백엔드: 만료/위조 access_token 이 `middleware.JWT` 에서 401.

## 7. 참고 자료

- Terraform: `infra/terraform/keycloak/{realm,clients,roles,idp}-learn.tf`
- 백엔드: `apps/api/internal/auth/oidc.go`
- RUNBOOK: `docs/RUNBOOK/learner-auth.md`
