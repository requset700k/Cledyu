# D2 데이터 파이프라인 설계 (Kafka → Airflow → BigQuery + GCS)

- 작성일: 2026-06-26
- 상태: 설계 승인 — 구현 플랜 대기
- 영향 레이어: 데이터(Kafka/Airflow/BigQuery), 인프라(GCP Terraform), 보안(SA·Vault/ESO)
- 소유: 데이터(김찬영) 주도, 인프라/시크릿은 [[project_secret_management]] 패턴
- 제약: [[project_deadline_terminating]] (2026-07-22 종료) — 속도·데모 우선, 내구성 과투자 금지
- 상위 설계: [학습 분석 3트랙](2026-06-26-learning-analytics-design.md) 의 D2. D1(대시보드)는 PR #191로 머지됨.

## 1. 목적 / 배경

`lab-events` Kafka 이벤트를 영구 저장소(BigQuery)로 적재하는 파이프라인을 **Airflow로 오케스트레이션**한다. 목적:
1. **데이터 엔지니어링 역량 증명** — 프로젝트가 표방한 Kafka/Airflow/BigQuery 스택을 동작으로 시연(평가 산출물).
2. **영구 아카이브** — 이벤트를 온프렘 밖 BQ/GCS에 무기한 보관(온프렘 스토리지 오프로드).
3. **D3 강사 분석 공급** — raw 위 집계 VIEW 가 랩 실패율·힌트 패턴·코호트를 제공.

### 현재 상태 (실측)
- 소스: `lab-events` Kafka 토픽(12파티션·3복제·7일 보존·mTLS) + producer가 main에서 발행 중(6 이벤트 타입). (`gitops/apps/kafka-cluster/topics/lab-events.yaml`, `apps/api/internal/events/`)
- Airflow: 설치됨(LocalExecutor, 2.10.2), **git-sync로 `apps/airflow/dags/` 를 main에서 60초마다 동기화** → DAG 파일 머지 시 자동 배포. DAG 0개. (`gitops/apps/airflow/values.yaml`)
- GCP Terraform: **없음** — `infra/terraform/gcp` 신설. (`infra/terraform`에 aws/keycloak/kvm만)
- Vault→ESO 시크릿 패턴 확립(ClusterSecretStore `vault-backend`, `cledyu/...` 경로).

## 2. 확정된 설계 결정

| 항목 | 결정 | 근거 |
|---|---|---|
| 토폴로지 | **Airflow DAG 중심 배치**: Kafka 소비 → GCS NDJSON → BQ raw load | 단일 스택, Airflow 오케스트레이션 전체 시연 |
| 변환 | **BigQuery VIEW**(dbt 생략) | D3 집계는 단순 GROUP BY — dbt 셋업 오버헤드 불필요 |
| 실행 주기 | **수동 트리거**(`schedule=None`) | 공개 배포 전이라 데이터 희소 — 스케줄 배치 무의미, 데모 때 트리거 |
| 데모 데이터 | **실 수동 사용**(합성 시더 미포함) | 실 랩 흐름이 Postgres(D1)+Kafka(D2) 동시·일관 충족 |
| 클라우드 | GCP(BQ+GCS), **$300 크레딧** | 종료프로젝트라 90일 만료 무관, BQ·GCS는 크레딧 대상 |

## 3. 아키텍처 / 데이터 흐름

```
lab-events(Kafka, 12p, mTLS)
  └─[Airflow DAG: lab_events_to_bq, schedule=None, 수동 트리거]
       1) consume : confluent-kafka consumer group(커밋 오프셋)으로 신규 이벤트 배치 폴
       2) land    : GCS gs://<bucket>/lab-events/dt=YYYY-MM-DD/run=<run_id>.ndjson (영구 아카이브)
       3) load    : BQ load job → cledyu_analytics.lab_events (WRITE_APPEND)
       4) views   : D3용 VIEW CREATE OR REPLACE (멱등)
```
- 온프렘 Airflow(LocalExecutor) → Kafka(in-cluster mTLS), GCP(인터넷)로 BQ/GCS.
- 증분: consumer group 커밋 오프셋(at-least-once). 7일 보존 안에서 수동 트리거로 충분.

## 4. BigQuery 스키마 + D3 뷰

- 데이터셋 **`cledyu_analytics`** (location: GCS state와 같은 리전 정렬 — 예: asia-northeast3).
- raw 테이블 **`lab_events`**: Event 구조 컬럼 + 적재 메타.
  - `event_type STRING, user_id STRING, session_id STRING, lab_id STRING, step_id INT64, hint_level INT64, hint_source STRING, vm_provisioned_source STRING, ts TIMESTAMP, _ingested_at TIMESTAMP`
  - `PARTITION BY DATE(ts)`, `CLUSTER BY event_type, lab_id`.
