-- 어떤 스텝에서 사람들이 막히는가를 분석하는 mart 테이블
-- 각 스텝 완료율, validation 실패율을 집계

{{
    config(
        partition_by={
            "field": "event_date",
            "data_type": "date",
            "granularity": "day"
        },
        cluster_by=["lab_id", "step_id"]
    )
}}

WITH step_events AS (
    SELECT
        lab_id,
        session_id,
        step_id,
        event_date,
        event_type
    FROM {{ ref('stg_lab_events') }}
    WHERE event_type IN ('step_completed', 'validation_failed')
      AND step_id IS NOT NULL
)

SELECT
    lab_id,
    step_id,
    event_date,

    -- 이 스텝을 시도한 세션 수 (중복 제거)
    COUNT(DISTINCT session_id)                                                AS sessions_reached,

    -- step_completed 이벤트 개수
    COUNTIF(event_type = 'step_completed')                                    AS step_completions,

    -- validation_failed 이벤트 개수
    COUNTIF(event_type = 'validation_failed')                                 AS validation_failures,

    -- 완료율 = 완료한 세션 수 / 도달한 세션 수
    -- 이벤트 횟수가 아닌 세션 수 기준 — 중복 이벤트로 비율이 1.0 초과하는 것 방지
    {{ safe_rate("COUNT(DISTINCT CASE WHEN event_type = 'step_completed' THEN session_id END)", "COUNT(DISTINCT session_id)") }} AS step_completion_rate,

    -- 실패율 = 실패 이벤트 수 / 도달한 세션 수
    {{ safe_rate("COUNTIF(event_type = 'validation_failed')", "COUNT(DISTINCT session_id)") }} AS failure_rate
FROM step_events
GROUP BY 1, 2, 3
