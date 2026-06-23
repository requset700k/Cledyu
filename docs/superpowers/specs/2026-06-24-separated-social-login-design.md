# 소셜 로그인 분리 (이메일 · Google · Kakao · Naver) — 설계

- 작성일: 2026-06-24
- 작성자: 김용균
- 상태: 설계 승인 대기

## 배경

웹 로그인 페이지(`apps/web/app/(auth)/login/page.tsx`)에는 현재 버튼이 하나뿐이다
(`로그인 / 소셜 로그인` → `/api/v1/auth/login`). 이 단일 진입점은 백엔드를 거쳐
Keycloak 통합 로그인 페이지로 리다이렉트되고, 거기서 이메일·소셜 수단이 한데 모여
표시된다. 사용자는 **이메일 · Google · Kakao · Naver 를 각각 분리된 버튼**으로
첫 화면에서 바로 선택하길 원한다.

추가로, 로컬 개발 시 로그인 시도하면 다음 에러가 난다:

```json
{ "error": "backend url is not configured" }
```

원인은 웹의 프록시 라우트(`apps/web/app/api/[...path]/route.ts`)가 `/api/*` 요청을
`CLEDYU_BACKEND_URL` 백엔드로 포워딩하는데, 로컬 `next dev` 환경에 이 변수가
설정돼 있지 않기 때문이다. 배포 환경(`gitops/apps/web/values.yaml`)에는
`CLEDYU_BACKEND_URL: http://api.api.svc.cluster.local` 로 주입돼 정상 동작하므로,
이 에러는 **순수 로컬 개발 환경의 공백**이다.

## 목표 / 비목표

### 목표
1. 백엔드 `Login` 핸들러가 `idp` 파라미터를 받아 Keycloak `kc_idp_hint` 로 특정
   IdP(google/kakao/naver)에 직접 라우팅한다.
2. 웹 로그인 페이지를 이메일·Google·Kakao·Naver **4개 분리 버튼** + 회원가입 링크로
   재구성한다.
3. 로컬 개발에서 로그인 플로우를 시작할 수 있도록 `CLEDYU_BACKEND_URL` 설정 절차를
   런북에 명시하고 `.env.local` 처리를 정리한다.

### 비목표 (후속)
- 퍼블릭 도메인 컷오버: `*.cledyu.local` → 실 공개 도메인 전환(인그레스·TLS·Keycloak
  redirect URI·Google/Kakao/Naver OAuth 앱 redirect 재등록).
- 완전한 localhost 로그인 세션(전체 로컬 스택: Go API + DB + Redis 로컬 구동, Keycloak
  web 클라이언트에 localhost redirect URI 등록).
- 계정 저장소 변경(아래 "계정 데이터 흐름" 참고 — 변경 없음).

## 계정 데이터 흐름 (변경 없음 — 확인용)

본 기능은 계정 저장소를 바꾸지 않는다. 결정 사항을 명시한다:

- **신원 · 자격증명 · 소셜 federation: Keycloak (`cledyu-learn` realm).**
  Google/Kakao/Naver IdP 가 identity brokering 으로 Keycloak 에 연동돼 있다
  (`infra/terraform/keycloak/idp-learn.tf`, alias = `google`/`kakao`/`naver`).
  "계정정보가 들어오는 곳"이 Keycloak 이다 — 모델 A 유지.
- **앱측 프로필 미러 + 학습 데이터: PostgreSQL.**
  로그인(콜백/리프레시) 시 `store.UpsertUser` 가 Keycloak 신원(subject·email·name·role)을
  `users` 테이블로 미러링한다(관리자 콘솔·FK용). DSN 은 `CLEDYU_DB_DSN` 시크릿.
- **AWS 는 신원 경로에 들어가지 않는다.** PR #149 의 AWS 사용은 컴퓨트(온프렘 만석 시
  Lab VM 을 EC2 로 버스트)이며 신원과 직교한다. Cognito 도입은 Keycloak federation 과
  중복·충돌(Cognito 는 Kakao/Naver 네이티브 미지원)하고 모델 A 를 뒤집으므로 채택하지
  않는다.

## 아키텍처 — `kc_idp_hint` 라우팅

Keycloak 은 인가 URL 에 `kc_idp_hint=<alias>` 쿼리를 주면 통합 로그인 페이지를 건너뛰고
해당 IdP 로 바로 보낸다. 흐름:

```
[웹 로그인 페이지]
  ├─ 이메일  → GET /api/v1/auth/login            (hint 없음 → Keycloak 폼)
  ├─ Google → GET /api/v1/auth/login?idp=google  (kc_idp_hint=google)
  ├─ Kakao  → GET /api/v1/auth/login?idp=kakao   (kc_idp_hint=kakao)
  └─ Naver  → GET /api/v1/auth/login?idp=naver   (kc_idp_hint=naver)
        │
        ▼ (Next 프록시 → CLEDYU_BACKEND_URL)
[백엔드 Login 핸들러]
  - state/nonce/PKCE 생성 + 임시 쿠키 (기존 그대로)
  - idp 화이트리스트 검증
  - AuthCodeURL(... , idp) → kc_idp_hint 부착
        │
        ▼ 302
[Keycloak] → (idp 지정 시) 해당 IdP 로 직행 → 콜백 → 토큰 → 세션 쿠키
```

state/nonce/PKCE 등 기존 보안 흐름은 그대로 유지되고 `kc_idp_hint` 쿼리만 추가된다.

## 백엔드 변경 (`apps/api`)

