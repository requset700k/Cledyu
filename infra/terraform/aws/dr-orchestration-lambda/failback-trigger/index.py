"""push 하트비트 알람 OK 복귀(us-east-1) → active 게이트 확인 → failback SFN 시작(ap-northeast-2).

failover-trigger 의 대칭. 다른 점:
  · **`/cledyu-dr/failover/active` SSM 파라미터가 있을 때만** 시작(= 정상 완료된 failover 가 되돌릴
    대상으로 존재). 평상시 하트비트 깜빡임이나 부분 실패 failover 후 복귀는 파라미터가 없어 무시.
  · [R2 개정 2026-07-18] 중복 방지를 **진행 중(RUNNING) 실행에 한정**한다. 실행 이름 접두는 active
    값(=failover 실행 id)에서 결정적으로 파생하되, 이름 전체는 시도마다 유니크(접두+타임스탬프)로 둔다.
    - 하트비트 flapping 으로 push OK 가 연속으로 떠도, 첫 실행이 RUNNING 이면 같은 접두라 무시 → 승인
      메시지 1개.
    - **첫 failback 이 실패/중단되면** RUNNING 이 없으므로 다음 회복 신호에 **새 이름으로 재시도**된다.
      (구현: 이름을 active-id 하나로 고정했더니 Standard SFN 이 닫힌 이름을 90일 재사용 못 해 실패한
       failback 이 영영 재시도 불가였다 — 그 회귀를 이 방식으로 해소.)
"""

import hashlib
import json
import os
import time

import boto3

_sfn = boto3.client("stepfunctions", region_name=os.environ["SFN_REGION"])
_ssm = boto3.client(
    "ssm", region_name=os.environ["SFN_REGION"]
)  # 활성 파라미터는 dr_failover SM(ap-northeast-2)이 쓴다 → 이 Lambda 는 us-east-1 이지만 SFN_REGION 으로 읽는다
_cw = boto3.client(
    "cloudwatch"
)  # push 알람은 이 Lambda 와 같은 us-east-1 → 로컬 조회(크로스리전 불요)

SM_ARN = os.environ["STATE_MACHINE_ARN"]
PUSH_ALARM = os.environ["PUSH_ALARM_NAME"]


def should_trigger(active_value):
    """failover 활성 플래그(값=failover 실행 id)가 있을 때만 failback 을 건다."""
    return bool(active_value)


def name_prefix(active_value):
    """이 failover(active id)에 대한 failback 실행 이름 **접두** — 결정적(RUNNING 중복 판정용).

    sha256 을 쓴다 — 보안 용도가 아니라 결정적 축약이 목적이지만 sha1 은 ruff S324 로 막힌다.
    접두 24자 + '-' + epoch(10자) + 'failback-'(9자) = 44자 ≤ 80(SFN 제약).
    """
    return "failback-" + hashlib.sha256(active_value.encode()).hexdigest()[:24]


def _running_exists(prefix):
    """이 failover 에 대한 failback 이 이미 RUNNING 인가(닫힌 실행은 무시 → 실패분은 재시도 가능)."""
    paginator = _sfn.get_paginator("list_executions")
    for page in paginator.paginate(stateMachineArn=SM_ARN, statusFilter="RUNNING"):
        if any(e["name"].startswith(prefix) for e in page["executions"]):
            return True
    return False


def _active_value():
    try:
        return _ssm.get_parameter(Name=os.environ["ACTIVE_PARAM"])["Parameter"]["Value"]
    except _ssm.exceptions.ParameterNotFound:
        return None


def _push_ok():
    """push 하트비트 알람이 현재 OK(온프렘 회복)인가 — verify_alarm 재확인용."""
    alarms = _cw.describe_alarms(AlarmNames=[PUSH_ALARM])["MetricAlarms"]
    return bool(alarms) and alarms[0]["StateValue"] == "OK"


def handler(event, context):
    active = _active_value()
    if not should_trigger(active):
        return {"started": False, "reason": "not-failed-over"}

    # verify_alarm(=EventBridge OK 이벤트가 아니라 **주기 reconcile 규칙의 직접 호출**): push 알람을 다시 보고
    # **OK 일 때만** 시작한다. 정규 EventBridge 경로는 이 플래그가 없어 건너뛴다(이벤트 자체가 이미 OK 전이).
    # dr_recovery 는 push→OK **전이**에만 울리므로 reconcile 이 두 사각을 커버한다:
    #  ① failover 중 온프렘이 이미 회복하면 OK 이벤트가 active 설정 전에 지나가 자동 failback 을 놓친다.
    #  ② failback 이 실패/타임아웃한 뒤 push 가 steady OK 면 새 전이가 없어 재시도가 안 걸린다(실패 경로가
    #     active 를 보존해도). reconcile 이 active+push OK+RUNNING없음을 보고 재개한다. (2026-07-18 리뷰 P2)
    if event.get("verify_alarm") and not _push_ok():
        return {"started": False, "reason": "onprem-still-down"}

    prefix = name_prefix(active)
    # ⚠️ RUNNING 체크와 start_execution 사이엔 좁은 레이스가 있다(두 push OK 가 첫 실행이 RUNNING 으로
    #    보이기 전 ~1s 내 겹치면 둘 다 통과). 하트비트 dead-man's-switch 의 →OK 전이는 sub-second 로
    #    반복되지 않아 실질 위험은 낮다. 완전 차단보다 **실패 failback 재시도 가능**을 우선한 트레이드오프.
    if _running_exists(prefix):
        return {"started": False, "reason": "already-running", "prefix": prefix}

    name = f"{prefix}-{int(time.time())}"  # 시도마다 유니크 → 닫힌 이름 90일 잠김 회피(재시도 허용)
    detail = event.get("detail", {})
    try:
        _sfn.start_execution(
            stateMachineArn=SM_ARN,
            name=name,
            input=json.dumps(
                {
                    "mode": "failback",
                    # 트리거 시점의 active(=failover 세대)를 실행에 고정한다. 승인이 24h 대기하는 동안
                    # 수동 failback 이 active 를 지우고 새 failover 가 다른 세대를 세팅할 수 있는데, SFN 이
                    # 승인 직후 이 값과 현재 SSM active 를 대조해 세대가 바뀌었으면 teardown 을 건너뛴다
                    # (옛 승인으로 최신 hot 레이어를 회수하는 것 방지, 2026-07-18 리뷰 P2).
                    "active": active,
                    "alarmName": detail.get("alarmName"),
                    "recoveredAt": (detail.get("state") or {}).get("timestamp"),
                }
            ),
        )
    except _sfn.exceptions.ExecutionAlreadyExists:  # 사실상 발생 안 함(타임스탬프 유니크) — 방어적
        return {"started": False, "reason": "duplicate", "executionName": name}
    return {"started": True, "executionName": name}
