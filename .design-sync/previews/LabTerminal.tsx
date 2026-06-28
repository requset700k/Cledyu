import { LabTerminal } from 'cledyu-web';

// LabTerminal is the live xterm.js terminal bound to the lab VM serial console
// over WebSocket. Without a reachable VM it shows its chrome and the connection
// state badge (연결 중… while it attempts the socket) — the honest standalone state.
export const Default = () => (
  <div className="bg-slate-950 p-6" style={{ width: 640 }}>
    <LabTerminal terminalPath="/api/v1/sessions/demo/ws" heightClass="h-72" />
  </div>
);
