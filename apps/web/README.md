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
