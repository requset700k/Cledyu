param(
  [string]$Kubeconfig = "$env:USERPROFILE\Cledyu\.tmp\cledyu-oidc-work.yaml",
  [string]$BootstrapPath = "$env:USERPROFILE\Documents\Cledyu-Secrets\vault-bootstrap-20260428-150300.json",
  [string]$VaultPod = "vault-0",
  [string]$VaultOidcClientId = "vault",
  [string]$VaultOidcClientSecret = $env:VAULT_OIDC_CLIENT_SECRET,
  [string]$VaultOidcDiscoveryUrl = "https://keycloak.cledyu.local/realms/cledyu",
  [string]$VaultOidcCliRedirectUri = "http://localhost:8250/oidc/callback",
  [string]$VaultOidcUiRedirectUri = "https://vault.cledyu.local/ui/vault/auth/oidc/oidc/callback",
  [string]$VaultOidcCaNamespace = "cert-manager",
  [string]$VaultOidcCaSecret = "cledyu-root-ca",
  [string]$VaultOidcCaSecretKey = "tls.crt"
)

$ErrorActionPreference = "Stop"

if (Test-Path "$env:USERPROFILE\bin") {
  $env:Path = "$env:USERPROFILE\bin;$env:Path"
}

function Get-Kubectl {
  $kubectl = Get-Command kubectl -ErrorAction SilentlyContinue
  if (-not $kubectl) {
    throw "kubectl not found in PATH."
  }
  return $kubectl.Source
}

function Get-SecretValue {
  param(
    [string]$Namespace,
    [string]$Name,
    [string]$Key,
    [switch]$Optional
  )

  if ($Optional) {
    $json = & $script:Kubectl --kubeconfig $Kubeconfig -n $Namespace get secret $Name --ignore-not-found -o json 2>$null
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($json)) {
      return $null
    }
  } else {
    $json = & $script:Kubectl --kubeconfig $Kubeconfig -n $Namespace get secret $Name -o json
  }

  if ($LASTEXITCODE -ne 0) {
    if ($Optional) {
      return $null
    }
    throw "Secret not found: $Namespace/$Name"
  }

  $secret = $json | ConvertFrom-Json
  $property = $secret.data.PSObject.Properties[$Key]
  if (-not $property) {
    if ($Optional) {
      return $null
    }
    throw "Secret key not found: $Namespace/$Name $Key"
  }

  $bytes = [Convert]::FromBase64String($property.Value)
  return [Text.Encoding]::UTF8.GetString($bytes)
}

function Invoke-VaultCommand {
  param(
    [string]$Command,
    [string]$InputBody = ""
  )

  $payload = $script:RootToken + "`n" + $InputBody
  $payload | & $script:Kubectl --kubeconfig $Kubeconfig -n vault exec -i $VaultPod -- sh -c "read -r VAULT_TOKEN; export VAULT_TOKEN; export VAULT_SKIP_VERIFY=true; $Command"
  if ($LASTEXITCODE -ne 0) {
    throw "Vault command failed: $Command"
  }
}

function Write-VaultJson {
  param(
    [string]$Path,
    [hashtable]$Data
  )

  $payload = @{
    data = $Data
  } | ConvertTo-Json -Depth 20 -Compress

  Invoke-VaultCommand `
    -Command "cat >/tmp/vault-payload.json && vault write $Path @/tmp/vault-payload.json >/dev/null && rm -f /tmp/vault-payload.json" `
    -InputBody $payload
}

function Write-VaultRawJson {
  param(
    [string]$Path,
    [hashtable]$Data
  )

  $payload = $Data | ConvertTo-Json -Depth 20 -Compress

  Invoke-VaultCommand `
    -Command "cat >/tmp/vault-payload.json && vault write $Path @/tmp/vault-payload.json >/dev/null && rm -f /tmp/vault-payload.json" `
    -InputBody $payload
}

function Write-VaultPolicy {
  param(
    [string]$Name,
    [string]$PolicyPath
  )

  $policy = Get-Content -Raw -LiteralPath $PolicyPath
  Invoke-VaultCommand `
    -Command "cat >/tmp/$Name.hcl && vault policy write $Name /tmp/$Name.hcl >/dev/null && rm -f /tmp/$Name.hcl" `
    -InputBody $policy
}

if (-not (Test-Path -LiteralPath $Kubeconfig)) {
  throw "Kubeconfig not found: $Kubeconfig"
}
if (-not (Test-Path -LiteralPath $BootstrapPath)) {
  throw "Vault bootstrap file not found: $BootstrapPath"
}

