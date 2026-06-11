package lock

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/redis/go-redis/v9"
)

// 분산 락 파라미터 — 임계영역(활성 세션 조회→VM 생성)은 보통 수백 ms 라
// TTL 30s 로 크래시 시 자동 만료를 보장하고, 짧은 재시도로 더블클릭을 직렬화한다.
const (
	lockTTL       = 30 * time.Second
	lockRetry     = 50 * time.Millisecond
	lockMaxWait   = 2 * time.Second
	lockKeyPrefix = "cledyu:lock:"
)

// releaseScript는 자신이 건 락(토큰 일치)만 해제한다 — 만료 후 타인의 락을 지우는 것을 방지.
var releaseScript = redis.NewScript(`
if redis.call("get", KEYS[1]) == ARGV[1] then
	return redis.call("del", KEYS[1])
end
return 0`)

// RedisLocker는 SET NX PX 기반 분산 락이다(다중 레플리카 안전).
type RedisLocker struct {
	rdb *redis.Client
}

func NewRedisLocker(rdb *redis.Client) *RedisLocker { return &RedisLocker{rdb: rdb} }

// Acquire는 키를 lockMaxWait 동안 재시도하며 잡는다. 못 잡으면 ok=false(동시 작업 진행 중).
// Redis 오류 시에도 ok=false 로 안전하게 거부한다(호출부가 503/409 처리).
func (l *RedisLocker) Acquire(ctx context.Context, key string) (func(), bool) {
	token := randToken()
	redisKey := lockKeyPrefix + key
	deadline := time.Now().Add(lockMaxWait)

	for {
		ok, err := l.rdb.SetNX(ctx, redisKey, token, lockTTL).Result()
		if err != nil {
			return nil, false
		}
		if ok {
			release := func() {
				// 해제 실패는 무시 — TTL 이 결국 정리한다.
				relCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_ = releaseScript.Run(relCtx, l.rdb, []string{redisKey}, token).Err()
			}
			return release, true
		}
		if time.Now().After(deadline) {
			return nil, false
		}
		select {
		case <-ctx.Done():
			return nil, false
		case <-time.After(lockRetry):
		}
	}
}

func randToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
