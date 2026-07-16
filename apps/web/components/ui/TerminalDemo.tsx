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
      <div className="flex items-center gap-3 border-b border-white/15 bg-white/[0.03] px-4 py-3">
        <div className="flex gap-1.5">
          <span className="h-2.5 w-2.5 rounded-full bg-white/25" />
          <span className="h-2.5 w-2.5 rounded-full bg-white/18" />
          <span className="h-2.5 w-2.5 rounded-full bg-white/10" />
        </div>
        <span className="min-w-0 flex-1 truncate text-center font-jbmono text-xs text-white/55">
          student — cledyu-vm — bash — 120×30
        </span>
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