$script:Kubectl = Get-Kubectl
$bootstrap = Get-Content -Raw -LiteralPath $BootstrapPath | ConvertFrom-Json
$script:RootToken = $bootstrap.root_token
if (-not $script:RootToken) {
  throw "root_token not found in bootstrap file."
}

Write-Host "Enable file audit device if needed..."
Invoke-VaultCommand -Command "vault audit list | grep -q '^file/' || vault audit enable file file_path=/vault/audit/audit.log"

$repoRoot = Resolve-Path (Join-Path $PSScriptRoot "..")
$policyDir = Join-Path $repoRoot "infra\vault\policies"

Write-Host "Enable KV v2 mount if needed..."
Invoke-VaultCommand -Command "vault secrets enable -path=cledyu kv-v2 >/dev/null 2>&1 || true"

Write-Host "Configure Kubernetes auth backend..."
Invoke-VaultCommand -Command "vault auth enable kubernetes >/dev/null 2>&1 || true"
Invoke-VaultCommand -Command "vault write auth/kubernetes/config kubernetes_host=https://kubernetes.default.svc:443 kubernetes_ca_cert=@/var/run/secrets/kubernetes.io/serviceaccount/ca.crt token_reviewer_jwt=@/var/run/secrets/kubernetes.io/serviceaccount/token >/dev/null"

Write-Host "Write Vault policies..."
Write-VaultPolicy -Name "cledyu-argocd" -PolicyPath (Join-Path $policyDir "cledyu-argocd.hcl")
Write-VaultPolicy -Name "cledyu-grafana" -PolicyPath (Join-Path $policyDir "cledyu-grafana.hcl")
Write-VaultPolicy -Name "cledyu-keycloak-admin" -PolicyPath (Join-Path $policyDir "cledyu-keycloak-admin.hcl")
Write-VaultPolicy -Name "cledyu-keycloak-db" -PolicyPath (Join-Path $policyDir "cledyu-keycloak-db.hcl")
Write-VaultPolicy -Name "cledyu-operator" -PolicyPath (Join-Path $policyDir "cledyu-operator.hcl")
Write-VaultPolicy -Name "cledyu-service-oidc" -PolicyPath (Join-Path $policyDir "cledyu-service-oidc.hcl")

Write-Host "Create Kubernetes auth roles..."
Invoke-VaultCommand -Command "vault write auth/kubernetes/role/cledyu-argocd bound_service_account_names=argocd-server bound_service_account_namespaces=argocd policies=cledyu-argocd ttl=1h >/dev/null"
Invoke-VaultCommand -Command "vault write auth/kubernetes/role/cledyu-grafana bound_service_account_names=grafana bound_service_account_namespaces=monitoring policies=cledyu-grafana ttl=1h >/dev/null"
Invoke-VaultCommand -Command "vault write auth/kubernetes/role/cledyu-keycloak bound_service_account_names=cledyu-keycloak bound_service_account_namespaces=keycloak policies=cledyu-keycloak-admin,cledyu-keycloak-db ttl=1h >/dev/null"
Invoke-VaultCommand -Command "vault write auth/kubernetes/role/cledyu-services bound_service_account_names=web,api,tutor bound_service_account_namespaces=web,api,tutor policies=cledyu-service-oidc ttl=1h >/dev/null"

if ([string]::IsNullOrWhiteSpace($VaultOidcClientSecret)) {
  throw "Vault OIDC client secret not provided. Set VAULT_OIDC_CLIENT_SECRET or pass -VaultOidcClientSecret."
}

Write-Host "Configure Vault OIDC auth backend..."
$vaultOidcCaPem = Get-SecretValue -Namespace $VaultOidcCaNamespace -Name $VaultOidcCaSecret -Key $VaultOidcCaSecretKey
Invoke-VaultCommand -Command "vault auth enable oidc >/dev/null 2>&1 || true"
Write-VaultRawJson -Path "auth/oidc/config" -Data @{
  oidc_discovery_url = $VaultOidcDiscoveryUrl
  oidc_client_id = $VaultOidcClientId
  oidc_client_secret = $VaultOidcClientSecret
  oidc_discovery_ca_pem = $vaultOidcCaPem
  default_role = "cledyu-platform"
  bound_issuer = $VaultOidcDiscoveryUrl
}
Write-VaultRawJson -Path "auth/oidc/role/cledyu-platform" -Data @{
  role_type = "oidc"
  user_claim = "sub"
  groups_claim = "groups"
  bound_audiences = @($VaultOidcClientId)
  allowed_redirect_uris = @($VaultOidcCliRedirectUri, $VaultOidcUiRedirectUri)
  oidc_scopes = @("openid", "profile", "email")
  bound_claims = @{
    groups = @("team-platform", "team-security")
  }
  policies = @("cledyu-operator")
  ttl = "1h"
}

