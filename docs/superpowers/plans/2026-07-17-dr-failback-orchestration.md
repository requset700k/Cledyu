# DR Failback 오케스트레이션 구현 계획

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 온프렘 하트비트 복귀를 자동 감지해 Discord 승인 게이트를 거쳐 failover를 정반대로 되돌리는(DNS 원복 + EKS hot teardown + DR 데이터 폐기) failback 오케스트레이션을 구현한다.

**Architecture:** failover 파이프라인(EventBridge→trigger Lambda→SFN→CodeBuild/Lambda→notify)을 대칭으로 미러링. 트리거는 push 하트비트 알람 OK 전환(previousState 필터 없음) + `/cledyu-dr/failover/active` SSM 게이트(실행이름을 failover-id에서 파생해 중복 트리거 멱등). 실행 SFN(approach B = AWS 레벨 정리, bastion 불요): 승인→DNS원복→노드0(aws-sdk)→CleanupOrphans Lambda(노드 강제종료+ALB·EBS·ENI·SG·GuardDuty 삭제)→hot teardown(CodeBuild, `eks_dr_active=false`, 17-target)→고아검증→플래그클리어→알림. **온프렘 DB 복원은 관리자 몫**(운영 CNPG bootstrap=import fail-safe, S3 recovery 미포함 — PVC 생존이면 재기동, 완전소실이면 복구 매니페스트 선-적용), AWS 자동화는 닿지 않고 승인 게이트가 확인 지점.

**Tech Stack:** Terraform(aws provider, us-east-1+ap-northeast-2), AWS Step Functions(ASL/jsonencode), Lambda(python3.12 + nodejs20), CloudWatch/EventBridge, CodeBuild, SSM, DynamoDB, Discord Bot API.

## Global Constraints

- 검증은 **origin/main 기준** 실측(로컬 브랜치는 최신 아님). 파일 확인 시 `git show origin/main:<path>`.
- **커밋·push는 사용자가 직접 실행** — 계획의 커밋 스텝은 완성된 명령어를 제시만 하고, 대신 실행하지 않는다.
- **커밋 메시지에 Co-Authored-By 금지**, heredoc 금지(`git commit -m` 방식).
- **확인 전 커밋 금지** — 각 태스크 산출물은 사용자 리뷰 후 커밋.
- **terraform: `-target` 필수, 전체 plan/apply 금지** — `infra/terraform/aws`는 tfvars가 없어 전체 평가 시 `enable_public_ingress`/`enable_eks_dr` 기본 false로 게이트 리소스(proxy·public ALB·Route53·warm DR)가 오-destroy된다. 검증은 `terraform validate`(state 무관) + 필요 시 `terraform plan -target=... -var enable_eks_dr=true -var eks_dr_active=true -var enable_public_ingress=true`(읽기 전용).
- **terraform 리소스/변수/출력 변경 시 재생성된 `infra/terraform/aws/README.md`를 함께 커밋**(안 하면 pre-commit `terraform_docs` 훅이 커밋 중단).
- **proxy(`aws_instance.proxy`)·public ALB·`aws_route53_record.public`는 절대 teardown 대상 아님** — DNS 원복의 목적지(온프렘 서빙 경로), `enable_public_ingress` 게이트로 failover와 별개.
- 기존 주석 보존(Edit 사용, Write 전체 재작성 금지).
- 스펙: `docs/superpowers/specs/2026-07-17-dr-failback-orchestration-design.md`.

---

## 파일 구조

**신규:**
- `infra/terraform/aws/dr-orchestration-lambda/failback-trigger/index.py` — push OK 이벤트 → active 게이트 확인 → `dr_failback` SFN 시작
- `infra/terraform/aws/dr-orchestration-lambda/dns-revert/index.py` — api·app·auth → `*-public` ALB alias (dns-switch 미러)
- `infra/terraform/aws/dr-orchestration-lambda/teardown-cleanup/index.py` — approach B: 노드 강제종료 + ALB·EBS·ENI·SG·GuardDuty 직접 삭제(AWS 레벨, bastion 불요)
- `infra/terraform/aws/dr-failback-teardown-buildspec.yml` — `terraform apply eks_dr_active=false` hot teardown(17-target)
- `infra/terraform/aws/dr-failback.tf` — failback 리소스 전부(EventBridge `dr_recovery`, `failback-trigger`·`dns-revert`·`teardown-cleanup` Lambda+IAM, `dr_failback` SFN+IAM)
- 각 Lambda 옆 `test_*.py` — 순수 로직 유닛 테스트

**수정:**
- `dr-orchestration-lambda/notify/index.py` — 성공 메시지 failback-준비 꼬리 제거 + failback 성공/실패 브랜치 추가
- `dr-orchestration-lambda/approval-request/index.py` — `mode="failback"` 분기(스냅샷 없이 단일 버튼)
- `dr-orchestration-lambda/interaction/index.mjs` — 승인 경로 `latestSnapshot` 접근 하드닝(failback은 스냅샷 없음)
- `dr-orchestration.tf` — `dr_failover` SFN ASL에 `MarkFailoverActive` 상태 삽입
- `infra/terraform/aws/README.md` — terraform_docs 재생성

---

## Task 1: notify Lambda — 꼬리 제거 + failback 브랜치

**Files:**
- Modify: `infra/terraform/aws/dr-orchestration-lambda/notify/index.py`
- Test: `infra/terraform/aws/dr-orchestration-lambda/notify/test_notify.py` (create)

**Interfaces:**
- Consumes: SFN이 넘기는 `event = {outcome, detectedAt, approvedAt, alb?, failedState?, dnsSwitched?, dnsReverted?, stdoutTail?, executionArn?}`
- Produces: `_render(event) -> str` 순수 함수(메시지 본문). `handler`가 이걸 호출해 웹훅 POST. `outcome` 허용값: `success`(failover 성공), `failure`(failover 실패, 기존 else), `failback-success`, `failback-failed`.

- [ ] **Step 1: 순수 렌더 함수 추출을 위한 실패 테스트 작성**

`test_notify.py` 생성:

```python
import importlib.util
import pathlib

# index.py 를 모듈로 로드(핸들러 실행 없이 _render 만 검사)
_spec = importlib.util.spec_from_file_location(
    "notify_index", pathlib.Path(__file__).parent / "index.py"
)
notify = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(notify)


def test_failover_success_has_no_failback_prep_tail():
    text = notify._render(
        {"outcome": "success", "detectedAt": "2026-07-17T00:00:00Z",
         "approvedAt": "2026-07-17T00:05:00Z", "alb": "dr-alb.example"}
    )
    assert "✅" in text and "페일오버 완료" in text
    assert "failback 준비" not in text
    assert "backupEnabled" not in text


def test_failback_success_message():
    text = notify._render(
        {"outcome": "failback-success",
         "approvedAt": "2026-07-17T00:00:00Z", "detectedAt": "2026-07-17T00:00:00Z"}
    )
    assert "failback 완료" in text
    assert "온프렘" in text


def test_failback_failed_dns_reverted_flag():
    reverted = notify._render(
        {"outcome": "failback-failed", "failedState": "TeardownHot", "dnsReverted": True}
    )
    not_reverted = notify._render(
        {"outcome": "failback-failed", "failedState": "RevertDNS", "dnsReverted": False}
    )
    assert "온프렘" in reverted
    assert "EKS" in not_reverted
```

- [ ] **Step 2: 테스트 실패 확인**

Run: `cd infra/terraform/aws/dr-orchestration-lambda/notify && python -m pytest test_notify.py -v`
Expected: FAIL — `AttributeError: module 'notify_index' has no attribute '_render'`

- [ ] **Step 3: `_render` 추출 + 꼬리 제거 + failback 브랜치 추가**

`index.py`의 `handler` 안 메시지 조립부를 `_render(event)`로 옮긴다. 기존 `success`/`else` 텍스트는 유지하되 **success 브랜치에서 아래 꼬리 3줄을 삭제**:

```
"**다음 할 일 — failback 준비:**\n"
"`postgres-cnpg-dr/values.yaml`·`keycloak-pg-dr/values.yaml` 의 `backupEnabled: false → true` PR.\n"
"안 켜면 DR-창 쓰기가 S3 에 안 남아 **failback 이 anchor 없이 실패**합니다.\n"
f"런북: {RUNBOOK}"
```

`_render` 최종 형태(기존 로직 보존 + 신규 분기):

```python
def _render(event):
    now = datetime.now(UTC).isoformat()
    outcome = event.get("outcome")

    if outcome == "success":
        return (
            "✅ **DR 페일오버 완료**\n"
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
            f"소요: {_mins(event.get('approvedAt'), now)}"
            + (f"\n{orphan}" if orphan else "")
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
            f"```\n{_diagnosis(event.get('stdoutTail'))[-1200:]}\n```\n"
            "**롤백하지 않았습니다** — 여기까지는 그대로 있습니다.\n"
            f"{dns_line}"
        )

    # 기존 failover 실패(else) — 원문 보존
    failed = event.get("failedState", "?")
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
```

