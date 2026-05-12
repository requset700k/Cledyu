module github.com/requset700k/cledyu/validation-engine

go 1.24

// 직접 쓰는 외부 라이브러리
require (
	github.com/segmentio/kafka-go v0.4.47
	go.uber.org/zap v1.27.0
)

// indirect는 직접 쓰진 않지만 kafka-go나 zap이 내부적으로 쓰는 라이브러리
require (
	github.com/klauspost/compress v1.15.9 // indirect
	github.com/pierrec/lz4/v4 v4.1.15 // indirect
	go.uber.org/multierr v1.10.0 // indirect
)
