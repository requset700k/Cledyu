# Break-glass Vault admin policy for the vault-admin Kubernetes auth role.
# Use only when Keycloak OIDC admin login is unavailable.

path "*" {
  capabilities = ["create", "read", "update", "delete", "list", "sudo"]
}
