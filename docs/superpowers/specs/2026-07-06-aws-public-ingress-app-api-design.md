# 설계: AWS 공개 노출 A안 — app/api/auth.cledyu.com (와일드카드 ACM + WAF)

- 작성일: 2026-07-06
- 상태: 승인됨(브레인스토밍 완료), 구현계획 대기
- 대상 마감: 2026-07-22 시연
- 관련 브랜치/스택: `infra/terraform/aws/`, `gitops/apps/{web,api}`, Keycloak realm `cledyu-learn`

## 1. 목표와 성공 기준

이번 작업은 학습자용 앱을 공개 인터넷에 노출해, **외부(비-Tailscale) 사용자가 구글로 가입/로그인하고 랩 세션을 시작하는 E2E 흐름**을 시연에서 보여주는 것이 목표다.

성공 기준(Definition of Done):

- 외부망 사용자가 `https://app.cledyu.com` 접속 → "구글로 로그인" → 구글 동의 →
  `auth.cledyu.com` 콜백 → `app.cledyu.com` 복귀·로그인 상태 유지
- 로그인 후 랩 시작 → `api.cledyu.com` 호출 200, 세션쿠키 도메인 `.cledyu.com`, 랩 세션 기동
- 공개 TLS는 ALB의 ACM 와일드카드로 종단, 앞단에 AWS WAF(관리형 + rate-based) 부착
- 전 스택은 `var.enable_public_ingress` 로 게이트되어 데모 후 원클릭 철거 가능

비목표(YAGNI):

- `.cledyu.local` 로의 "인증된" 접근 병행(아래 4장 트레이드오프 참고) — 하지 않는다
- ALB L7 호스트 라우팅/다중 타깃그룹 — 하지 않는다(Traefik이 담당)
- Tailscale Funnel 등 비-AWS 공개 경로 — 하지 않는다(AWS 포커스·WAF 결정과 상충)

## 2. 현재 상태(실측 기준선)

- `infra/terraform/aws/public-ingress.tf`: **auth.cledyu.com 단일 호스트만** 공개.
  단일 ACM 인증서(`aws_acm_certificate.auth`, `domain_name = public_keycloak_host`),
  ALB + 단일 타깃그룹(→프록시:80), 443/80 리스너, tailnet 리버스프록시 EC2(Caddy),
  Route53 A(ALIAS) 1개. **WAF 없음.**
- 프록시 Caddy는 단일 `public_host`/`upstream_url` 로 렌더(`cloud-init/keycloak-proxy.yaml.tftpl`),
  Host 보존해 Traefik LB `https://10.10.0.101` 로 전달, backend TLS skip-verify.
- gitops `web` = `app.cledyu.local`, `api` = `api.cledyu.local`(표준 k8s Ingress,
  `ingressClassName: traefik`, cert-manager 내부 CA TLS).
- api는 부분적으로 공개 인지: `keycloak.url = https://auth.cledyu.com`(구글 broker용).
  그러나 `redirectUri`·`cookieDomain`·`frontendUrl` 은 아직 `.cledyu.local`.

## 3. 아키텍처 & E2E 트래픽 흐름

세 호스트(app/api/auth) 공통 경로:

```
학습자 브라우저(공개 인터넷)
  -> Route53  app|api|auth.cledyu.com  (A ALIAS -> ALB)
  -> AWS WAF WebACL  (관리형 룰 + IP rate-based)
  -> ALB :443  (와일드카드 ACM *.cledyu.com, TLS 종단) / :80 -> 443 리다이렉트
  -> 단일 타깃그룹 -> tailnet 프록시 EC2(Caddy :80)
  -> (tailnet WireGuard) -> Traefik LB 10.10.0.101 :443  (Host 보존)
  -> Host 기준 in-cluster 라우팅:
       app.cledyu.com -> web(Next.js)
       api.cledyu.com -> api(BFF, Go)
       auth.cledyu.com -> Keycloak(realm cledyu-learn)
```

핵심 설계 성질:

- **ALB는 L7 라우팅을 하지 않는다.** 단일 타깃그룹(→프록시)으로 전부 보내고, 호스트 분기는
  Traefik이 담당(기존 라우팅 재사용). ALB 역할 = 공개 TLS 종단 + WAF 부착점 + 안정 DNS.
- **프록시는 Host를 보존**한다. Caddy가 `*.cledyu.com` 요청을 Host 그대로 Traefik에 전달.
- **인증서 1장**: `*.cledyu.com`(+ apex `cledyu.com` SAN) 와일드카드 → 리스너 cert 1개로 세 호스트 커버.
- **`.local` 라우팅은 병행 유지**하되, 인증 세션 config는 `.com` 단일(4장 참고).

E2E 로그인 흐름(구글):

1. 브라우저 `app.cledyu.com` → web 로그인 버튼 → `api.cledyu.com`이 Keycloak `auth.cledyu.com` authorize로 리다이렉트
2. Keycloak → 구글 → 콜백 `auth.cledyu.com/realms/cledyu-learn/broker/google/endpoint`
3. Keycloak → `api.cledyu.com/api/v1/auth/callback` → api가 세션쿠키(`.cledyu.com`) 발급 → `app.cledyu.com` 복귀
4. 이후 web→api 호출은 `api.cledyu.com`, 쿠키 도메인 `.cledyu.com`로 동작

