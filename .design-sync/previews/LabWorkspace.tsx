import { LabWorkspace } from 'cledyu-web';

// LabWorkspace is the IDE-lab right pane: a [터미널 | IDE (VS Code)] tab switcher.
// The terminal tab is active by default (the IDE tab polls code-server healthz and
// shows its loading state until ready). Both panels stay mounted to keep sessions.
export const Default = () => (
  <div className="bg-slate-950 p-6" style={{ width: 680 }}>
    <LabWorkspace
      sessionId="demo-session"
      terminalPath="/api/v1/sessions/demo/ws"
      idePath="/api/v1/sessions/demo/ide/"
      heightClass="h-72"
    />
  </div>
);
