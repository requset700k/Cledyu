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
# 온프렘 서빙 딥체크 — Host 헤더로 Caddy @public 라우팅을 강제해 ALB→Caddy→tailnet→Traefik→백엔드 전
# 체인을 실제로 태운다. /healthz(정적 200)로는 못 잡는 "프록시만 살고 백엔드 죽음"을 판별. 각 항은
# (Host, path, 본문에_있어야_하는_문자열|None); 기대 status 는 **2xx/3xx**.
DEEP_CHECKS = [
    # auth 는 실 학습자 realm 복원까지 확인 — /realms/master 는 Keycloak 프로세스만 살아도 200 이라
    # OAuth 가 깨진 온프렘을 통과시킨다. 실 realm(cledyu-learn, config.go)+본문 확인 = failover 게이트
    # (12-verify-serving.sh)와 동일. keycloak 실서빙은 이 체크가 커버하므로 api /ready 는 200 만 본다.
    ("auth", "/realms/cledyu-learn", "cledyu-learn"),
    ("api", "/ready", None),  # labs 로드=ready(200)
    ("app", "/", None),  # web 서빙(2xx/3xx)
]
# ⚠️ User-Agent 필수 — 온프렘 public ALB 의 WAF(cledyu-lab-public)에 AWSManagedRulesCommonRuleSet 이 있어
# **UA 없는 요청을 NoUserAgent 규칙으로 403 차단**한다(2026-07-18 E2E 드릴 실측). http.client 는 기본 UA 가
# 없어 딥체크가 403→fail-closed 로 잘못 막혔다. failover 게이트(12-verify)의 curl 은 UA 를 보내 200 이었다.
UA = "cledyu-dr-failback-healthcheck/1.0"
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


def _probe(alb_dns, host, path):
    """공개 ALB(alb_dns)에 TCP 접속하되 **SNI·검증 hostname 은 실제 공개 Host**로 준다. (status, body) 반환.

    그러면 ALB 가 그 Host 의 cert(*.cledyu.com)를 내주고 정식 TLS 검증이 성립한다 — ALB DNS 로 접속하면서도
    검증을 끄지 않는다(미검증 컨텍스트는 S323 + MITM 이 200 을 위조해 잘못 원복시킬 여지). cert 검증 실패는
    ssl.SSLError(=OSError) 로 올라와 호출부에서 '미서빙'으로 처리된다(fail-closed). body 는 realm 등 본문 확인용 앞부분만.
    """
    ctx = ssl.create_default_context()
    with (
        socket.create_connection((alb_dns, 443), timeout=5) as raw,
        ctx.wrap_socket(raw, server_hostname=host) as tls,
    ):
        conn = http.client.HTTPConnection(host, timeout=5)
        conn.sock = tls  # 이미 TLS 로 감싼 소켓 주입 → conn 은 재접속 없이 HTTP 만 태운다
        conn.request("GET", path, headers={"Host": host, "User-Agent": UA})
        resp = conn.getresponse()
        body = resp.read(4096).decode("utf-8", "replace")
        return resp.status, body


def _onprem_serving(alb_dns):
    """공개 ALB 에 **실제 공개 Host + 라우팅 경로**로 요청해 백엔드 체인 서빙을 확인한다.

    ALB listener 는 host 무조건 default forward → Caddy 가 Host 로 @public 라우팅(keycloak-proxy.yaml.tftpl).
    그래서 Host 헤더를 실어 보내면 프록시 너머 Traefik/Keycloak/API 까지 실제로 태운다.

    ⚠️ **도달 가능성과 서빙을 엄격히 구분한다(리뷰 P2).** 통과(inconclusive)로 삼키는 건 **연결 자체 실패
    (OSError: timeout/refused)** 뿐이다 — public_ingress_allowed_cidrs 를 좁히면 non-VPC Lambda 의 비고정
    egress 가 막혀 온프렘이 정상이어도 timeout 나는데, 이때 하드블록하면 승인된 failback 이 영영 멈춘다.
    그 외는 전부 **fail-closed(차단)**:
      · ssl.SSLError(도달했으나 TLS 깨짐 — cert 만료/호스트 불일치, 사용자가 실제로 보는 오류)
      · http.client.HTTPException(도달했으나 HTTP 프로토콜 오류 — '도달 불가' 아님)
      · **2xx/3xx 아닌 응답**(4xx=Host 라우팅 404/403, 5xx=백엔드 다운)
      · 본문 요구 불충족(auth 는 실 realm 복원 확인 — /realms/cledyu-learn 이 realm 데이터를 실제로 반환하는지)
    도달 불가만 보류하고 나머지는 막으면, 깨진 공개 엔드포인트로 api/app/auth 를 돌리는 상황을 차단한다.
    """
    for sub, path, want in DEEP_CHECKS:
        host = f"{sub}.{DOMAIN}"
        try:
            status, body = _probe(alb_dns, host, path)
        except ssl.SSLError as e:  # ssl.SSLError 는 OSError 하위 → **먼저** 잡는다
            return False, f"{host}{path}: TLS {type(e).__name__}"
        except http.client.HTTPException as e:  # 도달했으나 HTTP 오류 → 차단(도달불가 아님)
            return False, f"{host}{path}: HTTP {type(e).__name__}"
        except OSError as e:  # 연결 자체 실패(도달 불가)만 보류 → 통과
            return True, f"unreachable({host}{path}:{type(e).__name__}) — 딥체크 보류"
        # 기대 2xx/3xx 만 서빙 — 4xx(라우팅 404/403)·5xx(백엔드 다운) 는 차단.
        if not (200 <= status < 400):
            return False, f"{host}{path}: {status}"
        # 본문 요구(auth 는 실 realm 복원 확인) 불충족 → 차단.
        if want and want not in body:
            return False, f"{host}{path}: 본문에 '{want}' 없음(realm 미복원 의심)"
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
