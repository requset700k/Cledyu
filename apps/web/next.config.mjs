import { PHASE_PRODUCTION_BUILD } from 'next/constants.js';

// CLEDYU_BACKEND_URL: K8s deployment.yaml에서 런타임 주입.
// 빌드 단계에서는 미주입이 정상(placeholder 사용). 런타임 기동 시 미주입이면 throw.
export default function config(phase) {
  const BACKEND_URL =
    process.env.CLEDYU_BACKEND_URL ??
    (phase === PHASE_PRODUCTION_BUILD
      ? 'http://localhost:8080'
      : (() => {
          throw new Error('CLEDYU_BACKEND_URL is not set');
        })());

  /** @type {import('next').NextConfig} */
  return {
    // standalone: Docker 이미지 최적화 모드.
    // npm run build 후 .next/standalone에 필요한 파일만 추출 → 이미지 크기 대폭 감소.
    output: 'standalone',
    env: {
      // AUTH_ENABLED: Keycloak 미연동 환경에서 인증 게이트 비활성화용 feature flag.
      // Keycloak 연동 PR에서 K8s deployment.yaml에 AUTH_ENABLED=true 주입.
      AUTH_ENABLED: process.env.AUTH_ENABLED ?? 'false',
    },
    async rewrites() {
      return [
        {
          // 브라우저의 /api/* 요청을 Next.js 서버가 백엔드로 프록시.
          source: '/api/:path*',
          destination: `${BACKEND_URL}/api/:path*`,
        },
      ];
    },
  };
}
