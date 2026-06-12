-- 어떤 스텝에서 힌트를 얼마나, 어떤 방식으로 쓰는가를 분석
-- AI 힌트(ai_tutor)와 정적 힌트(static)의 사용 패턴을 랩 스텝/힌트소스/날짜별로 집계

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

SELECT
    lab_id,
    step_id,
    hint_source,
    event_date,
    -- 힌트 요청 이벤트 총 횟수 (중복 허용)
    COUNT(*)                    AS hint_count,

    -- 힌트를 쓴 고유 세션 수 (이 스텝에서 몇 명이 힌트를 봤나)
    COUNT(DISTINCT session_id)  AS sessions_used_hints,

    -- 평균 힌트 레벨
    ROUND(AVG(hint_level), 2)   AS avg_hint_level,

    -- 그 스텝에서 요청된 가장 깊은 힌트 레벨
    MAX(hint_level)             AS max_hint_level
FROM {{ ref('stg_lab_events') }}
WHERE event_type = 'hint_requested' --힌트 이벤트만 필터링
  AND step_id IS NOT NULL
GROUP BY 1, 2, 3, 4
