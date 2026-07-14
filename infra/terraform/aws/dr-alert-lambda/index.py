import json
import os
import urllib.request

import boto3

_sm = boto3.client("secretsmanager")


def _webhook_url():
    # 캐시하지 않고 매 invocation 마다 Secrets Manager 에서 읽는다. DR 알림은 드물게만
    # (재해·드릴 시) 발생하므로 호출당 1회 조회의 비용·지연은 무시할 수준이고, 웹훅
    # 로테이션·폐기가 warm Lambda 환경에서도 즉시 반영된다(런북 §7.1 "캐싱 없음" 과 일치).
    resp = _sm.get_secret_value(SecretId=os.environ["WEBHOOK_SECRET_ARN"])
    url = json.loads(resp["SecretString"])["url"]
    # https Discord 웹훅만 허용. file:/커스텀 스킴을 차단해 urlopen 오용(ruff S310)의
    # 실제 우려를 제거한다.
    if not url.startswith("https://"):
        raise ValueError("webhook URL must be https")
    return url


def handler(event, context):
    for record in event.get("Records", []):
        # 이벤트 shape 이 어긋나도 크래시하지 않도록 방어적으로 추출한다.
        raw = record.get("Sns", {}).get("Message", "")
        # 유효 JSON 이면서 객체(dict)일 때만 알람 필드를 뽑는다. 배열·숫자 등
        # dict 아닌 JSON 에서 .get 이 AttributeError 로 터지지 않도록 isinstance 가드.
        alarm = None
        try:
            parsed = json.loads(raw)
            if isinstance(parsed, dict):
                alarm = parsed
        except ValueError:
            pass
        if alarm is not None:
            text = (
                ":rotating_light: **DR 재해 감지** — "
                f"{alarm.get('AlarmName', 'unknown')}\n"
                f"상태: {alarm.get('NewStateValue', '?')}\n"
                f"사유: {alarm.get('NewStateReason', '')}"
            )
        else:
            text = f":rotating_light: DR alert: {raw}"
        payload = json.dumps({"content": text}).encode("utf-8")
        # URL 은 _webhook_url() 에서 https 스킴 검증을 마친 값이라 안전하다.
        req = urllib.request.Request(  # noqa: S310
            _webhook_url(),
            data=payload,
            headers={"Content-Type": "application/json"},
            method="POST",
        )
        # 실패 시 예외를 그대로 올려 Lambda 재시도(at-least-once)로 알림 유실을 막는다.
        # SNS→Lambda 는 호출당 1 레코드라 배치 재시도 중복 위험은 사실상 없다.
        urllib.request.urlopen(req, timeout=10)  # noqa: S310
    return {"ok": True}
