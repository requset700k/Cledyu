# cledyu-admin — cledyu-learn realm 의 역할 승격(role promotion) service-account client.
#
# api(Session API)가 학습자에게 realm 역할 instructor/admin 을 추가할 때 Keycloak Admin REST
# API 를 호출한다. 그 호출 주체가 이 confidential service-account client 다. client_secret 은
# Vault `cledyu/oidc/cledyu-admin:client_secret` 로 보관되고 ESO(api/cledyu-admin-oidc-client-secret)
# 가 주입한다. 미설정 시 승격 API 만 501 로 비활성된다(나머지 인증/학습 흐름은 정상).
#
# client 본체는 learn_clients(for_each var.learn_oidc_clients)로 생성되므로, tfvars 의
# learn_oidc_clients 에 cledyu-admin 항목(access_type CONFIDENTIAL, service_accounts_enabled true)
# 과 learn_oidc_client_secrets["cledyu-admin"] 를 추가해야 한다. 아래는 그 service account 에
# realm-management 역할(manage-users, view-realm)을 부여하는 부분으로, cledyu-admin 이
# 맵에 있을 때만 활성화된다(없으면 no-op).

locals {
  cledyu_admin_present = contains(keys(var.learn_oidc_clients), "cledyu-admin")
}

# 역할 승격에 필요한 realm-management client(빌트인)의 id 조회.
data "keycloak_openid_client" "learn_realm_management" {
  count     = local.cledyu_admin_present ? 1 : 0
  realm_id  = keycloak_realm.cledyu_learn.id
  client_id = "realm-management"
}

# cledyu-admin service account 에 최소권한으로 manage-users(역할 부여)·view-realm(역할 조회) 부여.
resource "keycloak_openid_client_service_account_role" "cledyu_admin_realm_management" {
  for_each = local.cledyu_admin_present ? toset(["manage-users", "view-realm"]) : toset([])

  realm_id                = keycloak_realm.cledyu_learn.id
  service_account_user_id = keycloak_openid_client.learn_clients["cledyu-admin"].service_account_user_id
  client_id               = data.keycloak_openid_client.learn_realm_management[0].id
  role                    = each.key
}
