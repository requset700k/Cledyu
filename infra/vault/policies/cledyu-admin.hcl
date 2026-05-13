# Cledyu Vault admin policy.
# Used by Keycloak OIDC users in team-security for Vault control-plane changes.

path "sys/*" {
  capabilities = ["create", "read", "update", "delete", "list", "sudo"]
}

path "auth/*" {
  capabilities = ["create", "read", "update", "delete", "list", "sudo"]
}

path "identity/*" {
  capabilities = ["create", "read", "update", "delete", "list"]
}
