// 버튼 클릭 시 /api/v1/auth/login → 백엔드가 Keycloak 인증 페이지로 리다이렉트.
export default function LoginPage() {
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

          {/* eslint-disable-next-line @next/next/no-html-link-for-pages */}
          <a
            href="/api/v1/auth/login"
            className="flex items-center justify-center gap-2 w-full bg-brand-500 hover:bg-brand-600 text-white font-medium py-3 px-4 rounded-xl transition-colors duration-150"
          >
            Cledyu로 로그인
          </a>

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
