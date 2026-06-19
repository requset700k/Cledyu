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
