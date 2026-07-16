// 히어로 라이브 터미널 데모 — Cledyu Platform 디자인의 타이핑 연출을 화이트 모노크롬으로 포팅.
// 실제 세션이 아니라 순수 연출(CSS 타이핑 애니메이션)이라 상태 없이 정적 마크업으로 충분.
const LINES: { text: string; color?: string; delay: number; duration: number; steps: number }[] = [
  { text: '$ cledyu start lab-docker-basics', delay: 0.4, duration: 0.9, steps: 32 },
  {
    text: '⠿ provisioning isolated vm …',
    color: 'text-white/45',
    delay: 1.6,
    duration: 0.7,
    steps: 30,
  },
  {
    text: '✔ session ready — 3h remaining',
    color: 'text-white/80',
    delay: 2.6,
    duration: 0.6,
    steps: 28,
  },
  { text: '$ docker run -d -p 8080:80 nginx', delay: 3.6, duration: 0.9, steps: 30 },
  { text: '9f2c1a7e33b0', color: 'text-white/45', delay: 4.8, duration: 0.5, steps: 14 },
  { text: '$ curl -s localhost:8080 | head -1', delay: 5.5, duration: 0.8, steps: 26 },
  { text: '<!DOCTYPE html>', color: 'text-white/45', delay: 6.5, duration: 0.5, steps: 16 },
  {
    text: '✔ LAB COMPLETED · +10 pts',
    color: 'text-white/80',
    delay: 7.3,
    duration: 0.6,
    steps: 28,
  },
];

export function TerminalDemo() {
  return (
    <div className="overflow-hidden rounded-2xl border border-white/25 bg-black">
      <div className="grid grid-cols-[1fr_auto_1fr] items-center border-b border-black/25 bg-[#f4f4f4] px-3 py-2.5">
        <div className="flex shrink-0 gap-2">
          <span className="h-3 w-3 rounded-full border border-black/15 bg-[#ff5f57]" />
          <span className="h-3 w-3 rounded-full border border-black/15 bg-[#febc2e]" />
          <span className="h-3 w-3 rounded-full border border-black/15 bg-[#28c840]" />
        </div>
        <div className="flex min-w-0 items-center justify-center gap-2">
          <span aria-hidden className="text-sm leading-none">
            📁
          </span>
          <span className="truncate text-xs font-semibold text-black/60">
            lab — docker-basics — bash
          </span>
        </div>
        <span aria-hidden />
      </div>
      <div className="px-5 py-6 font-jbmono text-[13.5px] leading-[2.05]">
        {LINES.map((line, i) => (
          <div key={i} className="overflow-hidden">
            <span
              className={`inline-block max-w-0 overflow-hidden whitespace-nowrap ${line.color ?? 'text-white'}`}
              style={{
                animation: `cledyu-typing ${line.duration}s steps(${line.steps},end) ${line.delay}s forwards`,
              }}
            >
              {line.text}
            </span>
            {i === LINES.length - 1 && (
              <span
                className="ml-1.5 inline-block h-[17px] w-[9px] bg-white align-middle"
                style={{
                  animation: 'cledyu-blink 1s step-end infinite',
                  animationDelay: `${line.delay}s`,
                }}
              />
            )}
          </div>
        ))}
      </div>
    </div>
  );
}
