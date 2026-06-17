resource "keycloak_openid_client" "clients" {
  for_each = var.oidc_clients

  realm_id  = keycloak_realm.cledyu.id
  client_id = each.key
  name      = each.value.name
  enabled   = true

  access_type                  = upper(each.value.access_type)
  standard_flow_enabled        = each.value.standard_flow_enabled
  direct_access_grants_enabled = each.value.direct_access_grants_enabled
  implicit_flow_enabled        = each.value.implicit_flow_enabled
  service_accounts_enabled     = each.value.service_accounts_enabled

  pkce_code_challenge_method = try(each.value.pkce_code_challenge_method, null)

  valid_redirect_uris             = try(each.value.valid_redirect_uris, [])
  valid_post_logout_redirect_uris = try(each.value.valid_post_logout_redirect_uris, [])
  web_origins                     = try(each.value.web_origins, [])

  root_url  = try(each.value.root_url, null)
  base_url  = try(each.value.base_url, null)
  admin_url = try(each.value.admin_url, null)

  client_secret = try(var.oidc_client_secrets[each.key], null)
}

resource "keycloak_openid_group_membership_protocol_mapper" "groups" {
  for_each = {
    for client_id, client in var.oidc_clients : client_id => client
    if upper(client.access_type) != "BEARER-ONLY"
  }

  realm_id  = keycloak_realm.cledyu.id
  client_id = keycloak_openid_client.clients[each.key].id
  name      = "groups"

  claim_name = "groups"
  full_path  = false

  add_to_access_token = true
  add_to_id_token     = true
  add_to_userinfo     = true
}

# kafka-ui RBAC(oauth role 매칭)용. Keycloak realm role은 기본적으로 중첩 클레임
# realm_access.roles에 들어가는데 kafbat-ui는 중첩 JSON path를 지원하지 않아
# (https://github.com/kafbat/kafka-ui/issues/1025) 평탄화된 최상위 "roles" 클레임이 필요하다.
resource "keycloak_openid_user_realm_role_protocol_mapper" "kafka_ui_roles" {
  realm_id  = keycloak_realm.cledyu.id
  client_id = keycloak_openid_client.clients["kafka-ui"].id
  name      = "roles"

  claim_name  = "roles"
  multivalued = true

  add_to_access_token = true
  add_to_id_token     = true
  add_to_userinfo     = true
}

# 위 매퍼는 장애 대응 중 Keycloak Admin API로 먼저 만들어 운영 state엔 아직 없다.
# import 블록을 커밋에 남기면 이 매퍼가 없는 환경(재해복구로 새로 세운 Keycloak 등)에서
# apply 자체가 막힌다. 그래서 커밋하지 않고, 운영 state에는 1회성으로 직접 적용한다:
#   terraform import keycloak_openid_user_realm_role_protocol_mapper.kafka_ui_roles \
#     "cledyu/client/<kafka-ui client id>/6675bdbd-cbb5-40aa-ac9a-cd2cd659d714"
