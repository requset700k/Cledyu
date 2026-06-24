# AWS EC2 오버플로우 인프라 (Phase 13)

온프렘 KubeVirt Lab VM 풀이 가득 찼을 때 학습 세션을 AWS EC2로 버스트하기 위한 베이스
리소스를 만든다. 세션 인스턴스 자체는 여기서 만들지 않는다 — `apps/api`(`internal/ec2`)가
이 Launch Template 을 참조해 세션마다 동적으로 띄우고 회수한다.

## 만드는 것

| 리소스 | 용도 |
| --- | --- |
| Security Group | 세션 인스턴스용. **인바운드 0** — 채점(SSM)·라이브 터미널(Tailscale) 모두 아웃바운드만 사용 |
| IAM 인스턴스 프로파일 | 세션 인스턴스가 SSM 명령을 받도록 `AmazonSSMManagedInstanceCore` 부여 |
| IAM 사용자 `*-api` | `apps/api` 용 최소권한(EC2 RunInstances/Terminate/Describe/CreateTags + PassRole) |
| IAM 사용자 `*-validation-engine` | 검증엔진용 최소권한(SSM SendCommand/GetCommandInvocation) |
| Launch Template | `apps/api` 가 RunInstances 시 참조. 인스턴스 타입·SG·인스턴스 프로파일·IMDSv2·EBS 암호화 |

## 사전 조건

- Terraform >= 1.5
- AWS 자격증명(프로비저닝용, 관리자급) — `aws configure` 또는 환경변수. 이 자격증명은
  terraform 실행 전용이며, 앱 런타임에는 쓰지 않는다(아래 '액세스 키' 참고).

## 사용

```bash
cd infra/terraform/aws
cp terraform.tfvars.example terraform.tfvars   # 필요 시 수정(tfvars 는 커밋 금지)
terraform init
terraform plan
terraform apply
```

적용 후 출력값을 `apps/api` 환경변수로 주입한다:

```bash
terraform output -raw launch_template_id   # → CLEDYU_AWS_LAUNCH_TEMPLATE_ID
terraform output -raw region               # → CLEDYU_AWS_REGION
```

`apps/api` 는 `CLEDYU_AWS_LAUNCH_TEMPLATE_ID` 와 `CLEDYU_AWS_MAX_ACTIVE_SESSIONS > 0` 이
설정돼야 EC2 오버플로우를 활성화한다. 미설정이면 KubeVirt 전용으로 동작한다(현행 동작 보존).

## 액세스 키 (Vault 경유 — 코드/state 에 두지 않음)

terraform 은 IAM 사용자와 정책만 만들고 **액세스 키는 만들지 않는다**(시크릿이 tfstate 에
남는 것을 피한다). 운영자가 키를 발급해 Vault 에 보관하고 External Secrets 로 컨테이너 env 에
주입한다:

```bash
aws iam create-access-key --user-name "$(terraform output -raw api_iam_user)"
aws iam create-access-key --user-name "$(terraform output -raw validation_engine_iam_user)"
# → 각 키를 Vault 에 저장: secret/cledyu/aws/api, secret/cledyu/aws/validation-engine
#   ExternalSecret 가 AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY 로 주입(W4).
```

api 와 validation-engine 은 **서로 다른 IAM 사용자/키**를 쓴다(권한 분리: api=EC2 수명주기,
engine=SSM 채점).

## AMI 전략

`ami_id` 를 비우면 Canonical Ubuntu 22.04 최신 AMI 를 자동 조회한다. 이 경우 cloud-init 이
부팅 시 SSM Agent·tailscale·code-server 를 설치하므로 부팅이 느리다.

운영에서는 **이 도구들을 미리 구운 커스텀 AMI(packer)** 를 만들어 `ami_id` 로 지정하는 것을
권장한다. EC2 는 `apps/api` 가 RunInstances 에서 넘긴 세션별 user-data 로 Launch Template 의
user-data 를 **대체**하므로(병합 아님), 베이스 도구는 AMI 에 있어야 세션 user-data 가
`tailscale up` 과 랩 초기화만 수행하면 된다. (packer 템플릿은 후속 작업.)

## 상태(state) 관리

기본은 로컬 backend 다. 온프렘 state(`infra/terraform/kvm`, `keycloak`)와 분리돼 있다.
팀 운영 시 S3 backend + DynamoDB lock 으로 전환한다(`versions.tf` 의 backend 블록 추가).

## 비용 주의

세션 인스턴스는 `apps/api` 의 reaper 가 TTL 만료·프로비저닝 타임아웃 시 terminate 한다.
가드레일·고아 인스턴스 청소·Budgets 알람은 W4 에서 다룬다. 수동 확인/청소 절차는
`docs/RUNBOOK/ec2-overflow.md`(W5) 참고.

<!-- BEGIN_TF_DOCS -->
## Requirements

