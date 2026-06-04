package validation

import "testing"

// KafkaPublisher가 Publisher 인터페이스를 만족하는지 컴파일 타임에 보장한다.
var _ Publisher = (*KafkaPublisher)(nil)

// LoadTLS는 인증서 파일이 없으면 에러를 반환해야 한다.
// main.go가 이 에러로 publisher를 nil로 두고 mock 검증으로 폴백하는 계약을 잠근다.
func TestLoadTLSMissingFilesReturnsError(t *testing.T) {
	if _, err := LoadTLS("/no/such/tls.crt", "/no/such/tls.key", "/no/such/ca.crt"); err == nil {
		t.Fatal("expected error when cert files are missing, got nil")
	}
}