`handler`는 텍스트 조립을 `_render` 호출로 대체:

```python
def handler(event, context):
    text = _render(event)
    req = urllib.request.Request(  # noqa: S310
        _webhook_url(),
        data=json.dumps({"content": text[:1900]}).encode("utf-8"),
        headers={
            "Content-Type": "application/json",
            "User-Agent": "Cledyu-DR-Notify/1.0 (+https://cledyu.com)",
        },
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=10) as resp:  # noqa: S310
        resp.read()
    return {"notified": True}
```

- [ ] **Step 4: 테스트 통과 확인**

Run: `cd infra/terraform/aws/dr-orchestration-lambda/notify && python -m pytest test_notify.py -v`
Expected: PASS (3 passed)

- [ ] **Step 5: 커밋(사용자 실행)**

```bash
git add infra/terraform/aws/dr-orchestration-lambda/notify/index.py infra/terraform/aws/dr-orchestration-lambda/notify/test_notify.py
git commit -m "feat(dr): notify에 failback 브랜치 추가 + failover 완료 메시지 꼬리 제거"
```

---

## Task 2: approval-request Lambda — failback 모드

**Files:**
- Modify: `infra/terraform/aws/dr-orchestration-lambda/approval-request/index.py`
- Test: `infra/terraform/aws/dr-orchestration-lambda/approval-request/test_approval_request.py` (create)

**Interfaces:**
- Consumes: `event = {taskToken, input: {mode?}}`. `mode` ∈ {없음(failover 실재해), `"test"`, `"failback"`}.
- Produces: `_build_message(approval_id, is_test, mode, snapshots) -> dict`(Discord 메시지). failback이면 `snapshots=None`, String Select 없이 단일 버튼. custom_id는 `dr-approve:{id}` 재사용(interaction 공용). DDB 항목은 failback이면 `latestSnapshot` 미기록.

- [ ] **Step 1: 메시지 빌더 순수 함수 실패 테스트**

`test_approval_request.py` 생성:

```python
import importlib.util
import pathlib

_spec = importlib.util.spec_from_file_location(
    "approval_index", pathlib.Path(__file__).parent / "index.py"
)
ar = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(ar)


def _button_types(msg):
    return [c["components"][0]["type"] for c in msg["components"]]


def test_failover_message_has_select_and_button():
    msg = ar._build_message("abc123", is_test=False, mode=None,
                            snapshots=["vault/a.snap", "vault/b.snap"])
    # ActionRow 2개: String Select(3) + Button(2)
    assert _button_types(msg) == [3, 2]
    assert "dr-approve:abc123" in msg["components"][1]["components"][0]["custom_id"]


def test_failback_message_single_button_no_select():
    msg = ar._build_message("fb01", is_test=False, mode="failback", snapshots=None)
    # ActionRow 1개: Button(2)만
    assert _button_types(msg) == [2]
    btn = msg["components"][0]["components"][0]
    assert btn["custom_id"] == "dr-approve:fb01"
    assert "failback" in msg["content"].lower() or "온프렘" in msg["content"]
```

- [ ] **Step 2: 실패 확인**

Run: `cd infra/terraform/aws/dr-orchestration-lambda/approval-request && python -m pytest test_approval_request.py -v`
Expected: FAIL — `AttributeError: ... '_build_message'`

- [ ] **Step 3: `_build_message` 추출 + failback 분기, handler 분기**

`handler`의 메시지 조립(`body`, `options`, `message`)을 `_build_message`로 추출하고 failback 분기를 넣는다:

```python
def _build_message(approval_id, is_test, mode, snapshots):
    if mode == "failback":
        body = (
            "♻️ **DR failback 승인 요청**\n"
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
                            "label": "♻️ DR failback 승인",
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
            {"type": 1, "components": [{
                "type": 3, "custom_id": f"dr-snap:{approval_id}",
                "placeholder": "Vault 스냅샷 시점", "options": options,
            }]},
            {"type": 1, "components": [{
                "type": 2, "style": 4 if not is_test else 2,
                "label": "🧪 테스트 승인" if is_test else "🔴 DR 페일오버 승인",
                "custom_id": f"dr-approve:{approval_id}",
            }]},
        ],
    }
```

`handler` 수정 — mode 판정 + failback이면 스냅샷 조회/기록 생략:

```python
    payload = event.get("input") or {}
    mode = payload.get("mode") if isinstance(payload, dict) else None
    is_test = mode == "test"
    is_failback = mode == "failback"

    approval_id = uuid.uuid4().hex[:16]
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
```

(이후 Discord POST 부분은 그대로 — `message` 를 그대로 보낸다.)

- [ ] **Step 4: 테스트 통과 확인**

Run: `cd infra/terraform/aws/dr-orchestration-lambda/approval-request && python -m pytest test_approval_request.py -v`
Expected: PASS (2 passed)

- [ ] **Step 5: 커밋(사용자 실행)**

```bash
git add infra/terraform/aws/dr-orchestration-lambda/approval-request/index.py infra/terraform/aws/dr-orchestration-lambda/approval-request/test_approval_request.py
git commit -m "feat(dr): approval-request에 failback 모드(스냅샷 없이 단일 승인 버튼)"
```

---

## Task 3: interaction Lambda — failback 승인 하드닝

**Files:**
- Modify: `infra/terraform/aws/dr-orchestration-lambda/interaction/index.mjs`

**Interfaces:**
- Consumes: failback 승인은 DDB 항목에 `latestSnapshot`이 **없다**(Task 2). 기존 승인 경로 `got.Item.snapshot?.S ?? got.Item.latestSnapshot.S` 는 failback에서 `latestSnapshot` undefined → `.S` 접근 시 TypeError.
- Produces: 하드닝된 승인 경로. `snapshot`이 없으면 `null`을 SFN output으로. failback SFN은 이 필드를 무시.

- [ ] **Step 1: node 문법 확인용 baseline**

Run: `node --check infra/terraform/aws/dr-orchestration-lambda/interaction/index.mjs`
Expected: 종료코드 0(현재 문법 정상)

- [ ] **Step 2: `latestSnapshot` 접근 하드닝**

`dr-approve` 경로의 다음 줄:

```javascript
  const snapshot = got.Item.snapshot?.S ?? got.Item.latestSnapshot.S;
```

를 optional chaining으로 바꿔 failback(스냅샷 없음)에서도 안전하게:

```javascript
  // failover 는 latestSnapshot(또는 드롭다운 snapshot)을 담지만, failback 항목엔 스냅샷이 없다.
  // 없으면 null — failback SFN 은 output.snapshot 을 쓰지 않는다.
  const snapshot = got.Item.snapshot?.S ?? got.Item.latestSnapshot?.S ?? null;
```

- [ ] **Step 3: 문법 재확인**

Run: `node --check infra/terraform/aws/dr-orchestration-lambda/interaction/index.mjs`
Expected: 종료코드 0

- [ ] **Step 4: 커밋(사용자 실행)**

```bash
git add infra/terraform/aws/dr-orchestration-lambda/interaction/index.mjs
git commit -m "fix(dr): interaction 승인 경로 latestSnapshot 접근 하드닝(failback 스냅샷 없음)"
```

---

## Task 4: failback-trigger Lambda (신규)

**Files:**
- Create: `infra/terraform/aws/dr-orchestration-lambda/failback-trigger/index.py`
- Test: `infra/terraform/aws/dr-orchestration-lambda/failback-trigger/test_failback_trigger.py`

**Interfaces:**
- Consumes: EventBridge `CloudWatch Alarm State Change`(push 알람 ALARM→OK) `event = {id, detail:{alarmName, state:{timestamp}}}`. 환경변수 `SFN_REGION`, `STATE_MACHINE_ARN`, `ACTIVE_PARAM=/cledyu-dr/failover/active`.
- Produces: `should_trigger(active_param_present: bool) -> bool` 순수 게이트. `handler`가 SSM 조회 후 게이트 통과 시 `dr_failback` SFN을 event id 멱등으로 시작.

- [ ] **Step 1: 게이트 로직 실패 테스트**

`test_failback_trigger.py` 생성:

```python
import importlib.util
import pathlib

_spec = importlib.util.spec_from_file_location(
    "fb_trigger", pathlib.Path(__file__).parent / "index.py"
)
t = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(t)


def test_gate_open_only_when_failover_active():
    assert t.should_trigger("arn:aws:states:...:execution:dr-failover:e1") is True
    assert t.should_trigger(None) is False


def test_exec_name_deterministic_from_failover_id():
    a = t.exec_name("arn:aws:states:...:execution:dr-failover:e1")
    b = t.exec_name("arn:aws:states:...:execution:dr-failover:e1")
    c = t.exec_name("arn:aws:states:...:execution:dr-failover:e2")
    assert a == b                                   # 같은 failover → 같은 이름(중복 트리거 멱등)
    assert a != c                                   # 다른 failover → 다른 이름
    assert a.startswith("failback-") and len(a) <= 80
```

- [ ] **Step 2: 실패 확인**

