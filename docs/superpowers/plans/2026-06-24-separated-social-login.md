# 소셜 로그인 분리 (이메일 · Google · Kakao · Naver) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 로그인 페이지를 이메일/Google/Kakao/Naver 분리 버튼으로 재구성하고, 백엔드가 `kc_idp_hint`로 특정 IdP에 직접 라우팅하도록 하며, 로컬 개발의 `CLEDYU_BACKEND_URL` 공백을 해소한다.

**Architecture:** 웹 4버튼이 `/api/v1/auth/login[?idp=<alias>]`을 호출 → Next 프록시 → 백엔드 `Login` 핸들러가 `idp`를 화이트리스트 검증 후 `AuthCodeURL`이 Keycloak 인가 URL에 `kc_idp_hint`를 부착. state/nonce/PKCE 기존 보안 흐름 유지.

**Tech Stack:** Go 1.26 (gin, golang.org/x/oauth2, coreos/go-oidc), Next.js 15 (App Router, TypeScript, Tailwind).

## Global Constraints

- IdP 화이트리스트 alias 는 정확히 `google` · `kakao` · `naver` (terraform `idp-learn.tf` 와 일치).
- `idp` · `register` 는 독립 — 회원가입 링크는 idp 를 넘기지 않는다.
- 비허용/미지정 `idp` → `kc_idp_hint` 미부착(이메일/Keycloak 폼 폴백).
- 커밋 메시지 subject 는 소문자 시작, Conventional Commits, scope ∈ {api, web}.
- GitHub 텍스트·코드 주석에 이모지 금지.
- dev 서버 실행 중 같은 디렉터리 `next build` 금지(`.next` 충돌) — 검증은 `lint`/`typecheck`만.

---

### Task 1: 백엔드 `AuthCodeURL` 에 `kc_idp_hint` 추가

**Files:**
- Modify: `apps/api/internal/auth/oidc.go:132-142` (AuthCodeURL)
- Modify: `apps/api/internal/api/handlers/auth.go:62` (호출부 — 빌드 유지용 `""` 전달)
- Test: `apps/api/internal/auth/oidc_test.go` (신규 테스트 추가)

**Interfaces:**
- Produces: `func (p *Provider) AuthCodeURL(state, nonce, pkceVerifier string, register bool, idp string) string` — `idp != ""` 이면 인가 URL 에 `kc_idp_hint=<idp>` 쿼리 부착.

- [ ] **Step 1: Write the failing test**

`apps/api/internal/auth/oidc_test.go` 끝에 추가 (파일은 `package auth` — 비공개 필드 접근 가능):

```go
import (
	"net/url"
	"testing"

	"golang.org/x/oauth2"
)

func testProvider() *Provider {
	return &Provider{
		oauth2: oauth2.Config{
			ClientID:    "web",
			RedirectURL: "https://api.cledyu.local/api/v1/auth/callback",
			Endpoint: oauth2.Endpoint{
				AuthURL:  "https://keycloak.cledyu.local/realms/cledyu-learn/protocol/openid-connect/auth",
				TokenURL: "https://keycloak.cledyu.local/realms/cledyu-learn/protocol/openid-connect/token",
			},
			Scopes: []string{"openid", "profile", "email"},
		},
	}
}

func TestAuthCodeURL_IdPHint(t *testing.T) {
	p := testProvider()

	got := p.AuthCodeURL("st", "no", "verifier", false, "google")
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	if u.Query().Get("kc_idp_hint") != "google" {
		t.Errorf("kc_idp_hint = %q, want google", u.Query().Get("kc_idp_hint"))
	}
}

func TestAuthCodeURL_NoHint_WhenEmpty(t *testing.T) {
	p := testProvider()

	got := p.AuthCodeURL("st", "no", "verifier", false, "")
	u, _ := url.Parse(got)
	if u.Query().Has("kc_idp_hint") {
		t.Errorf("kc_idp_hint must be absent, got %q", u.Query().Get("kc_idp_hint"))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd apps/api && go test ./internal/auth/ -run TestAuthCodeURL -v`
