# cledyu Keycloak 로그인 테마 설계

작성일 2026-06-25. 학습자(`cledyu-learn` realm)가 마주치는 Keycloak 호스팅 인증
페이지를 cledyu 브랜드로 통일하고 더 인터랙티브하게 만든다. PR #175 까지 social 로그인이
동작하지만 로그인/회원가입 폼이 Keycloak 기본 테마라 밋밋하다는 피드백에서 출발.

## 목표 / 비목표

- **목표**: cledyu-learn 자가가입 플로우 전 화면을 다크 slate/sky 톤 + 네온 워드마크 +
  절제된 마이크로인터랙션(스타일 A)으로 통일. web 앱 로그인 랜딩과 톤 일치.
- **비목표(이번 범위 밖)**: 이메일 본문 템플릿(별도 theme 타입), 운영 `cledyu` realm
  (ArgoCD/Grafana SSO) 테마, web 앱 자체 리디자인. Kakao/Naver 버튼은 게이트 켜질 때
  같은 스타일로 자연히 포함.

## 비주얼 방향 (브레인스토밍 확정)

- 스타일 **A — 절제된 마이크로인터랙션**: 버튼 hover 시 `translateY(-2px)` + 글로우,
  입력창 포커스 시 brand 링 글로우, 부드러운 트랜지션. 무거운 JS·배경 애니메이션 없음.
- 로고 **V1 — 네온 워드마크**: 대문자 자간 `CLEDYU`, sky 네온 글로우(text-shadow 다단).
- 토큰: 배경 그라데이션 `#0f172a→#0c1e3a→#172554`, brand `#0ea5e9`(hover `#0284c7`),
  카드 `rgba(17,28,46,.92)` + border `#1e293b`, 텍스트 `#e2e8f0`/`#94a3b8`.

## 범위 페이지 (login 테마 타입)

`login`(이메일+소셜), `register`, `login-reset-password`, `login-update-password`,
`login-verify-email`, `login-otp`, `error`, `info`, `login-page-expired`.

## 아키텍처

### 테마 구조 — `infra/keycloak-theme/cledyu/login/`
- `theme.properties`: `parent=keycloak`(base 상속, override 최소화), `styles=css/cledyu.css`,
  `locales` 한국어 우선.
- `resources/css/cledyu.css`: 디자인 토큰 + 스타일 A 인터랙션 + 네온 워드마크 + 카드/폼/
  소셜버튼(`kc-social-*`) 스타일.
- `template.ftl`: 공통 레이아웃 오버라이드(그라데이션 배경 + 네온 `CLEDYU` 헤더 + 카드 셸).
- 페이지별 `.ftl`: 기본 상속, 구조 변경이 필요한 것만 오버라이드.
- `messages/messages_ko.properties`: 태그라인·커스텀 라벨.
- `resources/img/`: favicon(네온 마크).

### 빌드·배포
- `infra/keycloak-theme/Dockerfile`: `FROM quay.io/keycloak/keycloak:26.6.1`, 테마 COPY 후
  `kc.sh build`(optimized 이미지).
- CI: build-apps 매트릭스에 `keycloak` leg 추가 → `ghcr.io/requset700k/keycloak:sha-*`
  (Trivy HIGH 게이트, 기존 앱과 동일 패턴).
- Keycloak operator CR(`ansible/roles/keycloak_foundation`): `keycloak_foundation_image`
  변수 추가, `spec.image`에 커스텀 이미지 지정 → 롤아웃.
- realm: terraform `realm-learn.tf` 의 cledyu-learn 에 `login_theme = "cledyu"` 설정 후 apply
  (시크릿은 -var/SM 소싱 규칙 유지).

### KC26 버전 결합 완화
`parent=keycloak` 상속 + CSS·`template.ftl`·페이지별 최소 오버라이드만 → Keycloak 업그레이드
시 깨질 면적 최소화.

## 테스트 / 검증

- 로컬: 테마 이미지 빌드+실행 → 각 페이지 렌더(네온 로고·다크·스타일 A) 육안 확인.
- 라이브: `login_theme` 적용 후 `auth.cledyu.com` 에서 login/register/reset/verify/otp/
  error 페이지 렌더 + 색 대비(접근성) 확인. social 로그인 왕복 회귀 확인.
- 회귀: 운영 `cledyu` realm 은 `login_theme` 미설정 유지(학습자 realm 만 영향).

## 리스크

- KC 템플릿 구조가 버전마다 변함 → 최소 오버라이드로 완화, 업그레이드 시 테마 점검 체크.
- operator 가 커스텀 이미지로 롤 시 짧은 auth 중단(허용, 실사용자 없음).
- 커스텀 이미지 CI leg 실패 시 태그 bump 막힘([[project_cd_build_apps_trivy_gate]] 패턴).

## 후속(선택)

web 앱 로그인 랜딩 로고를 동일 네온 워드마크로 교체해 Keycloak↔web 완전 통일.
