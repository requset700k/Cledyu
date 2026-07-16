// 앱 루트 레이아웃 — 모든 페이지에 공통 적용.
// Inter 폰트, TanStack Query Provider, 전역 CSS 설정.
import type { Metadata } from 'next';
import { Inter, Michroma, Chakra_Petch, JetBrains_Mono } from 'next/font/google';
import { Providers } from '@/components/Providers';
import './globals.css';

const inter = Inter({ subsets: ['latin'] });
// Michroma/Chakra Petch/JetBrains Mono — 리디자인 워드마크·헤드라인·모노 액센트 전용 (본문 폰트는 유지).
const michroma = Michroma({ subsets: ['latin'], weight: '400', variable: '--font-michroma' });
const chakraPetch = Chakra_Petch({
  subsets: ['latin'],
  weight: ['400', '600', '700'],
  variable: '--font-chakra',
});
const jbMono = JetBrains_Mono({
  subsets: ['latin'],
  weight: ['400', '500'],
  variable: '--font-jbmono',
});

export const metadata: Metadata = {
  title: 'Cledyu',
  description: '클라우드 엔지니어링 인터랙티브 실습 플랫폼',
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="ko">
      <body
        className={`${inter.className} ${michroma.variable} ${chakraPetch.variable} ${jbMono.variable}`}
      >
        <Providers>{children}</Providers>
      </body>
    </html>
  );
}
