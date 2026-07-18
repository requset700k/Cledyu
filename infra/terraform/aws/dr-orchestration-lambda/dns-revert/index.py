"""RevertDNS — api·app·auth 를 온프렘 공개 ALB(`*-public`)로 되돌린다(failback).

dns-switch 의 대칭. 다른 점:
  · 대상 ALB 를 SSM 파라미터가 아니라 **이름(`*-public`)으로 조회**한다(온프렘 서빙 ALB 는 상시 존재).
  · alias 는 public-ingress.tf 정의와 동일하게 **EvaluateTargetHealth=True**(온프렘 프록시 헬시 반영).
  · **온프렘 프록시 타깃그룹이 healthy 일 때만 UPSERT**(fail-closed) — 온프렘이 아직 서빙 못 하면
    DNS 를 EKS 에 둔 채 멈춘다(승인 게이트가 1차 확인, 이건 2차 안전망).
"""

import http.client
import logging
import os
import socket
import ssl

import boto3

_log = logging.getLogger()

_elb = boto3.client("elbv2")
_r53 = boto3.client("route53")

HOSTS = ["api", "app", "auth"]
DOMAIN = "cledyu.com"
# 온프렘 서빙 딥체크 경로 — Host 헤더로 Caddy @public 라우팅을 강제해 ALB→Caddy→tailnet→Traefik→백엔드
# 전 체인을 실제로 태운다. /healthz(정적 200)로는 못 잡는 "프록시만 살고 백엔드 죽음"을 5xx/연결실패로 판별.
DEEP_CHECKS = [("auth", "/realms/master"), ("api", "/ready"), ("app", "/")]
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
    """ALB 의 타깃그룹 중 하나라도 healthy 타깃이 있으면 온프렘 프록시 **도달** 가능(생존만 판정)."""
    tgs = _elb.describe_target_groups(LoadBalancerArn=lb["LoadBalancerArn"])["TargetGroups"]
    for tg in tgs:
        health = _elb.describe_target_health(TargetGroupArn=tg["TargetGroupArn"])
        if any(t["TargetHealth"]["State"] == "healthy" for t in health["TargetHealthDescriptions"]):
            return True
    return False


def _probe_status(alb_dns, host, path):
    """공개 ALB(alb_dns)에 TCP 접속하되 **SNI·검증 hostname 은 실제 공개 Host**로 준다.

    그러면 ALB 가 그 Host 의 cert(*.cledyu.com)를 내주고 정식 TLS 검증이 성립한다 — ALB DNS 로 접속하면서도
    검증을 끄지 않는다(미검증 컨텍스트는 S323 + MITM 이 200 을 위조해 잘못 원복시킬 여지). cert 검증 실패는
    ssl.SSLError(=OSError) 로 올라와 호출부에서 '미서빙'으로 처리된다(fail-closed).
    """
    ctx = ssl.create_default_context()
    with (
        socket.create_connection((alb_dns, 443), timeout=5) as raw,
        ctx.wrap_socket(raw, server_hostname=host) as tls,
    ):
        conn = http.client.HTTPConnection(host, timeout=5)
        conn.sock = tls  # 이미 TLS 로 감싼 소켓 주입 → conn 은 재접속 없이 HTTP 만 태운다
        conn.request("GET", path, headers={"Host": host})
        return conn.getresponse().status


def _onprem_serving(alb_dns):
    """공개 ALB 에 **실제 공개 Host + 라우팅 경로**로 요청해 백엔드 체인 서빙을 확인한다.

    ALB listener 는 host 무조건 default forward → Caddy 가 Host 로 @public 라우팅(keycloak-proxy.yaml.tftpl).
    그래서 Host 헤더를 실어 보내면 프록시 너머 Traefik/Keycloak/API 까지 실제로 태운다. 백엔드가 죽어
    있으면 Caddy reverse_proxy 가 5xx(502/503/504)를 뱉는다 → **미서빙**으로 판정(차단).

    ⚠️ 도달 가능성과 서빙을 구분한다(리뷰 P2). public_ingress_allowed_cidrs 를 사무실 IP 등으로 좁히면
    public ALB SG 가 non-VPC Lambda 의 **비고정 egress** 를 막아, 온프렘이 정상이어도 연결이 timeout 난다.
    그때 하드블록하면 승인된 failback 이 RevertDNS 에서 영영 멈춘다 → **도달 불가(연결 실패)는 '미서빙'과
    구분해 판정 보류(통과)** 하고, 게이트는 1차 _proxy_healthy(ELB API, SG 무관)+사람 승인에 맡긴다.
    막는 건 **도달했는데 5xx** 인 경우뿐(프록시만 살고 백엔드 죽음).
    """
    for sub, path in DEEP_CHECKS:
        host = f"{sub}.{DOMAIN}"
        try:
            status = _probe_status(alb_dns, host, path)
        except (OSError, http.client.HTTPException) as e:
            # 연결 자체 실패 = 도달 불가(SG 제한/네트워크) → inconclusive. 막지 않고 통과(다른 게이트 의존).
            return True, f"unreachable({host}{path}:{type(e).__name__}) — 딥체크 보류"
        if status >= 500:
            return False, f"{host}{path}: {status}"
    return True, "ok"


def handler(event, context):
    lb = _find_public_alb()
    # 1차: 프록시 EC2 가 ALB 에 healthy 로 등록됐나(도달 가능한가).
    if not _proxy_healthy(lb):
        raise RuntimeError("온프렘 프록시 타깃이 healthy 가 아님 — 프록시 미도달, DNS 원복 중단")
    # 2차(딥): /healthz 정적 200 너머 실제 백엔드가 서빙하나. 여기가 진짜 안전망 — 이게 없으면 프록시만
    # 살고 Keycloak/API 가 죽어도 원복돼 api·app·auth 를 고장난 온프렘으로 돌린다(P1, 2026-07-18 리뷰).
    ok, why = _onprem_serving(lb["DNSName"])
    if not ok:
        raise RuntimeError(f"온프렘 백엔드 미서빙({why}) — 실 체인 도달 실패, DNS 원복 중단")
    if why != "ok":
        # 도달 불가로 딥체크를 건너뛴 경우(예: ALB SG 가 좁혀짐) — 안전체크 우회를 CloudWatch 에 남긴다.
        _log.warning(
            "딥체크 보류(도달 불가) — %s. _proxy_healthy+승인 게이트에 의존해 원복 진행.", why
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
    resp = _r53.change_resource_record_sets(HostedZoneId=zone, ChangeBatch={"Changes": changes})
    # ChangeId 를 돌려준다 — SFN 이 getChange 로 INSYNC 를 폴링하고 TTL drain 뒤 teardown 하게(resolver 가
    # 옛 EKS ALB 를 캐시한 창에 EKS 를 회수해 단절되는 것 방지, 2026-07-18 리뷰 P2). Id 는 "/change/C..." 라
    # 접두를 떼어 getChange 가 바로 쓰게 한다.
    change_id = resp["ChangeInfo"]["Id"].rsplit("/", 1)[-1]
    return {"alb": lb["DNSName"], "records": HOSTS, "changeId": change_id}
