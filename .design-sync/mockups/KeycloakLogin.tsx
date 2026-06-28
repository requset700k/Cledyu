// DESIGN MOCKUP — not shipped code.
// The real Cledyu login/register pages are served by Keycloak (FTL templates +
// infra/keycloak-theme/cledyu/login/resources/css/cledyu.css), which Claude Design
// (React-only) cannot import. This React component reproduces that themed login
// form for design iteration. Visual spec mirrors cledyu.css: dark slate/sky
// gradient, neon CLEDYU wordmark, brand #0ea5e9 button, white social buttons.
// Keep in sync manually if the Keycloak theme changes.

const BG = 'linear-gradient(135deg,#0f172a 0%,#0c1e3a 45%,#172554 100%)';
const CARD = {
  background: 'rgba(17,28,46,0.92)',
  border: '1px solid #1e293b',
  borderRadius: 18,
  boxShadow: '0 20px 50px rgba(0,0,0,0.45)',
};
const INPUT = {
  background: '#1e293b',
  border: '1px solid #334155',
  borderRadius: 10,
  color: '#e2e8f0',
};
const BRAND = { background: '#0ea5e9', borderRadius: 10 };
const NEON = {
  color: '#dff3ff',
  letterSpacing: 8,
  textShadow:
    '0 0 6px rgba(56,189,248,0.85),0 0 18px rgba(14,165,233,0.6),0 0 34px rgba(14,165,233,0.35)',
};

function Header() {
  return (
    <div className="text-center mb-6">
      <div className="text-3xl font-bold uppercase" style={NEON}>
        CLEDYU
      </div>
      <p className="text-[#94a3b8] text-xs mt-2.5">클라우드 엔지니어링 인터랙티브 실습 플랫폼</p>
    </div>
  );
}

function Field({ label, type, placeholder }: { label: string; type: string; placeholder: string }) {
  return (
    <label className="block mb-4">
      <span className="block text-[13px] text-[#cbd5e1] mb-1.5">{label}</span>
      <input
        type={type}
        placeholder={placeholder}
        className="w-full px-3 py-2.5 text-sm outline-none"
        style={INPUT}
      />
    </label>
  );
}

function SocialButton({ icon, label }: { icon: React.ReactNode; label: string }) {
  return (
    <button
      type="button"
      className="flex items-center justify-center gap-2 w-full bg-white text-[#1e293b] font-semibold py-2.5 rounded-[10px] text-sm"
    >
      {icon}
      {label}
    </button>
  );
}

export function KeycloakLogin() {
  return (
    <div
      className="min-h-screen flex items-start justify-center p-4 pt-[6vh]"
      style={{ background: BG }}
    >
      <div className="w-full max-w-sm p-8" style={CARD}>
        <Header />
        <h1 className="text-white font-bold text-lg mb-5">로그인</h1>

        <Field label="사용자 이름 또는 이메일" type="text" placeholder="you@example.com" />
        <Field label="비밀번호" type="password" placeholder="••••••••" />

        <div className="flex items-center justify-between mb-5 text-[13px]">
          <label className="flex items-center gap-2 text-[#94a3b8]">
            <input type="checkbox" defaultChecked />
            로그인 상태 유지
          </label>
          <a className="text-[#38bdf8]" href="#">
            비밀번호를 잊으셨나요?
          </a>
        </div>

        <button type="button" className="w-full text-white font-bold py-3 text-sm" style={BRAND}>
          로그인
        </button>

        <div className="flex items-center gap-3 my-5">
          <span className="h-px flex-1 bg-[#1e293b]" />
          <span className="text-[#94a3b8] text-xs">또는 소셜 계정으로</span>
          <span className="h-px flex-1 bg-[#1e293b]" />
        </div>

        <div className="space-y-2.5">
          <SocialButton icon={<GoogleIcon />} label="Google로 로그인" />
          <SocialButton icon={<KakaoIcon />} label="Kakao로 로그인" />
          <SocialButton icon={<NaverIcon />} label="Naver로 로그인" />
        </div>

        <p className="text-center text-[#94a3b8] text-sm mt-6">
          처음이신가요?{' '}
          <a className="text-[#38bdf8] font-semibold" href="#">
            회원가입
          </a>
        </p>
      </div>
    </div>
  );
}

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

function KakaoIcon() {
  return (
    <svg className="w-5 h-5" viewBox="0 0 24 24" fill="#191600" aria-hidden="true">
      <path d="M12 3C6.477 3 2 6.477 2 10.5c0 2.57 1.7 4.83 4.27 6.16-.19.66-.68 2.43-.78 2.81-.12.47.17.46.36.34.15-.1 2.37-1.6 3.33-2.26.59.08 1.2.13 1.82.13C17.523 18 22 14.523 22 10.5S17.523 3 12 3z" />
    </svg>
  );
}

function NaverIcon() {
  return (
    <svg className="w-4 h-4" viewBox="0 0 24 24" fill="#03C75A" aria-hidden="true">
      <path d="M16.273 12.845 7.376 0H0v24h7.726V11.155L16.624 24H24V0h-7.727v12.845z" />
    </svg>
  );
}
