"""[10] SwitchDNS — api·app·auth 를 EKS ALB 로 전환.

ALB 호스트명은 [9] 가 SSM 파라미터에 써둔 값을 읽는다 — non-VPC Lambda 는 private EKS 에 못 닿고,
자식 SM 은 stdout 을 CloudWatch 로 보낼 뿐이므로 이게 유일한 전달 경로다(설계 §5.1.2).
파라미터가 없으면 **즉시 실패**한다 — 폴백·추측 금지. "[9] 가 안 썼다 = ALB 를 못 얻었다"이므로
DNS 를 건드리지 않고 멈추는 것이 옳다(DNS 는 온프렘을 가리킨 채 남아 상태가 안전).
"""

import boto3

_ssm = boto3.client("ssm")
_elb = boto3.client("elbv2")
_waf = boto3.client("wafv2")
_r53 = boto3.client("route53")

HOSTS = ["api", "app", "auth"]
PARAM = "/cledyu-dr/failover/alb-hostname"
DOMAIN = "cledyu.com"
WAF_ACL = "cledyu-lab-public"


def _find_zone():
    """공개 hosted zone 을 이름으로 찾는다 — **일치를 확인하고** 쓴다.

    ⚠️ list_hosted_zones_by_name 의 DNSName 은 필터가 아니라 **정렬 시작점**이다. cledyu.com 존이
    없으면 알파벳순 다음 존이 [0] 으로 돌아오고, 그대로 쓰면 **남의 존에 api·app·auth 를 UPSERT** 한다.
    계획 초안이 [0] 을 그냥 썼다 — 이 모듈의 fail-closed 원칙(파라미터 없으면 즉시 실패)과 어긋난다.
    private 존도 거른다(같은 이름의 split-horizon 존이 있으면 공개 전환이 조용히 내부에만 먹는다).
    """
    for z in _r53.list_hosted_zones_by_name(DNSName=DOMAIN)["HostedZones"]:
        if z["Name"].rstrip(".") == DOMAIN and not z["Config"].get("PrivateZone"):
            return z["Id"]
    raise RuntimeError(f"공개 hosted zone 없음: {DOMAIN} — DNS 를 건드리지 않고 멈춘다")


def handler(event, context):
    alb = _ssm.get_parameter(Name=PARAM)["Parameter"]["Value"]  # 없으면 ParameterNotFound → 실패

    lbs = _elb.describe_load_balancers()["LoadBalancers"]
    # next(...) 의 StopIteration 은 CloudWatch 에 원인 없는 에러만 남긴다 → 명시적으로 판정한다.
    lb = next((x for x in lbs if x["DNSName"] == alb), None)
    if lb is None:
        # [9] 가 쓴 alb-hostname 이 stale — warm etcd 의 Ingress status 호스트명이 이전 사이클 ALB 를
        # 가리키고(그 ALB 는 삭제·재생성돼 DNS 접미가 바뀜) [9] 가 live 검증 없이 그 값을 썼다(2026-07-18 실측).
        # LB 컨트롤러가 그룹 cledyu-dr 로 만드는 앱 ALB 는 이름이 안정적(k8s-cledyudr-*, DR VPC 유일)이므로
        # 이름으로 폴백해 **살아있는** ALB 의 실 DNSName 을 쓴다. (파라미터 존재 자체는 fail-closed 로 유지.)
        lb = next((x for x in lbs if x["LoadBalancerName"].startswith("k8s-cledyudr")), None)
        if lb is not None:
            alb = lb["DNSName"]
    if lb is None:
        raise RuntimeError(f"ALB 를 못 찾음: {alb} — 파라미터 stale + k8s-cledyudr ALB 도 부재")

    # WAF(/metrics 차단)는 api·web values-eks 의 wafv2-acl-arn annotation 으로 ALB 생성과 동시에
    # 자동 연결된다 → 여기선 **붙었는지 확인만**. 안 붙었으면 /metrics 가 조용히 열린다(설계 §6.3).
    acl = _waf.get_web_acl_for_resource(ResourceArn=lb["LoadBalancerArn"]).get("WebACL")
    if not acl or acl["Name"] != WAF_ACL:
        raise RuntimeError(f"WAF 미연결 — values-eks 의 wafv2-acl-arn 이 stale: {acl}")

    zone = _find_zone()
    changes = [
        {
            "Action": "UPSERT",
            "ResourceRecordSet": {
                "Name": f"{h}.{DOMAIN}",
                "Type": "A",
                "AliasTarget": {
                    "HostedZoneId": lb["CanonicalHostedZoneId"],
                    "DNSName": alb,
                    "EvaluateTargetHealth": False,
                },
            },
        }
        for h in HOSTS
    ]
    _r53.change_resource_record_sets(HostedZoneId=zone, ChangeBatch={"Changes": changes})
    return {"alb": alb, "records": HOSTS}
