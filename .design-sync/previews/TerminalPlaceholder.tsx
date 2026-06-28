import { TerminalPlaceholder } from 'cledyu-web';

// TerminalPlaceholder shows the step's shell commands read-only until the live VM
// terminal is wired up. The two states are: has commands, and the empty hint.
const Wrap = ({ children }: { children: React.ReactNode }) => (
  <div className="bg-slate-950 p-6" style={{ width: 560 }}>
    {children}
  </div>
);

export const WithCommands = () => (
  <Wrap>
    <TerminalPlaceholder
      commands={[
        'kubectl get pods -n default',
        'kubectl describe pod web-0',
        'kubectl logs web-0 --tail=20',
      ]}
    />
  </Wrap>
);

export const Empty = () => (
  <Wrap>
    <TerminalPlaceholder commands={[]} />
  </Wrap>
);
