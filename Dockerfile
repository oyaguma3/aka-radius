# aka-radius のイメージ。サブコマンド（auth・acct・admin・gateway）ごとに別々のコンテナで動かす
# （compose の command でサブコマンドを渡す）。

# ビルドステージ
FROM golang:1.27.3-bookworm AS builder

WORKDIR /src

# 依存関係のダウンロード（go.mod / go.sum が変わらなければキャッシュを使う）
COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/

# ビルドの版（compose の RADIUS_VERSION から渡す。起動ログの build に出る）
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X main.build=${VERSION}" -o /out/aka-radius ./cmd/aka-radius

# ランタイムステージ
# healthcheck の口（段階 2-5）ができるまでは、pgrep（procps）と curl（gateway の /health）を使うため slim にする
FROM debian:12.15-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    procps \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/aka-radius /usr/local/bin/aka-radius

EXPOSE 1812/udp 1813/udp 9444/tcp

ENTRYPOINT ["/usr/local/bin/aka-radius"]