| Name | Version |
| ---- | ------- |
| <a name="requirement_terraform"></a> [terraform](#requirement\_terraform) | >= 1.5.0 |
| <a name="requirement_aws"></a> [aws](#requirement\_aws) | ~> 5.0 |

## Providers

| Name | Version |
| ---- | ------- |
| <a name="provider_aws"></a> [aws](#provider\_aws) | ~> 5.0 |

## Modules

No modules.

## Resources

| Name | Type |
| ---- | ---- |
| [aws_acm_certificate.auth](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/acm_certificate) | resource |
| [aws_acm_certificate_validation.auth](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/acm_certificate_validation) | resource |
| [aws_budgets_budget.lab_ec2](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/budgets_budget) | resource |
| [aws_iam_instance_profile.lab_instance](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_instance_profile) | resource |
| [aws_iam_role.lab_instance](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_role) | resource |
| [aws_iam_role_policy_attachment.ssm_core](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_role_policy_attachment) | resource |
| [aws_iam_user.api](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_user) | resource |
| [aws_iam_user.engine](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_user) | resource |
| [aws_iam_user_policy.api_ec2](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_user_policy) | resource |
| [aws_iam_user_policy.engine_ssm](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_user_policy) | resource |
| [aws_instance.proxy](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/instance) | resource |
| [aws_launch_template.lab_session](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/launch_template) | resource |
| [aws_lb.public](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/lb) | resource |
| [aws_lb_listener.http_redirect](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/lb_listener) | resource |
| [aws_lb_listener.https](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/lb_listener) | resource |
| [aws_lb_target_group.keycloak_proxy](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/lb_target_group) | resource |
| [aws_lb_target_group_attachment.proxy](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/lb_target_group_attachment) | resource |
| [aws_route53_record.acm_validation](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/route53_record) | resource |
| [aws_route53_record.auth](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/route53_record) | resource |
| [aws_route53_zone.public](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/route53_zone) | resource |
| [aws_security_group.alb](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/security_group) | resource |
| [aws_security_group.lab_session](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/security_group) | resource |
| [aws_security_group.proxy](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/security_group) | resource |
| [aws_ami.ubuntu](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/ami) | data source |
| [aws_iam_policy_document.api_ec2](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/iam_policy_document) | data source |
| [aws_iam_policy_document.ec2_assume](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/iam_policy_document) | data source |
| [aws_iam_policy_document.engine_ssm](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/iam_policy_document) | data source |
| [aws_subnets.selected](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/subnets) | data source |
| [aws_vpc.selected](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/vpc) | data source |

## Inputs

| Name | Description | Type | Default | Required |
| ---- | ----------- | ---- | ------- | :------: |
| <a name="input_ami_id"></a> [ami\_id](#input\_ami\_id) | 세션 인스턴스 AMI. 빈 값이면 Canonical Ubuntu 22.04(amd64) 최신 AMI 를 자동 조회한다.<br/>운영에서는 SSM Agent·tailscale·code-server 를 미리 구운 커스텀 AMI(packer) ID 를 넣는 것을 권장한다<br/>(런타임 설치 시간 단축). README 의 'AMI 전략' 참고. | `string` | `""` | no |
| <a name="input_budget_limit_usd"></a> [budget\_limit\_usd](#input\_budget\_limit\_usd) | EC2 오버플로우 월 예산(USD). 0이면 예산 알람을 만들지 않는다. | `number` | `0` | no |
| <a name="input_budget_notification_emails"></a> [budget\_notification\_emails](#input\_budget\_notification\_emails) | 예산 임계 도달 시 알림 받을 이메일 목록(budget\_limit\_usd>0 일 때 사용). | `list(string)` | `[]` | no |
| <a name="input_enable_public_ingress"></a> [enable\_public\_ingress](#input\_enable\_public\_ingress) | 공개 진입점 스택(Route53/ACM/ALB/프록시) 생성 여부. 도메인 위임·tailscale authkey 준비 후 true. | `bool` | `false` | no |
| <a name="input_instance_type"></a> [instance\_type](#input\_instance\_type) | 세션 인스턴스 타입. Launch Template 기본값이며 api 가 런타임에 오버라이드할 수 있다. | `string` | `"t3.medium"` | no |
| <a name="input_keycloak_upstream_url"></a> [keycloak\_upstream\_url](#input\_keycloak\_upstream\_url) | 프록시가 auth.cledyu.io 요청을 포워딩할 tailnet 상의 Keycloak 업스트림 URL.<br/>환경의 tailnet 토폴로지에 맞춰 채운다 — 예: 클러스터 서브넷이 tailnet 에 광고돼 있으면<br/>"http://keycloak.cledyu.local:8080"(서브넷 라우터+split DNS) 또는 Keycloak service 의<br/>tailnet 도달 주소. 프록시는 Host 헤더를 public\_keycloak\_host 로 보존해 전달한다. | `string` | `""` | no |
| <a name="input_name_prefix"></a> [name\_prefix](#input\_name\_prefix) | 생성 리소스 이름 prefix. 레거시 hackathon 류 금지(레포 네이밍 규칙). | `string` | `"cledyu-lab"` | no |
| <a name="input_proxy_instance_type"></a> [proxy\_instance\_type](#input\_proxy\_instance\_type) | tailnet 리버스프록시 인스턴스 타입. 경량 프록시이므로 작게(비용 절감). | `string` | `"t3.nano"` | no |
| <a name="input_public_domain"></a> [public\_domain](#input\_public\_domain) | 공개 루트 도메인(Route53 hosted zone 으로 관리). 예 cledyu.io. NS 를 도메인 등록기관에 위임해야 한다. | `string` | `"cledyu.io"` | no |
| <a name="input_public_ingress_allowed_cidrs"></a> [public\_ingress\_allowed\_cidrs](#input\_public\_ingress\_allowed\_cidrs) | ALB 443/80 인바운드 허용 CIDR. 기본은 공개(0.0.0.0/0) — 검증 단계에서 사무실 IP 로 좁힐 수 있다. | `list(string)` | <pre>[<br/>  "0.0.0.0/0"<br/>]</pre> | no |
| <a name="input_public_keycloak_host"></a> [public\_keycloak\_host](#input\_public\_keycloak\_host) | Keycloak 공개 FQDN. 구글 OAuth redirect URI 의 호스트가 된다(.../realms/cledyu-learn/broker/google/endpoint). | `string` | `"auth.cledyu.io"` | no |
| <a name="input_region"></a> [region](#input\_region) | EC2 오버플로우 리전. 온프렘과 가까운 서울 리전을 기본값으로 둔다. | `string` | `"ap-northeast-2"` | no |
| <a name="input_root_volume_gb"></a> [root\_volume\_gb](#input\_root\_volume\_gb) | 세션 인스턴스 루트 볼륨 크기(GiB). | `number` | `20` | no |
| <a name="input_subnet_id"></a> [subnet\_id](#input\_subnet\_id) | 세션 인스턴스 서브넷. 빈 값이면 선택된 VPC 의 서브넷 중 하나를 자동 선택한다. | `string` | `""` | no |
| <a name="input_tailscale_auth_key"></a> [tailscale\_auth\_key](#input\_tailscale\_auth\_key) | 프록시 인스턴스가 tailnet 에 가입할 때 쓰는 일회용/재사용 authkey. TF\_VAR\_tailscale\_auth\_key 로 주입(state 평문 저장 회피 위해 tfvars 금지). | `string` | `""` | no |
| <a name="input_vpc_id"></a> [vpc\_id](#input\_vpc\_id) | 세션 인스턴스를 띄울 VPC. 빈 값이면 리전의 default VPC 를 사용한다. | `string` | `""` | no |

## Outputs

| Name | Description |
| ---- | ----------- |
| <a name="output_api_iam_user"></a> [api\_iam\_user](#output\_api\_iam\_user) | apps/api 용 IAM 사용자명 — 이 사용자의 액세스 키를 발급해 Vault 에 보관한다. |
| <a name="output_instance_profile_arn"></a> [instance\_profile\_arn](#output\_instance\_profile\_arn) | 세션 인스턴스 IAM 인스턴스 프로파일 ARN(SSM Core). |
| <a name="output_keycloak_proxy_instance_id"></a> [keycloak\_proxy\_instance\_id](#output\_keycloak\_proxy\_instance\_id) | tailnet Keycloak 프록시 인스턴스 ID(tailnet 가입·로그 확인용). |
| <a name="output_launch_template_id"></a> [launch\_template\_id](#output\_launch\_template\_id) | apps/api 의 CLEDYU\_AWS\_LAUNCH\_TEMPLATE\_ID 로 주입할 Launch Template ID. |
| <a name="output_launch_template_latest_version"></a> [launch\_template\_latest\_version](#output\_launch\_template\_latest\_version) | Launch Template 최신 버전(api 는 $Latest 를 쓰지만 참고용으로 노출). |
| <a name="output_public_alb_dns_name"></a> [public\_alb\_dns\_name](#output\_public\_alb\_dns\_name) | 공개 ALB 의 DNS 이름(auth.cledyu.io A ALIAS 타겟). 디버깅·검증용. |
| <a name="output_public_zone_name_servers"></a> [public\_zone\_name\_servers](#output\_public\_zone\_name\_servers) | public\_domain hosted zone 의 NS 4개. 도메인 등록기관에 이 값으로 위임해야 공개 해석된다. |
| <a name="output_region"></a> [region](#output\_region) | EC2 오버플로우 리전(apps/api 의 CLEDYU\_AWS\_REGION). |
| <a name="output_security_group_id"></a> [security\_group\_id](#output\_security\_group\_id) | 세션 인스턴스 Security Group ID(인바운드 0). |
| <a name="output_validation_engine_iam_user"></a> [validation\_engine\_iam\_user](#output\_validation\_engine\_iam\_user) | validation-engine 용 IAM 사용자명 — 이 사용자의 액세스 키를 발급해 Vault 에 보관한다. |
<!-- END_TF_DOCS -->
