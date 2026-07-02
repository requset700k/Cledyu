import assert from 'node:assert/strict';
import { describe, it } from 'node:test';
import {
  createTerminalReadinessGate,
  TERMINAL_READY_REDRAW,
  TERMINAL_READY_SENTINEL,
} from './terminal-readiness.mjs';

describe('createTerminalReadinessGate', () => {
  it('suppresses boot output until the terminal readiness sentinel is observed', () => {
    const gate = createTerminalReadinessGate();

    assert.deepEqual(gate.consume('cloud-init boot log\n'), {
      ready: false,
      becameReady: false,
      output: '',
    });
    assert.deepEqual(gate.consume(`more boot log\n${TERMINAL_READY_SENTINEL}`), {
      ready: true,
      becameReady: true,
      output: '',
    });
    assert.deepEqual(gate.consume('Cledyu ~ ➜ '), {
      ready: true,
      becameReady: false,
      output: 'Cledyu ~ ➜ ',
    });
  });

  it('passes output through when suppression is disabled', () => {
    const gate = createTerminalReadinessGate({ enabled: false });

    assert.deepEqual(gate.consume('cloud-init boot log\n'), {
      ready: true,
      becameReady: false,
      output: 'cloud-init boot log\n',
    });
  });
});

describe('TERMINAL_READY_REDRAW', () => {
  it('uses Ctrl+L instead of Enter so readiness probing does not execute a blank command', () => {
    assert.equal(TERMINAL_READY_REDRAW, '\x0c');
  });
});
