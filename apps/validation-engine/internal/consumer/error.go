package consumer

// FatalError는 consumer를 즉시 종료해야 하는 시스템 오류.
// DLQ 대상이 아니며, 파드 재시작 후 재시도가 필요한 경우에 사용한다.
// 예: Kafka 브로커 장애로 결과를 발행하지 못한 경우.
type FatalError struct{ Err error }

func (e *FatalError) Error() string { return e.Err.Error() }
func (e *FatalError) Unwrap() error { return e.Err }
