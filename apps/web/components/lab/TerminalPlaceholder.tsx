// 실제 xterm.js 터미널/VM WebSocket 연동 전까지의 placeholder.
// 현재 단계에서 실행할 명령을 읽기 전용으로 보여준다.
export function TerminalPlaceholder({ commands }: { commands: string[] }) {
  return (
    <div className="rounded-lg border border-slate-700 bg-slate-950 overflow-hidden">
      <div className="flex items-center gap-1.5 px-3 py-2 border-b border-slate-800 bg-slate-900">
        <span className="w-2.5 h-2.5 rounded-full bg-red-500/70" />
        <span className="w-2.5 h-2.5 rounded-full bg-amber-500/70" />
        <span className="w-2.5 h-2.5 rounded-full bg-emerald-500/70" />
        <span className="ml-2 text-slate-500 text-xs">terminal — VM 오케스트레이터 연동 후 실제 입력 가능</span>
      </div>
      <pre className="p-4 text-sm text-slate-300 font-mono leading-relaxed overflow-x-auto">
        {commands.length === 0 ? (
          <span className="text-slate-600"># 이 단계에는 실행할 명령이 없습니다</span>
        ) : (
          commands.map((cmd, i) => (
            <div key={i}>
              <span className="text-emerald-400 select-none">$ </span>
              {cmd}
            </div>
          ))
        )}
      </pre>
    </div>
  );
}
