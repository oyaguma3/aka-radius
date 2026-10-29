# aka-radius

[![Go](https://img.shields.io/badge/Go-1.27.3-00ADD8?logo=go)](https://go.dev/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

このリポジトリは、VPS に分けて置いて顧客を収容する **MVP セット**（[aka-mvpset](https://github.com/oyaguma3/aka-mvpset)）の RADIUS サーバーのノードです。PoC セット（[eapaka-pocset](https://github.com/oyaguma3/eapaka-pocset) の `pocset-v1.0.1`）の [eapaka-radius-server-poc](https://github.com/oyaguma3/eapaka-radius-server-poc) の履歴を引き継いで作りました（もとのコミット `main` の `1f8aad1`、`develop` の `86e05a9`）。

MVP のための改修は、[設計概要](docs/design-overview.md)（決定事項と段階の記録）のとおりに MVP セットの段階 2 で行いました（2-1〜2-6）。運用は[運用ガイド](docs/operation-guide.md)、細かい仕様は D-文書（[ドキュメント一覧](docs/aka-radius_ドキュメント一覧_r70.md)）、管理API は [OpenAPI](docs/openapi/admin-api.yaml) にあります。

## 概要

EAP-AKA/AKA' の RADIUS サーバーです。
Wi-Fi 認証 (WPA2/WPA3-Enterprise) 向けの RADIUS 認証・課金機能と、管理API を提供します。

## アーキテクチャ

1 つのイメージ `aka-radius` を、サブコマンドを変えて別々のコンテナで動かします（設計概要 §4）。

| サービス（サブコマンド） | 役割 | ポート |
|---|---|---|
| **auth** | RADIUS 認証 + EAP-AKA/AKA' ステートマシン制御。IMSI ごとの取得先から、認証ベクターを mTLS で 1 つずつ取る（取得先ごとに接続と遮断器を持つ） | UDP 1812 |
| **acct** | RADIUS 課金 (Accounting)。累計からの増分で、IMSI・テナント・日（日本時間）ごとの利用の集計を作る。セッションの索引の掃除 | UDP 1813 |
| **admin** | 管理API 2.0.0（`/admin/v2`。加入者（状態・取得先・認可ポリシーのルール）、セッション、利用の集計、RADIUS クライアントと取得先の読み出し、ノードの状態、監査ログ）。aka-provisioner から mTLS で操作する。運用のコマンド（`client` など）もこのコンテナで使う | HTTPS 9444（既定 127.0.0.1 のみ） |
| **valkey** | データストア (加入者情報・セッション等) | 6379（127.0.0.1 のみ） |
| **fluent-bit** | ログ収集・転送 | 24224（127.0.0.1 のみ） |
| **取得先**（外部） | 認証ベクターを払い出すサーバー（[認証ベクターAPI の契約](https://github.com/oyaguma3/aka-mvpset/blob/main/docs/av-api-contract.md)。3GPP TS 29.503 Nudm_UEAU GenerateAv ベース。MVP では [aka-vecgen](https://github.com/oyaguma3/aka-vecgen)）。auth から mTLS で接続。取得先の登録簿（ファイル）に並べる | HTTPS（aka-vecgen は 8443） |

## 技術スタック

- **言語:** Go 1.27.3
- **データストア:** Valkey 9.1
- **コンテナ:** Docker Compose
- **ログ収集:** Fluent Bit 4.2
- **ロギング:** log/slog (構造化ログ)
- **RADIUS:** [layeh.com/radius](https://pkg.go.dev/layeh.com/radius)
- **EAP-AKA:** [go-eapaka](https://github.com/oyaguma3/go-eapaka)
- **Circuit Breaker:** sony/gobreaker

## リポジトリ構成

```
aka-radius/
├── cmd/aka-radius/         # コマンド（サーバー auth・acct・admin の振り分けと、運用のコマンド client・sources・admin-cert・fingerprint・healthcheck）
├── internal/
│   ├── auth/               # RADIUS 認証サーバー（サブコマンド auth。auth/source が取得先の登録簿とクライアント）
│   ├── acct/               # RADIUS 課金サーバー（サブコマンド acct）
│   ├── admin/              # 管理API（サブコマンド admin）
│   ├── radiussrv/          # auth と acct に共通の RADIUS の受け口（待ち受け、共有シークレット、Message-Authenticator、Proxy-State、Status-Server、重複応答のキャッシュ・要求の期限・同時処理の上限）
│   ├── subscriber/         # 加入者（状態・取得先・ルール）の Valkey 上のデータとルールの評価（auth と admin で共通）
│   ├── sessions/           # セッションと、IMSI・テナントごとの索引（auth・acct・admin で共通）
│   ├── usage/              # 利用の集計（IMSI・テナント・日ごと）のキー、認証の成功の足し込み、取り出し
│   ├── clients/            # RADIUS クライアント（共有シークレットと刻印を信頼する印）の Valkey 上のデータ
│   ├── audit/              # 監査ログ（Valkey の Stream と日ごとのファイル。admin とコマンドで共通）
│   ├── health/             # ループバックの healthcheck の口（auth・acct・admin で共通）
│   ├── envconf/            # 環境変数の読み込み（Valkey・ログ・healthcheck・監査ログの共通の設定）
│   ├── logging/            # ログ設定
│   └── valkey/             # Valkey クライアント
├── configs/
│   └── fluent-bit/         # Fluent Bit 設定 (YAML)
├── deployments/
│   ├── docker-compose.yml  # Docker Compose 定義
│   ├── docker-compose.aka-av.yml  # aka-vecgen との共有ネットワーク用オーバーレイ
│   ├── .env.example        # 環境変数テンプレート
│   ├── sources/            # 取得先の登録簿と証明書の配置先（例の sources.example.json のほかは .gitignore 対象）
│   ├── certs/              # 管理API のサーバー証明書の配置先（certs/admin/。中身は .gitignore 対象）
│   ├── secrets/            # 管理クライアントのピンのファイル（作業で作る。.gitignore 対象）
│   ├── audit/              # 監査ログの日ごとのファイル（admin が書く。.gitignore 対象）
│   └── lnav_formats/       # lnav ログフォーマット定義
├── scripts/                # Valkey のバックアップと戻し（運用ガイドの 10 章）
├── docs/                   # 運用ガイド、MVP の設計概要、D-文書、OpenAPI
├── Dockerfile              # イメージ aka-radius
└── go.mod
```

## セットアップ

### 前提条件

- Docker & Docker Compose
- Go 1.27.3 (ローカル開発時)

### 起動手順

```bash
# リポジトリをクローン
git clone https://github.com/oyaguma3/aka-radius.git
cd aka-radius

# 環境変数を設定
cd deployments
cp .env.example .env
# .env を編集して各値を設定

# 取得先の登録簿を用意（下の「取得先の登録簿」を参照）
cp sources/sources.example.json sources/sources.json

# 管理クライアントのピン、管理API のサーバー証明書、監査ログのディレクトリ（下の「管理API」を参照）。
# ピンのファイルは admin のコンテナを一度でも動かす前に作る（ないと Docker が同じ名前のディレクトリを作ってしまう）
mkdir -p certs/admin secrets audit
echo "provisioner=<aka-provisioner のクライアント証明書のフィンガープリント>" > secrets/admin-clients
docker compose build
docker compose run --rm --no-deps -v ./certs/admin:/out admin admin-cert create -hosts aka-radius-admin,localhost,127.0.0.1 -out /out

# コンテナは UID 65532 で動くので、マウントするファイルとディレクトリをこの UID で読み書きできるようにする
sudo chown -R 65532:65532 sources certs/admin secrets/admin-clients audit

# Docker Compose を起動
docker compose up -d
```

`admin-cert create` で書き出すディレクトリ（`certs/admin`）は、先に UID 65532 で書けるようにしておくか、`openssl` で作ったサーバー証明書を置きます。

RADIUS（1812/1813/udp）と管理API（9444/tcp）は既定でループバックだけに公開します。本番の公開範囲は[運用ガイド](docs/operation-guide.md)の 8 章を参照してください。`RADIUS_FALLBACK_SECRET` は空のままにします。

### 主要な環境変数

compose の `.env` に書く変数です。アプリが読む環境変数（`RADIUS_VALKEY_HOST` など）は、compose がこれらから組み立てて渡します。

| 変数名 | 必須 | 説明 |
|---|---|---|
| `VALKEY_PASSWORD` | Yes | Valkey 接続パスワード（アプリには `RADIUS_VALKEY_PASSWORD` として渡す） |
| `RADIUS_FALLBACK_SECRET` | No | 予備の RADIUS 共有シークレット。設定すると、クライアント登録（送信元IP）のない送信元からのパケットもこの値で受け付ける。インターネットに公開する場合は空のままにする |
| `RADIUS_EAP_AKA_PRIME_NETWORK_NAME` | No | EAP-AKA' のネットワーク名 (デフォルト: `WLAN`) |
| `RADIUS_STAMP_VENDOR_ID`・`RADIUS_STAMP_VENDOR_TYPE` | No | テナントの刻印の Vendor-Specific の Vendor-Id と Vendor-Type (デフォルト: `32473` と `1`。エッジの設定と同じ値にする。auth・acct・admin) |
| `RADIUS_REQUEST_TIMEOUT` | No | 要求 1 つの処理の期限 (デフォルト: `4s`。auth・acct) |
| `RADIUS_MAX_INFLIGHT` | No | 同時に処理する要求の上限 (デフォルト: `256`。超えた要求は応えずに捨てる。auth・acct) |
| `RADIUS_LOG_LEVEL` | No | ログレベル (`DEBUG` / `INFO` / `WARN` / `ERROR`、デフォルト: `INFO`) |
| `RADIUS_LOG_MASK_IMSI` | No | ログの IMSI をマスクするか (デフォルト: `false`。MVP の決定事項 26) |
| `RADIUS_UDP_BIND` | No | RADIUS の 1812/udp・1813/udp を公開するアドレス (デフォルト: `127.0.0.1`。本番はサービス用の WireGuard のアドレス) |
| `RADIUS_ADMIN_BIND` | No | 管理API の 9444/tcp を公開するアドレス (デフォルト: `127.0.0.1`) |
| `RADIUS_ADMIN_NODE_NAME` | No | 管理API の `/status` が返すノード名 |
| `RADIUS_AUDIT_MAX_LEN` | No | 監査ログを Valkey の Stream に保持する件数の上限（おおよそ。デフォルト: `10000`。`GET /audit-logs` で参照する） |
| `RADIUS_AUDIT_RETENTION_DAYS` | No | 監査ログの日ごとのファイル（`deployments/audit/`）を持つ日数（デフォルト: `14`） |
| `RADIUS_ADMIN_SHARED_NETWORK` | No | 同じホストの呼び出し側と共有する Docker ネットワークの名前 (デフォルト: `aka-radius-admin`) |
| `RADIUS_VERSION` | No | イメージのタグと、起動ログの `build` に出すビルドの版 (デフォルト: `dev`) |
| `AKA_SHARED_NETWORK` | No | aka-vecgen の compose が作る共有ネットワークの名前（`docker-compose.aka-av.yml` 用。デフォルト: `aka-av`） |

詳細は `deployments/.env.example` を参照してください。

コンテナの中のアプリが読む環境変数は次のとおりです（compose を使わずに動かす場合）。

| サブコマンド | 環境変数 |
|---|---|
| 共通（auth・acct・admin） | `RADIUS_VALKEY_HOST`（既定 `valkey`）、`RADIUS_VALKEY_PORT`（既定 `6379`）、`RADIUS_VALKEY_PASSWORD`（必須）、`RADIUS_LOG_LEVEL`、`RADIUS_LOG_MASK_IMSI`、`RADIUS_HEALTH_ADDR`（healthcheck の口。既定 `127.0.0.1:9090`） |
| auth | `RADIUS_AUTH_LISTEN_ADDR`（既定 `:1812`）、`RADIUS_FALLBACK_SECRET`、`RADIUS_EAP_AKA_PRIME_NETWORK_NAME`（既定 `WLAN`。EAP-AKA' の `anId` と AT_KDF_INPUT）、`RADIUS_SOURCES_FILE`（既定 `/etc/aka-radius/sources/sources.json`）、`RADIUS_STAMP_VENDOR_ID`・`RADIUS_STAMP_VENDOR_TYPE`、`RADIUS_REQUEST_TIMEOUT`、`RADIUS_MAX_INFLIGHT` |
| acct | `RADIUS_ACCT_LISTEN_ADDR`（既定 `:1813`）、`RADIUS_FALLBACK_SECRET`、`RADIUS_REQUEST_TIMEOUT`、`RADIUS_MAX_INFLIGHT`、`RADIUS_STAMP_VENDOR_ID`・`RADIUS_STAMP_VENDOR_TYPE` |
| admin | `RADIUS_ADMIN_LISTEN_ADDR`（既定 `:9444`）、`RADIUS_ADMIN_TLS_CERT`・`RADIUS_ADMIN_TLS_KEY`（既定 `/certs/server.pem`・`/certs/server.key`）、`RADIUS_ADMIN_CLIENTS_FILE`（既定 `/run/secrets/admin-clients`）、`RADIUS_ADMIN_NODE_NAME`、`RADIUS_AUDIT_DIR`（既定 `/var/lib/aka-radius/audit`）・`RADIUS_AUDIT_RETENTION_DAYS`・`RADIUS_AUDIT_MAX_LEN`、`RADIUS_SOURCES_FILE`（auth と同じ登録簿を読み取り専用で読む）、`RADIUS_STAMP_VENDOR_ID`・`RADIUS_STAMP_VENDOR_TYPE`（`/status` に出す） |
| コマンド（client・admin-cert） | 共通の Valkey の変数と、監査ログの `RADIUS_AUDIT_*`（admin のコンテナで動かせば compose の値を使う） |

### 取得先の登録簿

auth は、加入者の `sub:{IMSI}` の `source_id` が指す取得先から、認証ベクターを 1 つずつ取ります（再同期のときはもう 1 つ）。取得先は `deployments/sources/sources.json` に並べます（設計概要 §7.1）。

| 項目 | 内容 |
|---|---|
| `id` | 取得先の ID（英小文字・数字・`-`、32 文字まで）。加入者の `source_id` が指す |
| `name` | 表示名（省略時は `id`） |
| `baseUrl` | `https://<ホスト>:<ポート>/nudm-ueau/v1`（平文の HTTP は使えない） |
| `clientCertFile`、`clientKeyFile` | aka-radius のクライアント証明書と秘密鍵（相対パスは登録簿のファイルのディレクトリから） |
| `serverCaFile` | 取得先のサーバー証明書を確かめる CA。aka-vecgen では、自己署名のサーバー証明書そのもの（`aka-vecgen av-cert` で表示）。ホスト名（SAN）も確かめる |
| `timeout` | 要求 1 回のタイムアウト（既定 `3s`。100ms〜30s）。再試行はしない |

- 知らない項目や形の誤りがあると、auth は起動しません。直したら `docker compose kill -s HUP auth admin` で読み直します。読み直す前に `docker compose run --rm --no-deps auth sources check` で確かめられます。取得先の鍵は UID 65532 で読めるようにします（`chmod 600` と `chown 65532`）。
- 遮断器、使用中の取得先の扱い、証明書の入れ替えは[運用ガイド](docs/operation-guide.md)の 3 章・7 章にあります。

aka-vecgen を取得先にする手順（同じホストの場合）:

```bash
cd deployments/sources

# 1. aka-radius のクライアント証明書と秘密鍵を、このノードで作る（鍵はノードの外に出さない）
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 825 \
  -subj /CN=aka-radius -addext extendedKeyUsage=clientAuth \
  -keyout vecgen-client.key -out vecgen-client.pem

# 2. 証明書（鍵ではない）を aka-vecgen に登録し、aka-vecgen の AV 用サーバー証明書をもらう（aka-vecgen の作業ディレクトリで）
#    docker compose exec -T aka-vecgen /aka-vecgen client add -name aka-radius -cert - < vecgen-client.pem
#    docker compose exec -T aka-vecgen /aka-vecgen av-cert > vecgen-av-server.pem

# 3. sources.json の baseUrl を https://aka-vecgen:8443/nudm-ueau/v1 にし、共有ネットワークに参加して起動する
cd ..
docker compose -f docker-compose.yml -f docker-compose.aka-av.yml up -d
```

### 加入者と認可

加入者は管理API（`/admin/v2/subscribers`）で登録します（MVP では aka-ossbss の画面から aka-provisioner を通して）。加入者は、状態（`active` / `inactive` / `terminated`）、認証ベクターの取得先（`sourceId`）、認可ポリシーのルール（`rules`）を持ちます。

```json
{"imsi":"440100123456789","state":"active","sourceId":"vecgen",
 "rules":[{"tenantId":42,"nasId":"*","ssids":["*"],"vlanId":100,"sessionTimeout":3600}]}
```

auth は、認証ベクターを取る前に、加入者の有無 → 状態 → テナントの刻印 → ルールの順に確かめ、どれかに当たれば取得先を呼ばずに拒否します。ルールの評価とログは[運用ガイド](docs/operation-guide.md)の 6 章にあります。

acct は Accounting の累計からの増分で、IMSI・テナント・日（日本時間）ごとの利用の集計を作ります（`GET /admin/v2/usage?date=YYYY-MM-DD`）。セッションは `GET /admin/v2/sessions?tenantId=…`（または `?imsi=…`）で取り出せます。

### RADIUS クライアント

RADIUS クライアント（エッジのトンネルの IP、名前、共有シークレット、「刻印を信頼する」の印）は、admin のコンテナのコマンドで管理します（設計概要 §12）。エッジを足すときに、WireGuard のピアの設定と一緒に行います。auth・acct はパケットのたびに Valkey を引くので、変更はすぐに効きます（読み直しは要りません）。

```bash
# 共有シークレットをランダムに作って登録する（表示されたシークレットをエッジに設定する。もう一度は表示しない）
docker compose exec admin aka-radius client add -ip 10.10.0.11 -name edge-01 -trust-stamp true -generate
# 決めてあるシークレットを使うときは、標準入力の 1 行で渡す（引数に書かない）
docker compose exec -T admin aka-radius client add -ip 10.10.0.12 -name edge-02 -trust-stamp true < secret.txt

docker compose exec admin aka-radius client list
docker compose exec admin aka-radius client update -ip 10.10.0.11 -generate        # シークレットの作り直し
docker compose exec admin aka-radius client update -ip 10.10.0.11 -trust-stamp false
docker compose exec admin aka-radius client remove -ip 10.10.0.11
```

- `-trust-stamp` は必須です。エッジは `true` にします（`false` のクライアントの刻印は使いません）。
- 共有シークレットは、空白と制御文字を除く ASCII で 16〜128 文字です。
- 操作は監査ログ（管理クライアント `cli`、操作者は `-operator`。既定 `cli`）に残します。共有シークレットは残しません。
- 管理API は読み出しだけです（`GET /admin/v2/clients`。共有シークレットは返しません）。

### 管理API

aka-provisioner から加入者を操作する API です（設計概要 §11）。admin は次の 2 つのファイルがないと起動しません。

- サーバー証明書 `deployments/certs/admin/server.pem`・`server.key`。`admin-cert create` で自己署名のものを作れます（上の「起動手順」）。今の証明書は `docker compose exec admin aka-radius admin-cert` で表示します（フィンガープリントを含む）。
- 管理クライアントのピン `deployments/secrets/admin-clients`。1 行に `識別名=クライアント証明書の SHA-256 のフィンガープリント` を書きます（`#` で始まる行は注釈。コロン区切り・大文字のままでよい）。証明書を入れ替える間は、同じ識別名に新旧の 2 行を書けます。フィンガープリントは `aka-radius fingerprint < client.pem` か `openssl x509 -in client.pem -noout -fingerprint -sha256` で確かめます。

どちらも、直したら `docker compose kill -s HUP admin` で読み直します（読めない・形が違うときは前のまま続け、ログに `ADMIN_CLIENTS_RELOAD_ERR`・`SERVER_CERT_RELOAD_ERR` を出します）。ピンのファイルはファイルごとマウントしているので、その場で書き換えます（`mv` で置き換えると、コンテナは古いファイルを見続けます。置き換えたときは `docker compose up -d --force-recreate admin`）。

同じホストで動かす呼び出し側は、共有の Docker ネットワーク（既定名 `aka-radius-admin`）に参加して `https://aka-radius-admin:9444` で接続します（サーバー証明書の SAN に `DNS:aka-radius-admin` を入れます）。

監査ログは、Valkey の Stream（`GET /admin/v2/audit-logs`）と、日ごとのファイル `deployments/audit/audit-YYYYMMDD.jsonl`（日付は日本時間。前の日までのファイルは gzip で圧縮し、14 日で消す）に書きます。ファイルは監視用の PC が取りに行きます。

各コンテナの healthcheck は、ループバックの口（`127.0.0.1:9090`。Valkey に届くか）を `aka-radius healthcheck` で確かめます。

API 仕様は [docs/openapi/admin-api.yaml](docs/openapi/admin-api.yaml)（2.0.0、`/admin/v2`）を参照してください。本PoC の Provisioning API（`/admin/v1`）は段階 2-3 でなくしました（OpenAPI は段階 2-6 で廃止しました。管理API の方針は D-13 管理API 詳細設計書）。

## テスト実行

```bash
# 全パッケージのテストを実行
make test

# データ競合の検出付きで実行（CI と同じ。CGO_ENABLED=1 が必要）
make test-race

# カバレッジ付きで実行
make test-cover

# 特定のパッケージのみ
go test ./internal/auth/...
```

## ドキュメント

| 文書 | 内容 |
|---|---|
| [運用ガイド](docs/operation-guide.md) | 導入、aka-vecgen とつなぐ、エッジ、管理API、加入者と認可、証明書、公開範囲、ログと監査ログ、バックアップ、障害時の確かめ |
| [設計概要](docs/design-overview.md) | MVP の決定事項と、段階 2 の各手順で決めたことの記録 |
| [ドキュメント一覧](docs/aka-radius_ドキュメント一覧_r70.md) | D-文書（設計・ログ・エラー処理・インフラ・各サーバーの詳細設計など）と、廃止した文書 |
| [管理API（OpenAPI）](docs/openapi/admin-api.yaml) | 管理API 2.0.0 |

## ライセンス

[MIT License](LICENSE)
