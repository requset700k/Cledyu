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
    -Command "vault write $Path @- >/dev/null" `
    -InputBody $payload
}

function Write-VaultRawJson {
  param(
    [string]$Path,
    [hashtable]$Data
  )

  $payload = $Data | ConvertTo-Json -Depth 20 -Compress

  Invoke-VaultCommand `
    -Command "vault write $Path @- >/dev/null" `
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
Write-VaultPolicy -Name "cledyu-admin" -PolicyPath (Join-Path $policyDir "cledyu-admin.hcl")
Write-VaultPolicy -Name "cledyu-eso-reader" -PolicyPath (Join-Path $policyDir "cledyu-eso-reader.hcl")
Write-VaultPolicy -Name "cledyu-operator" -PolicyPath (Join-Path $policyDir "cledyu-operator.hcl")
Write-VaultPolicy -Name "cledyu-service-oidc" -PolicyPath (Join-Path $policyDir "cledyu-service-oidc.hcl")
Write-VaultPolicy -Name "vault-admin" -PolicyPath (Join-Path $policyDir "vault-admin.hcl")

Write-Host "Create Kubernetes auth roles..."
Invoke-VaultCommand -Command "vault write auth/kubernetes/role/cledyu-argocd bound_service_account_names=argocd-server bound_service_account_namespaces=argocd policies=cledyu-argocd ttl=1h >/dev/null"
Invoke-VaultCommand -Command "vault write auth/kubernetes/role/cledyu-grafana bound_service_account_names=grafana bound_service_account_namespaces=monitoring policies=cledyu-grafana ttl=1h >/dev/null"
Invoke-VaultCommand -Command "vault write auth/kubernetes/role/cledyu-keycloak bound_service_account_names=cledyu-keycloak bound_service_account_namespaces=keycloak policies=cledyu-keycloak-admin,cledyu-keycloak-db ttl=1h >/dev/null"
Invoke-VaultCommand -Command "vault write auth/kubernetes/role/cledyu-services bound_service_account_names=web,api,tutor bound_service_account_namespaces=web,api,tutor policies=cledyu-service-oidc ttl=1h >/dev/null"
Invoke-VaultCommand -Command "vault write auth/kubernetes/role/external-secrets-operator bound_service_account_names=eso-controller bound_service_account_namespaces=external-secrets policies=cledyu-eso-reader ttl=1h >/dev/null"
Invoke-VaultCommand -Command "vault write auth/kubernetes/role/vault-admin bound_service_account_names=vault-admin bound_service_account_namespaces=vault policies=vault-admin ttl=30m max_ttl=1h >/dev/null"

# Cleanup: remove legacy cledyu-eso role replaced by external-secrets-operator (PR #40)
Invoke-VaultCommand -Command "vault delete auth/kubernetes/role/cledyu-eso 2>/dev/null || true"

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
  default_role = "cledyu-operator"
  bound_issuer = $VaultOidcDiscoveryUrl
}
Write-VaultRawJson -Path "auth/oidc/role/cledyu-operator" -Data @{
  role_type = "oidc"
  user_claim = "sub"
  groups_claim = "groups"
  bound_audiences = @($VaultOidcClientId)
  allowed_redirect_uris = @($VaultOidcCliRedirectUri, $VaultOidcUiRedirectUri)
  oidc_scopes = @("openid", "profile", "email")
  bound_claims = @{
    groups = @("team-platform")
  }
  policies = @("cledyu-operator")
  ttl = "1h"
}
Write-VaultRawJson -Path "auth/oidc/role/cledyu-admin" -Data @{
  role_type = "oidc"
  user_claim = "sub"
  groups_claim = "groups"
  bound_audiences = @($VaultOidcClientId)
  allowed_redirect_uris = @($VaultOidcCliRedirectUri, $VaultOidcUiRedirectUri)
  oidc_scopes = @("openid", "profile", "email")
  # 2026-07-04: team-security 그룹이 실제로 채워져 있지 않아(운영자는 team-platform 소속)
  # admin 로그인이 group 불일치로 막혔다. break-glass 확보를 위해 team-platform 도 수용한다.
  # (cledyu-platform 통합 role 이 이미 team-platform 을 operator 로 신뢰하므로 같은 신뢰경계.)
  # 엄격한 operator/admin 분리를 원하면 team-security 만 두고 운영자를 그 그룹에 넣어야 한다.
  bound_claims = @{
    groups = @("team-platform", "team-security")
  }
  policies = @("cledyu-operator", "cledyu-admin")
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

# keycloak DB 자격증명 시드 — CNPG 이관(Plan A-2) 후 이 Vault 경로가 소스 오브 트루스다
# (ESO keycloak-pg-credentials가 여기서 읽어 CNPG bootstrap owner + Keycloak CR 자격증명이 됨).
# 우선순위: ① Vault에 이미 있으면 보존(재실행·DR raft 복원 시 덮어쓰기 = 라이브 DB 비번과 어긋남)
#          ② 구 Bitnami secret이 있으면 이관(하위호환) ③ 둘 다 없으면 난수 생성(신규/DR 리빌드).
$vaultDbCheck = Invoke-VaultCommand `
  -Command "vault kv get cledyu/keycloak/postgres >/dev/null 2>&1 && echo EXISTS || echo MISSING"
if ("$vaultDbCheck".Trim() -eq "EXISTS") {
  Write-Host "cledyu/keycloak/postgres already seeded - keeping existing value."
}
else {
  $legacyDbPassword = Get-SecretValue -Namespace keycloak -Name keycloak-db-credentials -Key password -Optional
  if ($null -ne $legacyDbPassword) {
    $dbData = @{
      username = Get-SecretValue -Namespace keycloak -Name keycloak-db-credentials -Key username
      password = $legacyDbPassword
      database = Get-SecretValue -Namespace keycloak -Name keycloak-db-credentials -Key database
      host = Get-SecretValue -Namespace keycloak -Name keycloak-db-credentials -Key host
      port = Get-SecretValue -Namespace keycloak -Name keycloak-db-credentials -Key port
      source = "kubernetes:keycloak/keycloak-db-credentials"
    }
  }
  else {
    # 신규 환경: 구 secret 생성자(postgres_single)가 플레이북에서 제거돼 존재하지 않는다.
    # 영숫자만 사용(32자) — DSN 등에 embed 될 때 URL 인코딩 이슈를 원천 차단.
    $generatedPassword = -join ((48..57) + (65..90) + (97..122) | Get-Random -Count 32 | ForEach-Object { [char]$_ })
    $dbData = @{
      username = "keycloak"
      password = $generatedPassword
      database = "keycloak"
      host = "keycloak-pg-rw"
      port = "5432"
      source = "generated:vault-bootstrap-configure"
    }
  }
  Write-VaultJson -Path "cledyu/data/keycloak/postgres" -Data $dbData
}

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
