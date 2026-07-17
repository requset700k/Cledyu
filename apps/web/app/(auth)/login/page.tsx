// 버튼 클릭 시 /api/v1/auth/login → 백엔드가 Keycloak 인증 페이지로 리다이렉트.
// 소셜 버튼은 provider 별로 노출한다: CLEDYU_SOCIAL_LOGIN_PROVIDERS 에 적힌
// alias(쉼표 구분, 예 "google" 또는 "google,kakao,naver")만 렌더한다. terraform 의
// enabled_social_idps(생성된 IdP 목록)와 정렬 — 미프로비저닝 alias 로 라우팅돼
// "Identity Provider not found" 로 실패하는 것을 막는다. 빈 값이면 소셜 섹션 자체를
// 숨긴다. 런타임 env 를 읽으려면 동적 렌더가 필요하다.
import Link from 'next/link';
import { Suspense } from 'react';
import { LoginReturnTarget } from '@/components/auth/LoginReturnTarget';

export const dynamic = 'force-dynamic';

export default function LoginPage() {
  const socialProviders = new Set(
    (process.env.CLEDYU_SOCIAL_LOGIN_PROVIDERS ?? '')
      .split(',')
      .map((p) => p.trim())
      .filter(Boolean),
  );
  const anySocial = socialProviders.size > 0;
  return (
    <div className="relative flex min-h-screen items-center justify-center overflow-hidden bg-black p-4 text-[#F2F2F2]">
      {/* 은은한 그리드 배경 */}
      <div
        aria-hidden
        className="pointer-events-none absolute inset-0 bg-[linear-gradient(rgba(255,255,255,0.05)_1px,transparent_1px),linear-gradient(90deg,rgba(255,255,255,0.05)_1px,transparent_1px)] bg-[size:56px_56px] [mask-image:radial-gradient(ellipse_70%_70%_at_50%_50%,black_20%,transparent_70%)]"
      />
      <Suspense fallback={null}>
        <LoginReturnTarget />
      </Suspense>
      <div className="relative w-full max-w-md">
        <div className="mb-8 text-center">
          <Link href="/" className="font-michroma text-xl tracking-[0.08em] text-white">
            CLEDYU
          </Link>
          <p className="mt-3 text-sm tracking-[-0.02em] text-white/50">
            클라우드 엔지니어링 인터랙티브 실습 플랫폼
          </p>
        </div>

        <div className="rounded-2xl border border-white/20 bg-white/[0.02] p-8 backdrop-blur-sm">
          <p className="font-jbmono text-xs tracking-[0.12em] text-white/45">$ whoami</p>
          <h2 className="mt-2 font-chakra text-2xl font-bold text-white">로그인</h2>
          <p className="mb-7 mt-1.5 text-sm text-white/50">
            진행 중이던 세션과 학습 기록이 계정에 보관됩니다
          </p>

          {/* 이메일(Keycloak 폼) — 주 버튼 */}
          {/* eslint-disable-next-line @next/next/no-html-link-for-pages */}
          <a
            href="/api/v1/auth/login"
            aria-label="이메일로 로그인"
            className="flex w-full items-center justify-center gap-2 rounded-full bg-white px-4 py-3.5 text-[15px] font-bold text-black transition-colors hover:bg-white/85"
          >
            <MailIcon />
            이메일로 로그인
          </a>

          {anySocial && (
            <>
              <div className="my-5 flex items-center gap-3">
                <span className="h-px flex-1 bg-white/15" />
                <span className="text-xs text-white/40">또는 소셜 계정으로</span>
                <span className="h-px flex-1 bg-white/15" />
              </div>

              <div className="space-y-2.5">
                {socialProviders.has('google') && (
                  // eslint-disable-next-line @next/next/no-html-link-for-pages
                  <a
                    href="/api/v1/auth/login?idp=google"
                    aria-label="Google로 로그인"
                    className="flex w-full items-center justify-center gap-2 rounded-full border border-white/30 px-4 py-3 text-[15px] font-medium text-white transition-colors hover:border-white/70"
                  >
                    <GoogleIcon />
                    Google로 로그인
                  </a>
                )}
                {socialProviders.has('kakao') && (
                  // eslint-disable-next-line @next/next/no-html-link-for-pages
                  <a
                    href="/api/v1/auth/login?idp=kakao"
                    aria-label="Kakao로 로그인"
                    className="flex w-full items-center justify-center gap-2 rounded-full bg-[#FEE500] px-4 py-3 text-[15px] font-medium text-[#191600] transition-all hover:brightness-95"
                  >
                    <KakaoIcon />
                    Kakao로 로그인
                  </a>
                )}
                {socialProviders.has('naver') && (
                  // eslint-disable-next-line @next/next/no-html-link-for-pages
                  <a
                    href="/api/v1/auth/login?idp=naver"
                    aria-label="Naver로 로그인"
                    className="flex w-full items-center justify-center gap-2 rounded-full bg-[#03C75A] px-4 py-3 text-[15px] font-medium text-white transition-all hover:brightness-95"
                  >
                    <NaverIcon />
                    Naver로 로그인
                  </a>
                )}
              </div>
            </>
          )}

          <p className="mt-6 border-t border-white/10 pt-5 text-center text-sm text-white/50">
            처음이신가요? {/* eslint-disable-next-line @next/next/no-html-link-for-pages */}
            <a
              href="/api/v1/auth/login?screen=register"
              className="font-semibold text-white underline decoration-white/40 underline-offset-4 transition-colors hover:decoration-white"
            >
              회원가입
            </a>
          </p>
        </div>

        <p className="mt-6 text-center font-jbmono text-xs tracking-[0.06em] text-white/35">
          가입 즉시 첫 Lab이 무료로 제공됩니다
        </p>
      </div>
    </div>
  );
}

