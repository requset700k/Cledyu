// 마케팅 랜딩 페이지 — 히어로 스크롤 4섹션 (Cledyu Redesign v2 디자인 포팅)
// + Cledyu Platform 디자인에서 뽑아온 라이브 터미널 데모 / 기술스택 마퀴 / How it works (화이트 모노크롬으로 재배색).
import Link from 'next/link';
import { Navbar } from '@/components/ui/Navbar';
import { ParticleFx } from '@/components/ui/ParticleFx';
import { TerminalDemo } from '@/components/ui/TerminalDemo';

const TECH_STACK = ['LINUX', 'DOCKER', 'KUBERNETES', 'TERRAFORM', 'ANSIBLE', 'HELM', 'CILIUM'];

const HOW_IT_WORKS = [
  { num: '01', title: 'Lab 선택', desc: 'Linux부터 Kubernetes까지, 난이도별 커리큘럼' },
  { num: '02', title: 'VM 프로비저닝', desc: '격리된 전용 VM이 자동으로 준비됩니다' },
  { num: '03', title: '실습 & 검증', desc: '터미널에서 직접 수행, 단계마다 자동 채점' },
  { num: '04', title: '수료 & 포인트', desc: '완료한 Lab만큼 포인트가 쌓입니다' },
];

const FEATURES = [
  {
    tag: '01 VM',
    icon: '>_',
    title: '회원 전용 VM',
    desc: '격리된 전용 Ubuntu VM이 자동으로 준비됩니다. 브라우저 터미널에서 실제 서버 환경을 직접 다룹니다.',
  },
  {
    tag: '02 AI',
    icon: '?',
    title: '정답 대신 힌트를 주는 AI',
    desc: '막힌 지점에 맞춰 개념 → 방향 → 구체 3단계 힌트를 제공합니다. 정답을 대신 알려주지 않습니다.',
  },
  {
    tag: '03 CHECK',
    icon: '✔',
    title: '단계별 자동 채점',
    desc: '검증 엔진이 프로세스·포트·설정 파일 등 VM 내부 상태를 직접 확인하고, 통과한 단계만 다음으로 진행합니다.',
  },
];

