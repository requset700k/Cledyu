# Cledyu Keycloak RBAC

Cledyu Keycloak의 realm, OIDC client, realm role, group, 팀원 초기 계정을
선언적으로 관리하는 Terraform 스택.

## Realm 구조 (2-realm)

운영자와 외부 학습자를 realm 수준에서 격리한다 (ADR `docs/ADR/learner-realm-split.md`).

| Realm | 파일 | 용도 |
|---|---|---|
| `cledyu` (내부) | `realm.tf`, `clients.tf`, `roles.tf`(=`locals.tf`), `groups.tf`, `users.tf` | 팀원 + 운영자 SSO(ArgoCD/Grafana/Kafka-UI/kube-apiserver/Vault). `registration_allowed=false`. |
| `cledyu-learn` (외부) | `realm-learn.tf`, `clients-learn.tf`, `roles-learn.tf`, `idp-learn.tf` | 학습자 자가가입 + 소셜 IdP(구글/카카오/네이버) + `web`/`api`/`tutor` 클라이언트. |

- 운영자 realm 이름·issuer(`…/realms/cledyu`)는 절대 바꾸지 않는다 — SSO 소비자 5종이 의존.
- 소셜 IdP 는 `enabled_social_idps`(provider 별 목록, 예 `["google"]`), 이메일 인증은 `learn_verify_email` 로 게이트(실 발급/SMTP 설정 전 빈 목록/false).
- 운영 절차는 RUNBOOK `docs/RUNBOOK/learner-auth.md`.

## 시크릿 관리

실제 `terraform.tfvars`, `oidc_client_secrets`,
`team_member_initial_passwords` 값은 커밋하지 않음.
1Password 또는 승인된 보안 채널로만 공유하고, 운영과 유사한 환경에 적용하기 전
Terraform state는 보안 backend에 보관함.

## 사용 방법

```bash
cd infra/terraform/keycloak
cp terraform.tfvars.example terraform.tfvars
$EDITOR terraform.tfvars
terraform init
terraform plan
terraform apply
```

<!-- BEGIN_TF_DOCS -->
## Requirements