## 4. 설계 결정과 트레이드오프

### 4.1 접근 비교(채택: A)

- **A. 단일 프록시 재사용 + Traefik 호스트 라우팅(채택)** — 기존 t3.nano 프록시 하나를
  `*.cledyu.com` 전부 Traefik로 Host 보존 전달하도록 일반화. ALB 단일 타깃그룹. 리소스 최소,
  Traefik 라우팅 재사용, 비용 최저. 단점: 프록시 단일 장애점(데모 규모엔 무방).
- B. ALB 호스트 라우팅 + 다중 타깃그룹 — AWS 네이티브지만 프록시/TG 복수로 비용·복잡도↑,
  Traefik이 이미 하는 일 중복. 종료형 데모엔 오버엔지니어링. 기각.
- C. Tailscale Funnel(프록시/ALB 없이 공개) — AWS WAF·와일드카드 ACM 불가(ts.net cert),
  AWS 포커스·WAF 결정과 상충. 기각.

### 4.2 인증 호스트를 `.com` 단일로 전환(채택: a)

라우팅은 `.local`/`.com` 둘 다 살릴 수 있으나, **인증 세션 config(cookieDomain·frontendUrl·
redirectUri)는 응답당 단일값**이라 `.com` 하나로 간다. `.local` 로의 "인증된" 접근은 깨지고
(비인증 라우팅만 유지) 데모 대상이 공개 학습자이므로 `.com` 단일 인증이 맞다.

`.local` 인증 병행(대안 b)을 기각한 이유:

- 쿠키 도메인이 응답당 단일값이라 api가 요청 Host를 읽어 쿠키/redirect/frontend URL을 런타임
  분기해야 함 → **핸들러 코드 변경** 필요(values.yaml만으론 불가)
- OAuth 바운스의 호스트 일관성(시작 호스트를 `state`에 실어 모든 홉 유지)이 깨지기 쉬움
- 크로스도메인 쿠키 위해 `SameSite=None; Secure` 완화 + CORS 이중 오리진 → 공격면 증가
- 인증 E2E를 `.local`/`.com` 양쪽 검증 → 데모 직전 QA 2배
- 실익 거의 없음(내부 사용자도 `.com` 공개 경로 사용 가능)

## 5. 구현 상세

### 5.1 AWS 레이어 (`infra/terraform/aws/`)

파일: `public-ingress.tf`, `variables.tf`, `outputs.tf`, 신규 `waf.tf`

- **ACM**: `aws_acm_certificate.auth` → `domain_name = "*.cledyu.com"`,
  `subject_alternative_names = ["cledyu.com"]`. DNS 검증 for_each는 SAN 포함해 유지.
  리스너 `certificate_arn` = 와일드카드 검증 ARN. `public_keycloak_host` 변수는 유지하되
  인증서는 세 호스트 공용.
- **Route53**: `aws_route53_record` 를 `for_each = { auth, app, api }` 로 확장, 각 host를 같은
  ALB alias로. 신규 변수 `public_app_host = "app.cledyu.com"`, `public_api_host = "api.cledyu.com"`.
- **ALB/타깃그룹/리스너**: 구조 유지(단일 타깃그룹, 호스트 룰 없음). 헬스체크 경로만
  `/realms/cledyu-learn` → 프록시 로컬 `/healthz`(200)로 변경. interval 30s / healthy 2 /
  unhealthy 3 / matcher 200.
- **WAF(신규 `waf.tf`)**: `aws_wafv2_web_acl`(scope REGIONAL) + `aws_wafv2_web_acl_association`
  (→ALB ARN). 룰: `AWSManagedRulesCommonRuleSet`, `AWSManagedRulesKnownBadInputsRuleSet`,
  `AWSManagedRulesAmazonIpReputationList`(관리형 3종, **초기 count 모드**) + rate-based 룰
  (IP당 5분 **2000req**, block). `visibility_config` CloudWatch 메트릭 on. `enable_public_ingress`
  게이트.
- **프록시 Caddy 일반화(`cloud-init/keycloak-proxy.yaml.tftpl`)**: 단일 사이트에서
  `*.cledyu.com` 요청을 Host 보존해 Traefik LB(`https://10.10.0.101`, TLS skip-verify)로
  리버스프록시. `/healthz`는 프록시 로컬 200 응답. `keycloak_upstream_url` 변수를
  `traefik_upstream_url`(기본 `https://10.10.0.101`)로 일반화.
- **변수/게이팅**: 신규 `public_app_host`, `public_api_host`, `waf_rate_limit`(default 2000).
  `public_ingress_allowed_cidrs`(현재 0.0.0.0/0) 유지, 필요 시 검증단계에서 좁힘.
  전 스택 `enable_public_ingress`(기본 false) opt-in.

### 5.2 In-cluster 레이어 (gitops)

