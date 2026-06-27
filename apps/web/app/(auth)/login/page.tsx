// 버튼 클릭 시 /api/v1/auth/login → 백엔드가 Keycloak 인증 페이지로 리다이렉트.
// 소셜 버튼은 provider 별로 노출한다: CLEDYU_SOCIAL_LOGIN_PROVIDERS 에 적힌
// alias(쉼표 구분, 예 "google" 또는 "google,kakao,naver")만 렌더한다. terraform 의
// enabled_social_idps(생성된 IdP 목록)와 정렬 — 미프로비저닝 alias 로 라우팅돼
// "Identity Provider not found" 로 실패하는 것을 막는다. 빈 값이면 소셜 섹션 자체를
// 숨긴다. 런타임 env 를 읽으려면 동적 렌더가 필요하다.
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
    <div className="min-h-screen bg-gradient-to-br from-slate-900 via-blue-950 to-slate-900 flex items-center justify-center p-4">
      <div className="w-full max-w-md">
        <div className="text-center mb-8">
          <div className="inline-flex items-center justify-center w-16 h-16 bg-blue-500/20 rounded-2xl mb-4 border border-blue-500/30">
            <TerminalIcon />
          </div>
          <h1 className="text-3xl font-bold text-white tracking-tight">Cledyu</h1>
          <p className="text-slate-400 mt-2 text-sm">클라우드 엔지니어링 인터랙티브 실습 플랫폼</p>
        </div>

        <div className="bg-slate-800/60 backdrop-blur border border-slate-700 rounded-2xl p-8 shadow-2xl">
          <h2 className="text-lg font-semibold text-white mb-1">시작하기</h2>
          <p className="text-slate-400 text-sm mb-6">Cledyu 계정으로 로그인하세요</p>

          {/* 이메일(Keycloak 폼) — 주 버튼 */}
          {/* eslint-disable-next-line @next/next/no-html-link-for-pages */}
          <a
            href="/api/v1/auth/login"
            aria-label="이메일로 로그인"
            className="flex items-center justify-center gap-2 w-full bg-brand-500 hover:bg-brand-600 text-white font-medium py-3 px-4 rounded-xl transition-colors duration-150"
          >
            <MailIcon />
            이메일로 로그인
          </a>

          {anySocial && (
            <>
              <div className="flex items-center gap-3 my-5">
                <span className="h-px flex-1 bg-slate-700" />
                <span className="text-slate-500 text-xs">또는 소셜 계정으로</span>
                <span className="h-px flex-1 bg-slate-700" />
              </div>

              <div className="space-y-3">
                {socialProviders.has('google') && (
                  // eslint-disable-next-line @next/next/no-html-link-for-pages
                  <a
                    href="/api/v1/auth/login?idp=google"
                    aria-label="Google로 로그인"
                    className="flex items-center justify-center gap-2 w-full bg-white hover:bg-slate-100 text-slate-800 font-medium py-3 px-4 rounded-xl transition-colors duration-150"
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
                    className="flex items-center justify-center gap-2 w-full bg-[#FEE500] hover:brightness-95 text-[#191600] font-medium py-3 px-4 rounded-xl transition-all duration-150"
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
                    className="flex items-center justify-center gap-2 w-full bg-[#03C75A] hover:brightness-95 text-white font-medium py-3 px-4 rounded-xl transition-all duration-150"
                  >
                    <NaverIcon />
                    Naver로 로그인
                  </a>
                )}
              </div>
            </>
          )}

          <p className="text-center text-slate-400 text-sm mt-4">
            처음이신가요? {/* eslint-disable-next-line @next/next/no-html-link-for-pages */}
            <a
              href="/api/v1/auth/login?screen=register"
              className="text-brand-400 hover:text-brand-300 font-medium"
            >
              회원가입
            </a>
          </p>

          <div className="mt-8 pt-6 border-t border-slate-700 space-y-3">
            <Feature
              icon={<TerminalSmIcon />}
              text="격리된 VM에서 Linux · Ansible · Terraform · Kubernetes 실습"
            />
            <Feature
              icon={<BrainIcon />}
              text="AI 학습 도우미 — 소크라테스식 힌트로 스스로 답을 찾도록"
            />
            <Feature icon={<CheckIcon />} text="자동 채점 엔진 + 수료증 발급" />
          </div>
        </div>
      </div>
    </div>
  );
}

function Feature({ icon, text }: { icon: React.ReactNode; text: string }) {
  return (
    <div className="flex items-start gap-3">
      <span className="text-brand-400 mt-0.5 flex-shrink-0">{icon}</span>
      <span className="text-slate-400 text-sm">{text}</span>
    </div>
  );
}

// 카카오톡 말풍선 시그니처 — 노란 버튼 위 갈색/검정(#191600) 단색.
function KakaoIcon() {
  return (
    <svg className="w-5 h-5" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
      <path d="M12 3C6.477 3 2 6.477 2 10.5c0 2.57 1.7 4.83 4.27 6.16-.19.66-.68 2.43-.78 2.81-.12.47.17.46.36.34.15-.1 2.37-1.6 3.33-2.26.59.08 1.2.13 1.82.13C17.523 18 22 14.523 22 10.5S17.523 3 12 3z" />
    </svg>
  );
}

// 이메일 로그인 — 단순 봉투 아이콘.
function MailIcon() {
  return (
    <svg
      className="w-5 h-5"
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

// 구글 공식 멀티컬러 G (흰 버튼 위).
function GoogleIcon() {
  return (
    <svg className="w-5 h-5" viewBox="0 0 48 48" aria-hidden="true">
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
    <svg className="w-4 h-4" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
      <path d="M16.273 12.845 7.376 0H0v24h7.726V11.155L16.624 24H24V0h-7.727v12.845z" />
    </svg>
  );
}

function TerminalIcon() {
  return (
    <svg className="w-8 h-8 text-brand-400" fill="none" viewBox="0 0 24 24" stroke="currentColor">
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        strokeWidth={1.5}
        d="M6.75 7.5l3 2.25-3 2.25m4.5 0h3m-9 8.25h13.5A2.25 2.25 0 0021 18V6a2.25 2.25 0 00-2.25-2.25H5.25A2.25 2.25 0 003 6v12a2.25 2.25 0 002.25 2.25z"
      />
    </svg>
  );
}

function TerminalSmIcon() {
  return (
    <svg className="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        strokeWidth={2}
        d="M6.75 7.5l3 2.25-3 2.25m4.5 0h3m-9 8.25h13.5A2.25 2.25 0 0021 18V6a2.25 2.25 0 00-2.25-2.25H5.25A2.25 2.25 0 003 6v12a2.25 2.25 0 002.25 2.25z"
      />
    </svg>
  );
}

function BrainIcon() {
  return (
    <svg className="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        strokeWidth={2}
        d="M9.663 17h4.673M12 3v1m6.364 1.636l-.707.707M21 12h-1M4 12H3m3.343-5.657l-.707-.707m2.828 9.9a5 5 0 117.072 0l-.548.547A3.374 3.374 0 0014 18.469V19a2 2 0 11-4 0v-.531c0-.895-.356-1.754-.988-2.386l-.548-.547z"
      />
    </svg>
  );
}

function CheckIcon() {
  return (
    <svg className="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        strokeWidth={2}
        d="M9 12l2 2 4-4m6 2a9 9 0 11-18 0 9 9 0 0118 0z"
      />
    </svg>
  );
}