Run: `cd infra/terraform/aws/dr-orchestration-lambda/failback-trigger && python -m pytest test_failback_trigger.py -v`
Expected: FAIL — 파일/함수 없음

- [ ] **Step 3: 구현**

`index.py` 생성(failover-trigger 미러 + active 게이트):

```python
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
_ssm = boto3.client("ssm")  # 이 Lambda 는 us-east-1, 파라미터도 us-east-1(감지 스택과 동일)


def should_trigger(active_value):
    """failover 활성 플래그(값=failover 실행 id)가 있을 때만 failback 을 건다."""
    return bool(active_value)


def exec_name(active_value):
    """실행 이름을 failover 실행 id 에서 결정적으로 파생(≤80자 SFN 제약)."""
    return "failback-" + hashlib.sha1(active_value.encode()).hexdigest()[:32]


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
```

- [ ] **Step 4: 통과 확인 + 문법 검사**

Run: `cd infra/terraform/aws/dr-orchestration-lambda/failback-trigger && python -m pytest test_failback_trigger.py -v && python -m py_compile index.py`
Expected: PASS (1 passed), py_compile 종료코드 0

- [ ] **Step 5: 커밋(사용자 실행)**

```bash
git add infra/terraform/aws/dr-orchestration-lambda/failback-trigger/
git commit -m "feat(dr): failback-trigger Lambda(push OK + active 게이트 → failback SFN)"
```

---

## Task 5: dns-revert Lambda (신규)

**Files:**
- Create: `infra/terraform/aws/dr-orchestration-lambda/dns-revert/index.py`

**Interfaces:**
- Consumes: 입력 없음(파라미터 불요 — 온프렘 ALB는 이름으로 조회). 환경변수 없음(dns-switch와 동일하게 상수).
- Produces: `{"alb": <public-alb-dns>, "records": ["api","app","auth"]}`. SFN이 `ResultPath="$.dns"`로 받음(RevertDNS 통과 여부 = `$.dns.alb` 존재).

- [ ] **Step 1: dns-switch 대비 구조 확인(참조)**

Run: `git show origin/main:infra/terraform/aws/dr-orchestration-lambda/dns-switch/index.py | head -30`
Expected: fail-closed 패턴(ALB 조회→WAF 확인→zone 확인→UPSERT) 참조용 표시

- [ ] **Step 2: 구현(dns-switch 미러, 온프렘 `*-public` ALB로 UPSERT)**

`index.py` 생성:

```python
"""RevertDNS — api·app·auth 를 온프렘 공개 ALB(`*-public`)로 되돌린다(failback).

dns-switch 의 대칭. 다른 점:
  · 대상 ALB 를 SSM 파라미터가 아니라 **이름(`*-public`)으로 조회**한다(온프렘 서빙 ALB 는 상시 존재).
  · alias 는 public-ingress.tf 정의와 동일하게 **EvaluateTargetHealth=True**(온프렘 프록시 헬시 반영).
  · **온프렘 프록시 타깃그룹이 healthy 일 때만 UPSERT**(fail-closed) — 온프렘이 아직 서빙 못 하면
    DNS 를 EKS 에 둔 채 멈춘다(승인 게이트가 1차 확인, 이건 2차 안전망).
"""

import boto3

_elb = boto3.client("elbv2")
_r53 = boto3.client("route53")

HOSTS = ["api", "app", "auth"]
DOMAIN = "cledyu.com"
PUBLIC_ALB_SUFFIX = "-public"  # aws_lb.public: name = "${name_prefix}-public"


def _find_zone():
    for z in _r53.list_hosted_zones_by_name(DNSName=DOMAIN)["HostedZones"]:
        if z["Name"].rstrip(".") == DOMAIN and not z["Config"].get("PrivateZone"):
            return z["Id"]
    raise RuntimeError(f"공개 hosted zone 없음: {DOMAIN} — DNS 를 건드리지 않고 멈춘다")


def _find_public_alb():
    for lb in _elb.describe_load_balancers()["LoadBalancers"]:
        if lb["LoadBalancerName"].endswith(PUBLIC_ALB_SUFFIX):
            return lb
    raise RuntimeError(f"온프렘 공개 ALB(*{PUBLIC_ALB_SUFFIX})를 못 찾음 — public-ingress 미배포?")


def _proxy_healthy(lb):
    """ALB 의 타깃그룹 중 하나라도 healthy 타깃이 있으면 온프렘 프록시 도달 가능."""
    tgs = _elb.describe_target_groups(LoadBalancerArn=lb["LoadBalancerArn"])["TargetGroups"]
    for tg in tgs:
        health = _elb.describe_target_health(TargetGroupArn=tg["TargetGroupArn"])
        if any(t["TargetHealth"]["State"] == "healthy" for t in health["TargetHealthDescriptions"]):
            return True
    return False


def handler(event, context):
    lb = _find_public_alb()
    if not _proxy_healthy(lb):
        raise RuntimeError("온프렘 프록시 타깃이 healthy 가 아님 — 온프렘 서빙 미준비, DNS 원복 중단")

    zone = _find_zone()
    changes = [
        {
            "Action": "UPSERT",
            "ResourceRecordSet": {
                "Name": f"{h}.{DOMAIN}",
                "Type": "A",
                "AliasTarget": {
                    "HostedZoneId": lb["CanonicalHostedZoneId"],
                    "DNSName": lb["DNSName"],
                    "EvaluateTargetHealth": True,
                },
            },
        }
        for h in HOSTS
    ]
    _r53.change_resource_record_sets(HostedZoneId=zone, ChangeBatch={"Changes": changes})
    return {"alb": lb["DNSName"], "records": HOSTS}
```

- [ ] **Step 3: 문법 검사**

Run: `python -m py_compile infra/terraform/aws/dr-orchestration-lambda/dns-revert/index.py`
Expected: 종료코드 0

- [ ] **Step 4: 커밋(사용자 실행)**

```bash
git add infra/terraform/aws/dr-orchestration-lambda/dns-revert/
git commit -m "feat(dr): dns-revert Lambda(api·app·auth → 온프렘 *-public ALB, fail-closed)"
```

---

## Task 6: teardown buildspec (17-target) + CleanupOrphans Lambda (신규)

**Files:**
- Create: `infra/terraform/aws/dr-failback-teardown-buildspec.yml`
- Create: `infra/terraform/aws/dr-orchestration-lambda/teardown-cleanup/index.py`
- Test: `infra/terraform/aws/dr-orchestration-lambda/teardown-cleanup/test_teardown_cleanup.py`

**Interfaces:**
- Produces: teardown buildspec(`dr_failover_tf`가 `BuildspecOverride`로 실행, **17-target**). CleanupOrphans Lambda `dr_teardown_cleanup`(SFN이 DrainNodes 뒤 호출, AWS API로 ALB·EBS·ENI·SG·GuardDuty 직접 삭제). `is_orphan_eni`/`is_k8s_sg` 순수 술어.

- [ ] **Step 1: teardown buildspec 작성 (드릴-검증 17-target, `eks_dr_active=false`)**

`dr-failback-teardown-buildspec.yml` 생성:

```yaml
version: 0.2

# DR failback hot 회수 — dr_failback SFN 의 TeardownHot 이 codebuild:startBuild.sync +
# BuildspecOverride 로 dr_failover_tf 프로젝트에서 실행. failover buildspec 의 정반대(eks_dr_active=false).
#
# ⚠️ -target 17개 필수(dr-eks-bootstrap.md §failback 드릴-검증 목록과 동일). tfvars 가 .gitignore 라
#    CodeBuild 체크아웃에 없어 -target 없으면 enable_public_ingress 기본 false 로 proxy·public ALB·Route53
#    destroy. enable_eks_dr=true 생략 시 warm 컨트롤플레인까지 destroy.
# ⚠️ 노드 desired 는 모듈 ignore_changes → 여기 아님. SFN DrainNodes(aws-sdk)가 0 + 강제종료.

phases:
  install:
    runtime-versions:
      python: 3.12
    commands:
      - curl -fsSL --retry 5 https://releases.hashicorp.com/terraform/${TF_VERSION}/terraform_${TF_VERSION}_linux_amd64.zip -o /tmp/tf.zip
      - unzip -q /tmp/tf.zip -d /usr/local/bin && terraform version
  build:
    commands:
      - cd infra/terraform/aws
      - terraform init -input=false -lock-timeout=5m
      - |
        terraform apply -input=false -auto-approve -lock-timeout=5m \
          -var enable_eks_dr=true -var eks_dr_active=false -var eks_dr_node_desired=0 \
          -var enable_public_ingress=true \
          -target=module.eks_dr_vpc -target=module.eks_dr -target=module.eks_dr_ebs_csi_irsa \
          -target=module.eks_dr_alb_irsa -target=aws_security_group.eks_dr_endpoints \
          -target=aws_security_group.eks_dr_bastion -target=module.eks_dr_endpoints \
          -target=module.eks_dr_vault_unseal_irsa -target=aws_iam_policy.eks_dr_vault_unseal \
          -target=aws_iam_policy.eks_dr_cnpg_restore -target=module.eks_dr_cnpg_restore_irsa \
          -target=aws_iam_role.eks_dr_bastion -target=aws_iam_role_policy_attachment.eks_dr_bastion_ssm \
          -target=aws_iam_role_policy.eks_dr_bastion_describe -target=aws_iam_role_policy.eks_dr_bastion_vault_restore \
          -target=aws_iam_instance_profile.eks_dr_bastion -target=aws_instance.eks_dr_bastion
```

