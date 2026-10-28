# aka-radius のイメージ。サブコマンド（auth・acct・admin）ごとに別々のコンテナで動かす
# （compose の command でサブコマンドを渡す）。運用のコマンド（client・sources・admin-cert など）も同じバイナリ。

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
# healthcheck はコマンド aka-radius healthcheck（ループバックの口）で行うので、シェルも curl も pgrep も要らない。
# UID 65532（nonroot）で動く。マウントするファイル（取得先の鍵、ピン、管理API の証明書、監査ログのディレクトリ）は
# この UID で読み書きできるようにする（README の「起動」。aka-vecgen と同じ）。
FROM gcr.io/distroless/static-debian13:nonroot

COPY --from=builder /out/aka-radius /usr/local/bin/aka-radius

EXPOSE 1812/udp 1813/udp 9444/tcp

ENTRYPOINT ["/usr/local/bin/aka-radius"]
