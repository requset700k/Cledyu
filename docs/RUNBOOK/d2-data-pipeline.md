# D2 데이터 파이프라인 — 적용 런북

자동화 범위는 코드 작성 + 정적검증까지다. 아래는 라이브 적용을 위한 수동 단계(GCP/Vault
인증 필요 — 김용균/owner onimami1110).

## 1. GCP 인프라 apply

```
cd infra/terraform/gcp
cp terraform.tfvars.example terraform.tfvars   # bucket_name 등 확인
terraform init
terraform apply
```

## 2. 서비스 계정 키 발급 + Vault 저장

```
gcloud iam service-accounts keys create sa-key.json \
  --iam-account="$(terraform -chdir=infra/terraform/gcp output -raw sa_email)"
vault kv put cledyu/gcp/airflow-analytics-sa key.json=@sa-key.json
rm sa-key.json
```

## 3. Kafka 클라이언트 인증서 → Vault

KafkaUser airflow-analytics 시크릿(kafka ns)에서 인증서를 꺼내 Vault 에 저장:

```
kubectl -n kafka get secret airflow-analytics -o jsonpath='{.data.user\.crt}' | base64 -d > user.crt
kubectl -n kafka get secret airflow-analytics -o jsonpath='{.data.user\.key}' | base64 -d > user.key
kubectl -n kafka get secret airflow-analytics -o jsonpath='{.data.ca\.crt}'   | base64 -d > ca.crt
vault kv put cledyu/kafka/airflow-analytics user.crt=@user.crt user.key=@user.key ca.crt=@ca.crt
rm user.crt user.key ca.crt
```

## 4. ArgoCD 동기화 확인

- KafkaUser airflow-analytics, ExternalSecret airflow-gcp-sa/airflow-kafka-cert 가 Synced 인지.
- airflow ns 에 Secret airflow-gcp-sa/airflow-kafka-cert 생성됐는지.

## 5. DAG 트리거 + 검증

- Airflow UI 에서 lab_events_to_bq 수동 트리거.
- 이벤트가 있으면: BQ `cledyu_analytics.lab_events` 에 행, GCS 에 NDJSON, 뷰 4개 생성 확인.
- 이벤트가 없으면: 실제 랩 세션을 몇 개 돌려 lab-events 를 생성한 뒤 재트리거.

```
bq query --use_legacy_sql=false 'SELECT event_type, COUNT(*) FROM cledyu_analytics.lab_events GROUP BY 1'
```

- `refresh_views` 태스크는 4개의 CREATE OR REPLACE VIEW 문을 단일 BigQuery 멀티스테이트먼트 스크립트로 실행한다. 멀티스테이트먼트 오류 시 수동 폴백: `bq query --use_legacy_sql=false < apps/airflow/dags/sql/d3_views.sql`