- [ ] **Step 2: CleanupOrphans 순수 술어 실패 테스트**

`test_teardown_cleanup.py` 생성:

```python
import importlib.util
import pathlib

_spec = importlib.util.spec_from_file_location(
    "cleanup", pathlib.Path(__file__).parent / "index.py"
)
c = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(c)


def test_orphan_eni_predicate():
    assert c.is_orphan_eni({"Status": "available", "Description": "aws-K8S-i-0abc"}) is True
    assert c.is_orphan_eni({"Status": "in-use", "Description": "aws-K8S-i-0abc"}) is False   # 붙어있으면 skip
    assert c.is_orphan_eni({"Status": "available", "Description": "ELB app/xyz"}) is False    # ALB/기타 ENI skip


def test_k8s_sg_predicate():
    assert c.is_k8s_sg({"GroupName": "k8s-traffic-abc123"}) is True
    assert c.is_k8s_sg({"GroupName": "cledyu-dr-bastion-sg"}) is False
```

- [ ] **Step 3: 실패 확인**

Run: `cd infra/terraform/aws/dr-orchestration-lambda/teardown-cleanup && python -m pytest test_teardown_cleanup.py -v`
Expected: FAIL — 파일/함수 없음

- [ ] **Step 4: CleanupOrphans Lambda 구현**

`index.py` 생성:

```python
"""CleanupOrphans — EKS 가 out-of-band 로 만든 AWS 리소스를 직접 삭제(approach B, 2026-07-16 드릴 검증).

SFN ScaleToZero(nodegroup desired 0) 뒤 호출. 노드를 **강제 종료**하고(PDB graceful drain 15분 매달림
회피, desired=0 이라 ASG 재생성 없음), 볼륨/ENI 가 available 될 때까지 bounded 대기 후, LB 컨트롤러·
ebs-csi 가 죽어 재생성 없는 상태에서 AWS API 로 직접 삭제한다(bastion·kubectl 불요). 멱등(이미 없으면
skip). 대상: EC2 노드 · ALB+TG · k8s-* SG · EBS(cluster 태그) · aws-K8S-* ENI · GuardDuty 엔드포인트.
"""

import os
import time

import boto3

_eks = boto3.client("eks")
_elb = boto3.client("elbv2")
_ec2 = boto3.client("ec2")

CLUSTER = os.environ["CLUSTER_NAME"]  # cledyu-dr


def _running_node_ids(vpc):
    r = _ec2.describe_instances(Filters=[
        {"Name": "vpc-id", "Values": [vpc]},
        {"Name": f"tag:kubernetes.io/cluster/{CLUSTER}", "Values": ["owned"]},
        {"Name": "instance-state-name", "Values": ["pending", "running", "stopping", "stopped"]},
    ])
    return [i["InstanceId"] for res in r["Reservations"] for i in res["Instances"]]


def is_orphan_eni(eni):
    """DR VPC 내 available + CNI 보조 ENI(aws-K8S-*)만 삭제 대상(붙어있는 것·ALB ENI 제외)."""
    return eni.get("Status") == "available" and str(eni.get("Description", "")).startswith("aws-K8S-")


def is_k8s_sg(sg):
    """LB 컨트롤러가 만든 ALB 보안그룹(k8s-*)."""
    return str(sg.get("GroupName", "")).startswith("k8s-")


def _vpc_id():
    return _eks.describe_cluster(name=CLUSTER)["cluster"]["resourcesVpcConfig"]["vpcId"]


def handler(event, context):
    vpc = _vpc_id()
    deleted = {"ec2": [], "alb": [], "tg": [], "ebs": [], "eni": [], "sg": [], "vpce": []}

    # 0) 노드 강제 종료 (desired=0 은 SFN ScaleToZero 가 이미 함 → ASG 재생성 없음).
    #    PDB 로 graceful drain 이 15분 매달리는 것 회피(2026-07-16 드릴 실측).
    ids = _running_node_ids(vpc)
    if ids:
        _ec2.terminate_instances(InstanceIds=ids)
        deleted["ec2"] = ids
    # 볼륨이 detach→available 될 때까지 bounded 대기(최대 ~5분). 안 되면 잔여는 VerifyNoOrphans 가 경고.
    for _ in range(20):
        vols = _ec2.describe_volumes(Filters=[
            {"Name": f"tag:kubernetes.io/cluster/{CLUSTER}", "Values": ["owned"]},
        ])["Volumes"]
        if not _running_node_ids(vpc) and all(v["State"] == "available" for v in vols):
            break
        time.sleep(15)

    # 1) ALB + 타깃그룹 — [NEW-1] LB(listener)를 먼저 지워야 TG 참조가 해제돼 삭제된다.
    #    TG 를 먼저 지우면 listener 가 forward 로 참조 중이라 ResourceInUse. 또 LB 삭제 후엔
    #    describe_target_groups(LoadBalancerArn=...)로 못 찾으니 ARN 을 LB 삭제 전에 수집한다.
    tg_arns = []
    for lb in _elb.describe_load_balancers()["LoadBalancers"]:
        if lb.get("VpcId") != vpc:
            continue
        for tg in _elb.describe_target_groups(LoadBalancerArn=lb["LoadBalancerArn"])["TargetGroups"]:
            tg_arns.append(tg["TargetGroupArn"])
        _elb.delete_load_balancer(LoadBalancerArn=lb["LoadBalancerArn"])
        deleted["alb"].append(lb["LoadBalancerArn"])
    for arn in tg_arns:  # listener 사라진 뒤 삭제(짧은 in-use 창은 재시도로 흡수)
        for _ in range(6):
            try:
                _elb.delete_target_group(TargetGroupArn=arn)
                deleted["tg"].append(arn)
                break
            except _elb.exceptions.ResourceInUseException:
                time.sleep(5)

    # 2) EBS (cluster 태그, available) — EKS 데이터 실제 폐기(vault·CNPG·kafka 전부 일괄)
    vols = _ec2.describe_volumes(Filters=[
        {"Name": f"tag:kubernetes.io/cluster/{CLUSTER}", "Values": ["owned"]},
        {"Name": "status", "Values": ["available"]},
    ])["Volumes"]
    for v in vols:
        _ec2.delete_volume(VolumeId=v["VolumeId"])
        deleted["ebs"].append(v["VolumeId"])

    # 3) 고아 ENI (available, aws-K8S-*)
    for eni in _ec2.describe_network_interfaces(
        Filters=[{"Name": "vpc-id", "Values": [vpc]}]
    )["NetworkInterfaces"]:
        if is_orphan_eni(eni):
            _ec2.delete_network_interface(NetworkInterfaceId=eni["NetworkInterfaceId"])
            deleted["eni"].append(eni["NetworkInterfaceId"])

    # 4) k8s-* SG — [NEW-3] ALB 삭제 후 그 ENI 가 detach 돼야 SG 삭제 가능. detach 는 몇 초 걸리므로
    #    재시도로 대기(1회만 시도하면 SG 가 누수됨).
    for sg in _ec2.describe_security_groups(
        Filters=[{"Name": "vpc-id", "Values": [vpc]}]
    )["SecurityGroups"]:
        if is_k8s_sg(sg):
            for _ in range(6):
                try:
                    _ec2.delete_security_group(GroupId=sg["GroupId"])
                    deleted["sg"].append(sg["GroupId"])
                    break
                except _ec2.exceptions.ClientError:
                    time.sleep(5)

    # 5) GuardDuty 엔드포인트 (~$20/mo, 다음 failover 에 자동 재생성 → 삭제 안전).
    #    terraform 관리 엔드포인트(module.eks_dr_endpoints)는 TeardownHot 이 지우므로 여기선 guardduty 만.
    for ep in _ec2.describe_vpc_endpoints(
        Filters=[{"Name": "vpc-id", "Values": [vpc]}]
    )["VpcEndpoints"]:
        if "guardduty-data" in ep.get("ServiceName", ""):
            _ec2.delete_vpc_endpoints(VpcEndpointIds=[ep["VpcEndpointId"]])
            deleted["vpce"].append(ep["VpcEndpointId"])

    return {"deleted": deleted, "vpc": vpc}
```

- [ ] **Step 5: 테스트 통과 + 문법 검사**

Run: `cd infra/terraform/aws/dr-orchestration-lambda/teardown-cleanup && python -m pytest test_teardown_cleanup.py -v && python -m py_compile index.py`
Expected: PASS (2 passed), py_compile 종료코드 0

- [ ] **Step 6: buildspec yaml lint**

Run: `python -c "import yaml; yaml.safe_load(open('infra/terraform/aws/dr-failback-teardown-buildspec.yml')); print('yaml ok')"`
Expected: `yaml ok`

