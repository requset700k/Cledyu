# 1단계: builder (Go 코드 빌드)
FROM golang:1.26.4-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /engine ./cmd/engine

# 2단계: virtctl-downloader (virtctl 다운로드) — 클러스터의 KubeVirt 버전(v1.8.2)과 맞춘다.
FROM alpine:3.21@sha256:48b0309ca019d89d40f670aa1bc06e426dc0931948452e8491e3d65087abc07d AS virtctl-downloader
ARG KUBEVIRT_VERSION=v1.8.2
RUN apk add --no-cache curl && \
    curl -fsSL --retry 3 --retry-delay 5 -o /virtctl \
      "https://github.com/kubevirt/kubevirt/releases/download/${KUBEVIRT_VERSION}/virtctl-${KUBEVIRT_VERSION}-linux-amd64" && \
    chmod +x /virtctl

# 3단계 — 최종 이미지
# virtctl --local-ssh-opts 모드가 OpenSSH를 호출하므로 openssh-client 필요.
# UID 65532로 nonroot 사용자를 생성해 deployment.yaml의 runAsUser와 맞춘다.
FROM alpine:3.21@sha256:48b0309ca019d89d40f670aa1bc06e426dc0931948452e8491e3d65087abc07d
RUN apk add --no-cache openssh-client && \
    adduser -D -u 65532 nonroot
WORKDIR /app
COPY --from=builder /engine /engine
COPY --from=virtctl-downloader /virtctl /usr/local/bin/virtctl
USER nonroot
ENTRYPOINT ["/engine"]
