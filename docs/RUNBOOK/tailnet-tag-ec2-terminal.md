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

### 5.2 tagged authkey 발급 — **세션 키(B)는 reusable 금지(Codex P1 보안)**

`login.tailscale.com/admin/settings/keys` → Generate auth key:

- **키 A (api 파드 tsnet, `tag:cledyu-api`)**: **Reusable + Ephemeral**. api 파드 env(Secret)에만
  들어가고 학습자에 노출되지 않으므로 reusable 안전.
- **키 B (세션 EC2, `tag:lab-ec2`)**: **reusable 로 발급하지 말 것.** 이 키는 `renderCloudInit` 이
  세션 EC2 **user-data 의 `tailscale up --authkey` 에 baked** 하는데, 세션 `lab` 계정은 sudo 라
  학습자가 user-data/cloud-init 흔적(예: `/var/lib/cloud/`, IMDS)에서 키를 읽을 수 있다. reusable
  이면 탈취한 키로 **세션 종료 후에도 외부 장치를 `tag:lab-ec2` 로 계속 등록**(tailnet 잔존 접근).

  - **권장(정식, 구현됨 — issue #307)**: api 가 세션 프로비저닝 시 Tailscale API
    (`POST /api/v2/tailnet/-/keys`)로 **세션별 one-off(비재사용)+ephemeral+짧은 만료** authkey 를
    **동적 발급**한다(코드 반영: `apps/api/internal/ec2/authkey.go`, provisioner 배선, deployment
    env `CLEDYU_AWS_TAILSCALE_API_KEY` + `CLEDYU_AWS_TAILSCALE_OAUTH_CLIENT_ID`). **활성화 절차**:
    1) Tailscale에서 **OAuth client**(scope `auth_keys`, `tag:lab-ec2` 발급 권한) 생성 → **client id** 와
       **client secret**(`tskey-client-...`) 확보.
       (tagOwners 에 `"tag:lab-ec2": ["<oauth-client 소유자>"]` 형태로 그 client 가 태그를 찍을 수 있어야 함.)
    2) `vault kv patch cledyu/aws/api tailscale_api_key='<client secret>' tailscale_oauth_client_id='<client id>'`.
       - 코드는 client secret 을 `/api/v2/oauth/token` 에서 **client_credentials 로 교환**해 짧은수명(1h)
         액세스 토큰을 얻고 만료 시 **자동 갱신**한다. OAuth 액세스 토큰을 직접 넣으면 안 된다 —
         1시간 뒤 발급이 끊긴다(이 교환 배선이 그 문제를 없앤 이유다).
       - 대안(비권장): tag 스코프가 아닌 **API 액세스 토큰**을 쓸 거면 `tailscale_api_key` 에 그 토큰만
         넣고 `tailscale_oauth_client_id` 는 비운다(코드가 직접 Bearer). 단 API 토큰은 사용자 소유·90일
         만료라 담당자 이탈·회전 부담이 있어 OAuth client 를 권장.
    3) ESO `cledyu-api-tailscale-externalsecret.yaml` 의 `tailscale_api_key`(및 OAuth 방식이면
       `tailscale_oauth_client_id`) 항목 주석 해제(**반드시 (2) 이후** — Vault 에 없으면 ES 전체가
       SyncError). → api 재시작.
    4) 설정되면 정적 `tailscale_authkey` 는 폴백으로만 남고 세션은 동적 키를 쓴다. 발급 실패 시
       fail-secure(그 세션만 터미널 비활성, 정적 키로 폴백 안 함).
  - **잠정(정식 활성화 전, 정적 키 유지 시)**: 정적 `tailscale_authkey` 를 **짧은 만료 + ephemeral**
    로 발급하고 **자주 rotate**, 탈취 창을 최소화(잔여 위험 명시적 수용). 절대 만료 없는 reusable 금지.

> Ephemeral: 노드 종료 시 tailnet 목록에서 자동 정리.

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

- tailnet ACL 임시 `autogroup:self` accept 규칙(비작동) — **2026-07-14 제거 완료**(기본 check 로 복원).
- KubeVirt cap override — **2026-07-14 원복 완료**(기본 24).
- 드릴 테스트 EC2 세션(`i-0fe2cc639eca304fc`) — **terminate 완료**. 관련: 로그아웃 시 세션 미종료(이슈 #306).
- 드릴에 쓴 Tailscale **API access token 은 폐기**할 것.
