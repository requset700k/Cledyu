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
# 최소권한: db prefix 와일드카드 대신 실제로 읽는 두 키만 명시. db 자격증명 추가 시 여기도 추가.
path "cledyu/data/db/postgres" {
  capabilities = ["read"]
}

path "cledyu/metadata/db/postgres" {
  capabilities = ["read"]
}

path "cledyu/data/db/api" {
  capabilities = ["read"]
}

path "cledyu/metadata/db/api" {
  capabilities = ["read"]
}

# 세션 VM 읽기전용 파일 목록 전용 SSH keypair — api-file-list-ssh ExternalSecret 이 읽는다.
# 공개키는 새 세션 VM cloud-init forced command 로, private key 는 api Pod 에 read-only mount.
path "cledyu/data/api/file-list-ssh" {
  capabilities = ["read"]
}

path "cledyu/metadata/api/file-list-ssh" {
  capabilities = ["read"]
}

# Kafka→BigQuery 분석 파이프라인(#195) — airflow analytics DAG 가 쓰는 자격증명.
# airflow-gcp-sa 는 BigQuery 적재용 GCP 서비스계정 key.json, airflow-kafka-cert 는
# Strimzi mTLS user(user.crt/user.key/ca.crt)를 읽는다. 최소권한: 실제 읽는 키만 명시.
path "cledyu/data/gcp/airflow-analytics-sa" {
  capabilities = ["read"]
}

path "cledyu/metadata/gcp/airflow-analytics-sa" {
  capabilities = ["read"]
}

path "cledyu/data/kafka/airflow-analytics" {
  capabilities = ["read"]
}

path "cledyu/metadata/kafka/airflow-analytics" {
  capabilities = ["read"]
}
