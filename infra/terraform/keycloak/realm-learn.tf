# cledyu-learn realm — 외부 학습자 전용.
#
# 운영자(kube-apiserver/ArgoCD/Grafana/Kafka-UI/Vault)가 쓰는 cledyu realm 과
# 분리한다(ADR: realm 분리 옵션 C). 학습자는 여기서 자가가입(registration)하고
# 소셜 IdP(구글/카카오/네이버)로 로그인한다. 이 realm 의 토큰으로는 운영 권한에
# 접근할 수 없다 — issuer 가 다르므로 운영 OIDC 소비자가 거부한다.

resource "keycloak_realm" "cledyu_learn" {
  realm   = var.learn_realm_name
  enabled = true

  display_name = var.learn_realm_display_name

  # cledyu 브랜드 로그인 테마(네온 워드마크 + 다크). 테마 리소스는 keycloak 파드의
  # /opt/keycloak/themes/cledyu 에 마운트됨(ansible keycloak_foundation 의 configmap 볼륨).
  login_theme = "cledyu"

  # 한국어 우선(테마 messages_ko 라벨 적용). i18n 미설정 시 영어 폴백으로 라벨이 영문 노출됨.
  internationalization {
    supported_locales = ["ko", "en"]
    default_locale    = "ko"
  }

  # 핵심: 자가가입 허용 + 이메일 로그인 + 비번 재설정.
  registration_allowed           = true
  registration_email_as_username = true
  remember_me                    = true
  reset_password_allowed         = true
  login_with_email_allowed       = true
  duplicate_emails_allowed       = false
  edit_username_allowed          = false
  revoke_refresh_token           = true

  # 이메일 인증은 SMTP 설정(learn_smtp) 완료 후 learn_verify_email=true 로 활성화.
  verify_email = var.learn_verify_email

  access_token_lifespan    = "15m"
  sso_session_idle_timeout = "30m"
  sso_session_max_lifespan = "8h"

  dynamic "smtp_server" {
    for_each = var.learn_smtp == null ? [] : [var.learn_smtp]
    content {
      host              = smtp_server.value.host
      port              = smtp_server.value.port
      from              = smtp_server.value.from
      from_display_name = smtp_server.value.from_display_name
      ssl               = smtp_server.value.ssl
      starttls          = smtp_server.value.starttls

      dynamic "auth" {
        for_each = smtp_server.value.auth_username == null ? [] : [1]
        content {
          username = smtp_server.value.auth_username
          password = var.learn_smtp_password
        }
      }
    }
  }
}

resource "keycloak_realm_events" "cledyu_learn" {
  realm_id = keycloak_realm.cledyu_learn.id

  events_enabled       = true
  admin_events_enabled = true

  events_listeners = ["jboss-logging"]

  enabled_event_types = [
    "LOGIN",
    "LOGIN_ERROR",
    "LOGOUT",
    "REGISTER",
    "REGISTER_ERROR",
    "UPDATE_PASSWORD",
    "VERIFY_EMAIL",
    "IDENTITY_PROVIDER_LOGIN",
    "IDENTITY_PROVIDER_FIRST_LOGIN",
    "CLIENT_LOGIN",
    "CLIENT_LOGIN_ERROR",
  ]
}
