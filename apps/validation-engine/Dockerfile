# 1단계: builder (Go 코드 빌드)
FROM golang:1.26-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /engine ./cmd/engine

# 2단계: virtctl-downloader (virtctl 다운로드) — 클러스터의 KubeVirt 버전(v1.8.2)과 맞춘다.
FROM alpine:3.21 AS virtctl-downloader
ARG KUBEVIRT_VERSION=v1.8.2
RUN apk add --no-cache curl && \
    curl -fsSL --retry 3 --retry-delay 5 -o /virtctl \
      "https://github.com/kubevirt/kubevirt/releases/download/${KUBEVIRT_VERSION}/virtctl-${KUBEVIRT_VERSION}-linux-amd64" && \
    chmod +x /virtctl

# 3단계 — 최종 이미지
FROM gcr.io/distroless/static-debian12
WORKDIR /app
COPY --from=builder /engine /engine
COPY --from=virtctl-downloader /virtctl /usr/local/bin/virtctl
ENTRYPOINT ["/engine"]
