import { AiTutorPanel } from 'cledyu-web';

// AiTutorPanel is the Socratic AI hint panel shown beside the active step. Hints
// are fetched on click from the backend (level 1 concept -> 2 direction -> 3
// specific), so the static preview is the initial call-to-action state. It needs
// the QueryClientProvider, supplied by cfg.provider (Providers).
export const Default = () => (
  <div className="bg-slate-950 p-6" style={{ width: 420 }}>
    <AiTutorPanel sessionId="demo-session" stepId={2} getTerminalTail={() => ''} />
  </div>
);
