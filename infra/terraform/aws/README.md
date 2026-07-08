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

원격 암호화 backend(AWS **S3**)를 쓴다 — `versions.tf` 의 `backend "s3"`
(bucket `cledyu-tf-state`, key `aws/terraform.tfstate`, ap-northeast-2, DynamoDB 락).
DR이 AWS 기반이라 복구에 필요한 state 를 복구 대상 클라우드(AWS 계정 `504284203153`)에
자기완결적으로 둔다 — GCS 는 만료형 GCP 크레딧이라 복구-크리티컬 자산을 두지 않는다
(GCP 는 AI·학습 데이터 전용). state 에 client secret·tailscale authkey 등 민감값이
sensitive 로 들어가므로 버킷에 versioning·Block Public Access·SSE(AES256)를 적용한다.

> 이전 이력: `aws` state 는 원래 GCS(`gs://cledyu-tf-state`, prefix `aws`)에 있었고
> `terraform init -migrate-state` 로 S3 로 옮겼다(GCS 사본은 롤백용으로 남아 있음).
> `keycloak`·`gcp` 스택은 아직 GCS 유지(`gcp` 는 자기 리소스와 co-located 라 의도적).

**부트스트랩 (state 버킷·락 테이블은 out-of-band, 최초 1회, 생성 완료됨):**

```bash
export AWS_PROFILE=cledyu AWS_REGION=ap-northeast-2   # 계정 504284203153

# 0) S3 state 버킷 + 보호
aws s3api create-bucket --bucket cledyu-tf-state --region ap-northeast-2 \
  --create-bucket-configuration LocationConstraint=ap-northeast-2
aws s3api put-bucket-versioning --bucket cledyu-tf-state \
  --versioning-configuration Status=Enabled
aws s3api put-public-access-block --bucket cledyu-tf-state --public-access-block-configuration \
  BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true
aws s3api put-bucket-encryption --bucket cledyu-tf-state --server-side-encryption-configuration \
  '{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"},"BucketKeyEnabled":true}]}'

# 1) DynamoDB 상태 락 테이블
aws dynamodb create-table --table-name cledyu-tf-lock \
  --attribute-definitions AttributeName=LockID,AttributeType=S \
  --key-schema AttributeName=LockID,KeyType=HASH --billing-mode PAY_PER_REQUEST

# 2) 신규 운영자: AWS 크레덴셜만 있으면 init 된다(GCS 인증 불필요).
terraform init
```