- [ ] **Step 7: 커밋(사용자 실행)**

```bash
git add infra/terraform/aws/dr-failback-teardown-buildspec.yml infra/terraform/aws/dr-orchestration-lambda/teardown-cleanup/
git commit -m "feat(dr): failback teardown buildspec(17-target) + CleanupOrphans Lambda(AWS 레벨 고아 정리)"
```

---

## Task 7: dr-failback.tf — 배선(트리거·규칙·dns-revert·SFN·IAM)

**Files:**
- Create: `infra/terraform/aws/dr-failback.tf`
- Modify: `infra/terraform/aws/README.md`(terraform_docs 재생성)

**Interfaces:**
- Consumes: 기존 리소스 참조 — `aws_cloudwatch_metric_alarm.push`, `aws_lambda_function.dr_failover_trigger`(패턴 미러), `aws_lambda_function.{dr_approval_request,dr_notify}`, `aws_codebuild_project.dr_failover_tf`, `aws_dynamodb_table.dr_approvals`, `var.{name_prefix,region,dr_detection_armed,dr_orchestration_armed,eks_dr_node_max}`, `local.pub`, `local.eks_dr_name`. (approach B: bastion SM 미참조.)
- Produces: `aws_sfn_state_machine.dr_failback`, `aws_lambda_function.{dr_failback_trigger,dr_dns_revert,dr_teardown_cleanup}`, `aws_cloudwatch_event_rule.dr_recovery`.

> **참고**: 이 태스크는 terraform HCL이 대부분이라 TDD 대신 `terraform validate`(state 무관 문법·참조 검증) + 타깃 plan을 검증 사이클로 쓴다. 실제 동작 검증은 라이브 드릴(Task 9).

- [ ] **Step 1: `dr_failback_trigger`·`dr_dns_revert` Lambda + IAM (dr-failback.tf 상단)**

`dr-failback.tf` 생성, 두 Lambda부터. failover-trigger/dns-switch의 IAM·archive·log_group·depends_on 패턴을 그대로 미러:

```hcl
# ═══════════════════════════════════════════════════════════════════════════
# DR Failback 오케스트레이션 — failover(dr-orchestration.tf)의 정반대.
# 트리거: push 하트비트 OK 복귀 + /cledyu-dr/failover/active 게이트.
# 실행: 승인 → DNS 원복 → DR 데이터 폐기 → 노드0 → hot teardown → 플래그 클리어 → 알림.
# ═══════════════════════════════════════════════════════════════════════════

# ── failback-trigger Lambda (us-east-1, push OK 이벤트 수신) ──
data "archive_file" "dr_failback_trigger" {
  type        = "zip"
  source_file = "${path.module}/dr-orchestration-lambda/failback-trigger/index.py"
  output_path = "${path.module}/dr-orchestration-lambda/failback-trigger/failback-trigger.zip"
}

resource "aws_iam_role" "dr_failback_trigger" {
  name               = "${var.name_prefix}-dr-failback-trigger"
  assume_role_policy = data.aws_iam_policy_document.dr_lambda_assume.json
}

data "aws_iam_policy_document" "dr_failback_trigger" {
  statement {
    sid       = "ReadActiveFlag"
    actions   = ["ssm:GetParameter"]
    resources = ["arn:aws:ssm:us-east-1:${data.aws_caller_identity.current.account_id}:parameter/cledyu-dr/failover/active"]
  }
  statement {
    sid       = "StartFailback"
    actions   = ["states:StartExecution"]
    resources = [aws_sfn_state_machine.dr_failback.arn]
  }
  statement {
    sid       = "Logs"
    actions   = ["logs:CreateLogStream", "logs:PutLogEvents"]
    resources = [
      "arn:aws:logs:us-east-1:*:log-group:/aws/lambda/${var.name_prefix}-dr-failback-trigger",
      "arn:aws:logs:us-east-1:*:log-group:/aws/lambda/${var.name_prefix}-dr-failback-trigger:*",
    ]
  }
}

resource "aws_iam_role_policy" "dr_failback_trigger" {
  name   = "${var.name_prefix}-dr-failback-trigger"
  role   = aws_iam_role.dr_failback_trigger.id
  policy = data.aws_iam_policy_document.dr_failback_trigger.json
}

resource "aws_cloudwatch_log_group" "dr_failback_trigger" {
  provider          = aws.use1
  name              = "/aws/lambda/${var.name_prefix}-dr-failback-trigger"
  retention_in_days = 30
}

resource "aws_lambda_function" "dr_failback_trigger" {
  provider         = aws.use1 # push 알람 이벤트가 us-east-1
  depends_on       = [aws_cloudwatch_log_group.dr_failback_trigger, aws_iam_role_policy.dr_failback_trigger]
  function_name    = "${var.name_prefix}-dr-failback-trigger"
  filename         = data.archive_file.dr_failback_trigger.output_path
  source_code_hash = data.archive_file.dr_failback_trigger.output_base64sha256
  handler          = "index.handler"
  runtime          = "python3.12"
  role             = aws_iam_role.dr_failback_trigger.arn
  timeout          = 30
  environment {
    variables = {
      SFN_REGION        = var.region
      STATE_MACHINE_ARN = aws_sfn_state_machine.dr_failback.arn
      ACTIVE_PARAM      = "/cledyu-dr/failover/active"
    }
  }
}

# ── dns-revert Lambda (ap-northeast-2) ──
data "archive_file" "dr_dns_revert" {
  type        = "zip"
  source_file = "${path.module}/dr-orchestration-lambda/dns-revert/index.py"
  output_path = "${path.module}/dr-orchestration-lambda/dns-revert/dns-revert.zip"
}

resource "aws_iam_role" "dr_dns_revert" {
  name               = "${var.name_prefix}-dr-dns-revert"
  assume_role_policy = data.aws_iam_policy_document.dr_lambda_assume.json
}

data "aws_iam_policy_document" "dr_dns_revert" {
  statement {
    sid       = "DescribeAlb"
    actions   = ["elasticloadbalancing:DescribeLoadBalancers", "elasticloadbalancing:DescribeTargetGroups", "elasticloadbalancing:DescribeTargetHealth"]
    resources = ["*"] # Describe* 는 리소스 한정 미지원(AWS 문서)
  }
  statement {
    sid       = "ListZones"
    actions   = ["route53:ListHostedZonesByName"]
    resources = ["*"]
  }
  statement {
    sid       = "ChangeRecords"
    actions   = ["route53:ChangeResourceRecordSets"]
    resources = ["arn:aws:route53:::hostedzone/*"]
  }
  statement {
    sid       = "Logs"
    actions   = ["logs:CreateLogStream", "logs:PutLogEvents"]
    resources = [
      "arn:aws:logs:${var.region}:*:log-group:/aws/lambda/${var.name_prefix}-dr-dns-revert",
      "arn:aws:logs:${var.region}:*:log-group:/aws/lambda/${var.name_prefix}-dr-dns-revert:*",
    ]
  }
}

resource "aws_iam_role_policy" "dr_dns_revert" {
  name   = "${var.name_prefix}-dr-dns-revert"
  role   = aws_iam_role.dr_dns_revert.id
  policy = data.aws_iam_policy_document.dr_dns_revert.json
}

resource "aws_cloudwatch_log_group" "dr_dns_revert" {
  name              = "/aws/lambda/${var.name_prefix}-dr-dns-revert"
  retention_in_days = 30
}

resource "aws_lambda_function" "dr_dns_revert" {
  depends_on       = [aws_cloudwatch_log_group.dr_dns_revert, aws_iam_role_policy.dr_dns_revert]
  function_name    = "${var.name_prefix}-dr-dns-revert"
  filename         = data.archive_file.dr_dns_revert.output_path
  source_code_hash = data.archive_file.dr_dns_revert.output_base64sha256
  handler          = "index.handler"
  runtime          = "python3.12"
  role             = aws_iam_role.dr_dns_revert.arn
  timeout          = 60
}

# ── CleanupOrphans Lambda (ap-northeast-2): 노드 종료 + AWS 레벨 고아 삭제 ──
data "archive_file" "dr_teardown_cleanup" {
  type        = "zip"
  source_file = "${path.module}/dr-orchestration-lambda/teardown-cleanup/index.py"
  output_path = "${path.module}/dr-orchestration-lambda/teardown-cleanup/teardown-cleanup.zip"
}

resource "aws_iam_role" "dr_teardown_cleanup" {
  name               = "${var.name_prefix}-dr-teardown-cleanup"
  assume_role_policy = data.aws_iam_policy_document.dr_lambda_assume.json
}

data "aws_iam_policy_document" "dr_teardown_cleanup" {
  statement {
    sid     = "DiscoverAndDelete"
    actions = [
      "eks:DescribeCluster",
      "ec2:DescribeInstances", "ec2:TerminateInstances",
      "ec2:DescribeVolumes", "ec2:DeleteVolume",
      "ec2:DescribeNetworkInterfaces", "ec2:DeleteNetworkInterface",
      "ec2:DescribeSecurityGroups", "ec2:DeleteSecurityGroup",
      "ec2:DescribeVpcEndpoints", "ec2:DeleteVpcEndpoints",
      "elasticloadbalancing:DescribeLoadBalancers", "elasticloadbalancing:DescribeTargetGroups",
      "elasticloadbalancing:DeleteLoadBalancer", "elasticloadbalancing:DeleteTargetGroup",
    ]
    resources = ["*"] # 대상은 DR VPC 필터로 코드에서 한정(Describe/Delete 리소스레벨 제약)
  }
  statement {
    sid       = "Logs"
    actions   = ["logs:CreateLogStream", "logs:PutLogEvents"]
    resources = [
      "arn:aws:logs:${var.region}:*:log-group:/aws/lambda/${var.name_prefix}-dr-teardown-cleanup",
      "arn:aws:logs:${var.region}:*:log-group:/aws/lambda/${var.name_prefix}-dr-teardown-cleanup:*",
    ]
  }
}

resource "aws_iam_role_policy" "dr_teardown_cleanup" {
  name   = "${var.name_prefix}-dr-teardown-cleanup"
  role   = aws_iam_role.dr_teardown_cleanup.id
  policy = data.aws_iam_policy_document.dr_teardown_cleanup.json
}

resource "aws_cloudwatch_log_group" "dr_teardown_cleanup" {
  name              = "/aws/lambda/${var.name_prefix}-dr-teardown-cleanup"
  retention_in_days = 30
}

resource "aws_lambda_function" "dr_teardown_cleanup" {
  depends_on       = [aws_cloudwatch_log_group.dr_teardown_cleanup, aws_iam_role_policy.dr_teardown_cleanup]
  function_name    = "${var.name_prefix}-dr-teardown-cleanup"
  filename         = data.archive_file.dr_teardown_cleanup.output_path
  source_code_hash = data.archive_file.dr_teardown_cleanup.output_base64sha256
  handler          = "index.handler"
  runtime          = "python3.12"
  role             = aws_iam_role.dr_teardown_cleanup.arn
  timeout          = 600 # [R4] 노드 종료 + 볼륨 available 대기(내부 폴링) 흡수
  environment {
    variables = { CLUSTER_NAME = local.eks_dr_name }
  }
}
```

