# D3 강사 분석 — 적용 런북

자동화 범위는 코드 작성 + 정적검증까지다. 아래는 라이브 적용을 위한 수동 단계(GCP/Vault
인증 필요 — 김용균/owner onimami1110).

## 1. 읽기전용 SA apply + 키 → Vault

```bash
cd infra/terraform/gcp
terraform apply   # api-analytics-reader SA 생성, BigQuery dataset dataViewer 권한 부여
gcloud iam service-accounts keys create reader-key.json \
  --iam-account="$(terraform -chdir=infra/terraform/gcp output -raw api_reader_sa_email)"
vault kv put cledyu/gcp/api-analytics-reader key.json=@reader-key.json
rm reader-key.json
```

`api_reader_sa_email` 출력값은 Terraform state에서 참조한다. key 파일은 Vault 저장 후
즉시 삭제하여 로컬에 남기지 않는다.

## 2. api 차트에서 분석 기능 활성화

분석 기능은 `gitops/apps/api/values.yaml`의 `analytics` 블록으로 게이트된다(기본
`enabled: false` — env/Secret 미주입, 핸들러는 503). ExternalSecret `api-bq-reader`,
SA 키 볼륨 마운트, `GOOGLE_APPLICATION_CREDENTIALS`·`CLEDYU_ANALYTICS_*` env 는 모두
`analytics.enabled: true` 일 때만 차트 템플릿에서 렌더된다. `projectId` 만 채우면 된다.

```yaml
analytics:
  enabled: true
  projectId: "cledyu-project"   # CLEDYU_ANALYTICS_PROJECT_ID
  # dataset / secretName / secretKey / credentialsMountPath 는 기본값 사용(차트 values 참고)
```

적용하면 ArgoCD sync 시 `api` namespace 에 ExternalSecret 이 생성되어 Secret
`api-bq-reader` 가 채워지고, 파드에 `/etc/gcp/api-bq-reader/key.json` 으로 마운트되며
`GOOGLE_APPLICATION_CREDENTIALS` 가 해당 경로를 가리킨다. 렌더 결과는
`helm template api gitops/apps/api --set analytics.enabled=true --set analytics.projectId=cledyu-project`
로 사전 확인할 수 있다.

적용 후 ArgoCD sync:

```bash
argocd app sync service-api
kubectl -n api rollout status deployment/api
```

## 3. 검증

강사(instructor) 역할 계정으로 `/instructor` 페이지에 접근하여 완료율·이탈지점·힌트
표가 채워지는지 확인한다.

BigQuery 뷰에 데이터가 없으면 D2 파이프라인을 먼저 트리거한다(docs/RUNBOOK/d2-data-pipeline.md).

```bash
bq query --use_legacy_sql=false 'SELECT * FROM cledyu_analytics.v_lab_completion LIMIT 5'
bq query --use_legacy_sql=false 'SELECT * FROM cledyu_analytics.v_step_funnel LIMIT 5'
bq query --use_legacy_sql=false 'SELECT * FROM cledyu_analytics.v_hint_usage LIMIT 5'
```

API 엔드포인트 직접 확인(instructor 토큰 필요):

```bash
TOKEN="$(curl -s -X POST https://auth.cledyu.com/realms/cledyu/protocol/openid-connect/token \
  -d grant_type=password -d client_id=web \
  -d username=<instructor-user> -d password=<password> | jq -r .access_token)"
curl -H "Authorization: Bearer $TOKEN" https://api.cledyu.local/api/v1/instructor/analytics
```

기대 응답: HTTP 200, `lab_completion`/`step_funnel`/`hint_usage` 키가 포함된 JSON.
403 응답이면 Keycloak에서 해당 계정의 `instructor` 역할을 확인한다.
