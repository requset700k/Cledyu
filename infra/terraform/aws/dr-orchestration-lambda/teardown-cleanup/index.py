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
    r = _ec2.describe_instances(
        Filters=[
            {"Name": "vpc-id", "Values": [vpc]},
            {"Name": f"tag:kubernetes.io/cluster/{CLUSTER}", "Values": ["owned"]},
            {
                "Name": "instance-state-name",
                "Values": ["pending", "running", "stopping", "stopped"],
            },
        ]
    )
    return [i["InstanceId"] for res in r["Reservations"] for i in res["Instances"]]


def is_orphan_eni(eni):
    """DR VPC 내 available + CNI 보조 ENI(aws-K8S-*)만 삭제 대상(붙어있는 것·ALB ENI 제외)."""
    return eni.get("Status") == "available" and str(eni.get("Description", "")).startswith(
        "aws-K8S-"
    )


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
        vols = _ec2.describe_volumes(
            Filters=[
                {"Name": f"tag:kubernetes.io/cluster/{CLUSTER}", "Values": ["owned"]},
            ]
        )["Volumes"]
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
        tgs = _elb.describe_target_groups(LoadBalancerArn=lb["LoadBalancerArn"])["TargetGroups"]
        tg_arns.extend(tg["TargetGroupArn"] for tg in tgs)
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
    vols = _ec2.describe_volumes(
        Filters=[
            {"Name": f"tag:kubernetes.io/cluster/{CLUSTER}", "Values": ["owned"]},
            {"Name": "status", "Values": ["available"]},
        ]
    )["Volumes"]
    for v in vols:
        _ec2.delete_volume(VolumeId=v["VolumeId"])
        deleted["ebs"].append(v["VolumeId"])

    # 3) 고아 ENI (available, aws-K8S-*)
    for eni in _ec2.describe_network_interfaces(Filters=[{"Name": "vpc-id", "Values": [vpc]}])[
        "NetworkInterfaces"
    ]:
        if is_orphan_eni(eni):
            _ec2.delete_network_interface(NetworkInterfaceId=eni["NetworkInterfaceId"])
            deleted["eni"].append(eni["NetworkInterfaceId"])

    # 4) k8s-* SG — [NEW-3] ALB 삭제 후 그 ENI 가 detach 돼야 SG 삭제 가능. detach 는 몇 초 걸리므로
    #    재시도로 대기(1회만 시도하면 SG 가 누수됨).
    for sg in _ec2.describe_security_groups(Filters=[{"Name": "vpc-id", "Values": [vpc]}])[
        "SecurityGroups"
    ]:
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
    for ep in _ec2.describe_vpc_endpoints(Filters=[{"Name": "vpc-id", "Values": [vpc]}])[
        "VpcEndpoints"
    ]:
        if "guardduty-data" in ep.get("ServiceName", ""):
            _ec2.delete_vpc_endpoints(VpcEndpointIds=[ep["VpcEndpointId"]])
            deleted["vpce"].append(ep["VpcEndpointId"])

    return {"deleted": deleted, "vpc": vpc}