Write-Host "Migrate available bootstrap secrets to Vault..."
$keycloakAdminUsername = Get-SecretValue -Namespace keycloak -Name cledyu-keycloak-initial-admin -Key username
$keycloakAdminPassword = Get-SecretValue -Namespace keycloak -Name cledyu-keycloak-initial-admin -Key password
Write-VaultJson -Path "cledyu/data/keycloak/admin" -Data @{
  username = $keycloakAdminUsername
  password = $keycloakAdminPassword
  source = "kubernetes:keycloak/cledyu-keycloak-initial-admin"
}

$dbData = @{
  username = Get-SecretValue -Namespace keycloak -Name keycloak-db-credentials -Key username
  password = Get-SecretValue -Namespace keycloak -Name keycloak-db-credentials -Key password
  database = Get-SecretValue -Namespace keycloak -Name keycloak-db-credentials -Key database
  host = Get-SecretValue -Namespace keycloak -Name keycloak-db-credentials -Key host
  port = Get-SecretValue -Namespace keycloak -Name keycloak-db-credentials -Key port
  source = "kubernetes:keycloak/keycloak-db-credentials"
}
Write-VaultJson -Path "cledyu/data/keycloak/postgres" -Data $dbData

$argocdClientSecret = Get-SecretValue -Namespace argocd -Name argocd-secret -Key "oidc.keycloak.clientSecret"
Write-VaultJson -Path "cledyu/data/oidc/argocd" -Data @{
  client_id = "argocd"
  client_secret = $argocdClientSecret
  source = "kubernetes:argocd/argocd-secret:oidc.keycloak.clientSecret"
}

Write-VaultJson -Path "cledyu/data/oidc/web" -Data @{
  client_id = "web"
  access_type = "public"
  secret_required = "false"
}
Write-VaultJson -Path "cledyu/data/oidc/api" -Data @{
  client_id = "api"
  access_type = "bearer-only"
  secret_required = "false"
}
Write-VaultJson -Path "cledyu/data/oidc/tutor" -Data @{
  client_id = "tutor"
  access_type = "bearer-only"
  secret_required = "false"
}
Write-VaultJson -Path "cledyu/data/oidc/vault" -Data @{
  client_id = $VaultOidcClientId
  client_secret = $VaultOidcClientSecret
  source = "VAULT_OIDC_CLIENT_SECRET"
  access_type = "confidential"
}

$grafanaClientSecret = Get-SecretValue -Namespace monitoring -Name grafana -Key "client-secret" -Optional
if ($grafanaClientSecret) {
  Write-VaultJson -Path "cledyu/data/oidc/grafana" -Data @{
    client_id = "grafana"
    client_secret = $grafanaClientSecret
    source = "kubernetes:monitoring/grafana:client-secret"
  }
} else {
  Write-VaultJson -Path "cledyu/data/oidc/grafana" -Data @{
    client_id = "grafana"
    access_type = "confidential"
    secret_required = "true"
    migration_status = "pending"
    reason = "grafana client secret was not found in Kubernetes yet"
  }
}

Write-Host "Verify migrated paths..."
Invoke-VaultCommand -Command "vault kv metadata get cledyu/keycloak/admin >/dev/null"
Invoke-VaultCommand -Command "vault kv metadata get cledyu/keycloak/postgres >/dev/null"
Invoke-VaultCommand -Command "vault kv metadata get cledyu/oidc/argocd >/dev/null"
Invoke-VaultCommand -Command "vault kv metadata get cledyu/oidc/web >/dev/null"
Invoke-VaultCommand -Command "vault kv metadata get cledyu/oidc/api >/dev/null"
Invoke-VaultCommand -Command "vault kv metadata get cledyu/oidc/tutor >/dev/null"
Invoke-VaultCommand -Command "vault kv metadata get cledyu/oidc/vault >/dev/null"
Invoke-VaultCommand -Command "vault kv metadata get cledyu/oidc/grafana >/dev/null"

Write-Host "Vault bootstrap configuration completed."
