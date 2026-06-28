---
category: auth
---

# LoginPage

플랫폼 로그인 런처 화면 (실제 코드: `apps/web/app/(auth)/login/page.tsx`). 네온 Cledyu 브랜딩,
이메일 로그인 버튼(→ Keycloak), `CLEDYU_SOCIAL_LOGIN_PROVIDERS` env로 노출되는 소셜 버튼,
회원가입 링크, 기능 소개 리스트로 구성된다. props 없음 — 런타임 env를 읽는다.
