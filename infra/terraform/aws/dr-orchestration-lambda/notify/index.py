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


def _diagnosis(raw):
    """$.error.Cause 를 사람이 읽을 진단문으로 푼다.

    ⚠️ **파싱을 여기서 하는 게 설계다 — ASL 로 하면 안 된다**(스펙 §11.18 (e), 재현함):
    평문 Cause 에 States.StringToJson 을 쓰면 States.Runtime 이 나고, 그건 **어떤 Catch 로도 못 잡으며**,
    Pass 는 애초에 Catch 를 못 단다(스키마 거부: "Field 'Catch' is not supported").
    → 실패 경로에서 실패 = **실패 알림 자체가 무음으로 죽는다.** Python 의 try/except 는 두 형태를 다 삼킨다.

    Cause 가 JSON 인 건 **자식 SM(.sync:2) 실패일 때뿐**이다(§11.18 (b)):
      {"Error":"BastionScriptFailed","Cause":"<label> 실패 — aws logs tail ...","ExecutionArn":...,"Input":...}
    [2] CodeBuild · [2.4]/[2.5] SDK 실패의 Cause 는 **평문**이다
    (예: "Service returned error code ParameterNotFound (Service: Ssm, Status Code: 400, ...)").
    """
    if not raw:
        return "(진단 정보 없음)"
    try:
        d = json.loads(raw)
    except (ValueError, TypeError):
        return raw  # 평문 — 그대로 보여준다
    if not isinstance(d, dict):
        return raw
    # 자식 SM 실패. Cause 엔 CausePath 가 넣은 "<label> 실패 — aws logs tail <그룹> --log-stream-name-prefix
    # <commandId>" 가 들어 있다(§11.18 (d)) → 운영자가 **그대로 복사해 붙이면 로그 전문**이 나온다.
    parts = [p for p in (d.get("Error"), d.get("Cause")) if p]
    if d.get("ExecutionArn"):
        parts.append(f"자식 실행: {d['ExecutionArn']}")
    return "\n".join(parts) if parts else raw


def _render(event):
    now = datetime.now(UTC).isoformat()
    outcome = event.get("outcome")

    if outcome == "success":
        return (
            "✅ **DR 페일오버 완료**\n"
            # 두 구간을 나눈다 — 감지→승인은 사람 지연, 승인→서빙이 자동화 RTO 다.
            f"감지→승인: {_mins(event.get('detectedAt'), event.get('approvedAt'))} (사람 지연)\n"
            f"**승인→서빙: {_mins(event.get('approvedAt'), now)} ← 자동화 RTO**\n"
            f"ALB: {event.get('alb', '?')}"
        )

    if outcome == "failback-success":
        # [R7] failback 은 재해가 아니라 계획된 복귀 → RTO/RPO 라벨 안 붙인다(재해복구 지표 아님).
        orphan = event.get("orphanWarning")  # VerifyNoOrphans 가 채우면 경고 첨부
        return (
            "✅ **DR failback 완료**\n"
            "DNS: 온프렘(`*-public` ALB)\n"
            "EKS: pilot-light warm 으로 회수 · DR 데이터 폐기\n"
            f"소요: {_mins(event.get('approvedAt'), now)}" + (f"\n{orphan}" if orphan else "")
        )

    if outcome == "failback-failed":
        failed = event.get("failedState", "?")
        reverted = bool(event.get("dnsReverted"))
        dns_line = (
            "DNS 는 온프렘으로 원복됐습니다 — 트래픽은 온프렘으로 갑니다."
            if reverted
            else "⚠️ **DNS 가 아직 EKS 를 가리킵니다**(RevertDNS 실패) — 온프렘 서빙 안 됨. 런북 §원복 먼저."
        )
        return (
            "❌ **DR failback 실패**\n"
            f"실패 단계: `{failed}`\n"
            f"실행: {event.get('executionArn', '?')}\n\n"
            # failback SFN 은 stdoutTail 을 안 채운다(CodeBuild/bastion 로그 tail 이 없음) → 빈 진단 블록 생략.
            "**롤백하지 않았습니다** — 여기까지는 그대로 있습니다.\n"
            f"{dns_line}"
        )

    # ⚠️ failedState 는 메인 SM 의 **$.failedStep** 이다 — 각 Task 의 Catch 가 자기 이름을 static 으로
    #   주입한 값이라 진짜 상태 이름(예: "RestoreVault")이 온다.
    #   🔴 **$.error.Error 를 쓰면 안 된다** — .sync:2 가 자식 Error 를 감싸 **"States.TaskFailed"** 가
    #   오고, `.sync` 를 쓰는 모든 상태가 같은 값이다(스펙 §11.18 (b) 실측).
    failed = event.get("failedState", "?")

    # ⚠️ DNS 안내는 **이름 추론이 아니라 페이로드 실물**로 판정한다(스펙 §11.18 (c)).
    #   이전 구현은 allowlist(`failed in ("RestartApps","VerifyServing","NotifyComplete")`)로 분기했는데
    #   **셋 다 영원히 안 맞는 dead code** 였다(위 §11.18 (b) — 실제로 오는 값은 States.TaskFailed).
    #   그래서 [11]·[12] 실패 = **DNS 가 이미 EKS 인데** "온프렘 — 트래픽은 안전합니다" 를 보냈다.
    #   무조건 "온프렘"이던 수정 전보다 나빴다: 틀린 내용에 근거 없는 안심이 붙었다.
    #   → 메인 SM 의 `DnsSwitched?` Choice 가 **$.dns.alb 의 IsPresent**(= [10] SwitchDNS 통과 여부의
    #     지상 진실)로 판정해 이 불리언을 넣어준다. 이름이 아니라 실물이라 상태가 끼어들어도 안 틀린다.
    #   없으면 보수적으로 False("온프렘") — 안전한 쪽이다.
    dns_switched = bool(event.get("dnsSwitched"))
    dns_line = (
        "⚠️ **DNS 는 이미 EKS 로 전환됐습니다**(SwitchDNS 통과 후 실패) — 사용자는 EKS ALB 로 향합니다.\n"
        "롤백하려면 Route53 을 온프렘으로 되돌리는 것부터(런북 §복귀)."
        if dns_switched
        else "DNS 는 아직 온프렘을 가리킵니다 — 트래픽은 안전합니다."
    )
    return (
        "❌ **DR 페일오버 실패**\n"
        f"실패 단계: `{failed}`\n"
        f"실행: {event.get('executionArn', '?')}\n\n"
        f"```\n{_diagnosis(event.get('stdoutTail'))[-1200:]}\n```\n"
        "**롤백하지 않았습니다** — 여기까지 뜬 것은 그대로 있으니 런북으로 이어받으세요.\n"
        f"{dns_line}"
    )


def handler(event, context):
    text = _render(event)
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
