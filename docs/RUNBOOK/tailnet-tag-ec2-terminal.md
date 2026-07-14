# RUNBOOK: EC2 오버플로우 라이브 터미널 — Tailnet 태그 전환

- 작성: 2026-07-14 (DR 드릴에서 터미널 미기동 블로커로 발견)
- 대상: tailnet 관리자(팀). 라이브 팀 tailnet ACL·authkey·Vault 를 다루므로 팀 합의 하에 적용.
- 관련: [[project_ec2_overflow_e2e]] 잔여 결함 C, `apps/api/internal/ec2/`, `apps/api/internal/tailnet/`

## 1. 증상 / 배경

온프렘 KubeVirt 풀이 만석이면 세션이 AWS EC2 로 오버플로우한다. EC2 세션의 **라이브 터미널**은
api 파드(tsnet 노드)가 세션 EC2 에 **tailnet SSH** 로 붙어 PTY 를 WS 로 프록시한다. DR 드릴에서
오버플로우는 정상 발동했으나 **터미널이 안 떴다**:

```
ec2 ssh terminal connect failed  addr=lab-<id>
error: ... Tailscale SSH requires an additional check   (또는 초기엔 no such host)
```

## 2. 근본 원인 (2026-07-14 실측)

1. **tailnet 노드가 전부 user 소유(무태그).** `cledyu-api`, `lab-<id>` 모두 `user=ykgoesdumb@github`,
   `tags=(none)`. 즉 api/세션 authkey 가 **tagged 가 아니라 user authkey** 로 발급돼 있다(과거 ad-hoc 배선).
2. **tailnet ACL 이 기본 템플릿** — `ssh` 는 기본 `check` 규칙 1개(`src:autogroup:member,
   dst:autogroup:self, users:[nonroot,root]`), `tagOwners` 미선언. `check` 는 브라우저 재인증을
   요구하는데 api 는 머신이라 못 해 SSH 가 끊긴다(코드는 `accept` 전제 — `terminal.go` 주석
   "Tailscale SSH(accept) 면 none 인증이 먼저 통과").
3. **코드는 태그(`tag:cledyu-api`→`tag:lab-ec2`)를 전제**하나(config 주석·`cledyu-api-tailscale`
   ESO) 실제 tailnet 엔 태그가 없어, 태그 규칙을 못 쓴다.

임시로 `{action:accept, src:autogroup:member, dst:autogroup:self, users:["lab"]}` 규칙을 넣어봤으나
**`autogroup:self` 가 매칭되지 않아 여전히 check** 였다(멀티유저 팀 tailnet). → 신뢰 가능한 해결은
**태그 기반**이다.

## 3. 현재 배선 상태 (무엇이 되어있고 무엇이 안 됐나)

**이미 됨(코드/gitops 변경 불필요):**

- api 파드 tsnet 가입: `main.go` 가 `cfg.AWS.APITailscaleAuthKey` 로 `tailnet.New`(hostname `cledyu-api`).
- api 터미널 다이얼러: `SetEC2Dial(node.Dial)` 로 tsnet 다이얼러 주입(MagicDNS 해석 포함). 배선 정상.
- 두 authkey env 배선: `deployment.yaml` 이 Secret **`cledyu-api-tailscale`** 에서
  `tailscale_authkey`(세션 cloud-init)·`api_tailscale_authkey`(api tsnet)를 주입(둘 다 optional).
- ESO: `infra/kubernetes/external-secrets/cledyu-api-tailscale-externalsecret.yaml` 가 Vault
  **`cledyu/aws/api`**(KV mount `cledyu`, path `aws/api`)의 `tailscale_authkey`·`api_tailscale_authkey`
  property 를 Secret `cledyu-api-tailscale` 로 동기화. 라이브 Secret 에 두 키 존재 확인.

**안 됨(= 이 런북이 고치는 것):**

- 두 authkey 가 **tagged 가 아님**(user authkey) → 노드 무태그.
- tailnet ACL 에 `tagOwners`·태그 SSH accept 규칙 없음.

## 4. 목표 상태

- `cledyu-api` 노드 = `tag:cledyu-api`, 세션 EC2 노드 = `tag:lab-ec2`.
- ACL `ssh`: `tag:cledyu-api → tag:lab-ec2` 를 `users:["lab"]` 로 **accept**(머신 SSH). 사람 self-SSH 는 `check` 유지.

## 5. 적용 절차 (순서 중요 — 팀 tailnet 관리자)

> tagOwners 가 있어야 tagged authkey 를 만들 수 있으므로 **ACL(tagOwners) 먼저**.

### 5.1 Tailscale ACL — tagOwners + 태그 SSH 규칙

`login.tailscale.com/admin/acls` (GitHub SSO). `tagOwners`(현재 주석)와 `ssh` 를 아래로:

