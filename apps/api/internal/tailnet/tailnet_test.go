package tailnet

import (
	"context"
	"testing"

	"go.uber.org/zap"
)

// authkey 가 비면 tsnet 기동 전에 즉시 에러를 반환해야 한다(네트워크 없이 검증 가능).
func TestNew_EmptyAuthKey(t *testing.T) {
	_, err := New(context.Background(), Config{Hostname: "cledyu-api"}, zap.NewNop())
	if err == nil {
		t.Fatal("빈 authkey 에 대해 에러를 기대했으나 nil")
	}
}
