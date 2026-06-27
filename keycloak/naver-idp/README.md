# Cledyu Keycloak 네이버 Identity Provider (SPI)

cledyu-learn realm 의 **네이버 소셜 로그인**을 위한 Keycloak 커스텀 Identity Provider.

## 왜 커스텀 SPI 가 필요한가

네이버는 OAuth2 만 지원하고 **OIDC `id_token` 을 발급하지 않는다.** Keycloak 빌트인
`oidc` 프로바이더는 토큰 응답에서 `id_token`(JWT)을 강제로 추출하므로(`parseTokenInput`),
네이버에 적용하면 콜백 단계에서 다음으로 실패한다:

```
org.keycloak.broker.provider.IdentityBrokerException: No token from server.
  at org.keycloak.broker.oidc.OIDCIdentityProvider.parseTokenInput(...)
```

`validate_signature=false` 는 서명 검증만 끌 뿐 id_token 요구 자체를 없애지 못한다.

따라서 GitHub/Facebook 등 빌트인 소셜 프로바이더와 동일하게
`AbstractOAuth2IdentityProvider` 를 상속해 **access_token 으로 userinfo 를 호출**하는 방식으로
구현한다. provider id 는 `naver`.

- authorize: `https://nid.naver.com/oauth2.0/authorize`
- token: `https://nid.naver.com/oauth2.0/token`
- userinfo: `https://openapi.naver.com/v1/nid/me`
  → 응답이 `{ "response": { "id", "email", "name", "nickname" } }` 로 중첩되어 있어
    SPI 가 `response` 아래를 평탄화해 매핑한다(username=email|id, email, name, nickname).

## 빌드

```bash
# 로컬 (Maven + JDK 21+)
mvn clean package           # -> target/naver-idp.jar
cp target/naver-idp.jar dist/naver-idp.jar

# 또는 Docker (로컬 툴체인 없이) — 빌드 전용, 별도 Dockerfile 커밋하지 않는다.
docker run --rm -v "$PWD":/build -w /build maven:3.9-eclipse-temurin-21 \
  mvn -q -B clean package
cp target/naver-idp.jar dist/naver-idp.jar
```

빌드 타깃 Keycloak 버전은 `pom.xml` 의 `keycloak.version`(현재 26.6.1)이며 운영 이미지
(`quay.io/keycloak/keycloak`) 버전과 맞춰야 한다. keycloak 의존성은 `provided` 라 JAR 에
포함되지 않는다(약 6KB).

`dist/naver-idp.jar` 는 prebuilt 로 커밋되어 있다 — ansible 이 이 파일을 ConfigMap 으로 만든다.

## 배포 (커스텀 이미지 불필요)

operator 가 keycloak 를 비-optimized(`start`, `--optimized` 아님)로 기동하므로 **매 시작 시
augmentation** 이 `/opt/keycloak/providers` 를 스캔해 provider 를 등록한다. 따라서 JAR 을
ConfigMap(binaryData)으로 만들어 `unsupported.podTemplate` 볼륨으로 마운트하면 된다(테마와
동일 패턴).

ansible `keycloak_foundation` 역할이 처리한다:
- `keycloak_foundation_naver_spi_configmap` (기본 `cledyu-keycloak-naver-spi`) ConfigMap 생성
- CR `unsupported.podTemplate` 에 `/opt/keycloak/providers/naver-idp.jar` (subPath) 마운트

```bash
ansible-playbook ansible/playbooks/70-keycloak-foundation.yml
# 이후 operator 가 새 podTemplate 으로 롤아웃 → 시작 로그에 augmentation 재실행
```

등록 확인:

```bash
kubectl logs -n keycloak cledyu-keycloak-0 | grep -i "naver\|augmentation"
# Keycloak Admin → cledyu-learn → Identity Providers → "Add provider" 목록에 Naver 노출
```

terraform 쪽은 `infra/terraform/keycloak/idp-learn.tf` 의 `keycloak_oidc_identity_provider.naver`
가 `provider_id = "naver"` 로 이 SPI 를 사용한다. **SPI 가 먼저 등록된 뒤** terraform apply 해야
unknown provider 오류가 없다.
