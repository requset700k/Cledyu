import { LoginPage } from 'cledyu-web';

// LoginPage is the real platform login launcher (app/(auth)/login/page.tsx): neon
// Cledyu branding, email login (-> Keycloak), env-gated social buttons, 회원가입 link,
// feature list. It reads CLEDYU_SOCIAL_LOGIN_PROVIDERS at render, so each story sets
// that env to show the with-social and email-only variants.

export const WithSocial = () => {
  globalThis.process.env.CLEDYU_SOCIAL_LOGIN_PROVIDERS = 'google,kakao,naver';
  return <LoginPage />;
};

export const EmailOnly = () => {
  globalThis.process.env.CLEDYU_SOCIAL_LOGIN_PROVIDERS = '';
  return <LoginPage />;
};
