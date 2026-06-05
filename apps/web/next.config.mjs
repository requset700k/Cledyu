/** @type {import('next').NextConfig} */
const nextConfig = {
  // standalone: Docker 이미지 최적화 모드.
  // npm run build 후 .next/standalone에 필요한 파일만 추출 → 이미지 크기 대폭 감소.
  output: 'standalone',
  env: {
    // AUTH_ENABLED: Keycloak 미연동 환경에서 인증 게이트 비활성화용 feature flag.
    // Keycloak 연동 PR에서 K8s deployment.yaml에 AUTH_ENABLED=true 주입.
    AUTH_ENABLED: process.env.AUTH_ENABLED ?? 'false',
  },
};

export default nextConfig;
