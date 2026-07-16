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
            <h1 className="font-chakra text-[clamp(32px,4.6vw,72px)] font-extrabold leading-[1.15] tracking-[-0.035em] text-white">
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
              격리된 실제 VM 위에서,
              <br />
              클라우드 엔지니어링을 손으로 익힙니다
            </p>
          </div>
          <div className="mt-6 whitespace-nowrap font-chakra text-[clamp(34px,6.8vw,110px)] font-bold leading-none tracking-[-0.01em] text-white">
            Learn. Break. Rebuild
          </div>
        </section>

        {/* S2: 웨이브 */}
        <section
          style={{ scrollSnapAlign: 'start' }}
          className="flex min-h-screen flex-col px-6 pb-12 pt-28 sm:px-16"
        >
          <div className="max-w-[680px]">
            <p className="text-[clamp(16px,1.7vw,24px)] leading-[1.65] tracking-[-0.02em] text-white/90">
              <strong>보는 것</strong>과 <strong>해보는 것</strong>은 다릅니다
              <br />
              모니터 앞에 앉아 <strong>직접 명령을 치는 순간</strong>, 진짜 학습이 시작됩니다
            </p>
          </div>
          <div className="relative mt-10 min-h-[280px] flex-1 sm:min-h-[380px]">
            <ParticleFx kind="desk" className="absolute inset-0" />
          </div>
          <div className="whitespace-nowrap font-chakra text-[clamp(28px,5.6vw,92px)] font-bold leading-none tracking-[-0.01em] text-white">
            Skill comes from repetition
          </div>
        </section>

        {/* S3: 깔때기 */}
        <section
          style={{ scrollSnapAlign: 'start' }}
          className="flex min-h-screen flex-col px-6 pb-12 pt-28 sm:px-16"
        >
          <div className="flex justify-end">
            <p className="max-w-[620px] text-right text-[clamp(15px,1.6vw,22px)] leading-[1.65] tracking-[-0.02em] text-white/85">
              단계를 마칠 때마다 채점 엔진이 실제 시스템 상태를 확인합니다
              <br />
              정답을 외우는 게 아니라,{' '}
              <Link href="/labs" className="cursor-pointer border-b border-white/50">
                되는 걸 만드는 연습
              </Link>
              입니다
            </p>
          </div>
          <div className="relative min-h-[280px] flex-1 sm:min-h-[400px]">
            <ParticleFx kind="check" className="absolute inset-0" />
          </div>
          <div className="whitespace-nowrap font-chakra text-[clamp(26px,5vw,84px)] font-bold leading-none tracking-[-0.01em] text-white">
            You be the learner, I&apos;ll be the lab
          </div>
        </section>

        {/* S4: 로그인 CTA */}
        <section
          style={{ scrollSnapAlign: 'start' }}
          className="flex min-h-screen flex-col items-center justify-center gap-10 px-6 py-24 sm:px-16"
        >
          <h2 className="text-center font-michroma text-[clamp(24px,3.4vw,52px)] tracking-[0.06em] text-white">
            START TODAY
          </h2>
          <p className="text-center text-base tracking-[-0.02em] text-white/60">
            로그인하고 첫 번째 Lab을 시작하세요
          </p>
          <Link
            href="/login"
            className="rounded-full bg-white px-14 py-[17px] text-base font-bold tracking-[-0.01em] text-black transition-colors hover:bg-white/85"
          >
            로그인 하러 가기 →
          </Link>
          <div className="mt-12 flex flex-wrap items-center justify-center gap-7">
            <a href="#" className="text-[13px] tracking-[-0.01em] text-white/45">
              이용약관
            </a>
            <a href="#" className="text-[13px] tracking-[-0.01em] text-white/45">
              개인정보처리방침
            </a>
            <a href="#" className="text-[13px] tracking-[-0.01em] text-white/45">
              고객센터
            </a>
            <span className="font-jbmono text-xs tracking-[0.08em] text-white/35">
              © 2026 CLEDYU
            </span>
          </div>
        </section>
      </div>
    </div>
  );
}
