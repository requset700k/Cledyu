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

### 5.2 tagged authkey 발급 — **키 A 만 정적 배선, 키 B(세션)는 동적 발급(#307, PR #309 구현)**

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

  즉 세션 키 B 는 **운용 가능한 정적 fallback 이 없다.** 그래서 api 가 세션 프로비저닝마다 Tailscale
  API(`POST /api/v2/tailnet/-/keys`)로 **세션별 one-off(비재사용)+ephemeral+짧은 만료** authkey 를
  **동적 발급**하는 코드 변경(**이슈 #307**)이 필요했고, 이는 **PR #309 로 구현 완료**됐다(코드:
  `apps/api/internal/ec2/authkey.go`, provisioner 배선, deployment env `CLEDYU_AWS_TAILSCALE_API_KEY`
  +`CLEDYU_AWS_TAILSCALE_OAUTH_CLIENT_ID`). **활성화 절차**:

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
    4) 설정되면 세션은 동적 키를 쓴다(발급 실패 시 fail-secure — 그 세션만 터미널 비활성, 정적 키로
       폴백 안 함). 정적 `tailscale_authkey` 는 **동적 모드에서 세션에 쓰이지 않는다**(dead). §5.3 대로
       기존 정적 세션 키 B 는 Vault 에서 비우고(동적 전용) ES 의 `tailscale_authkey` data 항목도 함께
       주석 처리한다 — 규칙: ES data 항목 존재 ⟺ Vault property 시드됨.
       - **레거시 세션 배수(Codex)**: 정적 키를 비우기 **전에** pre-#309(태그 `cledyu.io/tailnet`
         부재) EC2 세션이 실행 중이 아닌지 확인한다. 태그 없는 레거시 세션은 도달성 판단이 정적 키
         유무로 폴백하는데, 정적 키를 비우면 만료(≤세션 TTL)까지 터미널을 잃는다. 활성 EC2 에
         `cledyu.io/tailnet=1` 을 backfill 하거나, 그 세션들이 만료되도록 두고 전환한다.
       - **TTL 확인(Codex)**: `session_key_ttl_seconds`(기본 1800) 가 EC2 부팅+cloud-init(apt)+SSM
         지연보다 짧으면 키가 `tailscale up` 전에 만료돼 가입이 실패한다(인스턴스는 tag=1 이라 터미널이
         광고돼도 dial 실패). 첫 세션에서 실제 접속까지 반드시 실검증하고, 부팅이 느리면 TTL 을 올린다.

  활성화(위 절차) 전까지는 §5.3 대로 정적 세션 키 B 를 비워 두고 EC2 라이브 터미널을 데모/운영
  경로로 두지 않는다(데모 필요 여부는 §9 참조).

> Ephemeral: 노드 종료 시 tailnet 목록에서 자동 정리.

### 5.3 Vault — 키 A 주입 + **기존 세션 키 B 는 폐기·제거**

