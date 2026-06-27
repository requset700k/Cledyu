# cledyu-learn realm 소셜 Identity Provider — 구글 / 카카오 / 네이버.
#
# Keycloak 이 이 IdP 들을 브로커링한다: 학습자가 "구글로 로그인" 을 누르면
# Keycloak 이 구글로 위임 인증한 뒤 cledyu-learn realm 토큰을 자체 발급한다.
# 따라서 앱(web/api)은 여전히 Keycloak 하나만 바라본다.
#
# enabled_social_idps 에 포함된 alias 만 IdP 리소스를 만든다 (실 client id/secret
# 발급된 provider 만 단계적으로 활성화 — 미발급 provider 로 라우팅돼 실패하는 것을
# 막는다). client id 는 공개값(idp_client_ids), secret 은 보안 저장소
# (idp_client_secrets)에서 주입.

# ── Google (네이티브 OIDC) ───────────────────────────────────────────────
resource "keycloak_oidc_google_identity_provider" "google" {
  # alias 는 provider 타입에서 "google" 로 고정된다(kc_idp_hint=google 와 일치).
  count = contains(var.enabled_social_idps, "google") ? 1 : 0

  realm         = keycloak_realm.cledyu_learn.id
  client_id     = var.idp_client_ids["google"]
  client_secret = var.idp_client_secrets["google"]

  trust_email                             = true
  store_token                             = false
  sync_mode                               = "IMPORT"
  default_scopes                          = "openid email profile"
  hide_on_login_page                      = false
  accepts_prompt_none_forward_from_client = false

  # 로그아웃 후 재로그인 시 구글 계정 선택 화면을 띄운다(멀티프로바이더 SaaS 표준 — 비밀번호
  # 재입력 없이 계정 확인·전환 가능). 앱은 IdP 세션을 끊을 수 없어(구글 세션 잔존) silent 자동
  # 재로그인 대신 select_account 로 보완한다. 네이버는 OAuth2 라 prompt 미지원 → 현행(silent) 유지.
  extra_config = {
    prompt = "select_account"
  }
}

# ── Kakao (OIDC 지원) ────────────────────────────────────────────────────
resource "keycloak_oidc_identity_provider" "kakao" {
  count = contains(var.enabled_social_idps, "kakao") ? 1 : 0

  realm        = keycloak_realm.cledyu_learn.id
  alias        = "kakao"
  display_name = "Kakao"
  provider_id  = "oidc"
  enabled      = true

  client_id     = var.idp_client_ids["kakao"]
  client_secret = var.idp_client_secrets["kakao"]

  authorization_url = "https://kauth.kakao.com/oauth/authorize"
  token_url         = "https://kauth.kakao.com/oauth/token"
  user_info_url     = "https://kapi.kakao.com/v1/oidc/userinfo"
  jwks_url          = "https://kauth.kakao.com/.well-known/jwks.json"
  issuer            = "https://kauth.kakao.com"

  default_scopes     = "openid account_email profile_nickname"
  validate_signature = true
  trust_email        = true
  store_token        = false
  sync_mode          = "IMPORT"
}

# ── Naver (커스텀 OAuth2 SPI) ────────────────────────────────────────────
# Naver 는 OIDC id_token 을 발급하지 않아 빌트인 provider_id="oidc" 로는 동작하지 않는다
# (토큰 응답에서 id_token 강제 추출 → "No token from server" 실패). 그래서
# keycloak/naver-idp 커스텀 SPI(provider_id="naver", AbstractOAuth2IdentityProvider 상속)를
# 배포해 access_token + userinfo(https://openapi.naver.com/v1/nid/me) 로 브로커링한다.
# SPI 가 userinfo 의 중첩 response.{id,email,name} 를 네이티브로 매핑하므로 별도 매퍼 불필요.
#
# 전제: keycloak 파드에 SPI JAR 이 /opt/keycloak/providers/ 로 마운트되어 "naver" provider
# 가 등록돼 있어야 한다(ansible keycloak_foundation, ConfigMap 마운트). 미등록 상태에서
# apply 하면 unknown provider 로 실패한다.
resource "keycloak_oidc_identity_provider" "naver" {
  count = contains(var.enabled_social_idps, "naver") ? 1 : 0

  realm        = keycloak_realm.cledyu_learn.id
  alias        = "naver"
  display_name = "Naver"
  provider_id  = "naver"
  enabled      = true

  client_id     = var.idp_client_ids["naver"]
  client_secret = var.idp_client_secrets["naver"]

  # SPI 가 엔드포인트를 하드코딩하지만 mrparkers 리소스가 두 URL 을 필수로 요구한다(SPI 가 덮어씀).
  authorization_url = "https://nid.naver.com/oauth2.0/authorize"
  token_url         = "https://nid.naver.com/oauth2.0/token"

  trust_email = true
  store_token = false
  sync_mode   = "IMPORT"
}
