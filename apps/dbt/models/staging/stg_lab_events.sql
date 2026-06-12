-- cledyu_raw.lab_events raw 데이터를 읽어서 NULL을 필터링하고 event_date 컬럼을 추가하는 정제 View
--  mart 세 개가 전부 이 모델을 {{ ref('stg_lab_events') }}로 참조

SELECT
    event_type,
    user_id,
    session_id,
    lab_id,
    step_id,
    hint_level,
    hint_source,
    vm_provisioned_source,
    ts,
    DATE(ts) AS event_date -- ts에서 날짜만 뽑아 event_date 컬럼 추가
FROM {{ source('raw', 'lab_events') }} -- _sources.yml에서 선언한 소스 참조
WHERE ts IS NOT NULL
  AND user_id IS NOT NULL
  AND session_id IS NOT NULL
