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

path "cledyu/data/ai/*" {
  capabilities = ["read"]
}

path "cledyu/metadata/ai/*" {
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

# 학습자 데이터 영속화(PostgreSQL) — postgres StatefulSet 비밀번호와 api 접속 DSN.
# postgres-credentials / cledyu-api-db ExternalSecret 이 읽는다(런북 learner-data.md §2).
path "cledyu/data/db/*" {
  capabilities = ["read"]
}

path "cledyu/metadata/db/*" {
  capabilities = ["read"]
}
