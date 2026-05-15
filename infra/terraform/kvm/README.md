<!-- BEGIN_TF_DOCS -->
## Requirements

| Name | Version |
| ---- | ------- |
| <a name="requirement_terraform"></a> [terraform](#requirement\_terraform) | >= 1.5.0 |
| <a name="requirement_libvirt"></a> [libvirt](#requirement\_libvirt) | ~> 0.8.0 |

## Providers

| Name | Version |
| ---- | ------- |
| <a name="provider_libvirt"></a> [libvirt](#provider\_libvirt) | ~> 0.8.0 |

## Modules

No modules.

## Resources

| Name | Type |
| ---- | ---- |
| [libvirt_cloudinit_disk.ci](https://registry.terraform.io/providers/dmacvicar/libvirt/latest/docs/resources/cloudinit_disk) | resource |
| [libvirt_domain.node](https://registry.terraform.io/providers/dmacvicar/libvirt/latest/docs/resources/domain) | resource |
| [libvirt_pool.wp](https://registry.terraform.io/providers/dmacvicar/libvirt/latest/docs/resources/pool) | resource |
| [libvirt_volume.base](https://registry.terraform.io/providers/dmacvicar/libvirt/latest/docs/resources/volume) | resource |
| [libvirt_volume.root](https://registry.terraform.io/providers/dmacvicar/libvirt/latest/docs/resources/volume) | resource |

## Inputs

| Name | Description | Type | Default | Required |
| ---- | ----------- | ---- | ------- | :------: |
| <a name="input_ssh_authorized_key"></a> [ssh\_authorized\_key](#input\_ssh\_authorized\_key) | cloud-init 으로 주입할 SSH 공개키 (예: file("~/.ssh/id\_ed25519.pub")) | `string` | n/a | yes |
| <a name="input_base_image_source"></a> [base\_image\_source](#input\_base\_image\_source) | 우분투 22.04 클라우드 이미지 소스(URL 또는 로컬 경로). provider가 받아서 pool에 업로드. | `string` | `"https://cloud-images.ubuntu.com/releases/22.04/release/ubuntu-22.04-server-cloudimg-amd64.img"` | no |
| <a name="input_cluster_name"></a> [cluster\_name](#input\_cluster\_name) | 클러스터 이름 prefix. VM·pool·disk 이름이 여기서 파생됨. | `string` | `"cledyu"` | no |
| <a name="input_dns_servers"></a> [dns\_servers](#input\_dns\_servers) | VM이 사용할 DNS 서버 | `list(string)` | <pre>[<br/>  "1.1.1.1",<br/>  "8.8.8.8"<br/>]</pre> | no |
| <a name="input_gateway"></a> [gateway](#input\_gateway) | VM 네트워크 기본 게이트웨이 | `string` | `"10.10.0.1"` | no |
| <a name="input_images_pool_path"></a> [images\_pool\_path](#input\_images\_pool\_path) | VM 디스크가 저장될 디렉토리 (default libvirt 풀과 충돌 방지를 위해 하위 디렉토리 사용) | `string` | `"/var/lib/libvirt/images/cledyu"` | no |
| <a name="input_libvirt_uri"></a> [libvirt\_uri](#input\_libvirt\_uri) | libvirt 연결 URI. 로컬 실행: qemu:///system, 원격: qemu+ssh://ubuntu@<host>/system | `string` | `"qemu:///system"` | no |
| <a name="input_network_name"></a> [network\_name](#input\_network\_name) | 사용할 libvirt 네트워크 이름(Ansible이 cledyubr0로 생성) | `string` | `"cledyubr0"` | no |
| <a name="input_nodes"></a> [nodes](#input\_nodes) | K8s 노드 정의. 계획서 기준 3노드 HA(컨트롤플레인+워커 겸용) | <pre>map(object({<br/>    role    = string # "control-plane" (Phase 2에서 taint 해제하여 워커 겸용)<br/>    ip      = string<br/>    cpu     = number<br/>    memory  = number # MB<br/>    disk_gb = number<br/>  }))</pre> | <pre>{<br/>  "cp01": {<br/>    "cpu": 16,<br/>    "disk_gb": 500,<br/>    "ip": "10.10.0.11/24",<br/>    "memory": 32768,<br/>    "role": "control-plane"<br/>  },<br/>  "cp02": {<br/>    "cpu": 16,<br/>    "disk_gb": 500,<br/>    "ip": "10.10.0.12/24",<br/>    "memory": 32768,<br/>    "role": "control-plane"<br/>  },<br/>  "cp03": {<br/>    "cpu": 16,<br/>    "disk_gb": 500,<br/>    "ip": "10.10.0.13/24",<br/>    "memory": 32768,<br/>    "role": "control-plane"<br/>  }<br/>}</pre> | no |

## Outputs

| Name | Description |
| ---- | ----------- |
| <a name="output_ansible_inventory_snippet"></a> [ansible\_inventory\_snippet](#output\_ansible\_inventory\_snippet) | ansible/inventory.yml 에 붙여넣을 k8s\_nodes 정의 |
| <a name="output_control_plane_ips"></a> [control\_plane\_ips](#output\_control\_plane\_ips) | 컨트롤플레인 IP 목록 (kubeadm init/join 대상) |
| <a name="output_nodes"></a> [nodes](#output\_nodes) | 노드 이름 → 고정 IP |
<!-- END_TF_DOCS -->