api authkey(A)=`api_tailscale_authkey` 를 주입하되, **세션 키 B(`tailscale_authkey`)는 보존하지
말고 반드시 비운다.** 현재 Vault 엔 2026-07-14 드릴 배선으로 세션 키가 이미 들어 있는데, 이걸
그대로 두면 Secret→env(`CLEDYU_AWS_TAILSCALE_AUTH_KEY`)로 계속 주입되어 api 가 **세션 cloud-init 에
그 키를 다시 bake**(`cloudinit.go`, authKey 가 비어있지 않으면 bake)하고, 그 세션은 tailnet 에 가입해
인스턴스 태그 `cledyu.io/tailnet=1` 이 붙어 **EC2 `terminal_url` 도 계속 광고**된다(PR #309 이후 광고
게이트는 정적 키 유무가 아니라 세션별 `TailnetEnabled` — 즉 정적 키로라도 가입하면 광고). 즉
"키 B 를 정적 배선하지 않는다"는 §5.2 목표는 **키를 안 넣는 것만으로는 달성되지 않고, 이미 있는
값을 비워야** 성립한다. (Codex PR #308 P1.)

```bash
export VAULT_ADDR=...   # 브레이크글래스 절차대로 로그인
# 1) 키 A 주입 + 세션 키 B 를 빈 값으로 (기존 AWS 키 access_key_id/secret_access_key 는 patch 라 보존)
vault kv patch cledyu/aws/api \
  api_tailscale_authkey='<키 A: tag:cledyu-api>' \
  tailscale_authkey=''
# 2) Tailscale admin 에서 기존 세션 키 B 자체를 revoke — 새 장치 등록만 막는다(기등록 장치는 유지됨)
#    login.tailscale.com/admin/settings/keys → 해당 키 Revoke
# 3) 이미 그 키로 등록된 장치를 삭제 — authkey revoke 는 기존 등록 노드의 접근을 끊지 못한다.
#    학습자가 키를 읽어 외부 장치를 tag:lab-ec2 로 등록했을 수 있으므로 반드시 조회 후 삭제/만료.
TS_APIKEY=<auth_keys 아닌 device write 권한 API 토큰>
curl -sS -H "Authorization: Bearer $TS_APIKEY" \
  https://api.tailscale.com/api/v2/tailnet/-/devices \
 | python3 -c "import sys,json;[print(d['id'],d['hostname'],d.get('tags')) for d in json.load(sys.stdin)['devices'] if 'tag:lab-ec2' in (d.get('tags') or []) or d['hostname'].startswith('lab-')]"
#   위 목록에서 정당한 활성 세션이 아닌(드릴 잔존·외부 등록) device 를 삭제:
curl -sS -X DELETE -H "Authorization: Bearer $TS_APIKEY" \
  https://api.tailscale.com/api/v2/device/<deviceID>
```

> `tailscale_authkey=''`(빈 값)이면 ESO 가 Secret 값을 빈 값으로 덮고, api 가 세션 cloud-init 에
> authkey 를 bake 하지 않는다 → 세션이 tailnet 에 안 붙어 인스턴스 태그 `cledyu.io/tailnet=0` 이
> 되고 `session.TailnetEnabled=false` 라 터미널 광고도 멈춘다. **단 authkey revoke 만으로는
> 부족** — Tailscale authkey 는 장치 등록용이라, 그 키로 이미 승인된 장치는 키 폐기 후에도 node key 만료
> 또는 device 삭제 전까지 tailnet 접근을 유지한다([공식 문서](https://tailscale.com/kb/1085/auth-keys)).
> 그래서 위 3) 에서 기등록 device 를 명시적으로 삭제한다. #307(동적 발급)은 PR #309 로 구현됐다 — §5.2
> 활성화 절차대로 api 에 Tailscale API 키(`auth_keys` write)를 Vault→ESO 로 주입하면 api 가 세션별로
> 발급한다(정적 property 미사용).

### 5.4 동기화 + api 재시작

```bash
# ESO 강제 재동기화(또는 refreshInterval 대기)
kubectl -n api annotate externalsecret cledyu-api-tailscale force-sync=$(date +%s) --overwrite
# 키 A 반영 + 세션 키 B 가 빈 값으로 덮였는지 확인(값 길이 0 이어야 함)
kubectl -n api get secret cledyu-api-tailscale \
  -o jsonpath='{.data.api_tailscale_authkey}' | base64 -d | wc -c   # > 0 이어야
kubectl -n api get secret cledyu-api-tailscale \
  -o jsonpath='{.data.tailscale_authkey}' | base64 -d | wc -c       # 0 이어야(키 B 비움)
# api 재시작 → tsnet 이 tagged api authkey 로 재가입(ephemeral 이라 새 노드 = 태그 적용)
kubectl -n api rollout restart deployment/api
kubectl -n api rollout status deployment/api
```

이 단계로 **api 파드(키 A)** 만 tagged 노드가 되고, **세션 키 B 는 비워져** api 가 더 이상 세션
cloud-init 에 bake 하지 않으며 EC2 `terminal_url` 도 광고하지 않는다(프론트는 placeholder 유지).
**세션 EC2(키 B) 태깅·라이브 터미널은 §5.2 활성화(동적 발급, PR #309 구현) 후** 동적 발급된 tagged
authkey 로 가입한다. 활성화 전에는 §6 검증의 터미널 실검증도 성립하지 않는다(정적 키 B 를 비워 뒀으므로).

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

EC2 오버플로우 **라이브 터미널**은 세션 키 B 동적 발급(#307, PR #309 로 구현)이 **활성화**돼야
안전·정상 동작한다. §5.2 활성화 전에는 **데모 경로에서 EC2 라이브 터미널을 제외**하는 것을 기본으로 한다.

- 온프렘 KubeVirt 세션 터미널(virtctl 경로)은 이 이슈·키 B 와 **무관하게 정상** — 데모는 이쪽으로.
- EC2 오버플로우 자체(용량 초과 시 AWS 로 스핀업)는 터미널과 별개로 실증 가능(세션 기동까지).
- §5.2 활성화(동적 발급) 후 EC2 라이브 터미널까지 데모 경로 편입 가능.
