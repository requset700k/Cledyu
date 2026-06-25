export const DEFAULT_TERMINAL_TAIL_CHARS = 4000;

const CSI_SEQUENCE = /\x1b\[[0-?]*[ -/]*[@-~]/g;
const OSC_SEQUENCE = /\x1b\][^\x07]*(?:\x07|\x1b\\)/g;

export function stripTerminalControls(chunk) {
  return String(chunk ?? '')
    .replace(OSC_SEQUENCE, '')
    .replace(CSI_SEQUENCE, '')
    .replace(/\r\n/g, '\n')
    .replace(/\r/g, '\n');
}

export function appendTerminalTail(current, chunk, maxChars = DEFAULT_TERMINAL_TAIL_CHARS) {
  if (maxChars <= 0) return '';

  const base = typeof current === 'string' ? current : '';
  const cleanChunk = stripTerminalControls(chunk);
  if (!cleanChunk) return base;

  const next = base + cleanChunk;
  return next.length > maxChars ? next.slice(-maxChars) : next;
}
