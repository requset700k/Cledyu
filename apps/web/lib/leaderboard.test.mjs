import assert from 'node:assert/strict';
import { describe, it } from 'node:test';
import { mergeMyRank } from './leaderboard.mjs';

describe('mergeMyRank', () => {
  it('marks the current user inside the top N', () => {
    const hof = [
      { rank: 1, name: 'A', score: 20, labs_completed: 2 },
      { rank: 2, name: 'Me', score: 10, labs_completed: 1 },
    ];
    const me = { rank: 2, score: 10, labs_completed: 1 };
    const rows = mergeMyRank(hof, me);
    assert.equal(rows.length, 2);
    assert.equal(rows[1].isMe, true);
    assert.equal(rows[0].isMe ?? false, false);
  });

  it('appends the current user when outside the top N', () => {
    const hof = [{ rank: 1, name: 'A', score: 20, labs_completed: 2 }];
    const me = { rank: 17, score: 10, labs_completed: 1 };
    const rows = mergeMyRank(hof, me);
    assert.equal(rows.length, 2);
    assert.equal(rows[1].isMe, true);
    assert.equal(rows[1].rank, 17);
  });

  it('does not append when the user has no public rank', () => {
    const hof = [{ rank: 1, name: 'A', score: 20, labs_completed: 2 }];
    const me = { rank: 0, score: 10, labs_completed: 1 };
    const rows = mergeMyRank(hof, me);
    assert.equal(rows.length, 1);
  });
});
