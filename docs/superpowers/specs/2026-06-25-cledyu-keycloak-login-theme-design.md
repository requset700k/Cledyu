# cledyu Keycloak 로그인 테마 설계

작성일 2026-06-25. 학습자(`cledyu-learn` realm)가 마주치는 Keycloak 호스팅 인증
페이지를 cledyu 브랜드로 통일하고 더 인터랙티브하게 만든다. PR #175 까지 social 로그인이
동작하지만 로그인/회원가입 폼이 Keycloak 기본 테마라 밋밋하다는 피드백에서 출발했다.
PR #324에서 web 로그인 화면이 monochrome/grid 디자인으로 변경된 뒤에는 인증 전환 시
시각적 단절이 생기지 않도록 Keycloak 테마도 같은 디자인 시스템으로 맞춘다.

## 목표 / 비목표

- **목표**: cledyu-learn 자가가입 플로우 전 화면을 검정 monochrome/grid 톤 + CLEDYU
  워드마크 + 절제된 마이크로인터랙션으로 통일. web 앱 로그인 랜딩과 톤 일치.
- **비목표(이번 범위 밖)**: 이메일 본문 템플릿(별도 theme 타입), 운영 `cledyu` realm
  (ArgoCD/Grafana SSO) 테마, web 앱 자체 리디자인. Kakao/Naver 버튼은 게이트 켜질 때
  같은 스타일로 자연히 포함.

## 비주얼 방향 (브레인스토밍 확정)

- 절제된 마이크로인터랙션: 버튼 hover 시 `translateY(-1px)`, 입력창 포커스 시 흰색 링,
  부드러운 트랜지션. 무거운 JS·배경 애니메이션 없음.
- 로고: 대문자 자간 `CLEDYU`, 글로우 없이 web과 같은 얇은 흰색 워드마크.
- 토큰: 배경 `#030303` + 56px grid, 카드 `rgba(255,255,255,.025)`, 흰색 primary CTA,
  텍스트 `#f2f2f2`와 반투명 보조색. Kakao/Naver는 각 공식 브랜드 색을 유지.

## 범위 페이지 (login 테마 타입)

`login`(이메일+소셜), `register`, `login-reset-password`, `login-update-password`,
`login-verify-email`, `login-otp`, `error`, `info`, `login-page-expired`.

## 아키텍처

### 테마 구조 — `infra/keycloak-theme/cledyu/login/`
- `theme.properties`: `parent=keycloak`(base 상속, override 최소화), `styles=css/cledyu.css`,
  `locales` 한국어 우선.
- `resources/css/cledyu.css`: monochrome 디자인 토큰 + grid 배경 + 카드/폼/소셜버튼
  (`kc-social-*`) 스타일. Keycloak 기본 템플릿 구조는 그대로 사용한다.
- `messages/messages_ko.properties`: 태그라인·커스텀 라벨.

### 배포 (구현: ConfigMap 마운트 — 커스텀 이미지 대신)
테마가 CSS+properties뿐(바이너리 에셋 없음)이라 커스텀 이미지/CI 없이 ConfigMap 으로
마운트한다 — 로컬 Docker 없이 즉시 배포, CSS 반복도 configmap 갱신+롤로 빠름.
- ansible `keycloak_foundation`: 테마 파일을 ConfigMap(`cledyu-keycloak-theme`)으로 생성하는
  태스크 + CR `unsupported.podTemplate` 에 볼륨/마운트(`/opt/keycloak/themes/cledyu`).
  `keycloak_foundation_theme_configmap` 변수(기본값=configmap 이름)로 게이트.
- realm: terraform `realm-learn.tf` cledyu-learn 에 `login_theme="cledyu"` +
  `internationalization{ supported_locales=[ko,en], default_locale=ko }`(테마 한국어 라벨)
  apply(시크릿 -var/SM 소싱).
- 라이브 적용(2026-06-25): configmap+CR 패치+realm i18n 으로 적용·검증 완료(headless 캡처).
  커밋된 ansible/terraform 이 이를 재현·영구화한다.
- **커스텀 이미지(향후)**: 테마가 바이너리 에셋(이미지/폰트)으로 커져 ConfigMap(1MB) 한계에
  닿으면 Dockerfile(FROM keycloak:26.6.1 + 테마 COPY) 추가 + CI 이미지 leg + CR spec.image 로
  승격. 현 단계(CSS+properties)에선 불필요해 Dockerfile 미포함(base 이미지 CVE Trivy 게이트 회피).

### KC26 버전 결합 완화
`parent=keycloak` 상속 + CSS·`template.ftl`·페이지별 최소 오버라이드만 → Keycloak 업그레이드
시 깨질 면적 최소화.

## 테스트 / 검증

- 로컬: Keycloak DOM 미리보기로 데스크톱·모바일·낮은 화면의 렌더와 스크롤 접근성 확인.
- 라이브: `login_theme` 적용 후 `auth.cledyu.com` 에서 login/register/reset/verify/otp/
  error 페이지 렌더 + 색 대비(접근성) 확인. social 로그인 왕복 회귀 확인.
- 회귀: 운영 `cledyu` realm 은 `login_theme` 미설정 유지(학습자 realm 만 영향).

## 리스크

- KC 템플릿 구조가 버전마다 변함 → 최소 오버라이드로 완화, 업그레이드 시 테마 점검 체크.
- operator 가 커스텀 이미지로 롤 시 짧은 auth 중단(허용, 실사용자 없음).
- 커스텀 이미지 CI leg 실패 시 태그 bump 막힘([[project_cd_build_apps_trivy_gate]] 패턴).

## 후속(선택)

Keycloak 업그레이드 시 로그인·회원가입·비밀번호 재설정 화면의 classic selector 호환성을
함께 점검한다.
