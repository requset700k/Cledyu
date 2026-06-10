# RUNBOOK: 학습자 인증 (cledyu-learn realm) 운영

외부 학습자의 자가가입·소셜 로그인을 담당하는 `cledyu-learn` realm 운영 절차.
설계 배경은 ADR `docs/ADR/learner-realm-split.md` 참고.

## 1. 구조 한눈에

```
학습자 브라우저
  │  로그인/회원가입
  ▼
app.cledyu.local (web)  ──►  api.cledyu.local (BFF)
                                  │  authorization code + PKCE
                                  ▼
                       Keycloak  cledyu-learn  realm
                          ├─ 이메일 자가가입 (registration_allowed=true)
                          └─ IdP 브로커링: Google / Kakao / Naver
```

- 운영자 SSO(ArgoCD/Grafana/Kafka-UI/kube-apiserver/Vault)는 **별도** `cledyu` realm. 학습자와 격리.
- 앱은 `cledyu-learn` 하나만 OIDC provider 로 바라본다. 소셜 로그인은 Keycloak 이 위임 처리.

## 2. 토큰/세션 흐름 (백엔드 BFF)

1. `GET /api/v1/auth/login` → state/nonce/PKCE 생성(임시 쿠키) → Keycloak 인가 URL 리다이렉트.
   - `?screen=register` 면 Keycloak **회원가입 폼**(`/registrations`)으로 딥링크.
2. Keycloak → `GET /api/v1/auth/callback?code=…&state=…`
   - state 쿠키 일치 검증 → code+PKCE 교환 → id_token nonce 검증 → `access_token`/`id_token`/`refresh_token` HttpOnly 쿠키 설정 → `app.cledyu.local/callback` 리다이렉트.
3. 보호 API: `middleware.JWT` 가 access_token 을 JWKS 로 검증, claims(`sub`/`email`/`name`/`realm_access.roles`)를 컨텍스트에 주입.
4. **silent refresh:** access_token(기본 15m) 만료로 보호 API 가 401 을 돌려주면
   프론트(`lib/api.ts`)가 `POST /api/v1/auth/refresh` 를 1회 호출(동시 401 은 single-flight 공유)
   → 백엔드가 refresh_token grant 로 토큰 재발급 + 쿠키 갱신(회전된 refresh_token 반영)
   → 원 요청 재시도. refresh 실패(SSO 세션 종료/refresh_token 만료) 시에만 로그인 페이지로 이동.
   덕분에 실습(최대 3h) 중 Keycloak SSO 세션이 유지되는 한 재로그인이 발생하지 않는다.
5. `GET /api/v1/auth/logout` → 쿠키 3종 삭제 + Keycloak end-session(SSO 로그아웃) → 프론트로 복귀.

> 세션 수명 튜닝: 실습 중 끊김 없는 갱신을 보장하려면 Keycloak `cledyu-learn` realm 의
> SSO Session Idle 을 Lab 세션 TTL(3h) 이상으로 설정한다(refresh 가 일어날 때마다 idle 은 연장됨).

## 3. 최초 활성화 절차

### 3.1 web client secret 발급 → Vault → ESO

```bash
# 1) Terraform tfvars 에 web client secret 설정 (보안 tfvars, gitignore)
#    learn_oidc_client_secrets = { web = "<32-byte hex>" }
openssl rand -hex 32   # 값 생성

# 2) terraform apply (cledyu-learn realm + web client 생성/secret 반영)
cd infra/terraform/keycloak && terraform apply

# 3) 같은 값을 Vault 에 등록 (api Deployment 가 ESO 로 주입받음)
vault kv put cledyu/oidc/cledyu-web client_secret=<위 값>   # KV mount=cledyu (ClusterSecretStore vault-backend)

# 4) ESO 동기화 확인 (api 네임스페이스)
kubectl -n api get externalsecret cledyu-web-oidc-client-secret
kubectl -n api get secret cledyu-api-oidc -o jsonpath='{.data.client_secret}' | base64 -d | head -c8; echo …
```

### 3.2 배포 값 확인

- `gitops/apps/api/values.yaml` → `keycloak.realm=cledyu-learn`, `clientId=web`.
- `gitops/apps/web/values.yaml` → `AUTH_ENABLED=true`.

## 4. 소셜 IdP 추가 (구글 / 카카오 / 네이버)

