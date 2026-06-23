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
