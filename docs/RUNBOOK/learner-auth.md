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
   - state 쿠키 일치 검증 → code+PKCE 교환 → id_token nonce 검증 → `access_token`/`id_token` HttpOnly 쿠키 설정 → `app.cledyu.local/callback` 리다이렉트.
3. 보호 API: `middleware.JWT` 가 access_token 을 JWKS 로 검증, claims(`sub`/`email`/`name`/`realm_access.roles`)를 컨텍스트에 주입.
4. `GET /api/v1/auth/logout` → 쿠키 삭제 + Keycloak end-session(SSO 로그아웃) → 프론트로 복귀.

## 3. 최초 활성화 절차

### 3.1 web client secret 발급 → Vault → ESO

```bash
# 1) Terraform tfvars 에 web client secret 설정 (보안 tfvars, gitignore)
#    learn_oidc_client_secrets = { web = "<32-byte hex>" }
openssl rand -hex 32   # 값 생성

# 2) terraform apply (cledyu-learn realm + web client 생성/secret 반영)
cd infra/terraform/keycloak && terraform apply

# 3) 같은 값을 Vault 에 등록 (api Deployment 가 ESO 로 주입받음)
vault kv put secret/oidc/cledyu-web client_secret=<위 값>

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

## 7. 트러블슈팅

| 증상 | 원인 / 조치 |
|---|---|
| 로그인 시 api 503 `auth provider not configured` | Keycloak 미가용으로 OIDC discovery 실패. `cledyu-learn` realm 존재·네트워크 확인. |
| callback `invalid oauth state` | state 쿠키 유실(SameSite/도메인). `CLEDYU_KEYCLOAK_COOKIE_DOMAIN`(`.cledyu.local`) 확인. |
| 보호 API 401 `invalid token` | access_token 만료/realm 불일치. api 의 `CLEDYU_KEYCLOAK_REALM=cledyu-learn` 확인. |
| 소셜 버튼 안 보임 | `enable_social_idp=false` 또는 IdP redirect URI 미등록. |
| 학습자가 ArgoCD 접근됨 | **격리 실패** — 학습자가 `cledyu`(운영) realm 에 생성되었는지 확인. learn realm 이어야 함. |
