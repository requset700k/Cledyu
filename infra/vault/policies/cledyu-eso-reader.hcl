path "cledyu/data/oidc/*" {
  capabilities = ["read"]
}

path "cledyu/metadata/oidc/*" {
  capabilities = ["read"]
}

path "cledyu/data/alerts/*" {
  capabilities = ["read"]
}

path "cledyu/metadata/alerts/*" {
  capabilities = ["read"]
}

path "cledyu/data/alerting/*" {
  capabilities = ["read"]
}

path "cledyu/metadata/alerting/*" {
  capabilities = ["read"]
}


# 1번 ExternalSecret이 Vault analytics/gcp-sa에서 GCP SA 키를 읽기 때문에, 이 경로가 없으면 권한 없음 에러
path "cledyu/data/analytics/*" { # GCP SA 키 값
  capabilities = ["read"]
}
path "cledyu/metadata/analytics/*" { # GCP SA 키 메타데이터
  capabilities = ["read"]
}

# 2번 ExternalSecret이 Vault registry/ghcr에서 ghcr.io 토큰을 읽기 때문에, 이 경로가 없으면 권한 없음 에러
path "cledyu/data/registry/*" { # ghcr.io 토큰 값
  capabilities = ["read"]
}
path "cledyu/metadata/registry/*" { # ghcr.io 토큰 메타데이터
  capabilities = ["read"]
}
