// 명예의 전당 목록에 본인 행을 합친다.
// - me.rank 가 0 이면(미공개/완료 없음) 추가하지 않는다.
// - me 가 Top N 안에 있으면 해당 행에 isMe=true 표시.
// - 밖에 있으면 끝에 본인 행을 isMe=true 로 덧붙인다.
export function mergeMyRank(hallOfFame, me) {
  const rows = hallOfFame.map((r) => ({ ...r, isMe: me.rank !== 0 && r.rank === me.rank }));
  if (me.rank === 0) return rows;
  if (rows.some((r) => r.isMe)) return rows;
  return [...rows, { rank: me.rank, name: '나', score: me.score, labs_completed: me.labs_completed, isMe: true }];
}
