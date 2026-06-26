package com.cledyu.keycloak.social.naver;

import com.fasterxml.jackson.databind.JsonNode;
import org.keycloak.broker.oidc.AbstractOAuth2IdentityProvider;
import org.keycloak.broker.oidc.OAuth2IdentityProviderConfig;
import org.keycloak.broker.oidc.mappers.AbstractJsonUserAttributeMapper;
import org.keycloak.broker.provider.BrokeredIdentityContext;
import org.keycloak.broker.provider.IdentityBrokerException;
import org.keycloak.broker.provider.util.SimpleHttp;
import org.keycloak.broker.social.SocialIdentityProvider;
import org.keycloak.models.KeycloakSession;

/**
 * 네이버 소셜 로그인 브로커.
 *
 * 네이버는 OAuth2 만 지원하고 OIDC id_token 을 발급하지 않으므로, 빌트인 {@code oidc}
 * 프로바이더(토큰 응답에서 id_token 을 강제 추출 → "No token from server" 실패)로는 동작하지
 * 않는다. 따라서 GitHub 빌트인과 동일하게 {@link AbstractOAuth2IdentityProvider} 를 상속해
 * access_token 으로 userinfo 엔드포인트를 호출하고 그 결과로 federated identity 를 만든다.
 *
 * 네이버 userinfo 응답은 {@code { "resultcode": "00", "message": "success",
 * "response": { "id": "...", "email": "...", "name": "...", "nickname": "..." } }} 형태로
 * 사용자 필드가 {@code response} 아래 중첩되어 있다.
 */
public class NaverIdentityProvider
        extends AbstractOAuth2IdentityProvider<OAuth2IdentityProviderConfig>
        implements SocialIdentityProvider<OAuth2IdentityProviderConfig> {

    public static final String AUTH_URL = "https://nid.naver.com/oauth2.0/authorize";
    public static final String TOKEN_URL = "https://nid.naver.com/oauth2.0/token";
    public static final String PROFILE_URL = "https://openapi.naver.com/v1/nid/me";

    // 네이버는 OAuth scope 를 사용하지 않는다(동의 항목은 개발자 콘솔에서 설정).
    public static final String DEFAULT_SCOPE = "";

    public NaverIdentityProvider(KeycloakSession session, OAuth2IdentityProviderConfig config) {
        super(session, config);
        config.setAuthorizationUrl(AUTH_URL);
        config.setTokenUrl(TOKEN_URL);
        config.setUserInfoUrl(PROFILE_URL);
    }

    @Override
    protected String getDefaultScopes() {
        return DEFAULT_SCOPE;
    }

    @Override
    protected BrokeredIdentityContext doGetFederatedIdentity(String accessToken) {
        try {
            JsonNode profile = SimpleHttp.doGet(PROFILE_URL, session)
                    .header("Authorization", "Bearer " + accessToken)
                    .asJson();

            JsonNode response = profile.get("response");
            if (response == null || response.isNull()) {
                throw new IdentityBrokerException(
                        "네이버 userinfo 응답에 'response' 필드가 없습니다: " + profile);
            }

            String id = getJsonProperty(response, "id");
            if (id == null || id.isEmpty()) {
                throw new IdentityBrokerException(
                        "네이버 userinfo 응답에 사용자 id 가 없습니다: " + profile);
            }

            BrokeredIdentityContext user = new BrokeredIdentityContext(id, getConfig());

            String email = getJsonProperty(response, "email");
            String name = getJsonProperty(response, "name");
            String nickname = getJsonProperty(response, "nickname");

            user.setUsername(email != null ? email : id);
            user.setName(name);
            if (email != null) {
                user.setEmail(email);
            }
            if (nickname != null) {
                user.setUserAttribute("nickname", nickname);
            }
            user.setIdp(this);

            // 평탄화된 전체 프로필을 저장해 두면 관리자가 "Attribute Importer (JSON)" 매퍼로
            // 추가 필드(예: response.profile_image)를 직접 매핑할 수 있다.
            AbstractJsonUserAttributeMapper.storeUserProfileForMapper(user, profile, getConfig().getAlias());

            return user;
        } catch (IdentityBrokerException e) {
            throw e;
        } catch (Exception e) {
            throw new IdentityBrokerException("네이버 사용자 프로필을 가져오지 못했습니다.", e);
        }
    }
}
