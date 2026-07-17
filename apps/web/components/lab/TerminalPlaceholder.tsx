// 실제 xterm.js 터미널/VM WebSocket 연동 전까지의 placeholder.
// Lab DSL commands는 정답지 역할을 할 수 있어 학습자 화면에는 노출하지 않는다.
export function TerminalPlaceholder() {
  return (
    <div className="overflow-hidden rounded-xl border border-white/25 bg-black">
      <div className="flex items-center gap-1.5 border-b border-white/15 bg-white/[0.03] px-3 py-2">
        <span className="w-2.5 h-2.5 rounded-full bg-red-500/70" />
        <span className="w-2.5 h-2.5 rounded-full bg-amber-500/70" />
        <span className="w-2.5 h-2.5 rounded-full bg-emerald-500/70" />
        <span className="ml-2 font-jbmono text-xs text-white/55">
          terminal — VM 오케스트레이터 연동 후 실제 입력 가능
        </span>
      </div>
      <pre className="overflow-x-auto p-4 font-jbmono text-sm leading-relaxed text-white/70">
        <span className="text-white/55"># 실습 안내를 확인한 뒤 환경이 준비되면 진행하세요</span>
      </pre>
    </div>
  );
}