export default function LandingPage() {
  return (
    <div className="relative isolate min-h-screen overflow-x-hidden bg-black text-[#F2F2F2]">
      <Navbar />

      {/* 전역 배경: 은은한 성운 + 별 */}
      <ParticleFx kind="stars" className="pointer-events-none fixed inset-0 -z-10 opacity-95" />

      {/* HUD 프레임 */}
      <div className="pointer-events-none fixed inset-2.5 z-[60] rounded border border-white/[0.16]">
        <div className="absolute -top-px left-6 h-px w-14 bg-white/60" />
        <div className="absolute left-[-1px] top-6 h-14 w-px bg-white/60" />
        <div className="absolute -bottom-px right-6 h-px w-14 bg-white/60" />
        <div className="absolute right-[-1px] bottom-6 h-14 w-px bg-white/60" />
      </div>

      <div style={{ scrollSnapType: 'y proximity' }}>
        {/* S1: 히어로 */}
        <section
          style={{ scrollSnapAlign: 'start' }}
          className="mx-auto flex min-h-screen w-full max-w-[1280px] flex-col justify-center px-6 pb-12 pt-24 sm:px-10"
        >
          <div className="grid grid-cols-1 items-center gap-12 sm:grid-cols-[1.05fr_0.95fr] sm:gap-14">
            <div>
              <h1 className="break-keep font-chakra text-[clamp(34px,4.4vw,68px)] font-bold leading-[1.15] tracking-[-0.035em] text-white">
                직접 실행하고
                <br />
                끝까지 해결하세요
              </h1>
              <p className="mt-[22px] font-michroma text-[clamp(11px,1vw,15px)] tracking-[0.08em] text-white/60">
                HANDS-ON CLOUD ENGINEERING
              </p>
              <p className="mt-6 max-w-[440px] text-[clamp(15px,1.6vw,18px)] leading-[1.7] tracking-[-0.02em] text-white/70">
                격리된 실제 VM에서 직접 명령을 실행하며 배웁니다.
                <br />
                단계마다 자동 채점, 막히면 AI 힌트.
              </p>
              <div className="mt-8 flex flex-wrap gap-4">
                <Link
                  href="/login"
                  className="rounded-full bg-white px-8 py-3.5 text-sm font-bold tracking-[-0.01em] text-black transition-colors hover:bg-white/85"
                >
                  무료로 시작하기 →
                </Link>
                <Link
                  href="#how-it-works"
                  className="rounded-full border border-white/35 px-8 py-3.5 text-sm font-semibold tracking-[-0.01em] text-white transition-colors hover:border-white/70"
                >
                  학습 방식 보기
                </Link>
              </div>
            </div>
            <TerminalDemo />
          </div>
          <div className="mt-14 whitespace-nowrap font-chakra text-[clamp(28px,6vw,90px)] font-bold leading-none tracking-[-0.01em] text-white">
            Learn. Break. Rebuild.
          </div>
        </section>

        {/* 기술스택 마퀴 */}
        <div className="overflow-hidden border-y border-white/15 bg-white/[0.02] py-4">
          <div
            className="flex w-max whitespace-nowrap font-chakra text-sm tracking-[0.18em] text-white/40"
            style={{ animation: 'cledyu-marquee 26s linear infinite' }}
          >
            {[0, 1].map((dup) => (
              <span key={dup} className="pr-12">
                {TECH_STACK.map((t) => (
                  <span key={t}>
                    {t} <span className="text-white/70">✦</span>{' '}
                  </span>
                ))}
              </span>
            ))}
          </div>
        </div>

        {/* 기능 카드: 왜 Cledyu인가 */}
        <section className="mx-auto w-full max-w-[1280px] px-6 py-24 sm:px-10">
          <p className="font-jbmono text-[13px] tracking-[0.14em] text-white/50">WHY CLEDYU</p>
          <h2 className="mt-3 max-w-[640px] font-chakra text-[clamp(26px,3.4vw,48px)] font-bold leading-[1.1] tracking-[-0.02em] text-white">
            강의를 보는 것과
            <br />
            서버를 다루는 것은 다릅니다
          </h2>
          <div className="mt-12 grid grid-cols-1 gap-3.5 sm:grid-cols-3">
            {FEATURES.map((f) => (
              <div
                key={f.tag}
                className="border border-white/15 bg-white/[0.02] p-8 transition-colors hover:border-white/40"
              >
                <div className="font-jbmono text-xs text-white/40">{f.tag}</div>
                <div className="mt-5 flex h-12 w-12 items-center justify-center border border-white/20 bg-white/[0.04] font-jbmono text-lg text-white">
                  {f.icon}
                </div>
                <h3 className="mt-5 font-chakra text-xl font-semibold text-white">{f.title}</h3>
                <p className="mt-2.5 break-keep text-sm leading-[1.75] text-white/55">
                  {f.desc}
                </p>
              </div>
            ))}
          </div>
        </section>

        {/* S2: 웨이브 */}
        <section
          style={{ scrollSnapAlign: 'start' }}
          className="mx-auto flex min-h-screen w-full max-w-[1280px] flex-col px-6 pb-12 pt-28 sm:px-10"
        >
          <div className="max-w-[680px]">
            <div className="text-[clamp(16px,1.7vw,24px)] leading-[1.65] tracking-[-0.02em] text-white/90">
              <p>
                강의를 보는 것만으로는
                <br />
                실전 감각을 익히기 어렵습니다
              </p>
              <p className="mt-4">
                직접 명령을 실행하고
                <br />
                실패한 환경을 다시 복구합니다
              </p>
            </div>
          </div>
          <div className="relative mt-10 min-h-[280px] flex-1 sm:min-h-[380px]">
            <ParticleFx kind="desk" className="absolute inset-0" />
          </div>
          <div className="whitespace-nowrap font-chakra text-[clamp(28px,5.6vw,92px)] font-bold leading-none tracking-[-0.01em] text-white">
            Practice builds skill.
          </div>
        </section>

        {/* S3: 깔때기 */}
        <section
          style={{ scrollSnapAlign: 'start' }}
          className="mx-auto flex min-h-screen w-full max-w-[1280px] flex-col px-6 pb-12 pt-28 sm:px-10"
        >
          <div className="flex justify-end">
            <div className="max-w-[620px] text-right text-[clamp(15px,1.6vw,22px)] leading-[1.65] tracking-[-0.02em] text-white/85">
              <p>
                단계를 마칠 때마다
                <br />
                채점 엔진이 실제 시스템 상태를 확인합니다
              </p>
              <p className="mt-4">
                정답을 외우는 대신
                <br />
                실제로 동작하는 환경을 완성하세요
              </p>
            </div>
          </div>
          <div className="relative min-h-[280px] flex-1 sm:min-h-[400px]">
            <ParticleFx kind="check" className="absolute inset-0" />
          </div>
          <div className="whitespace-nowrap font-chakra text-[clamp(26px,5vw,84px)] font-bold leading-none tracking-[-0.01em] text-white">
            Build it. Prove it.
          </div>
        </section>

        {/* How it works */}
        <section id="how-it-works" className="mx-auto w-full max-w-[1280px] scroll-mt-24 px-6 py-24 sm:px-10">
          <h2 className="font-chakra text-[clamp(24px,3vw,40px)] font-bold tracking-[-0.02em] text-white">
            어떻게 진행되나요
          </h2>
          <div className="mt-10 grid grid-cols-1 border border-white/15 sm:grid-cols-4">
            {HOW_IT_WORKS.map((step, i) => (
              <div
                key={step.num}
                className={`p-8 ${i < HOW_IT_WORKS.length - 1 ? 'border-b border-white/15 sm:border-b-0 sm:border-r' : ''}`}
              >
                <div
                  className={`font-chakra text-4xl font-bold ${
                    'text-white/20'
                  }`}
                >
                  {step.num}
                </div>
                <div className="mt-3 text-base font-semibold text-white">{step.title}</div>
                <div className="mt-1.5 text-sm leading-relaxed text-white/50">{step.desc}</div>
              </div>
            ))}
          </div>
        </section>

        {/* S4: 로그인 CTA */}
        <section
          style={{ scrollSnapAlign: 'start' }}
          className="flex min-h-screen flex-col items-center justify-center px-6 py-24 sm:px-16"
        >
          <h2 className="mt-6 text-center font-chakra text-[clamp(44px,8vw,110px)] font-bold leading-[0.95] tracking-[-0.025em] text-white">
            LEARN BY
            <br />
            DOING.
          </h2>
          <p className="mt-7 max-w-[440px] text-center text-base leading-[1.7] tracking-[-0.02em] text-white/60">
            결제 정보 없이 첫 번째 Lab을 시작하세요
          </p>
          <Link
            href="/login"
            className="mt-10 rounded-full bg-white px-14 py-[17px] text-base font-bold tracking-[-0.01em] text-black transition-colors hover:bg-white/85"
          >
            실습 시작하기 →
          </Link>
          <div className="mt-12 flex flex-wrap items-center justify-center gap-7">
            <span className="font-jbmono text-xs tracking-[0.08em] text-white/35">
              © 2026 CLEDYU
            </span>
          </div>
        </section>
      </div>
    </div>
  );
}
