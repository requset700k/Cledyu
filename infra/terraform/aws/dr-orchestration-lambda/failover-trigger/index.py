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
    _sfn.start_execution(
        stateMachineArn=os.environ["STATE_MACHINE_ARN"],
        # mode 를 넣지 않는다 → approval-request 가 실재해로 렌더한다(fail-safe, 설계 §7.2 H3).
        input=json.dumps(
            {
                "alarmName": detail.get("alarmName"),
                "reason": (detail.get("state") or {}).get("reason"),
                "detectedAt": (detail.get("state") or {}).get("timestamp"),
            }
        ),
    )
    return {"started": True}
