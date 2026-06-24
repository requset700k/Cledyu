variable "keycloak_url" {
  description = "Keycloak 배포의 기본 URL."
  type        = string
  default     = "https://keycloak.cledyu.local"
}

variable "keycloak_admin_realm" {
  description = "Terraform provider 인증에 사용하는 realm."
  type        = string
  default     = "master"
}

variable "keycloak_admin_client_id" {
  description = "Terraform provider가 사용하는 admin client ID."
  type        = string
  default     = "admin-cli"
}

variable "keycloak_admin_username" {
  description = "Terraform 실행에 사용하는 Keycloak admin 사용자명."
  type        = string
  sensitive   = true
}

variable "keycloak_admin_password" {
  description = "Terraform 실행에 사용하는 Keycloak admin 비밀번호."
  type        = string
  sensitive   = true
}

variable "keycloak_tls_insecure_skip_verify" {
  description = "Keycloak provider의 TLS 검증 생략 여부. Cledyu Root CA 신뢰 등록 후에는 false 유지."
  type        = bool
  default     = false
}

variable "realm_name" {
  description = "Cledyu 업무용 realm 이름."
  type        = string
  default     = "cledyu"
}

variable "realm_display_name" {
  description = "Cledyu realm의 화면 표시 이름."
  type        = string
  default     = "Cledyu"
}

variable "oidc_clients" {
  description = "Cledyu realm에서 관리하는 OIDC client 목록."
  type = map(object({
    name                            = string
    access_type                     = string
    standard_flow_enabled           = bool
    direct_access_grants_enabled    = bool
    implicit_flow_enabled           = bool
    service_accounts_enabled        = bool
    pkce_code_challenge_method      = optional(string)
    valid_redirect_uris             = optional(list(string), [])
    valid_post_logout_redirect_uris = optional(list(string), [])
    web_origins                     = optional(list(string), [])
    root_url                        = optional(string)
    base_url                        = optional(string)
    admin_url                       = optional(string)
  }))
}

variable "oidc_client_secrets" {
  description = "confidential OIDC client의 secret 값. 실제 값은 보안 tfvars 저장소에만 보관."
  type        = map(string)
  default     = {}
  sensitive   = true
}

variable "team_members" {
  description = "생성할 팀원 사용자와 그룹 매핑 정보."
  type = map(object({
    username           = string
    email              = string
    first_name         = string
    last_name          = string
    groups             = list(string)
    temporary_password = optional(bool, true)
    enabled            = optional(bool, true)
    email_verified     = optional(bool, true)
  }))
}

variable "team_member_initial_passwords" {
  description = "팀원 초기 임시 비밀번호. 실제 값은 보안 tfvars 저장소에만 보관."
  type        = map(string)
  sensitive   = true
}

variable "master_super_admins" {
  description = "master realm super-admin 사용자 목록(ADR-0001 13장). lifecycle.ignore_changes로 비밀번호 강제 재설정 방지."
  type = map(object({
    username   = string
    email      = string
    first_name = string
    last_name  = string
  }))
  default = {}
}

variable "master_admin_initial_passwords" {
  description = "master realm super-admin 초기 임시 비밀번호. 1Password 보관 후 첫 로그인 시 변경 강제."
  type        = map(string)
  default     = {}
  sensitive   = true
}

# ─────────────────────────────────────────────────────────────────────────
# cledyu-learn realm — 외부 학습자 자가가입 + 소셜 로그인
# ─────────────────────────────────────────────────────────────────────────

variable "learn_realm_name" {
  description = "외부 학습자용 realm 이름."
  type        = string
  default     = "cledyu-learn"
}

variable "learn_realm_display_name" {
  description = "학습자 realm 화면 표시 이름."
  type        = string
  default     = "Cledyu"
}

variable "learn_verify_email" {
  description = "학습자 realm 이메일 인증 요구 여부. SMTP(learn_smtp) 설정 후에만 true."
  type        = bool
  default     = false
}

variable "learn_smtp" {
  description = "학습자 realm SMTP 설정. null 이면 SMTP 미설정(verify_email/비번재설정 메일 발송 불가)."
  type = object({
    host              = string
    port              = string
    from              = string
    from_display_name = optional(string, "Cledyu")
    ssl               = optional(bool, false)
    starttls          = optional(bool, true)
    auth_username     = optional(string)
  })
  default = null
}

variable "learn_smtp_password" {
  description = "학습자 realm SMTP 인증 비밀번호. 실제 값은 보안 저장소에만 보관."
  type        = string
  default     = null
  sensitive   = true
}

variable "learn_oidc_clients" {
  description = "cledyu-learn realm에서 관리하는 OIDC client 목록(web/api/tutor)."
  type = map(object({
    name                            = string
    access_type                     = string
    standard_flow_enabled           = bool
    direct_access_grants_enabled    = bool
    implicit_flow_enabled           = bool
    service_accounts_enabled        = bool
    pkce_code_challenge_method      = optional(string)
    valid_redirect_uris             = optional(list(string), [])
    valid_post_logout_redirect_uris = optional(list(string), [])
    web_origins                     = optional(list(string), [])
    root_url                        = optional(string)
    base_url                        = optional(string)
    admin_url                       = optional(string)
  }))
}

variable "learn_oidc_client_secrets" {
  description = "cledyu-learn realm confidential client secret. 실제 값은 보안 저장소/Vault에만 보관."
  type        = map(string)
  default     = {}
  sensitive   = true
}

variable "enabled_social_idps" {
  description = "생성할 소셜 IdP alias 목록(google/kakao/naver). provider 별로 단계적 활성화한다 — 예 [\"google\"]. 실 client id/secret 발급된 것만 넣는다. 빈 목록이면 IdP 미생성."
  type        = list(string)
  default     = []

  validation {
    condition     = alltrue([for a in var.enabled_social_idps : contains(["google", "kakao", "naver"], a)])
    error_message = "enabled_social_idps 는 google/kakao/naver 만 허용한다."
  }
}

variable "idp_client_ids" {
  description = "소셜 IdP client id(공개값). google/kakao/naver 키."
  type        = map(string)
  default     = {}
}

variable "idp_client_secrets" {
  description = "소셜 IdP client secret. 실제 값은 보안 저장소/Vault에만 보관."
  type        = map(string)
  default     = {}
  sensitive   = true
}
