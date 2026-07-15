"""복합알람 ALARM(us-east-1) → Step Functions 시작(ap-northeast-2).

EventBridge 크로스리전 버스를 엮는 대신 작은 Lambda 하나가 리전을 넘긴다(설계 §3.2).
복합알람과 그 상태변화 이벤트는 us-east-1 전용(Route53 HealthCheckStatus 메트릭 제약)이고,
Step Functions·EKS DR 은 ap-northeast-2 에 있다.
"""

import json
import os

import boto3

# 타겟 SM 이 있는 리전으로 명시 고정 — Lambda 자신은 us-east-1 에서 돈다.
_sfn = boto3.client("stepfunctions", region_name=os.environ["SFN_REGION"])


def handler(event, context):
    detail = event.get("detail", {})

    # 실행 이름을 EventBridge 이벤트 id 로 고정해 **멱등**하게 만든다.
    # EventBridge→Lambda 는 비동기(at-least-once) 경로라 같은 알람 이벤트가 중복 전달될 수 있고,
    # Lambda 자체도 실패 시 재시도한다. name 을 안 주면 중복마다 **새 실행 + 새 승인 토큰**이 생겨
    # Discord 에 승인 버튼이 여러 개 뜨고, Plan 2 연결 후엔 **한 재해가 여러 페일오버**로 이어진다.
    #
    # event["id"] 는 이벤트마다 유일하므로 진짜 중복만 막고, 알람이 OK→ALARM 을 두 번 오가는
    # 정당한 두 전이는 서로 다른 id 라 각각 실행된다(원하는 동작).
    # SFN 실행 이름 제약: 1~80자. EventBridge id 는 UUID(36자)라 안전.
    name = event.get("id") or context.aws_request_id

    try:
        _sfn.start_execution(
            stateMachineArn=os.environ["STATE_MACHINE_ARN"],
            name=name,
            # mode 를 넣지 않는다 → approval-request 가 실재해로 렌더한다(fail-safe, 설계 §7.2 H3).
            input=json.dumps(
                {
                    "alarmName": detail.get("alarmName"),
                    "reason": (detail.get("state") or {}).get("reason"),
                    "detectedAt": (detail.get("state") or {}).get("timestamp"),
                }
            ),
        )
    except _sfn.exceptions.ExecutionAlreadyExists:
        # 같은 이벤트가 이미 실행을 시작했다 = 중복 전달. 성공으로 처리해 Lambda 재시도를 끊는다.
        # (예외를 올리면 Lambda 가 계속 재시도하고 매번 같은 예외를 맞는다.)
        return {"started": False, "reason": "duplicate", "executionName": name}

    return {"started": True, "executionName": name}
