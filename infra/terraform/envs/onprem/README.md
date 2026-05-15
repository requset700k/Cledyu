# onprem 환경 — KVM 3노드 프로비저닝

Phase 1 결과물. Ansible 로 libvirt + cledyubr0 네트워크가 준비된 **이후**에 실행.

## 사전 요구사항
1. `make host-prep HOST=<호스트-IP>` 완료 (libvirt + cledyubr0 + base 이미지 준비됨)
2. 로컬에 Terraform >= 1.6 설치
3. 호스트에 SSH 키 기반 접속 가능 (원격 libvirt 드라이버)

## 실행
```bash
cp terraform.tfvars.example terraform.tfvars
$EDITOR terraform.tfvars        # libvirt_uri, ssh_authorized_key 채우기

terraform -chdir=. init
terraform -chdir=. plan
terraform -chdir=. apply
```

## 결과 확인
```bash
terraform output nodes
terraform output ansible_inventory > ../../../ansible/inventory.yml
```

## 파기
```bash
terraform destroy    # VM/볼륨/cloud-init ISO 모두 삭제
```

<!-- BEGIN_TF_DOCS -->
## Requirements

| Name | Version |
| ---- | ------- |
| <a name="requirement_terraform"></a> [terraform](#requirement\_terraform) | >= 1.5.0 |

## Providers

No providers.

## Modules

| Name | Source | Version |
| ---- | ------ | ------- |
| <a name="module_kvm"></a> [kvm](#module\_kvm) | ../../kvm | n/a |

## Resources

No resources.

## Inputs

| Name | Description | Type | Default | Required |
| ---- | ----------- | ---- | ------- | :------: |
| <a name="input_libvirt_uri"></a> [libvirt\_uri](#input\_libvirt\_uri) | libvirt URI. 로컬 실행시 qemu:///system, 원격시 qemu+ssh://ubuntu@<호스트>/system | `string` | n/a | yes |
| <a name="input_ssh_authorized_key"></a> [ssh\_authorized\_key](#input\_ssh\_authorized\_key) | cloud-init 으로 주입할 SSH 공개키 내용 | `string` | n/a | yes |

## Outputs

| Name | Description |
| ---- | ----------- |
| <a name="output_ansible_inventory"></a> [ansible\_inventory](#output\_ansible\_inventory) | n/a |
| <a name="output_control_plane_ips"></a> [control\_plane\_ips](#output\_control\_plane\_ips) | n/a |
| <a name="output_nodes"></a> [nodes](#output\_nodes) | n/a |
<!-- END_TF_DOCS -->
