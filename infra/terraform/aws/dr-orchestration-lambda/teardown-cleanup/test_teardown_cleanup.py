import importlib.util
import pathlib

_spec = importlib.util.spec_from_file_location(
    "cleanup", pathlib.Path(__file__).parent / "index.py"
)
c = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(c)


def test_orphan_eni_predicate():
    assert c.is_orphan_eni({"Status": "available", "Description": "aws-K8S-i-0abc"}) is True
    assert (
        c.is_orphan_eni({"Status": "in-use", "Description": "aws-K8S-i-0abc"}) is False
    )  # 붙어있으면 skip
    assert (
        c.is_orphan_eni({"Status": "available", "Description": "ELB app/xyz"}) is False
    )  # ALB/기타 ENI skip


def test_k8s_sg_predicate():
    assert c.is_k8s_sg({"GroupName": "k8s-traffic-abc123"}) is True
    assert c.is_k8s_sg({"GroupName": "cledyu-dr-bastion-sg"}) is False
