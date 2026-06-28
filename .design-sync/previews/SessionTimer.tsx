import { SessionTimer } from 'cledyu-web';

// SessionTimer counts down to expiresAt. Tone is the variant axis: slate (plenty),
// amber (<10 min), red (<1 min or expired). Timestamps are computed relative to
// render time so each story lands in its band.
const Wrap = ({ children }: { children: React.ReactNode }) => (
  <div className="bg-slate-950 p-6 flex">{children}</div>
);

const inMinutes = (m: number) => new Date(Date.now() + m * 60_000).toISOString();

export const Plenty = () => (
  <Wrap>
    <SessionTimer expiresAt={inMinutes(150)} />
  </Wrap>
);

export const Warning = () => (
  <Wrap>
    <SessionTimer expiresAt={inMinutes(7)} />
  </Wrap>
);

export const Critical = () => (
  <Wrap>
    <SessionTimer expiresAt={inMinutes(0.5)} />
  </Wrap>
);

export const Expired = () => (
  <Wrap>
    <SessionTimer expiresAt={inMinutes(-5)} />
  </Wrap>
);
