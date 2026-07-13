# EC2 오버플로우 운영

온프렘 KubeVirt Lab VM 풀이 가득 찼을 때 학습 세션을 AWS EC2로 버스트하는 경로(Phase 13)의
동작 확인·디버그·비용 관리 절차다.

## 동작 개요

- 신규 세션은 `apps/api` 의 디스패처가 라우팅한다: 온프렘 활성 세션 수가 `CLEDYU_KUBEVIRT_MAX_ACTIVE_SESSIONS`
  미만이면 KubeVirt, 도달하면 EC2(`internal/ec2`)로 버스트한다.
- 글로벌 동시 세션 상한 = 온프렘(`CLEDYU_KUBEVIRT_MAX_ACTIVE_SESSIONS`) + EC2(`CLEDYU_AWS_MAX_ACTIVE_SESSIONS`).
  도달 시 세션 생성은 429(`capacity_reached`).
- EC2 세션은 SSM 으로 채점(virtctl 대신), tailnet(Tailscale)으로 라이브 터미널/IDE 도달.
- 인스턴스는 세션 태그(`cledyu.io/session-id` 등)로 식별하며, reaper 가 TTL 만료·프로비저닝
  타임아웃 시 terminate 한다.

## 트리거

- 학습자 세션이 EC2로 떠야 하는데 안 뜨거나, EC2 세션 채점/터미널이 실패함
- AWS 비용 알람(Budgets) 수신 — EC2 인스턴스가 회수되지 않는 것으로 의심
- 온프렘 만석인데 세션 생성이 429로 거부됨

## 사전 조건

- `kubectl` 이 Cledyu 클러스터에 인증돼 있어야 한다.
- AWS CLI 가 해당 리전(`ap-northeast-2`)에 인증돼 있어야 한다(`aws sts get-caller-identity`).
- EC2 오버플로우가 활성인지 확인:

```bash
kubectl -n api get deploy api -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="CLEDYU_AWS_MAX_ACTIVE_SESSIONS")].value}{"\n"}'
```

예상 출력(활성 시): `0` 보다 큰 값. `0` 이거나 비어 있으면 KubeVirt 전용(오버플로우 비활성).

## 절차

### 1. 오버플로우 설정 확인

```bash
kubectl -n api get externalsecret cledyu-api-aws cledyu-api-tailscale
kubectl -n api get secret cledyu-api-aws -o jsonpath='{.data}' | tr ',' '\n' | sed 's/:.*$//'
kubectl -n api get secret cledyu-api-tailscale -o jsonpath='{.data}' | tr ',' '\n' | sed 's/:.*$//'
```

예상 출력: `cledyu-api-aws` 에 `access_key_id`, `secret_access_key`(필수 — 없으면 api 기동 차단),
`cledyu-api-tailscale` 에 `tailscale_authkey`, `api_tailscale_authkey`(라이브 터미널 — 없으면 SSM 채점만 되고 터미널 비활성).
없으면 Vault 등록(`vault kv put cledyu/aws/api ...`) → ESO 동기화를 확인한다.

라이브 터미널(브라우저 터미널→EC2 세션, api 파드 tsnet)을 쓰려면 같은 Vault 경로에
`api_tailscale_authkey` 도 시드해야 한다. 이 키는 별도 ExternalSecret 로 주입되므로 따로 확인한다.

```bash
kubectl -n api get externalsecret cledyu-api-tailscale
```

미시드 시 이 ExternalSecret 만 Degraded(Not Ready) 로 남고 api 는 정상 기동하나(deployment env
`optional: true`) 라이브 터미널이 비활성이다 — Degraded 는 '라이브 터미널 키 시드 누락' 신호다.
`vault kv patch cledyu/aws/api api_tailscale_authkey=tskey-...` 로 시드하면 Healthy 로 전환된다.

### 2. EC2 세션 라우팅 확인

api 로그에서 버스트 결정을 확인한다.

```bash
kubectl -n api logs deploy/api | grep -E "오버플로우|bursting|ec2"
```

예상 출력: `온프렘 세션 풀 만석 — EC2 오버플로우로 버스트` (온프렘 만석 시).

