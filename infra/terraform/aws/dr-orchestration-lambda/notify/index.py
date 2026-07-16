"""[13] NotifyComplete + 모든 상태의 Catch → NotifyFailed 공용.

평문 알림이라 components 가 없다 → **웹훅으로 충분**(approval-request 가 Bot API 를 쓰는 것과 다름).
dr-alert(#310)와 같은 us-east-1 웹훅 시크릿을 읽으므로 **ARN 에서 리전을 파싱**한다 —
Secrets Manager 는 리전 서비스라 ap-northeast-2 클라이언트가 us-east-1 시크릿을 못 찾는다(스펙 §3.3).
"""

import json
import os
import urllib.request
from datetime import UTC, datetime

import boto3

RUNBOOK = "https://github.com/requset700k/Cledyu/blob/main/docs/RUNBOOK/dr-failback.md"


def _webhook_url():
    arn = os.environ["WEBHOOK_SECRET_ARN"]
    sm = boto3.client("secretsmanager", region_name=arn.split(":")[3])
    url = json.loads(sm.get_secret_value(SecretId=arn)["SecretString"])["url"]
    if not url.startswith("https://"):
        raise ValueError("webhook URL must be https")
    return url


def _ts(s):
    """ISO8601 파싱 — 두 출처의 형식이 다르다.

    ⚠️ strptime("%Y-%m-%dT%H:%M:%S.%fZ") 를 쓰면 안 된다(리뷰 지적 C2):
      · detectedAt = CloudWatch 알람 이벤트의 detail.state.timestamp → **"...328+0000"**(Z 아님)
      · approvedAt = interaction Lambda 의 new Date().toISOString()  → "...371Z"
    Z 포맷만 기대하면 detectedAt 에서 ValueError → [13] 이 죽고 Catch 가 NotifyFailed 로 보내
    **13단계를 다 성공하고도 "❌ 실패" 알림**이 간다. 게다가 T4 Step 5 의 테스트 payload 는
    손으로 쓴 "...000000Z" 라 **수동 검증은 통과하고 실재해에서만 터진다.**
    fromisoformat(py3.11+)은 offset·Z 둘 다 파싱한다(실측 확인).
    """
    if not s:
        return None
    try:
        return datetime.fromisoformat(s.replace("Z", "+00:00"))
    except ValueError:
        return None


def _mins(a, b):
    ta, tb = _ts(a), _ts(b)
    if not (ta and tb):
        return "?"
    return f"{(tb - ta).total_seconds() / 60:.0f}분"


def handler(event, context):
    now = datetime.now(UTC).isoformat()
    if event.get("outcome") == "success":
        text = (
            "✅ **DR 페일오버 완료**\n"
            # 두 구간을 나눈다 — 감지→승인은 사람 지연, 승인→서빙이 자동화 RTO 다.
            f"감지→승인: {_mins(event.get('detectedAt'), event.get('approvedAt'))} (사람 지연)\n"
            f"**승인→서빙: {_mins(event.get('approvedAt'), now)} ← 자동화 RTO**\n"
            f"ALB: {event.get('alb', '?')}\n\n"
            "**다음 할 일 — failback 준비:**\n"
            "`postgres-cnpg-dr/values.yaml`·`keycloak-pg-dr/values.yaml` 의 `backupEnabled: false → true` PR.\n"
            "안 켜면 DR-창 쓰기가 S3 에 안 남아 **failback 이 anchor 없이 실패**합니다.\n"
            f"런북: {RUNBOOK}"
        )
    else:
        failed = event.get("failedState", "?")
        # ⚠️ DNS 안내를 **실패 단계로 분기**한다(codex P2). [10] SwitchDNS 가 Route53 UPSERT 를 하므로
        #   그 **이후**([11] RestartApps·[12] VerifyServing) 실패면 DNS 는 이미 **EKS 를 가리킨다.**
        #   무조건 "온프렘을 가리킵니다" 로 보내면 운영자가 트래픽 위치를 오판해 롤백 순서를 잘못 잡는다.
        #   단계 이름은 메인 SM(Task 5)의 State 명 — 스펙 §5 표의 SwitchDNS/RestartApps/VerifyServing 과 일치.
        #   판정은 **allowlist(전환 후 단계)** 로 한다 — 모르는 이름(?, 오타)은 보수적으로 "온프렘"(안전)으로.
        post_dns = failed in ("RestartApps", "VerifyServing", "NotifyComplete")
        dns_line = (
            "⚠️ **DNS 는 이미 EKS 로 전환됐습니다**(SwitchDNS 통과 후 실패) — 사용자는 EKS ALB 로 향합니다.\n"
            "롤백하려면 Route53 을 온프렘으로 되돌리는 것부터(런북 §복귀)."
            if post_dns
            else "DNS 는 아직 온프렘을 가리킵니다 — 트래픽은 안전합니다."
        )
        text = (
            "❌ **DR 페일오버 실패**\n"
            f"실패 단계: `{failed}`\n"
            f"실행: {event.get('executionArn', '?')}\n\n"
            f"```\n{(event.get('stdoutTail') or '')[-1200:]}\n```\n"
            "**롤백하지 않았습니다** — 여기까지 뜬 것은 그대로 있으니 런북으로 이어받으세요.\n"
            f"{dns_line}"
        )

    req = urllib.request.Request(  # noqa: S310
        _webhook_url(),
        data=json.dumps({"content": text[:1900]}).encode("utf-8"),
        headers={
            "Content-Type": "application/json",
            # Discord 는 Cloudflare 뒤라 기본 UA 를 403 으로 막는다(#311).
            "User-Agent": "Cledyu-DR-Notify/1.0 (+https://cledyu.com)",
        },
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=10) as resp:  # noqa: S310
        resp.read()
    return {"notified": True}
