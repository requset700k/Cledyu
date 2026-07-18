"""push 하트비트 알람 OK 복귀(us-east-1) → active 게이트 확인 → failback SFN 시작(ap-northeast-2).

failover-trigger 의 대칭. 다른 점:
  · **`/cledyu-dr/failover/active` SSM 파라미터가 있을 때만** 시작(= 정상 완료된 failover 가 되돌릴
    대상으로 존재). 평상시 하트비트 깜빡임이나 부분 실패 failover 후 복귀는 파라미터가 없어 무시.
  · [R2] 실행 이름을 **event id 가 아니라 active 파라미터 값(=failover 실행 id)에서 파생**한다.
    하트비트 flapping 으로 push OK 가 여러 번 떠도 같은 failover 를 가리키니 이름이 같아
    ExecutionAlreadyExists → failover 당 failback 딱 1개(중복 승인 메시지 없음).
"""

import hashlib
import json
import os

import boto3

_sfn = boto3.client("stepfunctions", region_name=os.environ["SFN_REGION"])
_ssm = boto3.client(
    "ssm", region_name=os.environ["SFN_REGION"]
)  # 활성 파라미터는 dr_failover SM(ap-northeast-2)이 쓴다 → 이 Lambda 는 us-east-1 이지만 SFN_REGION 으로 읽는다


def should_trigger(active_value):
    """failover 활성 플래그(값=failover 실행 id)가 있을 때만 failback 을 건다."""
    return bool(active_value)


def exec_name(active_value):
    """실행 이름을 failover 실행 id 에서 결정적으로 파생(≤80자 SFN 제약).

    sha256 을 쓴다 — 보안 용도가 아니라 결정적 축약이 목적이지만 sha1 은 ruff S324 로 막힌다.
    """
    return "failback-" + hashlib.sha256(active_value.encode()).hexdigest()[:32]


def _active_value():
    try:
        return _ssm.get_parameter(Name=os.environ["ACTIVE_PARAM"])["Parameter"]["Value"]
    except _ssm.exceptions.ParameterNotFound:
        return None


def handler(event, context):
    active = _active_value()
    if not should_trigger(active):
        return {"started": False, "reason": "not-failed-over"}

    name = exec_name(active)
    detail = event.get("detail", {})
    try:
        _sfn.start_execution(
            stateMachineArn=os.environ["STATE_MACHINE_ARN"],
            name=name,
            input=json.dumps(
                {
                    "mode": "failback",
                    "alarmName": detail.get("alarmName"),
                    "recoveredAt": (detail.get("state") or {}).get("timestamp"),
                }
            ),
        )
    except _sfn.exceptions.ExecutionAlreadyExists:
        return {"started": False, "reason": "duplicate", "executionName": name}
    return {"started": True, "executionName": name}