세션 응답의 `vm_provider` 가 `ec2` 인지 확인한다(프론트/Network 탭 또는 직접 호출).

### 3. EC2 세션 인스턴스 조회

```bash
aws ec2 describe-instances --region ap-northeast-2 \
  --filters "Name=tag:cledyu.io/managed-by,Values=cledyu-session" \
            "Name=instance-state-name,Values=pending,running" \
  --query 'Reservations[].Instances[].{id:InstanceId,session:Tags[?Key==`cledyu.io/session-id`]|[0].Value,expires:Tags[?Key==`cledyu.io/expires-at`]|[0].Value,state:State.Name}' \
  --output table
```

예상 출력: 활성 세션 인스턴스 목록(session-id·만료시각·상태).

### 4. SSM 채점 경로 확인

```bash
# 인스턴스가 SSM 에 등록됐는지(채점 가능 조건)
aws ssm describe-instance-information --region ap-northeast-2 \
  --query 'InstanceInformationList[].{id:InstanceId,ping:PingStatus}' --output table
```

예상 출력: 세션 인스턴스가 `Online`. `ConnectionLost`/누락이면 SSM Agent·인스턴스 프로파일
(`AmazonSSMManagedInstanceCore`)·아웃바운드 443 을 점검한다.

### 5. 라이브 터미널/IDE(tailnet) 확인

```bash
# 세션 인스턴스가 tailnet 에 가입했는지(MagicDNS 호스트네임 lab-<sessionID>)
tailscale status | grep "lab-"
```

미가입이면 `CLEDYU_AWS_TAILSCALE_AUTH_KEY`(ESO) 누락 또는 만료를 의심한다 — 이 경우 EC2 세션은
SSM 채점만 가능하고 라이브 터미널/IDE 는 동작하지 않는다.

## 비용 관리 / 수동 인스턴스 청소

reaper 가 TTL 만료·타임아웃 인스턴스를 자동 terminate 하지만, 오작동·고아 인스턴스 의심 시
수동 확인·청소한다.

```bash
# 만료 시각이 지났는데도 살아있는 인스턴스(고아 후보) 조회
NOW=$(date -u +%Y-%m-%dT%H:%M:%SZ)
aws ec2 describe-instances --region ap-northeast-2 \
  --filters "Name=tag:cledyu.io/managed-by,Values=cledyu-session" \
            "Name=instance-state-name,Values=running" \
  --query "Reservations[].Instances[?Tags[?Key=='cledyu.io/expires-at' && Value<'${NOW}']].InstanceId" \
  --output text
```

위 명령이 인스턴스 ID 를 반환하면 reaper 가 회수하지 못한 것이다. api reaper 로그를 확인하고,
긴급 시 수동 terminate 한다(과금 차단):

```bash
aws ec2 terminate-instances --region ap-northeast-2 --instance-ids <i-...>
```

## 검증

- `aws ec2 describe-instances` 의 활성 세션 수가 `CLEDYU_AWS_MAX_ACTIVE_SESSIONS` 이하
- 만료 시각이 지난 running 인스턴스가 없음(reaper 정상)
- EC2 세션에서 스텝 검증 요청 시 validation-engine 이 SSM 으로 채점, 결과 왕복

## 롤백 / 오버플로우 비활성화

EC2 오버플로우를 끄려면 `gitops/apps/api/values.yaml` 의 `aws.enabled: false`(또는
`aws.maxActiveSessions: 0`)로 되돌린다. 신규 세션은 KubeVirt 전용으로 라우팅되고, 기존 EC2
세션은 TTL 만료 시 reaper 가 정리한다. 즉시 모두 정리하려면 위 수동 terminate 를 쓴다.

## 참고

- 인프라: `infra/terraform/aws/` (Launch Template/IAM/SG/Budgets)
- 자격증명: `infra/kubernetes/external-secrets/cledyu-api-aws-externalsecret.yaml`,
  `cledyu-validation-engine-aws-externalsecret.yaml`
- 코드: `apps/api/internal/{ec2,session}`, `apps/validation-engine/internal/executor/ec2.go`