### `internal/auth/oidc.go`
`AuthCodeURL` 시그니처에 `idp string` 추가:

```go
func (p *Provider) AuthCodeURL(state, nonce, pkceVerifier string, register bool, idp string) string {
    opts := []oauth2.AuthCodeOption{
        oidc.Nonce(nonce),
        oauth2.S256ChallengeOption(pkceVerifier),
    }
    if idp != "" {
        opts = append(opts, oauth2.SetAuthURLParam("kc_idp_hint", idp))
    }
    u := p.oauth2.AuthCodeURL(state, opts...)
    if register {
        u = strings.Replace(u, "/protocol/openid-connect/auth", "/protocol/openid-connect/registrations", 1)
    }
    return u
}
```

`idp` 와 `register` 는 독립이다(소셜은 자체 가입을 IdP 가 처리). 동시 지정 시 register
딥링크 + kc_idp_hint 가 함께 붙지만, UI 상 회원가입 링크는 idp 를 넘기지 않으므로 충돌
없음.

### `internal/api/handlers/auth.go` — `Login`
`idp` 쿼리를 **화이트리스트로 검증**하여 통과시킨다:

```go
// 허용된 IdP alias 만 kc_idp_hint 로 전달한다(오픈 리다이렉트·파라미터 주입 방지).
var allowedIdP = map[string]bool{"google": true, "kakao": true, "naver": true}

idp := c.Query("idp")
if !allowedIdP[idp] {
    idp = "" // 미지정·미허용 값은 이메일/Keycloak 폼으로 폴백
}
register := c.Query("screen") == "register"
c.Redirect(http.StatusFound, h.auth.AuthCodeURL(state, nonce, pkce, register, idp))
```

### 테스트 (`internal/auth/oidc_test.go` 신규 또는 핸들러 테스트)
- idp=google/kakao/naver → 인가 URL 에 `kc_idp_hint=<idp>` 포함.
- idp 미지정 → `kc_idp_hint` 미포함.
- idp 비허용 값(예: `evil`) → `kc_idp_hint` 미포함(폴백).

## 웹 변경 (`apps/web/app/(auth)/login/page.tsx`)

단일 `<a>` 버튼을 4개 분리 버튼으로 교체한다. 구조:

- **이메일로 로그인** → `/api/v1/auth/login` (기존 brand 색 주 버튼)
- **Google 로 계속** → `/api/v1/auth/login?idp=google`
- **Kakao 로 계속** → `/api/v1/auth/login?idp=kakao`
- **Naver 로 계속** → `/api/v1/auth/login?idp=naver`
- 하단 회원가입 링크(`?screen=register`)와 기능 소개(Feature)는 유지.

각 소셜 버튼은 공급자 브랜드 색/로고와 `aria-label` 을 갖는다(Kakao 노랑 `#FEE500`/검정
글자, Naver 초록 `#03C75A`, Google 흰 배경+컬러 로고). 구분선("또는") 으로 이메일과 소셜
영역을 분리. 기존 다크 테마(slate/brand) 톤 유지.

## 로컬 개발 언블록

`apps/web/.env.local` 에 `CLEDYU_BACKEND_URL` 을 설정한다. 런북에 두 옵션을 명시:

1. **(간편·권장) 포트포워드 + http** — TLS 우회:
   ```bash
   kubectl -n api port-forward svc/api 8080:80
   # apps/web/.env.local
   CLEDYU_BACKEND_URL=http://localhost:8080
   ```
2. **(인그레스) cledyu.local + https** — 자체서명 인증서:
   ```bash
   # /etc/hosts (keycloak.cledyu.local 은 이미 등록됨)
   10.10.0.101 api.cledyu.local app.cledyu.local
   # apps/web/.env.local
   CLEDYU_BACKEND_URL=https://api.cledyu.local
   NODE_TLS_REJECT_UNAUTHORIZED=0   # dev 전용 — Node fetch 가 내부 CA 인증서 거부 회피
   ```

**주의(설계 한계)**: redirect_uri·frontend_url·cookie_domain 이 `*.cledyu.local` 이므로
OAuth 왕복은 배포 웹(`app.cledyu.local`)에서 완료되고 localhost:3000 에는 세션 쿠키가
남지 않는다. 따라서 localhost:3000 은 **버튼 → 올바른 IdP 라우팅 확인용**이고, 완전한
localhost 로그인 세션은 비목표(전체 로컬 스택)다.

`.gitignore` 에 `apps/web/.env*.local` 추가(로컬 백엔드 URL 커밋 방지).

## 검증

- 백엔드: `go build ./...`, `go test ./internal/auth/... ./internal/api/...`.
- 웹: `npm run lint`, `npm run typecheck` (dev 서버 실행 중 같은 디렉터리 `next build`
  금지 — `.next` 충돌).
- 클러스터 실측: 각 버튼이 Keycloak 을 거쳐 올바른 IdP(Google/Kakao/Naver) 인증 화면으로
  직행하는지, 이메일 버튼은 Keycloak 폼으로 가는지 확인.

## 리스크 / 완화

- **kc_idp_hint alias 불일치** → 잘못된 alias 는 Keycloak 이 통합 페이지로 폴백. terraform
  alias(`google`/`kakao`/`naver`)와 화이트리스트를 일치시켜 방지.
- **오픈 리다이렉트** → idp 를 서버측 화이트리스트로만 통과(임의 값 차단).
- **NODE_TLS_REJECT_UNAUTHORIZED=0 유출** → dev 전용, `.env.local`(gitignore)에만. 배포
  values 에는 절대 넣지 않음.