- [ ] **Step 2: `dr_failback` SFN + 실행 롤 (dr-failback.tf 계속)**

SFN 롤은 승인·dns-revert·cleanup Lambda 호출 · eks scale · codebuild · ssm · ec2/elbv2 describe(verify) · notify 권한. ASL은 approach B 순서:

```hcl
# ── dr_failback SFN 실행 롤 ──
resource "aws_iam_role" "dr_failback_sfn" {
  name = "${var.name_prefix}-dr-failback-sfn"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "states.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

data "aws_iam_policy_document" "dr_failback_sfn" {
  statement {
    sid       = "InvokeLambdas"
    actions   = ["lambda:InvokeFunction"]
    resources = [
      aws_lambda_function.dr_approval_request.arn,
      aws_lambda_function.dr_dns_revert.arn,
      aws_lambda_function.dr_teardown_cleanup.arn, # approach B: 노드종료+고아정리
      aws_lambda_function.dr_notify.arn,
    ]
  }
  statement {
    sid       = "Teardown"
    actions   = ["codebuild:StartBuild", "codebuild:BatchGetBuilds", "codebuild:StopBuild"]
    resources = [aws_codebuild_project.dr_failover_tf.arn]
  }
  statement {
    sid       = "ScaleToZero" # SFN 이 노드그룹 desired 0(강제종료는 cleanup Lambda 가 함)
    actions   = ["eks:ListNodegroups", "eks:UpdateNodegroupConfig", "eks:DescribeNodegroup", "eks:DescribeUpdate"]
    resources = ["*"]
  }
  statement {
    sid       = "VerifyOrphans" # [R8] 잔존 ALB/EBS 확인
    actions   = ["elasticloadbalancing:DescribeLoadBalancers", "ec2:DescribeVolumes"]
    resources = ["*"]
  }
  statement {
    sid       = "ClearFlags"
    actions   = ["ssm:DeleteParameter"]
    resources = ["arn:aws:ssm:${var.region}:${data.aws_caller_identity.current.account_id}:parameter/cledyu-dr/failover/*"]
  }
  # .sync(codebuild) 통합용 EventBridge 관리형 규칙
  statement {
    sid       = "SyncRules"
    actions   = ["events:PutTargets", "events:PutRule", "events:DescribeRule"]
    resources = ["arn:aws:events:${var.region}:${data.aws_caller_identity.current.account_id}:rule/StepFunctions*"]
  }
}

resource "aws_iam_role_policy" "dr_failback_sfn" {
  name   = "${var.name_prefix}-dr-failback-sfn"
  role   = aws_iam_role.dr_failback_sfn.id
  policy = data.aws_iam_policy_document.dr_failback_sfn.json
}

resource "aws_cloudwatch_log_group" "dr_failback_sfn" {
  name              = "/aws/vendedlogs/states/${var.name_prefix}-dr-failback"
  retention_in_days = 30
}

locals {
  # 각 상태 Catch → NotifyFailbackFailed. failedState 를 static 주입(failover dr_catch 패턴).
  fb_catch = { for s in ["RevertDNS", "ListNodegroup", "ScaleToZero", "CleanupOrphans", "TeardownHot", "VerifyNoOrphans", "ClearFlags"] :
    s => [{
      ErrorEquals = ["States.ALL"]
      ResultPath  = "$.error"
      Next        = "Mark_${s}_Failed"
    }]
  }
}

resource "aws_sfn_state_machine" "dr_failback" {
  name       = "${var.name_prefix}-dr-failback"
  role_arn   = aws_iam_role.dr_failback_sfn.arn
  depends_on = [aws_iam_role_policy.dr_failback_sfn]

  logging_configuration {
    log_destination        = "${aws_cloudwatch_log_group.dr_failback_sfn.arn}:*"
    include_execution_data = false
    level                  = "ALL"
  }

  definition = jsonencode({
    Comment = "DR failback(approach B) — 승인→DNS원복→노드0→AWS레벨 고아정리(ALB·EBS·ENI·GuardDuty)→hot teardown→고아검증→플래그클리어→알림"
    StartAt = "RequestApproval"
    States = merge({
      # [1] 승인(approval-request 재사용, mode=failback → 단일 버튼)
      RequestApproval = {
        Type       = "Task"
        Resource   = "arn:aws:states:::lambda:invoke.waitForTaskToken"
        Parameters = {
          FunctionName = aws_lambda_function.dr_approval_request.arn
          Payload = {
            "taskToken.$" = "$$.Task.Token"
            "input.$"     = "$"
          }
        }
        ResultPath = "$.approval"
        Catch      = [{ ErrorEquals = ["States.ALL"], ResultPath = "$.error", Next = "Mark_RequestApproval_Failed" }]
        Next       = "RevertDNS"
      }

      # [2] DNS 원복(→온프렘 *-public ALB) — 맨 앞. 트래픽부터 온프렘으로.
      RevertDNS = {
        Type           = "Task"
        Resource       = "arn:aws:states:::lambda:invoke"
        Parameters     = { FunctionName = aws_lambda_function.dr_dns_revert.arn }
        ResultSelector = { "alb.$" = "$.Payload.alb" }
        ResultPath     = "$.dns"
        Catch          = local.fb_catch["RevertDNS"]
        Next           = "ListNodegroup"
      }

      # [3] 노드그룹 이름 발견(모듈이 이름 변형 가능 → 하드코딩 금지, failover ScaleNodes 미러)
      ListNodegroup = {
        Type           = "Task"
        Resource       = "arn:aws:states:::aws-sdk:eks:listNodegroups"
        Parameters     = { ClusterName = local.eks_dr_name }
        ResultSelector = { "name.$" = "$.Nodegroups[0]" }
        ResultPath     = "$.ng"
        Catch          = local.fb_catch["ListNodegroup"]
        Next           = "ScaleToZero"
      }

      # [4] 노드그룹 desired 0 (강제종료는 CleanupOrphans Lambda 가 함 → ASG 재생성 방지 위해 먼저)
      ScaleToZero = {
        Type     = "Task"
        Resource = "arn:aws:states:::aws-sdk:eks:updateNodegroupConfig"
        Parameters = {
          ClusterName       = local.eks_dr_name
          "NodegroupName.$" = "$.ng.name"
          ScalingConfig     = { MinSize = 0, MaxSize = var.eks_dr_node_max, DesiredSize = 0 }
        }
        ResultPath = null
        Catch      = local.fb_catch["ScaleToZero"]
        Next       = "WaitScaleApplied"
      }

      # [4.5] [NEW-2] ASG desired=0 이 반영될 짧은 대기 — 그 전에 CleanupOrphans 가 강제종료하면
      # ASG 가 종료한 노드를 재생성할 수 있다(desired 가 아직 N). Wait 는 실패 불가라 Catch 불요.
      WaitScaleApplied = {
        Type    = "Wait"
        Seconds = 30
        Next    = "CleanupOrphans"
      }

      # [5] AWS 레벨 고아 정리(approach B): 노드 강제종료 + 볼륨 available 대기 + ALB·EBS·ENI·SG·GuardDuty 삭제
      CleanupOrphans = {
        Type       = "Task"
        Resource   = "arn:aws:states:::lambda:invoke"
        Parameters = { FunctionName = aws_lambda_function.dr_teardown_cleanup.arn }
        ResultPath = "$.cleanup"
        Catch      = local.fb_catch["CleanupOrphans"]
        Next       = "TeardownHot"
      }

      # [6] hot teardown(CodeBuild + BuildspecOverride, eks_dr_active=false, 17-target)
      TeardownHot = {
        Type     = "Task"
        Resource = "arn:aws:states:::codebuild:startBuild.sync"
        Parameters = {
          ProjectName       = aws_codebuild_project.dr_failover_tf.name
          BuildspecOverride = "infra/terraform/aws/dr-failback-teardown-buildspec.yml"
        }
        ResultPath = null
        Catch      = local.fb_catch["TeardownHot"]
        Next       = "VerifyNoOrphans"
      }

      # [7] [R8] 고아 검증 — 잔존 cluster-태그 EBS 확인(있으면 경고 첨부)
      VerifyNoOrphans = {
        Type     = "Task"
        Resource = "arn:aws:states:::aws-sdk:ec2:describeVolumes"
        Parameters = {
          Filters = [{ Name = "tag:kubernetes.io/cluster/${local.eks_dr_name}", Values = ["owned"] }]
        }
        ResultSelector = { "volumes.$" = "$.Volumes" }
        ResultPath     = "$.verify"
        Catch          = local.fb_catch["VerifyNoOrphans"]
        Next           = "OrphanCheck"
      }
      OrphanCheck = {
        Type    = "Choice"
        Choices = [{ Variable = "$.verify.volumes[0]", IsPresent = true, Next = "MarkOrphanWarning" }]
        Default = "MarkClean"
      }
      # 두 분기 모두 $.warn.orphanWarning 을 세팅(notify payload JSONPath 가 항상 존재하도록 — 없으면 States.Runtime).
      MarkOrphanWarning = {
        Type       = "Pass"
        Result     = { orphanWarning = "⚠️ 고아 의심: cluster 태그 EBS 잔존 — 콘솔 확인 필요" }
        ResultPath = "$.warn"
        Next       = "ClearFlags"
      }
      MarkClean = {
        Type       = "Pass"
        Result     = { orphanWarning = "" }
        ResultPath = "$.warn"
        Next       = "ClearFlags"
      }

      # [8] 플래그 클리어
      ClearFlags = {
        Type     = "Task"
        Resource = "arn:aws:states:::aws-sdk:ssm:deleteParameter"
        Parameters = { Name = "/cledyu-dr/failover/active" }
        Catch = concat([{
          ErrorEquals = ["Ssm.ParameterNotFoundException"]
          ResultPath  = null
          Next        = "NotifyFailbackComplete"
        }], local.fb_catch["ClearFlags"])
        ResultPath = null
        Next       = "NotifyFailbackComplete"
      }

      # [9] 완료 알림 ([R7] RTO/RPO 라벨 없음, orphanWarning 있으면 첨부)
      NotifyFailbackComplete = {
        Type     = "Task"
        Resource = "arn:aws:states:::lambda:invoke"
        Parameters = {
          FunctionName = aws_lambda_function.dr_notify.arn
          Payload = {
            outcome           = "failback-success"
            "approvedAt.$"    = "$.approval.approvedAt"
            "orphanWarning.$" = "$.warn.orphanWarning"
          }
        }
        Retry = [{ ErrorEquals = ["States.ALL"], IntervalSeconds = 5, MaxAttempts = 3, BackoffRate = 2.0 }]
        End   = true
      }
    },
    # 각 단계 실패 마커 상태(dnsReverted 지상진실 = $.dns.alb 존재) → NotifyFailbackFailed
    { for s in ["RequestApproval", "RevertDNS", "ListNodegroup", "ScaleToZero", "CleanupOrphans", "TeardownHot", "VerifyNoOrphans", "ClearFlags"] :
      "Mark_${s}_Failed" => {
        Type       = "Pass"
        Result     = { failedState = s }
        ResultPath = "$.failed"
        Next       = "NotifyFailbackFailed"
      }
    },
    {
      NotifyFailbackFailed = {
        Type     = "Task"
        Resource = "arn:aws:states:::lambda:invoke"
        Parameters = {
          FunctionName = aws_lambda_function.dr_notify.arn
          Payload = {
            outcome         = "failback-failed"
            "failedState.$" = "$.failed.failedState"
            "dnsReverted.$" = "States.IsPresent($.dns.alb)" # RevertDNS 통과 여부 지상진실
            "executionArn.$" = "$$.Execution.Id"
          }
        }
        Retry = [{ ErrorEquals = ["States.ALL"], IntervalSeconds = 5, MaxAttempts = 3, BackoffRate = 2.0 }]
        End   = true
      }
    })
  })
}
```

