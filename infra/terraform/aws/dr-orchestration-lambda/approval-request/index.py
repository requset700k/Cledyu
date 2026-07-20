"""DR 페일오버 승인 요청 — SFN .waitForTaskToken 의 첫 상태.

S3 의 Vault raft 스냅샷을 최신순 25개(Discord String Select 상한) 뽑아 드롭다운으로 만들고,
승인 버튼과 함께 Discord 채널에 게시한 뒤 taskToken 을 DynamoDB 에 저장하고 반환 없이 끝난다
(SFN 은 interaction Lambda 의 SendTaskSuccess 를 받을 때까지 대기).

⚠️ **웹훅이 아니라 Bot API 로 보낸다.** dr-alert(#310)처럼 채널 설정에서 만든 일반 incoming
웹훅은 **버튼·드롭다운을 못 보낸다** — Discord 공식 문서: "Non-application-owned webhooks cannot
send interactive components, and the components field will be ignored". 더 나쁜 건 **에러가 아니라
2xx 를 주고 components 만 조용히 버린다**는 점이다(실측 2026-07-15: Lambda 성공·DDB 저장까지 됐는데
메시지에 버튼만 없음). 그래서 승인 메시지는 application 소유 주체(=봇)로 보내야 한다.
평문 알림(dr-alert)은 컴포넌트가 없어 웹훅 그대로 둔다.
"""

import json
import os
import time
import urllib.request
import uuid

import boto3

_s3 = boto3.client("s3")
_ddb = boto3.client("dynamodb")
# 봇 토큰 시크릿은 이 Lambda 와 같은 리전(ap-northeast-2, 기본 provider)에 만든다 →
# 크로스리전 파싱 불요(us-east-1 에 있는 dr-alert 웹훅 시크릿과 다른 점).
_sm = boto3.client("secretsmanager")

# Discord String Select 옵션 상한(공식 문서). 6h 주기 스냅샷 기준 약 6일치.
MAX_OPTIONS = 25
# 승인 대기 24h = SFN .waitForTaskToken 타임아웃과 일치시킨다.
TTL_SECONDS = 24 * 60 * 60
DISCORD_API = "https://discord.com/api/v10"


def _bot_token():
    # 캐싱하지 않는다 — 승인 요청은 재해·드릴 때만 발생하므로 호출당 1회 조회 비용이 무시할 수준이고,
    # 토큰 로테이션(Developer Portal 의 Reset Token)이 warm Lambda 에도 즉시 반영된다(dr-alert 와 동일 원칙).
    resp = _sm.get_secret_value(SecretId=os.environ["BOT_TOKEN_SECRET_ARN"])
    return json.loads(resp["SecretString"])["token"]


def _list_snapshots():
    """s3://<bucket>/vault/ 의 스냅샷을 최신순으로 최대 25개."""
    bucket = os.environ["BACKUP_BUCKET"]
    paginator = _s3.get_paginator("list_objects_v2")
    keys = []
    for page in paginator.paginate(Bucket=bucket, Prefix="vault/"):
        keys.extend(
            (obj["LastModified"], obj["Key"])
            for obj in page.get("Contents", [])
            if obj["Key"].endswith(".snap")
        )
    if not keys:
        raise RuntimeError(f"vault 스냅샷이 없다: s3://{bucket}/vault/")
    keys.sort(reverse=True)  # 최신순
    return [k for _, k in keys[:MAX_OPTIONS]]


