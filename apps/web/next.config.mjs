/** @type {import('next').NextConfig} */
const nextConfig = {
  // standalone: Docker 이미지 최적화 모드.
  // npm run build 후 .next/standalone에 필요한 파일만 추출 → 이미지 크기 대폭 감소.
  output: 'standalone',
};

export default nextConfig;