> **approach B 확정**: 정리는 bastion 없이 AWS 레벨. 노드그룹 이름은 `ListNodegroup`(listNodegroups→`$.ng.name`)로 발견 → `ScaleToZero`(desired 0) → `CleanupOrphans` Lambda가 강제종료+볼륨대기+ALB/EBS/ENI/SG/GuardDuty 삭제. `terminate_instances`가 desired=0 뒤라 ASG 재생성 없음(origin/main·2026-07-16 드릴 실측 패턴).

- [ ] **Step 3: EventBridge `dr_recovery` 규칙 + 타깃 + permission (dr-failback.tf 마무리)**

```hcl
# ── push OK 복귀 → failback-trigger (dr_disaster 규칙 미러) ──
resource "aws_cloudwatch_event_rule" "dr_recovery" {
  count       = (local.pub == 1 && var.dr_detection_armed && var.dr_orchestration_armed) ? 1 : 0
  provider    = aws.use1
  name        = "${var.name_prefix}-dr-recovery"
  description = "push 하트비트 ALARM→OK(온프렘 회복) → failback 트리거"
  event_pattern = jsonencode({
    source      = ["aws.cloudwatch"]
    detail-type = ["CloudWatch Alarm State Change"]
    detail = {
      alarmName = [aws_cloudwatch_metric_alarm.push.alarm_name]
      state     = { value = ["OK"] }
      # [R3] previousState 필터 없음 — ALARM→OK 뿐 아니라 INSUFFICIENT_DATA→OK 도 잡아야 회복을 놓치지 않는다.
      # 평상시 push 는 steady OK 라 →OK 전이 자체가 없으므로 오발 없음. 진짜 필터는 trigger 의 active 게이트.
    }
  })
}

resource "aws_cloudwatch_event_target" "dr_recovery" {
  count     = (local.pub == 1 && var.dr_detection_armed && var.dr_orchestration_armed) ? 1 : 0
  provider  = aws.use1
  rule      = aws_cloudwatch_event_rule.dr_recovery[0].name
  target_id = "failback-trigger"
  arn       = aws_lambda_function.dr_failback_trigger.arn
}

resource "aws_lambda_permission" "dr_recovery" {
  count         = (local.pub == 1 && var.dr_detection_armed && var.dr_orchestration_armed) ? 1 : 0
  provider      = aws.use1
  statement_id  = "AllowEventBridgeInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.dr_failback_trigger.function_name
  principal     = "events.amazonaws.com"
  source_arn    = aws_cloudwatch_event_rule.dr_recovery[0].arn
}
```

- [ ] **Step 4: terraform validate + 타깃 plan(읽기 전용)**

Run:
```bash
cd infra/terraform/aws && terraform validate
```
Expected: `Success! The configuration is valid.` (SFN ASL jsonencode·참조 오류 없음)

추가 확인(approach B): (1) `BuildspecOverride` 경로가 CodeBuild 소스 루트 기준(`infra/terraform/aws/dr-failback-teardown-buildspec.yml`)인지, (2) `$.verify.volumes[0]` Choice `IsPresent`가 빈 배열에서 false로 평가되는지(정상 = 고아 없음 → MarkClean), (3) SFN IAM에 cleanup Lambda invoke·eks scale·ec2 describeVolumes·elbv2 describe가 다 있는지. `terraform validate` 재실행.

Expected: valid.

- [ ] **Step 5: terraform_docs README 재생성**

Run:
```bash
cd infra/terraform/aws && terraform-docs markdown table --output-file README.md .
```
Expected: README.md 갱신(신규 리소스 반영). terraform-docs 미설치면 pre-commit 훅이 커밋 시 자동 생성하므로 스킵 가능.

- [ ] **Step 6: 커밋(사용자 실행)**

```bash
git add infra/terraform/aws/dr-failback.tf infra/terraform/aws/README.md
git commit -m "feat(dr): failback 배선(dr_recovery 규칙·failback-trigger·dns-revert·dr_failback SFN)"
```