def _build_message(approval_id, is_test, mode, snapshots):
    if mode == "failback":
        body = (
            "♻️ **DR 페일백 승인 요청**\n"
            "push 하트비트 복귀 — **온프렘 회복 감지**\n\n"
            "⚠️ **승인 전 직접 확인**: 온프렘 CNPG/Keycloak 이 실제 복원·서빙되는지\n"
            "승인하면 DNS 가 온프렘으로 원복되고 EKS hot 이 회수되며 **DR 데이터는 폐기**됩니다."
        )
        return {
            "content": body,
            "components": [
                {
                    "type": 1,
                    "components": [
                        {
                            "type": 2,  # Button
                            "style": 1,  # Primary(파랑) — failover 의 Danger 와 구분
                            "label": "♻️ DR 페일백 승인",
                            "custom_id": f"dr-approve:{approval_id}",
                        }
                    ],
                }
            ],
        }

    # ── failover(실재해/테스트) — 기존 로직 보존 ──
    latest = snapshots[0]
    prefix = "🧪 [테스트] " if is_test else "🚨 "
    body = (
        f"{prefix}**DR 페일오버 승인 요청**\n"
        "pull(Route53) + push(하트비트) 복합알람 ALARM — 온프렘 상실 감지\n\n"
        "⚠️ **승인 전 직접 확인**: 사이트 접속 · 온프렘 콘솔 · 일시적 네트워크 장애 여부\n"
        "승인하면 EKS 기동 → 복원 → **공개 DNS 전환**까지 자동 진행됩니다."
    )
    options = [
        {"label": s.split("/")[-1][:100], "value": s[:100], "default": s == latest}
        for s in snapshots
    ]
    return {
        "content": body,
        "components": [
            {
                "type": 1,
                "components": [
                    {
                        "type": 3,
                        "custom_id": f"dr-snap:{approval_id}",
                        "placeholder": "Vault 스냅샷 시점",
                        "options": options,
                    }
                ],
            },
            {
                "type": 1,
                "components": [
                    {
                        "type": 2,
                        "style": 4 if not is_test else 2,
                        "label": "🧪 테스트 승인" if is_test else "🔴 DR 페일오버 승인",
                        "custom_id": f"dr-approve:{approval_id}",
                    }
                ],
            },
        ],
    }


def handler(event, context):
    task_token = event["taskToken"]
    # SFN 이 실행 입력 전체를 event["input"] 으로 넘긴다("input.$": "$").
    # ⚠️ ASL 에서 "mode.$": "$.mode" 로 직접 뽑으면 안 된다 — 실재해 경로(failover-trigger)는
    # 입력에 mode 를 넣지 않으므로 그 JSONPath 가 없어 States.Runtime 으로 즉시 죽는다.
    # 입력 전체를 받아 여기서 꺼내면 mode 유무와 무관하게 동작한다.
    payload = event.get("input") or {}
    mode = payload.get("mode") if isinstance(payload, dict) else None
    # mode 는 메시지의 긴급도를 바꾸는 스위치라 fail-safe 로 판정한다 — 정확히 "test" 일 때만
    # 테스트 렌더, 그 외(필드 없음·null·오타·타입 불일치)는 전부 실재해다(설계 §7.2 H3).
    is_test = mode == "test"
    is_failback = mode == "failback"

    approval_id = uuid.uuid4().hex[:16]  # custom_id 100자 상한 여유

    item = {
        "approvalId": {"S": approval_id},
        "taskToken": {"S": task_token},
        "ttl": {"N": str(int(time.time()) + TTL_SECONDS)},
    }
    snapshots = None
    if not is_failback:
        snapshots = _list_snapshots()
        item["latestSnapshot"] = {"S": snapshots[0]}  # failback 은 스냅샷 개념 없음

    _ddb.put_item(TableName=os.environ["APPROVALS_TABLE"], Item=item)

    message = _build_message(approval_id, is_test, mode, snapshots)

    # Bot API 로 게시 — 웹훅은 components 를 조용히 버린다(모듈 docstring 참조).
    # 봇은 이 채널에 Send Messages 권한이 있어야 한다(없으면 403 Missing Permissions).
    req = urllib.request.Request(  # noqa: S310
        f"{DISCORD_API}/channels/{os.environ['DISCORD_CHANNEL_ID']}/messages",
        data=json.dumps(message).encode("utf-8"),
        headers={
            "Content-Type": "application/json",
            # 봇 인증. "Bot " 접두는 Discord 규격이며 빠지면 401 이다.
            "Authorization": f"Bot {_bot_token()}",
            # Discord 는 Cloudflare 뒤라 기본 UA(Python-urllib/*)를 403 으로 막는다(#311).
            "User-Agent": "Cledyu-DR-Approval/1.0 (+https://cledyu.com)",
        },
        method="POST",
    )
    # 실패 시 예외를 그대로 올려 SFN Task 를 실패시킨다 — 승인 메시지가 안 갔는데 토큰만 24h 대기하는
    # 상태(사람이 영영 못 누름)를 만들지 않는다.
    with urllib.request.urlopen(req, timeout=10) as resp:  # noqa: S310
        resp.read()

    # 반환하지 않는다 — SFN 은 SendTaskSuccess 를 기다린다.
    return {"approvalId": approval_id}
