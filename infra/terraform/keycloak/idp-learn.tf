# cledyu-learn realm 소셜 Identity Provider — 구글 / 카카오 / 네이버.
#
# Keycloak 이 이 IdP 들을 브로커링한다: 학습자가 "구글로 로그인" 을 누르면
# Keycloak 이 구글로 위임 인증한 뒤 cledyu-learn realm 토큰을 자체 발급한다.
# 따라서 앱(web/api)은 여전히 Keycloak 하나만 바라본다.
#
# enable_social_idp=false 면 IdP 리소스를 만들지 않는다 (실 client id/secret
# 발급 전 안전). client id 는 공개값(idp_client_ids), secret 은 보안 저장소
# (idp_client_secrets)에서 주입.

# ── Google (네이티브 OIDC) ───────────────────────────────────────────────
resource "keycloak_oidc_google_identity_provider" "google" {
  count = var.enable_social_idp ? 1 : 0

  realm         = keycloak_realm.cledyu_learn.id
  client_id     = var.idp_client_ids["google"]
  client_secret = var.idp_client_secrets["google"]

  trust_email                             = true
  store_token                             = false
  sync_mode                               = "IMPORT"
  default_scopes                          = "openid email profile"
  hide_on_login_page                      = false
  accepts_prompt_none_forward_from_client = false
}

# ── Kakao (OIDC 지원) ────────────────────────────────────────────────────
resource "keycloak_oidc_identity_provider" "kakao" {
  count = var.enable_social_idp ? 1 : 0

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

# ── Naver (OAuth2 — 완전한 OIDC 아님) ────────────────────────────────────
# Naver 는 ID 토큰을 발급하지 않으므로 서명검증을 끄고(user_info 기반) 매핑한다.
# userinfo 응답이 { "response": { id, email, name, ... } } 형태로 중첩되어 있어
# 아래 attribute importer 매퍼로 평탄화한다.
resource "keycloak_oidc_identity_provider" "naver" {
  count = var.enable_social_idp ? 1 : 0

  realm        = keycloak_realm.cledyu_learn.id
  alias        = "naver"
  display_name = "Naver"
  provider_id  = "oidc"
  enabled      = true

  client_id     = var.idp_client_ids["naver"]
  client_secret = var.idp_client_secrets["naver"]

  authorization_url = "https://nid.naver.com/oauth2.0/authorize"
  token_url         = "https://nid.naver.com/oauth2.0/token"
  user_info_url     = "https://openapi.naver.com/v1/nid/me"

  validate_signature = false
  trust_email        = true
  store_token        = false
  sync_mode          = "IMPORT"
}

# Naver username: response.id 를 고유 키로.
resource "keycloak_user_template_importer_identity_provider_mapper" "naver_username" {
  count = var.enable_social_idp ? 1 : 0

  realm                   = keycloak_realm.cledyu_learn.id
  name                    = "naver-username"
  identity_provider_alias = keycloak_oidc_identity_provider.naver[0].alias
  template                = "$${CLAIM.response.id}"

  extra_config = {
    syncMode = "INHERIT"
  }
}

# Naver email → user.email
resource "keycloak_attribute_importer_identity_provider_mapper" "naver_email" {
  count = var.enable_social_idp ? 1 : 0

  realm                   = keycloak_realm.cledyu_learn.id
  name                    = "naver-email"
  identity_provider_alias = keycloak_oidc_identity_provider.naver[0].alias
  claim_name              = "response.email"
  user_attribute          = "email"

  extra_config = {
    syncMode = "INHERIT"
  }
}

# Naver name → user.firstName
resource "keycloak_attribute_importer_identity_provider_mapper" "naver_name" {
  count = var.enable_social_idp ? 1 : 0

  realm                   = keycloak_realm.cledyu_learn.id
  name                    = "naver-name"
  identity_provider_alias = keycloak_oidc_identity_provider.naver[0].alias
  claim_name              = "response.name"
  user_attribute          = "firstName"

  extra_config = {
    syncMode = "INHERIT"
  }
}
