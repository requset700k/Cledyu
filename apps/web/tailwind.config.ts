// brand 색상: 플랫폼 전체의 주요 액센트 색상 (버튼, 활성 탭, 링크 등).
// sky 계열(0ea5e9)을 기반으로 커스터마이징. Tailwind의 기본 sky와 혼용하지 않도록 별도 정의.
import type { Config } from 'tailwindcss';

const config: Config = {
  content: ['./app/**/*.{js,ts,jsx,tsx,mdx}', './components/**/*.{js,ts,jsx,tsx,mdx}'],
  theme: {
    extend: {
      colors: {
        brand: {
          50: '#f0f9ff',
          100: '#e0f2fe',
          400: '#38bdf8',
          500: '#0ea5e9',
          600: '#0284c7',
          700: '#0369a1',
          900: '#0c4a6e',
        },
      },
      fontFamily: {
        michroma: ['var(--font-michroma)'],
        chakra: ['var(--font-chakra)'],
        jbmono: ['var(--font-jbmono)', 'monospace'],
      },
    },
  },
  plugins: [],
};

export default config;
