"""RevertDNS — api·app·auth 를 온프렘 공개 ALB(`*-public`)로 되돌린다(failback).

dns-switch 의 대칭. 다른 점:
  · 대상 ALB 를 SSM 파라미터가 아니라 **이름(`*-public`)으로 조회**한다(온프렘 서빙 ALB 는 상시 존재).
  · alias 는 public-ingress.tf 정의와 동일하게 **EvaluateTargetHealth=True**(온프렘 프록시 헬시 반영).
  · **온프렘 프록시 타깃그룹이 healthy 일 때만 UPSERT**(fail-closed) — 온프렘이 아직 서빙 못 하면
    DNS 를 EKS 에 둔 채 멈춘다(승인 게이트가 1차 확인, 이건 2차 안전망).
"""

import os

import boto3

_elb = boto3.client("elbv2")
_r53 = boto3.client("route53")

HOSTS = ["api", "app", "auth"]
DOMAIN = "cledyu.com"
# 온프렘 공개 ALB 의 **정확한** 이름(TF 가 "${name_prefix}-public" 를 주입). ALB 이름은 리전 내 유일하므로
# 접미(-public) 매칭 대신 정확 일치로 오-선택을 원천 차단한다(다른 *-public ALB 가 생겨도 안전).
PUBLIC_ALB_NAME = os.environ["PUBLIC_ALB_NAME"]


def _find_zone():
    for z in _r53.list_hosted_zones_by_name(DNSName=DOMAIN)["HostedZones"]:
        if z["Name"].rstrip(".") == DOMAIN and not z["Config"].get("PrivateZone"):
            return z["Id"]
    raise RuntimeError(f"공개 hosted zone 없음: {DOMAIN} — DNS 를 건드리지 않고 멈춘다")


def _find_public_alb():
    for lb in _elb.describe_load_balancers()["LoadBalancers"]:
        if lb["LoadBalancerName"] == PUBLIC_ALB_NAME:
            return lb
    raise RuntimeError(f"온프렘 공개 ALB({PUBLIC_ALB_NAME})를 못 찾음 — public-ingress 미배포?")


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
        raise RuntimeError(
            "온프렘 프록시 타깃이 healthy 가 아님 — 온프렘 서빙 미준비, DNS 원복 중단"
        )

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
