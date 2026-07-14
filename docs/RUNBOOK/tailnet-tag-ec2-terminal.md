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

### 5.2 tagged authkey 발급 — **키 A 만 정적 배선, 키 B(세션)는 #307 선행 필수**

`login.tailscale.com/admin/settings/keys` → Generate auth key:

- **키 A (api 파드 tsnet, `tag:cledyu-api`)**: **Reusable + Ephemeral**. api 파드 env(Secret)에만
  들어가고 학습자에 노출되지 않으므로 reusable 안전. → §5.3~5.4 정적 배선 대상은 **키 A 뿐**.
- **키 B (세션 EC2, `tag:lab-ec2`)**: **정적 키(Vault 에 고정 주입)로는 안전하게 배선할 수 없다.**
  이 키는 `renderCloudInit` 이 세션 EC2 **user-data 의 `tailscale up --authkey` 에 baked** 하는데,
  세션 `lab` 계정은 sudo 라 학습자가 user-data/cloud-init 흔적(예: `/var/lib/cloud/`, IMDS)에서
  키를 읽을 수 있다. 정적 키에는 **안전한 형태가 없다**(Codex PR #305 P1, 2차 리뷰):

  - **reusable 정적 키**: 탈취하면 **세션 종료 후에도 외부 장치를 `tag:lab-ec2` 로 계속 등록**
    (tailnet 잔존 접근). → 보안상 불가.
  - **one-off/비재사용 정적 키**: `renderCloudInit` 은 **하나의 `cfg.AWSConfig.TailscaleAuthKey`
    를 모든 세션 user-data 에 반복 bake** 하는데 one-off 키는 [1회만 소비 가능](https://tailscale.com/kb/1085/auth-keys)
    하므로, Vault 에 넣은 정적 one-off 키는 **첫 세션만 성공**하고 이후 세션은 tailnet 가입이
    깨져 **라이브 터미널/IDE 가 다시 미기동**한다. → 운영 불가.

  즉 세션 키 B 는 **운용 가능한 정적 fallback 이 없다.** EC2 라이브 터미널을 신뢰 가능하게 켜려면
  api 가 세션 프로비저닝마다 Tailscale API(`POST /api/v2/tailnet/-/keys`)로 **세션별 one-off
  (비재사용)+ephemeral+짧은 만료** authkey 를 **동적 발급**하도록 하는 코드 변경(**이슈 #307**)이
  **선행 필수**다. #307 전에는 §5.3~5.4 로 키 B 를 정적 배선하지 말 것 —
  EC2 라이브 터미널을 데모/운영 경로로 두지 않는다(데모 필요 여부는 §9 참조).

> Ephemeral: 노드 종료 시 tailnet 목록에서 자동 정리.

### 5.3 Vault 에 tagged authkey 주입 (키 A 만)

api authkey(A)=`api_tailscale_authkey`. **`kv patch`** 로 기존 AWS 키(access_key_id/
secret_access_key)와 세션 키 property 를 보존:

```bash
export VAULT_ADDR=...   # 브레이크글래스 절차대로 로그인
vault kv patch cledyu/aws/api \
  api_tailscale_authkey='<키 A: tag:cledyu-api>'
```

> **`tailscale_authkey`(세션 키 B)는 여기서 정적 주입하지 않는다** — §5.2 참조. #307(동적 발급)
> 구현 후 api 가 세션별로 발급하며, api 가 Tailscale API 로 키를 만들 권한(`auth_keys` write)을
> 갖도록 별도 Tailscale API 키를 Vault→ESO 로 주입한다(#307 범위).

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

이 단계로 **api 파드(키 A)** 만 tagged 노드가 된다. **세션 EC2(키 B) 태깅·라이브 터미널은 #307
구현 후** 동적 발급된 tagged authkey 로 가입한다. #307 전에는 §6 검증의 터미널 실검증도 성립하지
않는다(정적 키로는 첫 세션만 뜨거나 아예 미기동).

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

- Vault authkey(키 A) 원복: `vault kv patch cledyu/aws/api api_tailscale_authkey=<이전>` → api 재시작.
- ACL: 태그 규칙 제거하고 기본 check 로 되돌림(터미널만 다시 비활성, 나머지 무관).

## 8. 참고 — 2026-07-14 남긴 것(적용 시 정리)

- tailnet ACL 임시 `autogroup:self` accept 규칙(비작동) — **2026-07-14 제거 완료**(기본 check 로 복원).
- KubeVirt cap override — **2026-07-14 원복 완료**(기본 24).
- 드릴 테스트 EC2 세션(`i-0fe2cc639eca304fc`) — **terminate 완료**. 관련: 로그아웃 시 세션 미종료(이슈 #306).
- 드릴에 쓴 Tailscale **API access token 은 폐기**할 것.

## 9. 데모 판단 (2026-07-22 시연)

EC2 오버플로우 **라이브 터미널**은 세션 키 B 동적 발급(#307)이 선행돼야 안전·정상 동작하므로,
#307 미완 상태에서는 **데모 경로에서 EC2 라이브 터미널을 제외**하는 것을 기본으로 한다.

- 온프렘 KubeVirt 세션 터미널(virtctl 경로)은 이 이슈·키 B 와 **무관하게 정상** — 데모는 이쪽으로.
- EC2 오버플로우 자체(용량 초과 시 AWS 로 스핀업)는 터미널과 별개로 실증 가능(세션 기동까지).
- #307 완료 시 §5.2 의 동적 발급으로 EC2 라이브 터미널까지 데모 경로 편입 가능.
