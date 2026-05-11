# Cledyu Vault operator policy.
# Used by Keycloak OIDC users in team-platform and team-security.

path "sys/*" {
  capabilities = ["create", "read", "update", "delete", "list", "sudo"]
}

path "auth/*" {
  capabilities = ["create", "read", "update", "delete", "list", "sudo"]
}

path "identity/*" {
  capabilities = ["create", "read", "update", "delete", "list"]
}

path "cledyu/*" {
  capabilities = ["create", "read", "update", "delete", "list", "patch"]
}