> TF 를 1.10+ 로 올리면 `dynamodb_table` 대신 `use_lockfile = true`(S3 네이티브 락)로
> 바꿔 락 테이블을 없앨 수 있다. 현재 툴체인이 1.5.7 이라 DynamoDB 를 유지한다.

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
| [aws_iam_instance_profile.baker_instance](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_instance_profile) | resource |
| [aws_iam_instance_profile.lab_instance](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_instance_profile) | resource |
| [aws_iam_openid_connect_provider.github](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_openid_connect_provider) | resource |
| [aws_iam_role.baker_instance](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_role) | resource |
| [aws_iam_role.gha_baker](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_role) | resource |
| [aws_iam_role.lab_instance](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_role) | resource |
| [aws_iam_role.vmimport](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_role) | resource |
| [aws_iam_role_policy.baker_instance](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_role_policy) | resource |
| [aws_iam_role_policy.gha_baker](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_role_policy) | resource |
| [aws_iam_role_policy.vmimport](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_role_policy) | resource |
| [aws_iam_role_policy_attachment.ssm_core](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_role_policy_attachment) | resource |
| [aws_iam_user.api](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_user) | resource |
| [aws_iam_user.backup](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_user) | resource |
| [aws_iam_user.engine](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_user) | resource |
| [aws_iam_user_policy.api_ec2](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_user_policy) | resource |
| [aws_iam_user_policy.backup](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_user_policy) | resource |
| [aws_iam_user_policy.engine_ssm](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_user_policy) | resource |
| [aws_instance.proxy](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/instance) | resource |
| [aws_kms_alias.dr_backups](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/kms_alias) | resource |
| [aws_kms_key.dr_backups](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/kms_key) | resource |
| [aws_launch_template.lab_session](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/launch_template) | resource |
| [aws_lb.public](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/lb) | resource |
| [aws_lb_listener.http_redirect](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/lb_listener) | resource |
| [aws_lb_listener.https](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/lb_listener) | resource |
| [aws_lb_target_group.keycloak_proxy](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/lb_target_group) | resource |
| [aws_lb_target_group_attachment.proxy](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/lb_target_group_attachment) | resource |
| [aws_route53_record.acm_validation](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/route53_record) | resource |
| [aws_route53_record.public](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/route53_record) | resource |
| [aws_s3_bucket.baker](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket) | resource |
| [aws_s3_bucket.dr_backups](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket) | resource |
| [aws_s3_bucket_lifecycle_configuration.dr_backups](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_lifecycle_configuration) | resource |
| [aws_s3_bucket_object_lock_configuration.dr_backups](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_object_lock_configuration) | resource |
| [aws_s3_bucket_public_access_block.dr_backups](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_public_access_block) | resource |
| [aws_s3_bucket_server_side_encryption_configuration.dr_backups](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_server_side_encryption_configuration) | resource |
| [aws_s3_bucket_versioning.dr_backups](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_versioning) | resource |
| [aws_security_group.alb](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/security_group) | resource |
| [aws_security_group.lab_session](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/security_group) | resource |
| [aws_security_group.proxy](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/security_group) | resource |
| [aws_wafv2_web_acl.public](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/wafv2_web_acl) | resource |
| [aws_wafv2_web_acl_association.public](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/wafv2_web_acl_association) | resource |
| [aws_ami.ubuntu](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/ami) | data source |
| [aws_caller_identity.current](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/caller_identity) | data source |
| [aws_iam_policy_document.api_ec2](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/iam_policy_document) | data source |
| [aws_iam_policy_document.backup](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/iam_policy_document) | data source |
| [aws_iam_policy_document.baker_assume](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/iam_policy_document) | data source |
| [aws_iam_policy_document.baker_instance](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/iam_policy_document) | data source |
| [aws_iam_policy_document.ec2_assume](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/iam_policy_document) | data source |
| [aws_iam_policy_document.engine_ssm](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/iam_policy_document) | data source |
| [aws_iam_policy_document.gha_baker](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/iam_policy_document) | data source |
| [aws_iam_policy_document.gha_baker_assume](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/iam_policy_document) | data source |
| [aws_iam_policy_document.vmimport](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/iam_policy_document) | data source |
| [aws_iam_policy_document.vmimport_assume](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/iam_policy_document) | data source |
| [aws_route53_zone.public](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/route53_zone) | data source |
| [aws_subnets.selected](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/subnets) | data source |
| [aws_vpc.selected](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/vpc) | data source |

## Inputs

