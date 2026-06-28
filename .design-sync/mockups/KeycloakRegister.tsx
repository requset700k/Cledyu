// DESIGN MOCKUP — not shipped code.
// React reproduction of the Keycloak-served registration form themed by
// infra/keycloak-theme/cledyu/login/resources/css/cledyu.css (Claude Design is
// React-only and cannot import Keycloak FTL). For design iteration only; keep in
// sync manually if the Keycloak register template/theme changes.

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

export function KeycloakRegister() {
  return (
    <div
      className="min-h-screen flex items-start justify-center p-4 pt-[6vh]"
      style={{ background: BG }}
    >
      <div className="w-full max-w-sm p-8" style={CARD}>
        <div className="text-center mb-6">
          <div className="text-3xl font-bold uppercase" style={NEON}>
            CLEDYU
          </div>
          <p className="text-[#94a3b8] text-xs mt-2.5">
            클라우드 엔지니어링 인터랙티브 실습 플랫폼
          </p>
        </div>

        <h1 className="text-white font-bold text-lg mb-1">회원가입</h1>
        <p className="text-[#94a3b8] text-sm mb-5">Cledyu 계정을 만들어 실습을 시작하세요</p>

        <Field label="이메일" type="email" placeholder="you@example.com" />
        <Field label="사용자 이름" type="text" placeholder="cloud_learner" />
        <Field label="비밀번호" type="password" placeholder="••••••••" />
        <Field label="비밀번호 확인" type="password" placeholder="••••••••" />

        <button
          type="button"
          className="w-full text-white font-bold py-3 text-sm mt-1"
          style={BRAND}
        >
          등록
        </button>

        <p className="text-center text-[#94a3b8] text-sm mt-6">
          이미 계정이 있으신가요?{' '}
          <a className="text-[#38bdf8] font-semibold" href="#">
            로그인
          </a>
        </p>
      </div>
    </div>
  );
}
