"""[5] InstallAddons — coredns·ebs-csi 관리형 애드온 멱등 설치.

warm(node0) 에선 이 둘이 Deployment 라 DEGRADED 로 terraform apply 를 블록한다 → cluster_addons 에서
빼두고(eks-dr.tf) 노드가 뜬 뒤 CLI 로 설치한다(런북 Phase 1 (3)).
"""

import contextlib

import boto3

_eks = boto3.client("eks")
CLUSTER = "cledyu-dr"


def _start(name, **kw):
    """설치를 **시작만** 한다 — 기다리지 않는다.

    ⚠️ Lambda 하드 상한은 900s 다. 초안은 애드온 2개를 각각 최대 600s(15s x 40) **순차 대기**해서
    최대 1200s → **타임아웃**이었다([2] 를 CodeBuild 로 만든 바로 그 이유에 다시 걸린 것 — 리뷰 지적 C4).
    → 시작만 하고 ACTIVE 대기는 SFN 의 Wait+Choice 폴링에 맡긴다(자식 SM 과 같은 패턴).
    """
    # failback 이 애드온을 warm 에 남기므로 재-failover 시 create 는 409 로 죽는다 → describe 후 분기.
    try:
        _eks.describe_addon(clusterName=CLUSTER, addonName=name)
        verb = _eks.update_addon
    except _eks.exceptions.ResourceNotFoundException:
        verb = _eks.create_addon
    # ⚠️ ResourceInUseException 은 두 가지 뜻이다(T4 실측 — 초안은 앞의 하나만 알았다):
    #   (a) create 인데 이미 존재함     → 위 describe 분기가 막는다
    #   (b) **update 인데 이미 변경 중** → 여기서 막는다(3회차 start 가 실제로 이걸로 죽었다)
    # (b) 는 [5] 가 재시도되거나 운영자가 실패한 페일오버를 UPDATING 창(~1-3분) 안에 다시 실행하면
    # 난다 — 재해 중 충분히 있을 법한 행동이다. 어느 쪽이든 **설치는 이미 진행 중**이니 성공으로
    # 흘려보낸다. 완료 판정은 check(=SFN 폴링)의 몫이지 여기가 아니다.
    with contextlib.suppress(_eks.exceptions.ResourceInUseException):
        verb(clusterName=CLUSTER, addonName=name, resolveConflicts="OVERWRITE", **kw)


def handler(event, context):
    """action="start" 면 설치 시작, action="check" 면 현재 상태 반환. SFN 이 폴링한다."""
    acct = boto3.client("sts").get_caller_identity()["Account"]
    names = {
        "coredns": {},
        "aws-ebs-csi-driver": {
            "serviceAccountRoleArn": f"arn:aws:iam::{acct}:role/cledyu-dr-ebs-csi"
        },
    }

    if event.get("action") == "start":
        for n, kw in names.items():
            _start(n, **kw)
        return {"started": list(names)}

    # check — 둘 다 ACTIVE 여야 done. CREATE_FAILED 면 즉시 실패시킨다(P1c 가 여기서 드러난다).
    st = {}
    for n in names:
        s = _eks.describe_addon(clusterName=CLUSTER, addonName=n)["addon"]["status"]
        if s in ("CREATE_FAILED", "UPDATE_FAILED", "DEGRADED"):
            raise RuntimeError(f"{n} 애드온 실패: {s} — 고아 ALB webhook(P1c)이 남았는지 확인")
        st[n] = s
    return {"status": st, "done": all(v == "ACTIVE" for v in st.values())}
