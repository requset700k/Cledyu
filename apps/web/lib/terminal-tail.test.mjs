import assert from 'node:assert/strict';
import { describe, it } from 'node:test';
import { appendTerminalTail, stripTerminalControls } from './terminal-tail.mjs';

describe('stripTerminalControls', () => {
  it('removes common ANSI control sequences and normalizes CRLF', () => {
    assert.equal(stripTerminalControls('\x1b[31merror\x1b[0m\r\nnext'), 'error\nnext');
  });
});

describe('appendTerminalTail', () => {
  it('appends clean terminal output', () => {
    assert.equal(appendTerminalTail('abc', 'def', 10), 'abcdef');
  });

  it('keeps only the most recent characters', () => {
    assert.equal(appendTerminalTail('abcdef', 'ghij', 5), 'fghij');
  });

  it('returns an empty tail when maxChars is disabled', () => {
    assert.equal(appendTerminalTail('abc', 'def', 0), '');
  });
});