Expected: 컴파일 실패 — `too many arguments in call to p.AuthCodeURL`.

- [ ] **Step 3: Update AuthCodeURL signature and add hint**

`apps/api/internal/auth/oidc.go` 의 AuthCodeURL 을 교체:

```go
// AuthCodeURL은 state/nonce/PKCE(S256) 를 적용한 Keycloak 인가 URL을 만든다.
// register=true 면 로그인 대신 회원가입 폼으로 딥링크한다(Keycloak 의 /registrations
// 엔드포인트). idp != "" 면 kc_idp_hint 를 붙여 해당 IdP 로 직행한다 —
// OIDC 흐름(state/PKCE/redirect)은 그대로 유지된다.
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

- [ ] **Step 4: Keep the existing caller compiling**

`apps/api/internal/api/handlers/auth.go:62` 의 호출에 `""` 인자 추가(아직 idp 미배선):

```go
	c.Redirect(http.StatusFound, h.auth.AuthCodeURL(state, nonce, pkce, register, ""))
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd apps/api && go build ./... && go test ./internal/auth/ -run TestAuthCodeURL -v`
Expected: PASS (두 테스트 모두), 빌드 성공.

- [ ] **Step 6: Commit**

```bash
git add apps/api/internal/auth/oidc.go apps/api/internal/auth/oidc_test.go apps/api/internal/api/handlers/auth.go
git commit -m "feat(api): add kc_idp_hint support to authcodeurl"
```

---

### Task 2: 백엔드 `Login` 핸들러 idp 화이트리스트 배선

**Files:**
- Modify: `apps/api/internal/api/handlers/auth.go` (normalizeIdP 헬퍼 + Login 에서 사용)
- Test: `apps/api/internal/api/handlers/auth_idp_test.go` (신규)

**Interfaces:**
- Consumes: `AuthCodeURL(..., idp string)` (Task 1).
- Produces: `func normalizeIdP(raw string) string` — 허용 alias(`google`/`kakao`/`naver`)면 그대로, 아니면 `""`.

- [ ] **Step 1: Write the failing test**

`apps/api/internal/api/handlers/auth_idp_test.go` 신규 생성:

```go
package handlers

import "testing"

