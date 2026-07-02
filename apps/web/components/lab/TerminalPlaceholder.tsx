// 실제 xterm.js 터미널/VM WebSocket 연동 전까지의 placeholder.
// Lab DSL commands는 정답지 역할을 할 수 있어 학습자 화면에는 노출하지 않는다.
export function TerminalPlaceholder() {
  return (
    <div className="rounded-lg border border-slate-700 bg-slate-950 overflow-hidden">
      <div className="flex items-center gap-1.5 px-3 py-2 border-b border-slate-800 bg-slate-900">
        <span className="w-2.5 h-2.5 rounded-full bg-red-500/70" />
        <span className="w-2.5 h-2.5 rounded-full bg-amber-500/70" />
        <span className="w-2.5 h-2.5 rounded-full bg-emerald-500/70" />
        <span className="ml-2 text-slate-500 text-xs">
          terminal — VM 오케스트레이터 연동 후 실제 입력 가능
        </span>
      </div>
      <pre className="p-4 text-sm text-slate-300 font-mono leading-relaxed overflow-x-auto">
        <span className="text-slate-600"># 실습 안내를 확인한 뒤 환경이 준비되면 진행하세요</span>
      </pre>
    </div>
  );
}
