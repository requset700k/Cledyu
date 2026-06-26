-- D3 강사 분석용 BigQuery 뷰. raw lab_events 위 집계 + dedup.
-- dedup: append 적재라 동일 이벤트가 중복될 수 있어 (user,session,event_type,step,ts) 기준 1건만.

CREATE OR REPLACE VIEW `cledyu_analytics.v_dedup_events` AS
SELECT * EXCEPT (rn) FROM (
  SELECT *, ROW_NUMBER() OVER (
    PARTITION BY user_id, session_id, event_type, step_id, ts
    ORDER BY _ingested_at
  ) AS rn
  FROM `cledyu_analytics.lab_events`
)
WHERE rn = 1;

-- 랩별 시작/완료/완료율.
CREATE OR REPLACE VIEW `cledyu_analytics.v_lab_completion` AS
SELECT
  lab_id,
  COUNTIF(event_type = 'lab_started')   AS started,
  COUNTIF(event_type = 'lab_completed') AS completed,
  SAFE_DIVIDE(COUNTIF(event_type = 'lab_completed'),
              COUNTIF(event_type = 'lab_started')) AS completion_rate
FROM `cledyu_analytics.v_dedup_events`
WHERE lab_id IS NOT NULL
GROUP BY lab_id;

-- 랩·스텝별 검증 실패(이탈 지점).
CREATE OR REPLACE VIEW `cledyu_analytics.v_step_funnel` AS
SELECT
  lab_id,
  step_id,
  COUNT(*) AS validation_failures
FROM `cledyu_analytics.v_dedup_events`
WHERE event_type = 'validation_failed'
GROUP BY lab_id, step_id
ORDER BY validation_failures DESC;

-- 랩·스텝별 힌트 사용 패턴(ai/static).
CREATE OR REPLACE VIEW `cledyu_analytics.v_hint_usage` AS
SELECT
  lab_id,
  step_id,
  hint_source,
  COUNT(*) AS hint_count
FROM `cledyu_analytics.v_dedup_events`
WHERE event_type = 'hint_requested'
GROUP BY lab_id, step_id, hint_source;