- D3 뷰 (CREATE OR REPLACE VIEW, raw GROUP BY; dedup 포함):
  - `v_lab_completion` — 랩별 시작/완료/완료율(lab_started vs lab_completed).
  - `v_step_funnel` — 랩·스텝별 `validation_failed` 집계 → 이탈 지점.
  - `v_hint_usage` — 랩·스텝별 `hint_requested`(source ai/static) 패턴.
  - dedup: raw append 라 `(user_id, session_id, event_type, step_id, ts)` 기준 `SELECT DISTINCT`/`QUALIFY ROW_NUMBER()` 로 중복 흡수.

## 5. 인프라 / 자격증명 (신규)

### `infra/terraform/gcp` (신규 모듈)
- `google_storage_bucket` (raw 랜딩/아카이브 — uniform access, 종료프로젝트라 단순 lifecycle).
- `google_bigquery_dataset` `cledyu_analytics`.
- `google_service_account` `airflow-analytics` + IAM: `roles/bigquery.dataEditor`, `roles/bigquery.jobUser`, 버킷 `roles/storage.objectAdmin`.
- backend: 기존 GCS state(프로젝트 owner onimami1110 [[reference_gcp_owner_account]]).
- D3 VIEW DDL: Terraform `google_bigquery_table`(view) 또는 DAG 셋업 태스크 — 플랜에서 결정(Terraform이 멱등·선언적이라 우선).

### 자격증명 (Vault → ESO → Airflow)
- GCP SA 키 → Vault `cledyu/gcp/airflow-analytics-sa` → ESO → Airflow ns Secret → DAG `GOOGLE_APPLICATION_CREDENTIALS`.
- Kafka mTLS: Strimzi `KafkaUser`(분석 소비자) → ESO 로 클라이언트 인증서 → DAG 소비자 설정.
- **사람 단계(차단요소)**: GCP SA 생성·키 발급·Vault put 은 owner/break-glass 작업. 플랜은 이를 명시적 사전 단계로 둔다.

### Airflow Python 의존성
- `apache-airflow-providers-google`(BQ/GCS operator), `confluent-kafka`(소비).
- 주입 방식: `_PIP_ADDITIONAL_REQUIREMENTS`(데모 간편, 시작 시 설치) vs 커스텀 이미지 — 플랜에서 결정(데모면 _PIP 우선, 단 시작 지연 주의).

## 6. 빌드 분해 (순차 — 각자 검증 가능)

- **D2a 인프라**: `infra/terraform/gcp`(버킷·데이터셋·SA·IAM) + Vault/ESO 자격증명 ExternalSecret. 산출물: `terraform validate`/`plan` + 매니페스트 적용.
- **D2b DAG**: `apps/airflow/dags/lab_events_to_bq.py`(consume→GCS→BQ) + Python 의존성 + DAG import 테스트.
- **D2c 뷰**: D3용 BigQuery VIEW DDL(Terraform view 리소스 또는 DAG 태스크).

## 7. 에러 처리 / 멱등성

- 소비 실패: 오프셋 미커밋 → 다음 트리거 재소비(at-least-once). DAG 태스크 retry 2회.
- BQ append 부분실패 중복: D3 뷰 dedup 으로 흡수(위 §4).
- GCS 랜딩 멱등: 배치 파일명에 `run_id` 포함 → 재실행 시 덮어쓰기/신규.
- 빈 토픽: 소비 0건이면 GCS/BQ 단계 스킵(빈 load 회피), DAG 성공 종료.

## 8. 테스트 / 검증

- **자동 검증 가능**: DAG import/파싱(`python -c`/airflow DAG 로드), SQL 문법, Terraform `validate`/`plan`, ruff(Python lint), yamllint(ESO 매니페스트).
- **라이브 필요(별도 스모크)**: 실제 Kafka 소비 + BQ 적재 end-to-end — 라이브 클러스터 + GCP SA 필요. SA 생성은 사람/break-glass. Helm 랩과 동일하게 **실 환경 스모크는 데모 전 별도**.
- 게이트: ruff, yamllint/pre-commit, terraform fmt/validate.

## 9. SLO / 비용 / DR

- 온프렘 Lab/Validation/VM/WS SLO 무관(분석은 별도 경로, Airflow LocalExecutor 자원은 기존 할당 내).
- GCP 크레딧(BQ·GCS 대상, 종료프로젝트라 만료 무관). 추가 상시 비용 미미(쿼리 무료 1TB/월 + 크레딧).
- DR 무관 — 웨어하우스/아카이브는 재구성 가능한 싱크(raw는 GCS NDJSON 으로 replay 가능, system-of-record 아님).

## 10. 의도적 비포함 (YAGNI / 후속)

- 스케줄 배치(@hourly), 실시간 스트리밍(Kafka Connect/Dataflow), dbt, DataHub 리니지.
- 합성 데이터 시더(실 사용으로 대체, 필요 시 옵션).
- D3 강사 분석 UI/엔드포인트 — 별도 sub-project(이 D2는 뷰까지 제공).