- **Traefik 라우팅**: web/api Ingress의 `ingress.host` 를 리스트로 확장(템플릿 `range`),
  `[app.cledyu.local, app.cledyu.com]` / `[api.cledyu.local, api.cledyu.com]` 둘 다 라우팅.
  cert-manager Certificate dnsNames에 `.com` 추가(내부 CA, 공개신뢰 불필요 — 프록시 skip-verify).
- **api config 플립(`gitops/apps/api/values.yaml`)**:
  - `keycloak.redirectUri`: `https://api.cledyu.com/api/v1/auth/callback`
  - `keycloak.cookieDomain`: `.cledyu.com`
  - `keycloak.frontendUrl`: `https://app.cledyu.com`
  - `keycloak.url`: 변경 없음(이미 `https://auth.cledyu.com`)
- **web config(`gitops/apps/web/values.yaml`)**: 백엔드 호출 URL은 **변경하지 않는다**.
  web은 `CLEDYU_BACKEND_URL = http://api.api.svc.cluster.local`(in-cluster)로 api를
  **서버사이드(Next.js route handler) 프록시**한다. 브라우저가 `api.cledyu.com`을 직접
  때리는 지점은 **OAuth 콜백 리다이렉트뿐**이다. 따라서 web은 `.com` 인그레스 호스트만
  추가(라우팅)하면 되고 backend URL 플립은 불필요·부적절.
- **Keycloak client**: realm `cledyu-learn` web/api 클라이언트 valid redirect URIs에
  `https://api.cledyu.com/*`, `https://app.cledyu.com/*` 추가(기존 `.local` 유지). 구글 콘솔
  authorized redirect(`auth.cledyu.com`)는 변경 불필요.

## 6. 에러 처리 · 헬스체크 · 게이팅

- **ALB 헬스체크는 프록시 생존만 판정**(`/healthz`, Traefik 안 탐). 업스트림 상태를 헬스체크에
  섞으면 프록시 교체 루프 발생 → 분리. Traefik 다운 시 5xx를 그대로 전달(디버깅 가능).
- **WAF count-first 롤아웃**: 관리형 룰을 처음엔 count 모드로 배포 → sampled requests로 정상
  학습자 오탐 관측 → 이상 없으면 block 전환. rate-based는 처음부터 block.
- **게이팅/롤백**: 전 스택 `enable_public_ingress=false` + apply 로 원클릭 철거. 앱 config
  플립은 gitops PR 분리 → ArgoCD 롤아웃, 문제 시 revert. `create_before_destroy` 유지로 무중단 교체.
- **비용/노출 가드**: 프록시 t3.nano·ALB·WAF 저비용. 데모 종료 후 게이트 off로 노출·과금 종료.

실패 모드별 관측 지점:

| 증상 | 1차 확인 |
|---|---|
| TLS/인증서 오류 | ACM 검증 상태, 리스너 cert ARN |
| 502/504(프록시→Traefik) | 프록시 Caddy 로그, `tailscale status`, `curl -k https://10.10.0.101 -H Host:...` |
| 403(WAF 차단) | WAF WebACL CloudWatch 메트릭·sampled requests 매칭 룰 |
| redirect_uri_mismatch | Keycloak client redirect URIs, 구글 콘솔 authorized redirect |
| 로그인 후 세션 유실 | 쿠키 도메인 `.cledyu.com`, `frontendUrl`, api 콜백 로그 |

## 7. 검증 계획

1. **Terraform 정적**: `fmt -check`, `validate`, `tflint`. `plan` 리뷰 — 와일드카드 ACM 1장,
   Route53 3개, WAF WebACL+association, 프록시 user_data 변경(=replace 예상)만 나오는지.
2. **인프라 실측(AWS_PROFILE=cledyu)**: ACM ISSUED·SAN 확인, `dig +short app.cledyu.com`→ALB,
   `curl -vI https://app.cledyu.com` TLS/상태코드, WAF count 모드 오탐 관측 후 block 전환,
   프록시 SSH `tailscale status`·`/healthz`·`curl -k https://10.10.0.101 -H Host:...`.
3. **in-cluster 실측(kubeconfig)**: Traefik `.com` IngressRoute 매칭(API server proxy로 실측),
   api 팟 env `.com` 값 반영, Keycloak client redirect URIs `.com` 포함.
4. **E2E(성공 기준)**: 외부망에서 `app.cledyu.com` → 구글 로그인 → 복귀·로그인 유지 →
   랩 시작 `api.cledyu.com` 200, 쿠키 `.cledyu.com`, 랩 세션 기동.
5. **회귀/CI**: 앱 config PR은 web/api 단위테스트·`go build`·eslint·`next build`(dev 서버 중복
   실행 금지) 통과. terraform은 CI fmt/validate/tflint 게이트.
6. **산출물**: 각 단계 실측 로그를 작업요약 포맷으로 정리해 PR 본문 첨부.

## 8. 미해결/후속

- apex `cledyu.com` 랜딩페이지는 이번 범위 밖(SAN만 확보).
- 데모 후 `enable_public_ingress=false` 철거 절차를 런북(`docs/RUNBOOK/learner-auth.md`)에 반영.