// 카카오톡 말풍선 시그니처 — 노란 버튼 위 갈색/검정(#191600) 단색.
function KakaoIcon() {
  return (
    <svg className="h-5 w-5" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
      <path d="M12 3C6.477 3 2 6.477 2 10.5c0 2.57 1.7 4.83 4.27 6.16-.19.66-.68 2.43-.78 2.81-.12.47.17.46.36.34.15-.1 2.37-1.6 3.33-2.26.59.08 1.2.13 1.82.13C17.523 18 22 14.523 22 10.5S17.523 3 12 3z" />
    </svg>
  );
}

// 이메일 로그인 — 단순 봉투 아이콘.
function MailIcon() {
  return (
    <svg
      className="h-5 w-5"
      fill="none"
      viewBox="0 0 24 24"
      stroke="currentColor"
      strokeWidth={1.8}
      aria-hidden="true"
    >
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        d="M4.5 6h15A1.5 1.5 0 0121 7.5v9a1.5 1.5 0 01-1.5 1.5h-15A1.5 1.5 0 013 16.5v-9A1.5 1.5 0 014.5 6z"
      />
      <path strokeLinecap="round" strokeLinejoin="round" d="M3.5 7.5l8.5 6 8.5-6" />
    </svg>
  );
}

// 구글 공식 멀티컬러 G.
function GoogleIcon() {
  return (
    <svg className="h-5 w-5" viewBox="0 0 48 48" aria-hidden="true">
      <path
        fill="#EA4335"
        d="M24 9.5c3.54 0 6.71 1.22 9.21 3.6l6.85-6.85C35.9 2.38 30.47 0 24 0 14.62 0 6.51 5.38 2.56 13.22l7.98 6.19C12.43 13.72 17.74 9.5 24 9.5z"
      />
      <path
        fill="#4285F4"
        d="M46.98 24.55c0-1.57-.15-3.09-.38-4.55H24v9.02h12.94c-.58 2.96-2.26 5.48-4.78 7.18l7.73 6c4.51-4.18 7.09-10.36 7.09-17.65z"
      />
      <path
        fill="#FBBC05"
        d="M10.53 28.59c-.48-1.45-.76-2.99-.76-4.59s.27-3.14.76-4.59l-7.98-6.19C.92 16.46 0 20.12 0 24c0 3.88.92 7.54 2.56 10.78l7.97-6.19z"
      />
      <path
        fill="#34A853"
        d="M24 48c6.48 0 11.93-2.13 15.89-5.81l-7.73-6c-2.15 1.45-4.92 2.3-8.16 2.3-6.26 0-11.57-4.22-13.47-9.91l-7.98 6.19C6.51 42.62 14.62 48 24 48z"
      />
    </svg>
  );
}

// 네이버 N 시그니처 (녹색 버튼 위 흰색 currentColor).
function NaverIcon() {
  return (
    <svg className="h-4 w-4" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
      <path d="M16.273 12.845 7.376 0H0v24h7.726V11.155L16.624 24H24V0h-7.727v12.845z" />
    </svg>
  );
}