기본은 `enable_social_idp=false`(IdP 미생성). 실 client 발급 후 켠다.

| 제공자 | 콘솔 | redirect URI 등록값 |
|---|---|---|
| Google | Google Cloud Console → OAuth client | `https://keycloak.cledyu.local/realms/cledyu-learn/broker/google/endpoint` |
| Kakao | Kakao Developers → 내 앱 → 카카오 로그인 | `…/realms/cledyu-learn/broker/kakao/endpoint` |
| Naver | Naver Developers → 애플리케이션 | `…/realms/cledyu-learn/broker/naver/endpoint` |

```bash
# tfvars: 공개값은 idp_client_ids, secret 은 idp_client_secrets
idp_client_ids     = { google = "...", kakao = "...", naver = "..." }
idp_client_secrets = { google = "...", kakao = "...", naver = "..." }
enable_social_idp  = true

terraform apply
```

- Naver 는 OIDC 가 아닌 OAuth2 라서 서명검증을 끄고(`validate_signature=false`) userinfo 의 중첩 `response.{id,email,name}` 를 attribute mapper 로 평탄화한다(`idp-learn.tf`).
- Kakao 는 OIDC 지원 → 표준 discovery 사용.

## 5. 이메일 인증 활성화

`learn_verify_email=true` 는 SMTP 가 있어야 가입 메일이 나간다.

```bash
# tfvars
learn_smtp = {
  host          = "smtp.example.com"
  port          = "587"
  from          = "no-reply@cledyu.io"
  auth_username = "no-reply@cledyu.io"
}
learn_smtp_password = "<smtp 비번>"
learn_verify_email  = true

terraform apply
```

## 6. 운영 작업

- **강사 승격:** 신규 가입자는 `student` 만. 강사는 admin console 에서 해당 사용자를 `instructors` 그룹에 추가(또는 kcadm.sh).
- **가입 이벤트 확인:** `kcadm.sh get events -r cledyu-learn` → `REGISTER`/`IDENTITY_PROVIDER_FIRST_LOGIN`.
- **client secret 회전:** §3.1 의 1~3 반복 후 `kubectl -n api annotate externalsecret cledyu-web-oidc-client-secret force-sync=$(date +%s) --overwrite`.

## 6.1 역할 기반 인가 (RBAC)

`realm_access.roles` 의 역할은 api 미들웨어 `JWT` 가 단일 역할(우선순위 admin >
instructor > student)로 정규화해 컨텍스트에 주입하고, `RequireMinRole` 이 라우트 그룹에서
최소 역할 이상인지 검사한다(상위 역할은 하위 라우트도 통과).

| 라우트 그룹 | 최소 역할 | 비고 |
|---|---|---|
| `/api/v1/*` (세션·랩) | student | JWT 검증만, 역할 무관 |
| `/api/v1/admin/*` | admin | 예: `GET /admin/users` (유저 목록) |

- 역할 변경(강사 승격)은 Keycloak 에서 하고, 다음 로그인/`/auth/refresh` 시 새 토큰에
  반영된다. 즉시 적용이 필요하면 해당 사용자에게 재로그인을 요청한다(토큰 수명 ~15m 내 자동 반영).
- 권한 부족 응답은 `403 {code: forbidden}` — 프론트(`lib/api.ts`)는 이를 `FORBIDDEN` 으로 처리한다.
- 강사(instructor) 전용 그룹은 강사 모드 도입 시 `RequireMinRole("instructor")` 로 같은 패턴 추가.

## 6.2 관리자 유저 관리 API + 역할 승격 service-account

admin(최소 역할) 전용 엔드포인트:

| 메서드 · 경로 | 동작 |
|---|---|
| `GET /api/v1/admin/users?limit=50` | 유저 목록(최근 로그인 순) |
| `GET /api/v1/admin/users/:uid/activity` | 유저의 랩 완료 이력 |
| `POST /api/v1/admin/users/:uid/role` `{role}` | instructor/admin 승격(Keycloak realm 역할 추가 + DB 미러 갱신) |
| `DELETE /api/v1/admin/users/:uid/session` | 활성 세션 강제 종료 |

역할 승격은 Keycloak Admin REST API 를 호출하므로 **service-account 클라이언트**가 필요하다.
미설정 시 승격 API 만 `501` 로 비활성되고 나머지는 동작한다.