func TestNormalizeIdP(t *testing.T) {
	cases := map[string]string{
		"google": "google",
		"kakao":  "kakao",
		"naver":  "naver",
		"":       "",
		"evil":   "",
		"GOOGLE": "", // 대소문자 정확 일치만 허용
		"google ": "",
	}
	for in, want := range cases {
		if got := normalizeIdP(in); got != want {
			t.Errorf("normalizeIdP(%q) = %q, want %q", in, got, want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd apps/api && go test ./internal/api/handlers/ -run TestNormalizeIdP -v`
Expected: 컴파일 실패 — `undefined: normalizeIdP`.

- [ ] **Step 3: Add helper and wire into Login**

`apps/api/internal/api/handlers/auth.go` 의 `randToken` 위에 헬퍼 추가:

```go
// allowedIdP 는 kc_idp_hint 로 통과시킬 Keycloak IdP alias 화이트리스트다
// (infra/terraform/keycloak/idp-learn.tf 의 alias 와 일치).
var allowedIdP = map[string]bool{"google": true, "kakao": true, "naver": true}

// normalizeIdP 는 허용된 alias 만 반환하고, 그 외(미지정·임의 값)는 "" 로 폴백한다.
// 오픈 리다이렉트·파라미터 주입을 막는 서버측 검증 지점이다.
func normalizeIdP(raw string) string {
	if allowedIdP[raw] {
		return raw
	}
	return ""
}
```

`Login` 핸들러에는 이미 `register := c.Query("screen") == "register"` 줄이 있다.
그 줄은 그대로 두고, 바로 아래에 `idp :=` 한 줄을 추가한 뒤 redirect 호출(Task 1 에서
`""` 로 둔 마지막 인자)을 `idp` 로 바꾼다. 즉 기존 두 줄:

```go
	register := c.Query("screen") == "register"
	c.Redirect(http.StatusFound, h.auth.AuthCodeURL(state, nonce, pkce, register, ""))
```

를 다음으로 교체:

```go
	register := c.Query("screen") == "register"
	idp := normalizeIdP(c.Query("idp"))
	c.Redirect(http.StatusFound, h.auth.AuthCodeURL(state, nonce, pkce, register, idp))
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd apps/api && go build ./... && go test ./internal/api/handlers/ -run TestNormalizeIdP -v`
Expected: PASS, 빌드 성공.

- [ ] **Step 5: Run full api test suite (regression)**

Run: `cd apps/api && go test ./...`
Expected: 전체 PASS.

- [ ] **Step 6: Commit**

```bash
git add apps/api/internal/api/handlers/auth.go apps/api/internal/api/handlers/auth_idp_test.go
git commit -m "feat(api): route login to specific idp via whitelisted hint"
```

---

### Task 3: 웹 로그인 페이지 4버튼 분리

**Files:**
- Modify: `apps/web/app/(auth)/login/page.tsx` (버튼 영역 교체)

**Interfaces:**
- Consumes: 백엔드 라우트 `/api/v1/auth/login`, `?idp=google|kakao|naver`, `?screen=register`.

- [ ] **Step 1: Replace the single-button block with four provider buttons**

`apps/web/app/(auth)/login/page.tsx` 에서 기존 단일 `<a href="/api/v1/auth/login">로그인 / 소셜 로그인</a>` 블록(시작하기 카드 안)을 아래로 교체. 회원가입 링크(`?screen=register`)와 Feature 목록은 유지:

```tsx
          {/* 이메일(Keycloak 폼) — 주 버튼 */}
          {/* eslint-disable-next-line @next/next/no-html-link-for-pages */}
          <a
            href="/api/v1/auth/login"
            aria-label="이메일로 로그인"
            className="flex items-center justify-center gap-2 w-full bg-brand-500 hover:bg-brand-600 text-white font-medium py-3 px-4 rounded-xl transition-colors duration-150"
          >
            이메일로 로그인
          </a>

          <div className="flex items-center gap-3 my-5">
            <span className="h-px flex-1 bg-slate-700" />
            <span className="text-slate-500 text-xs">또는 소셜 계정으로</span>
            <span className="h-px flex-1 bg-slate-700" />
          </div>

          <div className="space-y-3">
            {/* eslint-disable-next-line @next/next/no-html-link-for-pages */}
            <a
              href="/api/v1/auth/login?idp=google"
              aria-label="Google 계정으로 계속"
              className="flex items-center justify-center gap-2 w-full bg-white hover:bg-slate-100 text-slate-800 font-medium py-3 px-4 rounded-xl transition-colors duration-150"
            >
              <span className="font-bold text-[#4285F4]">G</span>
              Google 로 계속
            </a>
            {/* eslint-disable-next-line @next/next/no-html-link-for-pages */}
            <a
              href="/api/v1/auth/login?idp=kakao"
              aria-label="카카오 계정으로 계속"
              className="flex items-center justify-center gap-2 w-full bg-[#FEE500] hover:brightness-95 text-[#191600] font-medium py-3 px-4 rounded-xl transition-all duration-150"
            >
              Kakao 로 계속
            </a>
            {/* eslint-disable-next-line @next/next/no-html-link-for-pages */}
            <a
              href="/api/v1/auth/login?idp=naver"
              aria-label="네이버 계정으로 계속"
              className="flex items-center justify-center gap-2 w-full bg-[#03C75A] hover:brightness-95 text-white font-medium py-3 px-4 rounded-xl transition-all duration-150"
            >
              <span className="font-bold">N</span>
              Naver 로 계속
            </a>
          </div>
```

- [ ] **Step 2: Typecheck**

Run: `cd apps/web && npm run typecheck`
Expected: 에러 없음(exit 0).

- [ ] **Step 3: Lint**

Run: `cd apps/web && npm run lint`
Expected: 에러 없음. (dev 서버 실행 중이면 `next build` 는 절대 실행하지 말 것 — `.next` 충돌.)

- [ ] **Step 4: Commit**

```bash
git add apps/web/app/\(auth\)/login/page.tsx
git commit -m "feat(web): split login into email and per-provider buttons"
```

---

### Task 4: 로컬 개발 `CLEDYU_BACKEND_URL` 언블록

**Files:**
- Create: `apps/web/.env.local.example`
- Modify: `.gitignore` (루트)
- Create: `apps/web/README.md`

**Interfaces:**
- Consumes: 프록시 라우트 `apps/web/app/api/[...path]/route.ts` 가 읽는 `process.env.CLEDYU_BACKEND_URL`.

- [ ] **Step 1: Create the committed env template**

`apps/web/.env.local.example`:

```bash
# 로컬 개발용 백엔드(Session API) URL. 복사해서 .env.local 로 쓴다:
#   cp apps/web/.env.local.example apps/web/.env.local
#
# 옵션 A (권장) — api 서비스 포트포워드 + http (TLS 우회):
#   kubectl -n api port-forward svc/api 8080:80
CLEDYU_BACKEND_URL=http://localhost:8080
#
# 옵션 B — 클러스터 인그레스(self-signed TLS):
#   /etc/hosts 에 추가:  10.10.0.101 api.cledyu.local app.cledyu.local
# CLEDYU_BACKEND_URL=https://api.cledyu.local
# NODE_TLS_REJECT_UNAUTHORIZED=0   # dev 전용 — 내부 CA 인증서 거부 회피
```

- [ ] **Step 2: Ignore real local env files (keep the example tracked)**

루트 `.gitignore` 에 추가(파일 없으면 생성):

```bash
printf '\n# 로컬 env (예시 파일은 추적 유지)\napps/web/.env*.local\n' >> .gitignore
```

확인: `git check-ignore apps/web/.env.local` → 매치, `git check-ignore apps/web/.env.local.example` → 미매치(추적 유지).

- [ ] **Step 3: Create the local dev README**

`apps/web/README.md`:

```markdown
# Cledyu Web (Next.js)

## 로컬 개발

1. 백엔드 URL 설정:

   ```bash
   cp apps/web/.env.local.example apps/web/.env.local
   # 옵션 A(권장): 별도 터미널에서 api 포트포워드
   kubectl -n api port-forward svc/api 8080:80
   ```

2. 개발 서버:

   ```bash
   cd apps/web && npm run dev    # http://localhost:3000
   ```

`CLEDYU_BACKEND_URL` 이 없으면 로그인 시
`{ "error": "backend url is not configured" }` 가 난다 — 위 1번으로 해소한다.

### 한계

redirect_uri·frontend_url·쿠키 도메인이 `*.cledyu.local` 이므로 OAuth 왕복은 배포 웹
(`app.cledyu.local`)에서 완료되고 localhost:3000 에는 세션 쿠키가 남지 않는다.
localhost:3000 은 버튼 → IdP 라우팅 확인용이다. 완전한 localhost 세션은 전체 로컬 스택
(API+DB+Redis 로컬 구동 + Keycloak 에 localhost redirect URI 등록)이 필요하다.
```

- [ ] **Step 4: Verify ignore rules**

Run: `git check-ignore apps/web/.env.local.example || echo "example tracked (good)"`
Expected: `example tracked (good)` 출력(예시 파일은 무시되지 않음).

- [ ] **Step 5: Commit**

```bash
git add apps/web/.env.local.example apps/web/README.md .gitignore
git commit -m "docs(web): document local backend url setup for dev"
```

---

## 검증 (전체)

- 백엔드: `cd apps/api && go build ./... && go test ./...` 전부 PASS.
- 웹: `cd apps/web && npm run typecheck && npm run lint` 에러 없음.
- 클러스터 실측(머지 후, service-api 싱크 뒤): 각 버튼 클릭 시
  - 이메일 → Keycloak 로그인 폼,
  - Google/Kakao/Naver → 해당 IdP 인증 화면으로 직행(통합 페이지 건너뜀).
