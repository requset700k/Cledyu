-- stg_lab_events를 읽어서 랩별 일별 완료율과 이탈률을 계산해 BigQuery 테이블로 저장하는 파일

{{
    config(
        partition_by={
            "field": "event_date",
            "data_type": "date",
            "granularity": "day"
        },
        cluster_by=["lab_id"]
    )
}}

WITH sessions AS (
    SELECT
        lab_id,
        session_id,
        user_id,
        -- lab_started 이벤트 기준 날짜로 고정 (자정 넘어 완료 시 세션이 두 날짜로 쪼개지는 것 방지)
        MIN(event_date)                              AS event_date,
        LOGICAL_OR(event_type = 'lab_completed')     AS is_completed, --세션 안의 이벤트 중 lab_completed가 하나라도 있으면 is_completed = true
        LOGICAL_OR(event_type = 'lab_abandoned')     AS is_abandoned
    FROM {{ ref('stg_lab_events') }}
    WHERE event_type IN ('lab_started', 'lab_completed', 'lab_abandoned') -- 완료율 계산에 필요한 3개만 필터링
    GROUP BY 1, 2, 3
)

SELECT
    lab_id,
    event_date,

    -- 해당 랩의 해당 날짜 전체 세션 수
    COUNT(DISTINCT session_id)                                                    AS total_sessions,

    -- 완료율
    COUNTIF(is_completed)                                                         AS completed_sessions,
    {{ safe_rate('COUNTIF(is_completed)', 'COUNT(DISTINCT session_id)') }}         AS completion_rate,

    -- 이탈률
    COUNTIF(is_abandoned)                                                         AS abandoned_sessions,
    {{ safe_rate('COUNTIF(is_abandoned)', 'COUNT(DISTINCT session_id)') }}        AS abandonment_rate
FROM sessions
GROUP BY 1, 2