---

## Task 8: dr_failover SFN — MarkFailoverActive 플래그 세팅

**Files:**
- Modify: `infra/terraform/aws/dr-orchestration.tf`(`dr_failover` SFN ASL)

**Interfaces:**
- Consumes: 기존 `VerifyServing` 상태(현재 `Next = "NotifyComplete"`).
- Produces: `/cledyu-dr/failover/active` SSM 파라미터(값=executionArn) — failback-trigger 게이트가 이걸 읽음.

- [ ] **Step 1: VerifyServing → MarkFailoverActive → NotifyComplete 삽입**

`dr-orchestration.tf`의 `VerifyServing` 상태에서 `Next = "NotifyComplete"`를 `Next = "MarkFailoverActive"`로 바꾸고, `NotifyComplete` 앞에 신규 상태 추가:

```hcl
      # ── [12.5] failover 활성 플래그 — failback 트리거의 게이트 ──
      # VerifyServing 통과(= failover 정상 완료) 후에만 세팅. failback-trigger 가 이 파라미터가
      # 있을 때만 발화 → 부분 실패·평상시 하트비트 깜빡임이 failback 을 유발하지 않는다.
      # dr_failback SFN 의 ClearFlags 가 failback 완료 시 삭제한다.
      MarkFailoverActive = {
        Type     = "Task"
        Resource = "arn:aws:states:::aws-sdk:ssm:putParameter"
        Parameters = {
          Name      = "/cledyu-dr/failover/active"
          "Value.$" = "$$.Execution.Id"
          Type      = "String"
          Overwrite = true
        }
        ResultPath = null
        # 플래그 세팅 실패로 완료 알림을 막지 않는다 — failover 는 이미 성공. 로깅만.
        Catch = [{ ErrorEquals = ["States.ALL"], ResultPath = null, Next = "NotifyComplete" }]
        Next  = "NotifyComplete"
      }
```

⚠️ **[최종리뷰 C1 정정] `dr_sfn` 롤은 원래 `ssm:DeleteParameter`만 있었다(line 330 `ClearAlbParam`). MarkFailoverActive의 putParameter를 위해 그 statement actions에 `ssm:PutParameter`를 추가해야 한다** — 안 하면 AccessDenied→Catch 삼킴→failback 미무장(validate·테스트는 통과, 실 드릴로만 발견).

- [ ] **Step 2: terraform validate**

Run: `cd infra/terraform/aws && terraform validate`
Expected: `Success! The configuration is valid.`

- [ ] **Step 3: 커밋(사용자 실행)**

```bash
git add infra/terraform/aws/dr-orchestration.tf
git commit -m "feat(dr): failover 완료 시 /cledyu-dr/failover/active 세팅(failback 게이트)"
```

---

## Task 9: 라이브 드릴 검증(실 동작 게이트)

> 정적 검증(validate/pytest)은 배선만 본다. **실제 동작은 라이브 드릴로만 확정**된다([[feedback_full_verification_sweep]]). 이 태스크는 사용자가 DR 창/시연 리허설에서 수행하며, 계획은 체크리스트를 제공한다.

**Files:** 없음(운영 검증)

- [ ] **Step 1: 배포**(사용자) — `-target`으로 failback 리소스만 apply:
```bash
cd infra/terraform/aws
terraform apply -var enable_eks_dr=true -var eks_dr_active=true -var enable_public_ingress=true \
  -var dr_detection_armed=true -var dr_orchestration_armed=true \
  -target=aws_lambda_function.dr_failback_trigger \
  -target=aws_lambda_function.dr_dns_revert \
  -target=aws_sfn_state_machine.dr_failback \
  -target=aws_cloudwatch_event_rule.dr_recovery \
  -target=aws_cloudwatch_event_target.dr_recovery \
  -target=aws_lambda_permission.dr_recovery
```

- [ ] **Step 2: 트리거 게이트 확인** — active 플래그 없이 push 알람 OK 전환 유도 → failback-trigger가 `not-failed-over`로 no-op인지 CloudWatch 로그 확인.
- [ ] **Step 3: failover 후 플래그 확인** — failover 드릴 완료 후 `aws ssm get-parameter --name /cledyu-dr/failover/active` 존재 확인.
- [ ] **Step 4: failback 발화** — 온프렘 하트비트 복귀 시뮬레이션 → Discord에 단일 승인 버튼 메시지 도착 확인.
- [ ] **Step 5: 승인 → 실행** — 승인 버튼 → SFN 실행: RevertDNS(DNS→`*-public` ALB) → ListNodegroup→ScaleToZero(desired 0) → CleanupOrphans(노드 강제종료 + ALB·EBS·ENI·SG·GuardDuty 삭제) → TeardownHot(NAT·endpoints·bastion destroy, **proxy·public ALB 잔존 확인**) → VerifyNoOrphans → ClearFlags → ✅ failback 완료 알림.
- [ ] **Step 6: 사후 확인** — `dig auth.cledyu.com`가 온프렘 ALB, EKS warm(컨트롤플레인 ACTIVE·노드 0) 잔존, `/cledyu-dr/failover/active` 삭제됨, **고아 0**(`elbv2 describe-load-balancers`·`ec2 describe-volumes tag:...cluster/cledyu-dr`·`describe-security-groups k8s-*` 전부 비어야), proxy EC2·public ALB·GuardDuty(다음 failover에 재생성) 무손상.
  - **[NEW-2] 노드 재생성 없음 확인**: CleanupOrphans 강제종료 후 30초~수분 관찰 → 노드그룹 desired=0 유지, 새 인스턴스 안 뜸(WaitScaleApplied 30s가 충분한지 실측 — 부족하면 Wait 상향).
  - **[NEW-1] 실측**: CloudWatch 로그에서 CleanupOrphans가 ALB→TG 순서로 삭제(ResourceInUse 없이) 확인.
  - **[NEW-4] 실패 재시도**: 만약 failback SFN이 실패로 끝나면, 같은 하트비트 이벤트로는 재트리거 안 됨(exec_name 동일 90일) → **수동 재시도 = SFN을 다른 이름으로 콘솔/CLI start**.
- [ ] **Step 7(드릴 전용)**: 알람 disarm `aws cloudwatch disable-alarm-actions --region us-east-1 --alarm-names <name_prefix>-dr-disaster`(재과금 방지). **실 failback은 생략**(온프렘 healthy면 알람 OK라 재발화 안 함).

---

## Self-Review (계획 작성자 체크)

**Spec coverage:**
- §4.1 활성 플래그 세팅 → Task 8, 클리어 → Task 7(ClearFlags) ✅
- §4.2 트리거(push OK, previousState 없음[R3] + active 게이트[R2 exec-name]) → Task 4 + Task 7(dr_recovery) ✅
- §5 승인 failback 모드 → Task 2, interaction 하드닝 → Task 3 ✅
- §6.1 RevertDNS(gate-0 fail-closed) → Task 5 + Task 7(SFN) ✅
- §6.2 DrainNodes+CleanupOrphans(approach B AWS 레벨: 강제종료·ALB·EBS·ENI·SG·GuardDuty) → Task 6(Lambda) + Task 7(SFN) ✅
- §6.4 TeardownHot(재사용+override, 17-target[R5]) → Task 6(buildspec) + Task 7(SFN) ✅
- §6.5 VerifyNoOrphans[R8]+ClearFlags+Notify(RTO/RPO 없음[R7]) → Task 7 + Task 1(notify 브랜치) ✅
- §7 기존 코드 수정 5곳 → Task 1·2·3·7·8 ✅
- §8 신규 리소스(trigger·dns-revert·**teardown-cleanup**·SFN·규칙) → Task 6·7 ✅
- R9(온프렘 복원=관리자 몫) → spec §2, 자동화 무변경(닿지 않음) ✅

**적대적 재리뷰(approach B) 반영:** NEW-1(ALB→TG 삭제 순서 버그, LB 먼저+TG ARN 선수집) 수정 · NEW-2(ScaleToZero 직후 강제종료 ASG 재생성 레이스 → WaitScaleApplied 30s) 추가 · NEW-3(k8s-* SG detach 재시도) 보강 · NEW-4/5(exec_name 재시도 제약·BuildspecOverride 리비전) 드릴 노트화.

**남은 갭(Task 9 라이브 드릴 실측):** WaitScaleApplied 30s 충분성(노드 무재생성) · GuardDuty ServiceName 필터 문자열 · 볼륨-available 폴링 상한(20×15s) 튜닝 · `k8s-*` SG detach 재시도 횟수 · `BuildspecOverride` 경로.

**Type consistency:** `outcome`(`failback-success`/`failback-failed`)이 notify(_render)와 SFN 일치 ✅. custom_id `dr-approve:{id}` 공용 ✅. `$.warn.orphanWarning`을 두 분기(MarkOrphanWarning/MarkClean) 모두 세팅해 notify payload JSONPath 항상 존재 ✅. `exec_name`(failback-trigger)이 active 값에서 결정적 파생 → 중복 트리거 멱등 ✅.
