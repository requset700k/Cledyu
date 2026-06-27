package com.cledyu.keycloak.social.naver;

import org.keycloak.broker.oidc.OAuth2IdentityProviderConfig;
import org.keycloak.broker.provider.AbstractIdentityProviderFactory;
import org.keycloak.broker.social.SocialIdentityProviderFactory;
import org.keycloak.models.IdentityProviderModel;
import org.keycloak.models.KeycloakSession;

/**
 * {@code naver} provider id 를 Keycloak 에 등록하는 팩토리.
 *
 * 이 provider id 로 cledyu-learn realm 에 Identity Provider 를 만들면(terraform
 * {@code provider_id = "naver"}) Keycloak 이 {@link NaverIdentityProvider} 로 브로커링한다.
 */
public class NaverIdentityProviderFactory
        extends AbstractIdentityProviderFactory<NaverIdentityProvider>
        implements SocialIdentityProviderFactory<NaverIdentityProvider> {

    public static final String PROVIDER_ID = "naver";

    @Override
    public String getName() {
        return "Naver";
    }

    @Override
    public NaverIdentityProvider create(KeycloakSession session, IdentityProviderModel model) {
        return new NaverIdentityProvider(session, new OAuth2IdentityProviderConfig(model));
    }

    @Override
    public OAuth2IdentityProviderConfig createConfig() {
        return new OAuth2IdentityProviderConfig();
    }

    @Override
    public String getId() {
        return PROVIDER_ID;
    }
}