### service-account 설정 (cledyu-admin)

```bash
# 1) cledyu-learn realm 에 confidential client 생성 (service account 활성)
#    + realm-management 의 manage-users, view-realm 역할 부여
#    (Terraform: client cledyu-admin + service_account 역할 매핑 권장)

# 2) client secret 을 Vault 에 등록
vault kv put cledyu/oidc/cledyu-admin client_secret=<cledyu-admin client secret>

# 3) ESO 매니페스트 적용 + 동기화 확인
kubectl apply -f infra/kubernetes/external-secrets/cledyu-admin-oidc-externalsecret.yaml
kubectl -n api get externalsecret cledyu-admin-oidc-client-secret
```

- 승격은 realm 역할 `instructor`/`admin` 을 **추가**한다(데모트는 Keycloak 콘솔에서). 해당 realm
  역할이 없으면 `500 realm role not provisioned` — Terraform 으로 realm 역할을 먼저 생성한다.
- 승격 직후 DB 미러(`users.role`)는 즉시 갱신되지만, **세션 토큰**은 다음 로그인/`/auth/refresh`
  까지 이전 역할을 유지한다(§6.1). 즉시 강사 권한이 필요하면 재로그인을 안내한다.

## 6.3 조직 멀티테넌트 (RAG 조직 중립성)

기획서 1.1/3.5 의 '조직 중립성' — 학습자 소속 조직별로 AI 튜터 RAG collection 을 분리한다.
같은 플랫폼을 코드 변경 없이 다른 기업·교육과정에 배포할 수 있게 한다.

흐름:

```
Keycloak 그룹 "/org-<이름>"  ──(groups 클레임)──►  api JWT 미들웨어 Identity.Org()
                                                        │  user_org = "org-<이름>"(없으면 public)
                                                        ▼
                        POST /sessions/:id/hint ──► ai-tutor org_id
                                                        ▼
                        RAG 검색 = [org-<이름> collection, public collection]
```

- **조직 배정:** Keycloak 에서 사용자를 `/org-<이름>` 그룹에 추가한다(예: `/org-kt-cloud`).
  그룹명 규약 `org-` 접두사가 곧 ChromaDB collection 이름이다. 그룹이 없으면 `public` 만 검색.
- **문서 주입:** 해당 조직 문서를 같은 이름 collection 으로 인덱싱한다 —
  `python apps/ai-tutor/scripts/index_docs.py --collection org-kt-cloud --source ...`
  (인덱싱 가이드는 `docs/architecture/ai-tutor.md`).
- `groups` 클레임이 토큰에 포함되려면 Keycloak client scope 에 group membership mapper 가
  있어야 한다(full group path on). 현재 토큰에 그룹이 없으면 전원 `public` 으로 동작(안전한 기본값).
- `GET /api/v1/me` 응답의 `org` 필드로 현재 소속 조직을 확인할 수 있다.

## 7. 트러블슈팅

| 증상 | 원인 / 조치 |
|---|---|
| 로그인 시 api 503 `auth provider not configured` | Keycloak 미가용으로 OIDC discovery 실패. `cledyu-learn` realm 존재·네트워크 확인. |
| callback `invalid oauth state` | state 쿠키 유실(SameSite/도메인). `CLEDYU_KEYCLOAK_COOKIE_DOMAIN`(`.cledyu.local`) 확인. |
| 보호 API 401 `invalid token` | access_token 만료/realm 불일치. api 의 `CLEDYU_KEYCLOAK_REALM=cledyu-learn` 확인. 만료라면 프론트가 자동으로 refresh 후 재시도하므로 사용자 영향 없음. |
| 실습 중 주기적으로 로그인 페이지로 튕김 | refresh 실패 — Keycloak SSO Session Idle 이 너무 짧거나(§2 참고) refresh_token 이 폐기됨. `POST /api/v1/auth/refresh` 응답 코드(`refresh_failed`)와 Keycloak 세션 설정 확인. |
| 소셜 버튼 안 보임 | `enable_social_idp=false` 또는 IdP redirect URI 미등록. |
| 학습자가 ArgoCD 접근됨 | **격리 실패** — 학습자가 `cledyu`(운영) realm 에 생성되었는지 확인. learn realm 이어야 함. |
