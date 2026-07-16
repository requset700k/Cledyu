// 마케팅 랜딩 페이지 — 히어로 스크롤 4섹션 (Cledyu Redesign v2 디자인 포팅).
import Link from 'next/link';
import { Navbar } from '@/components/ui/Navbar';
import { ParticleFx } from '@/components/ui/ParticleFx';

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
          className="flex min-h-screen flex-col px-6 pb-12 pt-28 sm:px-16 sm:pt-[110px]"
        >
          <div>
            <h1 className="font-chakra text-[clamp(32px,4.6vw,72px)] font-bold leading-[1.15] tracking-[-0.035em] text-white">
              터미널에서 배우는
              <br />
              클라우드 엔지니어링
            </h1>
            <p className="mt-[22px] font-michroma text-[clamp(11px,1vw,15px)] tracking-[0.08em] text-white/60">
              HANDS-ON CLOUD ENGINEERING
            </p>
          </div>
          <div className="relative my-[-40px] min-h-[300px] flex-1 sm:min-h-[420px]">
            <ParticleFx kind="rack" className="absolute inset-0" />
          </div>
          <div className="flex justify-end">
            <p className="max-w-[480px] text-right text-[clamp(15px,1.6vw,22px)] leading-[1.6] tracking-[-0.02em] text-white/85">
              격리된 실제 VM에서
              <br />
              직접 명령을 실행하며 배웁니다
            </p>
          </div>
          <div className="mt-6 whitespace-nowrap font-chakra text-[clamp(32px,6.8vw,110px)] font-bold leading-none tracking-[-0.01em] text-white">
            Learn. Break. Rebuild.
          </div>
        </section>

        {/* S2: 웨이브 */}
        <section
          style={{ scrollSnapAlign: 'start' }}
          className="flex min-h-screen flex-col px-6 pb-12 pt-28 sm:px-16"
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
          className="flex min-h-screen flex-col px-6 pb-12 pt-28 sm:px-16"
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

        {/* S4: 로그인 CTA */}
        <section
          style={{ scrollSnapAlign: 'start' }}
          className="flex min-h-screen flex-col px-6 pb-8 pt-28 sm:px-16 sm:pb-10 sm:pt-32"
        >
          <div className="flex flex-1 items-center">
            <div className="mx-auto grid w-full max-w-[1120px] items-end gap-10 border-y border-white/15 py-12 sm:grid-cols-[minmax(0,1fr)_minmax(280px,340px)] sm:gap-12 sm:py-16 lg:gap-16">
              <h2 className="max-w-[820px] text-left font-chakra text-[clamp(32px,4.8vw,72px)] font-bold leading-[1.12] tracking-[-0.03em] text-white">
                첫 번째 실습을
                <br className="hidden sm:block" /> 시작하세요
              </h2>
              <div>
                <p className="text-left text-base leading-relaxed tracking-[-0.02em] text-white/60">
                  브라우저만 있으면 바로 시작할 수 있습니다
                </p>
                <Link
                  href="/login"
                  className="mt-7 flex w-full items-center justify-between border border-white bg-white px-7 py-5 text-base font-bold tracking-[-0.01em] text-black transition-colors hover:bg-white/85"
                >
                  <span>실습 시작하기</span>
                  <span aria-hidden="true">→</span>
                </Link>
              </div>
            </div>
          </div>
          <div className="mx-auto mt-8 flex w-full max-w-[1120px] flex-col gap-5 sm:flex-row sm:items-center sm:justify-between">
            <div className="flex flex-wrap items-center gap-x-7 gap-y-3">
              <a href="#" className="text-[13px] tracking-[-0.01em] text-white/45">
                이용약관
              </a>
              <a href="#" className="text-[13px] tracking-[-0.01em] text-white/45">
                개인정보처리방침
              </a>
              <a href="#" className="text-[13px] tracking-[-0.01em] text-white/45">
                고객센터
              </a>
            </div>
            <span className="font-jbmono text-xs tracking-[0.08em] text-white/35">
              © 2026 CLEDYU
            </span>
          </div>
        </section>
      </div>
    </div>
  );
}