| Name | Description | Type | Default | Required |
| ---- | ----------- | ---- | ------- | :------: |
| <a name="input_ami_id"></a> [ami\_id](#input\_ami\_id) | 세션(lab) 런치 템플릿 AMI(ap-northeast-2). 운영 tfvars 는 베이크된 lab-base 이미지로 설정한다<br/>(EC2 오버플로우 세션 VM 이 code-server·tailscale 등 프리베이크를 쓰기 위함). 빈 값이면 Canonical<br/>Ubuntu 22.04(amd64) '최신' 을 자동 조회하는데(data.aws\_ami.ubuntu, most\_recent), 신규 릴리스마다<br/>lab\_session 런치 템플릿이 드리프트하므로 현재 AMI 로 pin 한다(2026-07). **프록시는 이 값을 쓰지<br/>않는다** — 경량 Caddy 프록시는 stock Ubuntu(var.proxy\_ami\_id)로 분리했다. lab-base 이미지 위에<br/>프록시를 띄우면 lab 자체 cloud-init 과 충돌하거나 불필요하게 무겁다. README 의 'AMI 전략' 참고. | `string` | `"ami-0afe1fd15675c3f15"` | no |
| <a name="input_assign_public_ip"></a> [assign\_public\_ip](#input\_assign\_public\_ip) | 세션 인스턴스에 퍼블릭 IP 를 할당할지. Launch Template 이 network\_interfaces 를 명시하면<br/>subnet 의 MapPublicIpOnLaunch 가 무시되어 기본 미할당이 되므로, default VPC(IGW) 환경에서는<br/>true 여야 인스턴스가 인터넷(tailscale 가입·SSM·패키지 설치)에 도달한다.<br/>private subnet + NAT 구성이면 false 로 둔다. | `bool` | `true` | no |
| <a name="input_budget_limit_usd"></a> [budget\_limit\_usd](#input\_budget\_limit\_usd) | EC2 오버플로우 월 예산(USD). 0이면 예산 알람을 만들지 않는다. | `number` | `0` | no |
| <a name="input_budget_notification_emails"></a> [budget\_notification\_emails](#input\_budget\_notification\_emails) | 예산 임계 도달 시 알림 받을 이메일 목록(budget\_limit\_usd>0 일 때 사용). | `list(string)` | `[]` | no |
| <a name="input_enable_public_ingress"></a> [enable\_public\_ingress](#input\_enable\_public\_ingress) | 공개 진입점 스택(Route53/ACM/ALB/프록시) 생성 여부. 도메인 위임·tailscale authkey 준비 후 true. | `bool` | `false` | no |
| <a name="input_github_repo"></a> [github\_repo](#input\_github\_repo) | 베이커 워크플로를 실행하는 GitHub 레포(owner/name). OIDC sub 제한에 사용. | `string` | `"requset700k/Cledyu"` | no |
| <a name="input_instance_type"></a> [instance\_type](#input\_instance\_type) | 세션 인스턴스 타입. Launch Template 기본값이며 api 가 런타임에 오버라이드할 수 있다. | `string` | `"t3.medium"` | no |
| <a name="input_keycloak_upstream_url"></a> [keycloak\_upstream\_url](#input\_keycloak\_upstream\_url) | 프록시가 auth.cledyu.com 요청을 포워딩할 tailnet 상의 Keycloak 업스트림 URL.<br/>Cledyu 토폴로지에서는 하이퍼바이저 subnet router 가 10.10.0.0/24 를 tailnet 에<br/>광고하고 Traefik LB 가 10.10.0.101 이므로 "https://10.10.0.101" 를 쓴다(프록시는<br/>--accept-routes 로 이 라우트를 받고, Host=public\_keycloak\_host 로 보내 Traefik 이<br/>keycloak ingress 로 라우팅). pod/service ClusterIP 는 라우팅 불가하므로 Traefik LB<br/>경유 필수. Traefik 내부 CA 인증서는 프록시가 검증 생략(tailnet WireGuard 암호화). | `string` | `"https://10.10.0.101"` | no |
| <a name="input_name_prefix"></a> [name\_prefix](#input\_name\_prefix) | 생성 리소스 이름 prefix. 레거시 hackathon 류 금지(레포 네이밍 규칙). | `string` | `"cledyu-lab"` | no |
| <a name="input_proxy_ami_id"></a> [proxy\_ami\_id](#input\_proxy\_ami\_id) | tailnet 리버스프록시(Caddy) 전용 AMI(ap-northeast-2). 프록시는 경량 stock Ubuntu 로 충분하고<br/>세션 VM 용 lab-base 이미지(var.ami\_id)와 무관하므로 분리해 pin 한다. 기본값은 현재 프록시가<br/>실행 중인 Canonical Ubuntu 22.04(amd64) — var.ami\_id 를 lab-base 로 바꿔도 프록시는 이 값을<br/>유지해 강제 교체·잘못된 이미지(lab cloud-init 충돌) 부팅을 막는다. 최신 stock 으로 갱신하려면<br/>이 값을 새 Ubuntu AMI 로 교체한다. | `string` | `"ami-0afe1fd15675c3f15"` | no |
| <a name="input_proxy_instance_type"></a> [proxy\_instance\_type](#input\_proxy\_instance\_type) | tailnet 리버스프록시 인스턴스 타입. 경량 프록시이므로 작게(비용 절감). | `string` | `"t3.nano"` | no |
| <a name="input_public_api_host"></a> [public\_api\_host](#input\_public\_api\_host) | 학습자 api(BFF) 공개 FQDN. 브라우저는 OAuth 콜백(api.cledyu.com/api/v1/auth/callback)에서만<br/>직접 도달하고, 일반 데이터 호출은 web 이 in-cluster(http://api.api.svc.cluster.local)로 프록시한다. | `string` | `"api.cledyu.com"` | no |
| <a name="input_public_app_host"></a> [public\_app\_host](#input\_public\_app\_host) | 학습자 web 앱 공개 FQDN(ALB→프록시→Traefik→web). 와일드카드 ACM(*.public\_domain)로 커버. | `string` | `"app.cledyu.com"` | no |
| <a name="input_public_domain"></a> [public\_domain](#input\_public\_domain) | 공개 루트 도메인(Route53 hosted zone 으로 관리). 예 cledyu.com. NS 를 도메인 등록기관에 위임해야 한다. | `string` | `"cledyu.com"` | no |
| <a name="input_public_ingress_allowed_cidrs"></a> [public\_ingress\_allowed\_cidrs](#input\_public\_ingress\_allowed\_cidrs) | ALB 443/80 인바운드 허용 CIDR. 기본은 공개(0.0.0.0/0) — 검증 단계에서 사무실 IP 로 좁힐 수 있다. | `list(string)` | <pre>[<br/>  "0.0.0.0/0"<br/>]</pre> | no |
| <a name="input_public_keycloak_host"></a> [public\_keycloak\_host](#input\_public\_keycloak\_host) | Keycloak 공개 FQDN. 구글 OAuth redirect URI 의 호스트가 된다(.../realms/cledyu-learn/broker/google/endpoint). | `string` | `"auth.cledyu.com"` | no |
| <a name="input_region"></a> [region](#input\_region) | EC2 오버플로우 리전. 온프렘과 가까운 서울 리전을 기본값으로 둔다.<br/>이 스택은 ap-northeast-2 단일 리전 전제다(S3 state·백업 버킷·KMS·서브넷·pin 된 var.ami\_id 가<br/>모두 리전 종속). 다른 리전을 쓰려면 ami\_id 등 리전 종속 값을 함께 교체해야 하므로 validation<br/>으로 막아 둔다 — 의도적 멀티리전 시 이 validation 을 완화하고 리전별 값을 정비할 것. | `string` | `"ap-northeast-2"` | no |
| <a name="input_root_volume_gb"></a> [root\_volume\_gb](#input\_root\_volume\_gb) | 세션 인스턴스 루트 볼륨 크기(GiB). | `number` | `20` | no |
| <a name="input_subnet_id"></a> [subnet\_id](#input\_subnet\_id) | 세션 인스턴스 서브넷. 빈 값이면 선택된 VPC 의 서브넷 중 하나를 자동 선택한다. | `string` | `""` | no |
| <a name="input_tailscale_auth_key"></a> [tailscale\_auth\_key](#input\_tailscale\_auth\_key) | 프록시 인스턴스가 tailnet 에 가입할 때 쓰는 일회용/재사용 authkey. TF\_VAR\_tailscale\_auth\_key 로 주입(state 평문 저장 회피 위해 tfvars 금지). | `string` | `""` | no |
| <a name="input_vpc_id"></a> [vpc\_id](#input\_vpc\_id) | 세션 인스턴스를 띄울 VPC. 빈 값이면 리전의 default VPC 를 사용한다. | `string` | `""` | no |
| <a name="input_waf_rate_limit"></a> [waf\_rate\_limit](#input\_waf\_rate\_limit) | WAF rate-based 룰의 IP당 5분(기본 평가창) 요청 상한. 초과 시 block. 데모 부하 기준 2000. | `number` | `2000` | no |

## Outputs

| Name | Description |
| ---- | ----------- |
| <a name="output_api_iam_user"></a> [api\_iam\_user](#output\_api\_iam\_user) | apps/api 용 IAM 사용자명 — 이 사용자의 액세스 키를 발급해 Vault 에 보관한다. |
| <a name="output_backup_bucket"></a> [backup\_bucket](#output\_backup\_bucket) | DR 백업 S3 버킷명(Postgres·Vault·Velero 백업 대상; Longhorn은 별도 버킷). |
| <a name="output_backup_iam_users"></a> [backup\_iam\_users](#output\_backup\_iam\_users) | 프리픽스별 백업 IAM 사용자명 맵(키: postgres, vault, velero) — 각 사용자의 액세스 키를 발급해 Vault 경로 cledyu/aws/backup-<키>(backup-postgres·backup-vault·backup-velero)에 보관한다. |
| <a name="output_baker_bucket"></a> [baker\_bucket](#output\_baker\_bucket) | 이미지 베이커 S3 버킷명. |
| <a name="output_baker_instance_profile"></a> [baker\_instance\_profile](#output\_baker\_instance\_profile) | metal 베이커 인스턴스 프로파일명. |
| <a name="output_gha_baker_role_arn"></a> [gha\_baker\_role\_arn](#output\_gha\_baker\_role\_arn) | GitHub Action 이 assume 할 베이커 role ARN. |
| <a name="output_instance_profile_arn"></a> [instance\_profile\_arn](#output\_instance\_profile\_arn) | 세션 인스턴스 IAM 인스턴스 프로파일 ARN(SSM Core). |
| <a name="output_keycloak_proxy_instance_id"></a> [keycloak\_proxy\_instance\_id](#output\_keycloak\_proxy\_instance\_id) | tailnet Keycloak 프록시 인스턴스 ID(tailnet 가입·로그 확인용). |
| <a name="output_launch_template_id"></a> [launch\_template\_id](#output\_launch\_template\_id) | apps/api 의 CLEDYU\_AWS\_LAUNCH\_TEMPLATE\_ID 로 주입할 Launch Template ID. |
| <a name="output_launch_template_latest_version"></a> [launch\_template\_latest\_version](#output\_launch\_template\_latest\_version) | Launch Template 최신 버전(api 는 $Latest 를 쓰지만 참고용으로 노출). |
| <a name="output_public_alb_dns_name"></a> [public\_alb\_dns\_name](#output\_public\_alb\_dns\_name) | 공개 ALB 의 DNS 이름(app/api/auth.cledyu.com A ALIAS 타겟). 디버깅·검증용. |
| <a name="output_public_api_record"></a> [public\_api\_record](#output\_public\_api\_record) | 학습자 api 공개 FQDN(api.cledyu.com). OAuth 콜백 도달점. 검증용. |
| <a name="output_public_app_record"></a> [public\_app\_record](#output\_public\_app\_record) | 학습자 web 공개 FQDN(app.cledyu.com). 검증용. |
| <a name="output_public_waf_web_acl_arn"></a> [public\_waf\_web\_acl\_arn](#output\_public\_waf\_web\_acl\_arn) | 공개 ALB 에 연결된 WAF WebACL ARN(CloudWatch 메트릭·sampled requests 확인용). |
| <a name="output_public_zone_name_servers"></a> [public\_zone\_name\_servers](#output\_public\_zone\_name\_servers) | public\_domain hosted zone 의 NS(참고용). registrar=Route53 면 자동 연결돼 수동 위임 불필요. |
| <a name="output_region"></a> [region](#output\_region) | EC2 오버플로우 리전(apps/api 의 CLEDYU\_AWS\_REGION). |
| <a name="output_security_group_id"></a> [security\_group\_id](#output\_security\_group\_id) | 세션 인스턴스 Security Group ID(인바운드 0). |
| <a name="output_validation_engine_iam_user"></a> [validation\_engine\_iam\_user](#output\_validation\_engine\_iam\_user) | validation-engine 용 IAM 사용자명 — 이 사용자의 액세스 키를 발급해 Vault 에 보관한다. |
<!-- END_TF_DOCS -->