```jsonc
"tagOwners": {
  "tag:cledyu-api": ["autogroup:admin"],
  "tag:lab-ec2":    ["autogroup:admin"],
},

"ssh": [
  // api(머신) 라이브 터미널: tag:cledyu-api → tag:lab-ec2 는 user "lab" 로 accept.
  { "action": "accept", "src": ["tag:cledyu-api"], "dst": ["tag:lab-ec2"], "users": ["lab"] },
  // 사람 self-SSH 는 check 유지.
  { "action": "check", "src": ["autogroup:member"], "dst": ["autogroup:self"], "users": ["autogroup:nonroot", "root"] },
],
```

- 2026-07-14 임시로 넣은 `{users:["lab"], autogroup:self}` accept 규칙이 있으면 **삭제**(위 태그 규칙이 대체).
- API 로 할 경우: `GET /api/v2/tailnet/-/acl` → 수정 → `POST .../acl/validate` → `POST .../acl`(If-Match).

### 5.2 tagged reusable+ephemeral authkey 2개 발급

`login.tailscale.com/admin/settings/keys` → Generate auth key:

- 키 A: **Reusable + Ephemeral + Tags `tag:cledyu-api`** (api 파드 tsnet 용).
- 키 B: **Reusable + Ephemeral + Tags `tag:lab-ec2`** (세션 EC2 cloud-init 용).

> Ephemeral: 노드 종료 시 tailnet 목록에서 자동 정리. Reusable: 파드 재시작·세션 반복 생성에 재사용.

### 5.3 Vault 에 tagged authkey 주입

세션 authkey(B)=`tailscale_authkey`, api authkey(A)=`api_tailscale_authkey`. **`kv patch`** 로 기존
AWS 키(access_key_id/secret_access_key)를 보존:

```bash
export VAULT_ADDR=...   # 브레이크글래스 절차대로 로그인
vault kv patch cledyu/aws/api \
  tailscale_authkey='<키 B: tag:lab-ec2>' \
  api_tailscale_authkey='<키 A: tag:cledyu-api>'
```

### 5.4 동기화 + api 재시작

```bash
# ESO 강제 재동기화(또는 refreshInterval 대기)
kubectl -n api annotate externalsecret cledyu-api-tailscale force-sync=$(date +%s) --overwrite
# 새 authkey 반영 확인
kubectl -n api get secret cledyu-api-tailscale -o go-template='{{range $k,$v := .data}}{{$k}}{{"\n"}}{{end}}'
# api 재시작 → tsnet 이 tagged api authkey 로 재가입(ephemeral 이라 새 노드 = 태그 적용)
kubectl -n api rollout restart deployment/api
kubectl -n api rollout status deployment/api
```

세션 EC2 는 **새로 만들어지는 것부터** tagged(B) authkey 로 가입한다. 기존 user-owned 세션은 종료되게 둔다.

## 6. 검증

```bash
# (1) 노드 태그 확인 — cledyu-api 가 tag:cledyu-api 인가
curl -sS -H "Authorization: Bearer $TS_APIKEY" https://api.tailscale.com/api/v2/tailnet/-/devices \
 | python3 -c "import sys,json;[print(d['hostname'],d.get('tags')) for d in json.load(sys.stdin)['devices'] if 'cledyu-api' in d['hostname']]"

# (2) 오버플로우 강제 → 터미널 실검증 (KubeVirt cap 을 현재 api-카운트 이하로 잠깐 낮춤)
#     주의: service-api 는 ServerSideApply 라 kubectl set env override 를 selfHeal 이 안 지운다.
#     검증 후 반드시 'set env ...-' 로 제거해 원복할 것.
kubectl -n api set env deployment/api CLEDYU_KUBEVIRT_MAX_ACTIVE_SESSIONS=<현재 활성 수>
#   → 브라우저에서 새 랩 시작 → EC2 오버플로우 → 세션 노드가 tag:lab-ec2 로 가입하는지 +
#     api 로그에 SSH 성공(에러 없음) + 브라우저에 터미널 뜨는지 확인.
kubectl -n api set env deployment/api CLEDYU_KUBEVIRT_MAX_ACTIVE_SESSIONS-   # 원복

# (3) SSH accept 직접 확인(선택) — tailnet 노드에서
#     ssh -o BatchMode=yes lab@<세션 tailnet IP> 'echo ok'  → check URL 없이 즉시 성공이면 accept OK
```

- `lab-ssh-test`(장수 테스트 VM) 같은 것이 api 세션 카운트에 안 잡히면 cap 계산이 어긋나니 유의
  (2026-07-14 첫 시도가 이 때문에 KubeVirt 로 갔다).

## 7. 롤백

- Vault authkey 원복: `vault kv patch cledyu/aws/api tailscale_authkey=<이전> api_tailscale_authkey=<이전>` → api 재시작.
- ACL: 태그 규칙 제거하고 기본 check 로 되돌림(터미널만 다시 비활성, 나머지 무관).

## 8. 참고 — 2026-07-14 남긴 것(적용 시 정리)

- tailnet ACL 에 임시 `{action:accept, ..., autogroup:self, users:["lab"]}` 규칙(비작동) — 5.1 에서 삭제.
- KubeVirt cap override 는 그날 원복 완료(기본 24).
- 드릴에 쓴 Tailscale **API access token 은 폐기**할 것(있다면).