| Name | Version |
| ---- | ------- |
| <a name="requirement_terraform"></a> [terraform](#requirement\_terraform) | >= 1.5.0 |
| <a name="requirement_keycloak"></a> [keycloak](#requirement\_keycloak) | ~> 4.4 |

## Providers

| Name | Version |
| ---- | ------- |
| <a name="provider_keycloak"></a> [keycloak](#provider\_keycloak) | ~> 4.4 |

## Modules

No modules.

## Resources

| Name | Type |
| ---- | ---- |
| [keycloak_attribute_importer_identity_provider_mapper.naver_email](https://registry.terraform.io/providers/mrparkers/keycloak/latest/docs/resources/attribute_importer_identity_provider_mapper) | resource |
| [keycloak_attribute_importer_identity_provider_mapper.naver_name](https://registry.terraform.io/providers/mrparkers/keycloak/latest/docs/resources/attribute_importer_identity_provider_mapper) | resource |
| [keycloak_default_groups.learn_default](https://registry.terraform.io/providers/mrparkers/keycloak/latest/docs/resources/default_groups) | resource |
| [keycloak_group.groups](https://registry.terraform.io/providers/mrparkers/keycloak/latest/docs/resources/group) | resource |
| [keycloak_group.learn_admins](https://registry.terraform.io/providers/mrparkers/keycloak/latest/docs/resources/group) | resource |
| [keycloak_group.learn_instructors](https://registry.terraform.io/providers/mrparkers/keycloak/latest/docs/resources/group) | resource |
| [keycloak_group.learn_students](https://registry.terraform.io/providers/mrparkers/keycloak/latest/docs/resources/group) | resource |
| [keycloak_group_roles.group_roles](https://registry.terraform.io/providers/mrparkers/keycloak/latest/docs/resources/group_roles) | resource |
| [keycloak_group_roles.learn_admins](https://registry.terraform.io/providers/mrparkers/keycloak/latest/docs/resources/group_roles) | resource |
| [keycloak_group_roles.learn_instructors](https://registry.terraform.io/providers/mrparkers/keycloak/latest/docs/resources/group_roles) | resource |
| [keycloak_group_roles.learn_students](https://registry.terraform.io/providers/mrparkers/keycloak/latest/docs/resources/group_roles) | resource |
| [keycloak_oidc_google_identity_provider.google](https://registry.terraform.io/providers/mrparkers/keycloak/latest/docs/resources/oidc_google_identity_provider) | resource |
| [keycloak_oidc_identity_provider.kakao](https://registry.terraform.io/providers/mrparkers/keycloak/latest/docs/resources/oidc_identity_provider) | resource |
| [keycloak_oidc_identity_provider.naver](https://registry.terraform.io/providers/mrparkers/keycloak/latest/docs/resources/oidc_identity_provider) | resource |
| [keycloak_openid_client.clients](https://registry.terraform.io/providers/mrparkers/keycloak/latest/docs/resources/openid_client) | resource |
| [keycloak_openid_client.learn_clients](https://registry.terraform.io/providers/mrparkers/keycloak/latest/docs/resources/openid_client) | resource |
| [keycloak_openid_group_membership_protocol_mapper.groups](https://registry.terraform.io/providers/mrparkers/keycloak/latest/docs/resources/openid_group_membership_protocol_mapper) | resource |
| [keycloak_openid_group_membership_protocol_mapper.learn_groups](https://registry.terraform.io/providers/mrparkers/keycloak/latest/docs/resources/openid_group_membership_protocol_mapper) | resource |
| [keycloak_openid_user_realm_role_protocol_mapper.kafka_ui_roles](https://registry.terraform.io/providers/mrparkers/keycloak/latest/docs/resources/openid_user_realm_role_protocol_mapper) | resource |
| [keycloak_realm.cledyu](https://registry.terraform.io/providers/mrparkers/keycloak/latest/docs/resources/realm) | resource |
| [keycloak_realm.cledyu_learn](https://registry.terraform.io/providers/mrparkers/keycloak/latest/docs/resources/realm) | resource |
| [keycloak_realm_events.cledyu](https://registry.terraform.io/providers/mrparkers/keycloak/latest/docs/resources/realm_events) | resource |
| [keycloak_realm_events.cledyu_learn](https://registry.terraform.io/providers/mrparkers/keycloak/latest/docs/resources/realm_events) | resource |
| [keycloak_role.learn_admin](https://registry.terraform.io/providers/mrparkers/keycloak/latest/docs/resources/role) | resource |
| [keycloak_role.learn_instructor](https://registry.terraform.io/providers/mrparkers/keycloak/latest/docs/resources/role) | resource |
| [keycloak_role.learn_student](https://registry.terraform.io/providers/mrparkers/keycloak/latest/docs/resources/role) | resource |
| [keycloak_role.realm_roles](https://registry.terraform.io/providers/mrparkers/keycloak/latest/docs/resources/role) | resource |
| [keycloak_user.master_super_admins](https://registry.terraform.io/providers/mrparkers/keycloak/latest/docs/resources/user) | resource |
| [keycloak_user.team_members](https://registry.terraform.io/providers/mrparkers/keycloak/latest/docs/resources/user) | resource |
| [keycloak_user_groups.team_members](https://registry.terraform.io/providers/mrparkers/keycloak/latest/docs/resources/user_groups) | resource |
| [keycloak_user_roles.master_super_admins](https://registry.terraform.io/providers/mrparkers/keycloak/latest/docs/resources/user_roles) | resource |
| [keycloak_user_template_importer_identity_provider_mapper.naver_username](https://registry.terraform.io/providers/mrparkers/keycloak/latest/docs/resources/user_template_importer_identity_provider_mapper) | resource |
| [keycloak_realm.master](https://registry.terraform.io/providers/mrparkers/keycloak/latest/docs/data-sources/realm) | data source |
| [keycloak_role.master_admin](https://registry.terraform.io/providers/mrparkers/keycloak/latest/docs/data-sources/role) | data source |
| [keycloak_role.master_default_roles](https://registry.terraform.io/providers/mrparkers/keycloak/latest/docs/data-sources/role) | data source |

## Inputs

| Name | Description | Type | Default | Required |
| ---- | ----------- | ---- | ------- | :------: |
| <a name="input_keycloak_admin_password"></a> [keycloak\_admin\_password](#input\_keycloak\_admin\_password) | Terraform 실행에 사용하는 Keycloak admin 비밀번호. | `string` | n/a | yes |
| <a name="input_keycloak_admin_username"></a> [keycloak\_admin\_username](#input\_keycloak\_admin\_username) | Terraform 실행에 사용하는 Keycloak admin 사용자명. | `string` | n/a | yes |
| <a name="input_learn_oidc_clients"></a> [learn\_oidc\_clients](#input\_learn\_oidc\_clients) | cledyu-learn realm에서 관리하는 OIDC client 목록(web/api/tutor). | <pre>map(object({<br/>    name                            = string<br/>    access_type                     = string<br/>    standard_flow_enabled           = bool<br/>    direct_access_grants_enabled    = bool<br/>    implicit_flow_enabled           = bool<br/>    service_accounts_enabled        = bool<br/>    pkce_code_challenge_method      = optional(string)<br/>    valid_redirect_uris             = optional(list(string), [])<br/>    valid_post_logout_redirect_uris = optional(list(string), [])<br/>    web_origins                     = optional(list(string), [])<br/>    root_url                        = optional(string)<br/>    base_url                        = optional(string)<br/>    admin_url                       = optional(string)<br/>  }))</pre> | n/a | yes |
| <a name="input_oidc_clients"></a> [oidc\_clients](#input\_oidc\_clients) | Cledyu realm에서 관리하는 OIDC client 목록. | <pre>map(object({<br/>    name                            = string<br/>    access_type                     = string<br/>    standard_flow_enabled           = bool<br/>    direct_access_grants_enabled    = bool<br/>    implicit_flow_enabled           = bool<br/>    service_accounts_enabled        = bool<br/>    pkce_code_challenge_method      = optional(string)<br/>    valid_redirect_uris             = optional(list(string), [])<br/>    valid_post_logout_redirect_uris = optional(list(string), [])<br/>    web_origins                     = optional(list(string), [])<br/>    root_url                        = optional(string)<br/>    base_url                        = optional(string)<br/>    admin_url                       = optional(string)<br/>  }))</pre> | n/a | yes |
| <a name="input_team_member_initial_passwords"></a> [team\_member\_initial\_passwords](#input\_team\_member\_initial\_passwords) | 팀원 초기 임시 비밀번호. 실제 값은 보안 tfvars 저장소에만 보관. | `map(string)` | n/a | yes |
| <a name="input_team_members"></a> [team\_members](#input\_team\_members) | 생성할 팀원 사용자와 그룹 매핑 정보. | <pre>map(object({<br/>    username           = string<br/>    email              = string<br/>    first_name         = string<br/>    last_name          = string<br/>    groups             = list(string)<br/>    temporary_password = optional(bool, true)<br/>    enabled            = optional(bool, true)<br/>    email_verified     = optional(bool, true)<br/>  }))</pre> | n/a | yes |
| <a name="input_enabled_social_idps"></a> [enabled\_social\_idps](#input\_enabled\_social\_idps) | 생성할 소셜 IdP alias 목록(google/kakao/naver). provider 별로 단계적 활성화한다 — 예 ["google"]. 실 client id/secret 발급된 것만 넣는다. 빈 목록이면 IdP 미생성. | `list(string)` | `[]` | no |
| <a name="input_idp_client_ids"></a> [idp\_client\_ids](#input\_idp\_client\_ids) | 소셜 IdP client id(공개값). google/kakao/naver 키. | `map(string)` | `{}` | no |
| <a name="input_idp_client_secrets"></a> [idp\_client\_secrets](#input\_idp\_client\_secrets) | 소셜 IdP client secret. 실제 값은 보안 저장소/Vault에만 보관. | `map(string)` | `{}` | no |
| <a name="input_keycloak_admin_client_id"></a> [keycloak\_admin\_client\_id](#input\_keycloak\_admin\_client\_id) | Terraform provider가 사용하는 admin client ID. | `string` | `"admin-cli"` | no |
| <a name="input_keycloak_admin_realm"></a> [keycloak\_admin\_realm](#input\_keycloak\_admin\_realm) | Terraform provider 인증에 사용하는 realm. | `string` | `"master"` | no |
| <a name="input_keycloak_tls_insecure_skip_verify"></a> [keycloak\_tls\_insecure\_skip\_verify](#input\_keycloak\_tls\_insecure\_skip\_verify) | Keycloak provider의 TLS 검증 생략 여부. Cledyu Root CA 신뢰 등록 후에는 false 유지. | `bool` | `false` | no |
| <a name="input_keycloak_url"></a> [keycloak\_url](#input\_keycloak\_url) | Keycloak 배포의 기본 URL. | `string` | `"https://keycloak.cledyu.local"` | no |
| <a name="input_learn_oidc_client_secrets"></a> [learn\_oidc\_client\_secrets](#input\_learn\_oidc\_client\_secrets) | cledyu-learn realm confidential client secret. 실제 값은 보안 저장소/Vault에만 보관. | `map(string)` | `{}` | no |
| <a name="input_learn_realm_display_name"></a> [learn\_realm\_display\_name](#input\_learn\_realm\_display\_name) | 학습자 realm 화면 표시 이름. | `string` | `"Cledyu"` | no |
| <a name="input_learn_realm_name"></a> [learn\_realm\_name](#input\_learn\_realm\_name) | 외부 학습자용 realm 이름. | `string` | `"cledyu-learn"` | no |
| <a name="input_learn_smtp"></a> [learn\_smtp](#input\_learn\_smtp) | 학습자 realm SMTP 설정. null 이면 SMTP 미설정(verify\_email/비번재설정 메일 발송 불가). | <pre>object({<br/>    host              = string<br/>    port              = string<br/>    from              = string<br/>    from_display_name = optional(string, "Cledyu")<br/>    ssl               = optional(bool, false)<br/>    starttls          = optional(bool, true)<br/>    auth_username     = optional(string)<br/>  })</pre> | `null` | no |
| <a name="input_learn_smtp_password"></a> [learn\_smtp\_password](#input\_learn\_smtp\_password) | 학습자 realm SMTP 인증 비밀번호. 실제 값은 보안 저장소에만 보관. | `string` | `null` | no |
| <a name="input_learn_verify_email"></a> [learn\_verify\_email](#input\_learn\_verify\_email) | 학습자 realm 이메일 인증 요구 여부. SMTP(learn\_smtp) 설정 후에만 true. | `bool` | `false` | no |
| <a name="input_master_admin_initial_passwords"></a> [master\_admin\_initial\_passwords](#input\_master\_admin\_initial\_passwords) | master realm super-admin 초기 임시 비밀번호. 1Password 보관 후 첫 로그인 시 변경 강제. | `map(string)` | `{}` | no |
| <a name="input_master_super_admins"></a> [master\_super\_admins](#input\_master\_super\_admins) | master realm super-admin 사용자 목록(ADR-0001 13장). lifecycle.ignore\_changes로 비밀번호 강제 재설정 방지. | <pre>map(object({<br/>    username   = string<br/>    email      = string<br/>    first_name = string<br/>    last_name  = string<br/>  }))</pre> | `{}` | no |
| <a name="input_oidc_client_secrets"></a> [oidc\_client\_secrets](#input\_oidc\_client\_secrets) | confidential OIDC client의 secret 값. 실제 값은 보안 tfvars 저장소에만 보관. | `map(string)` | `{}` | no |
| <a name="input_realm_display_name"></a> [realm\_display\_name](#input\_realm\_display\_name) | Cledyu realm의 화면 표시 이름. | `string` | `"Cledyu"` | no |
| <a name="input_realm_name"></a> [realm\_name](#input\_realm\_name) | Cledyu 업무용 realm 이름. | `string` | `"cledyu"` | no |

## Outputs

| Name | Description |
| ---- | ----------- |
| <a name="output_confidential_client_ids"></a> [confidential\_client\_ids](#output\_confidential\_client\_ids) | 보안 채널로 credential 전달이 필요한 confidential OIDC client 목록. |
| <a name="output_groups"></a> [groups](#output\_groups) | Terraform이 관리하는 group 이름 목록. |
| <a name="output_learn_oidc_client_ids"></a> [learn\_oidc\_client\_ids](#output\_learn\_oidc\_client\_ids) | cledyu-learn realm OIDC client ID 목록. |
| <a name="output_learn_realm_id"></a> [learn\_realm\_id](#output\_learn\_realm\_id) | 외부 학습자용 cledyu-learn realm ID. |
| <a name="output_oidc_client_ids"></a> [oidc\_client\_ids](#output\_oidc\_client\_ids) | Terraform이 관리하는 OIDC client ID 목록. |
| <a name="output_realm_id"></a> [realm\_id](#output\_realm\_id) | Terraform이 관리하는 Cledyu realm ID. |
| <a name="output_realm_roles"></a> [realm\_roles](#output\_realm\_roles) | Terraform이 관리하는 realm role 이름 목록. |
| <a name="output_social_idps_enabled"></a> [social\_idps\_enabled](#output\_social\_idps\_enabled) | 생성된 소셜 IdP alias 목록(구글/카카오/네이버). |
<!-- END_TF_DOCS -->
