# Cledyu Vault operator policy.
# Used by Keycloak OIDC users in team-platform for day-to-day secret operations.

path "cledyu/*" {
  capabilities = ["create", "read", "update", "delete", "list", "patch"]
}

path "sys/health" {
  capabilities = ["read"]
}

path "sys/mounts" {
  capabilities = ["read"]
}

path "sys/policies/acl/*" {
  capabilities = ["read"]
}
