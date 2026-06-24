path "cledyu/data/oidc/*" {
  capabilities = ["read"]
}

path "cledyu/metadata/oidc/*" {
  capabilities = ["read"]
}

path "cledyu/data/airflow/fernet-key" {
  capabilities = ["read"]
}

path "cledyu/metadata/airflow/fernet-key" {
  capabilities = ["read"]
}

path "cledyu/data/airflow/webserver-secret-key" {
  capabilities = ["read"]
}

path "cledyu/metadata/airflow/webserver-secret-key" {
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

# EC2 오버플로우(Phase 13) — api/validation-engine 의 AWS 최소권한 키·Tailscale authkey.
# cledyu-api-aws / cledyu-validation-engine-aws ExternalSecret 이 읽는다.
path "cledyu/data/aws/*" {
  capabilities = ["read"]
}

path "cledyu/metadata/aws/*" {
  capabilities = ["read"]
}
