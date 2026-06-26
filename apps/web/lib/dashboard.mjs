// 랩 상태 코드를 학습자 화면용 한국어 라벨로 매핑한다. 알 수 없는 값은 원문 그대로.
const LABELS = { completed: '수료', in_progress: '진행중', not_started: '미시작' };

export function labStatusLabel(status) {
  return LABELS[status] ?? status;
}
