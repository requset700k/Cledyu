"""DR 페일오버 승인 요청 — SFN .waitForTaskToken 의 첫 상태.

S3 의 Vault raft 스냅샷을 최신순 25개(Discord String Select 상한) 뽑아 드롭다운으로 만들고,
승인 버튼과 함께 Discord 채널에 게시한 뒤 taskToken 을 DynamoDB 에 저장하고 반환 없이 끝난다
(SFN 은 interaction Lambda 의 SendTaskSuccess 를 받을 때까지 대기).
"""

import json
import os
import time
import urllib.request
import uuid

import boto3

_s3 = boto3.client("s3")
_ddb = boto3.client("dynamodb")

# Discord String Select 옵션 상한(공식 문서). 6h 주기 스냅샷 기준 약 6일치.
MAX_OPTIONS = 25
# 승인 대기 24h = SFN .waitForTaskToken 타임아웃과 일치시킨다.
TTL_SECONDS = 24 * 60 * 60


def _webhook_url():
    # dr-alert 와 동일 — 캐싱하지 않아 웹훅 로테이션이 즉시 반영된다.
    #
    # 이 Lambda 는 ap-northeast-2 에서 도는데 시크릿은 dr-alert-lambda.tf 가
    # provider = aws.use1 로 만든 us-east-1 리소스다. Secrets Manager 는 리전
    # 서비스라 클라이언트는 자기 리전 엔드포인트로만 요청하고, ARN 에 리전이
    # 박혀 있어도 자동으로 그 리전으로 라우팅해주지 않는다(크로스리전 ARN 지원
    # 없음 — AWS 문서의 "ARN 사용" 안내는 크로스계정 얘기지 크로스리전이 아니다).
    # 그래서 시크릿 ARN 에서 리전을 파싱해 그 리전으로 클라이언트를 만든다.
    # 하드코딩("us-east-1")하지 않는 이유: ARN 이 진실의 원천이라 시크릿이
    # 나중에 다른 리전으로 옮겨져도 코드 변경 없이 따라간다.
    arn = os.environ["WEBHOOK_SECRET_ARN"]
    region = arn.split(":")[3]
    sm = boto3.client("secretsmanager", region_name=region)
    resp = sm.get_secret_value(SecretId=arn)
    url = json.loads(resp["SecretString"])["url"]
    if not url.startswith("https://"):
        raise ValueError("webhook URL must be https")
    return url


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


def handler(event, context):
    task_token = event["taskToken"]
    # SFN 이 실행 입력 전체를 event["input"] 으로 넘긴다("input.$": "$").
    # ⚠️ ASL 에서 "mode.$": "$.mode" 로 직접 뽑으면 안 된다 — 실재해 경로(failover-trigger)는
    # 입력에 mode 를 넣지 않으므로 그 JSONPath 가 없어 States.Runtime 으로 즉시 죽는다.
    # 입력 전체를 받아 여기서 꺼내면 mode 유무와 무관하게 동작한다.
    payload = event.get("input") or {}
    # mode 는 메시지의 긴급도를 바꾸는 스위치라 fail-safe 로 판정한다 — 정확히 "test" 일 때만
    # 테스트 렌더, 그 외(필드 없음·null·오타·타입 불일치)는 전부 실재해다(설계 §7.2 H3).
    is_test = payload.get("mode") == "test" if isinstance(payload, dict) else False

    snapshots = _list_snapshots()
    latest = snapshots[0]
    approval_id = uuid.uuid4().hex[:16]  # custom_id 100자 상한 여유

    _ddb.put_item(
        TableName=os.environ["APPROVALS_TABLE"],
        Item={
            "approvalId": {"S": approval_id},
            "taskToken": {"S": task_token},
            "latestSnapshot": {"S": latest},
            "ttl": {"N": str(int(time.time()) + TTL_SECONDS)},
        },
    )

    prefix = "🧪 [테스트] " if is_test else "🚨 "
    title = f"{prefix}**DR 페일오버 승인 요청**"
    body = (
        f"{title}\n"
        "pull(Route53) + push(하트비트) 복합알람 ALARM — 온프렘 상실 감지\n\n"
        "⚠️ **승인 전 직접 확인**: 사이트 접속 · 온프렘 콘솔 · 일시적 네트워크 장애 여부\n"
        "승인하면 EKS 기동 → 복원 → **공개 DNS 전환**까지 자동 진행됩니다."
    )

    options = [
        {
            "label": s.split("/")[-1][:100],
            "value": s[:100],
            "default": s == latest,
        }
        for s in snapshots
    ]

    message = {
        "content": body,
        "components": [
            {
                "type": 1,  # ActionRow
                "components": [
                    {
                        "type": 3,  # String Select
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
                        "type": 2,  # Button
                        "style": 4 if not is_test else 2,  # Danger / Secondary
                        "label": "🧪 테스트 승인" if is_test else "🔴 DR 페일오버 승인",
                        "custom_id": f"dr-approve:{approval_id}",
                    }
                ],
            },
        ],
    }

    req = urllib.request.Request(  # noqa: S310
        _webhook_url(),
        data=json.dumps(message).encode("utf-8"),
        headers={
            "Content-Type": "application/json",
            # Discord 는 Cloudflare 뒤라 기본 UA(Python-urllib/*)를 403 으로 막는다(#311).
            "User-Agent": "Cledyu-DR-Approval/1.0 (+https://cledyu.com)",
        },
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=10) as resp:  # noqa: S310
        resp.read()

    # 반환하지 않는다 — SFN 은 SendTaskSuccess 를 기다린다.
    return {"approvalId": approval_id}
