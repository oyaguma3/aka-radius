# aka-radius

[![Go](https://img.shields.io/badge/Go-1.27.3-00ADD8?logo=go)](https://go.dev/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

このリポジトリは、VPS に分けて置いて顧客を収容する **MVP セット**（[aka-mvpset](https://github.com/oyaguma3/aka-mvpset)）の RADIUS サーバーのノードです。PoC セット（[eapaka-pocset](https://github.com/oyaguma3/eapaka-pocset) の `pocset-v1.0.1`）の [eapaka-radius-server-poc](https://github.com/oyaguma3/eapaka-radius-server-poc) の履歴を引き継いで作りました（もとのコミット `main` の `1f8aad1`、`develop` の `86e05a9`）。

MVP のための改修は、[設計概要](docs/design-overview.md)のとおりに MVP セットの段階 2 で進めています。いまは段階 2-2 まで済んでいます。2-1 で 7 つの Go モジュールを 1 つのモジュールとコマンド `aka-radius` にまとめ、vector-api と Admin-TUI を外し、環境変数の名前を `RADIUS_` で始まるものに改めました。2-2 で vector-gateway を auth に取り込み、IMSI ごとの取得先から [認証ベクターAPI の契約](https://github.com/oyaguma3/aka-mvpset/blob/main/docs/av-api-contract.md) で認証ベクターを取るようにしました（鍵は持ちません）。テナントの刻印による認可・状態（2-3）、利用の集計（2-4）、管理API 2.0.0（2-3〜2-5）はこれからです。`docs/` の D-文書は段階 2-6 でまとめて改版するので、それまでは設計概要と本書を正とします（D-文書には PoC の名前と作りが残っています）。

## 概要

EAP-AKA/AKA' の RADIUS サーバーです。
Wi-Fi 認証 (WPA2/WPA3-Enterprise) 向けの RADIUS 認証・課金機能と、管理API を提供します。

## アーキテクチャ

1 つのイメージ `aka-radius` を、サブコマンドを変えて別々のコンテナで動かします（設計概要 §4）。

| サービス（サブコマンド） | 役割 | ポート |
|---|---|---|
| **auth** | RADIUS 認証 + EAP-AKA/AKA' ステートマシン制御。IMSI ごとの取得先から、認証ベクターを mTLS で 1 つずつ取る（取得先ごとに接続と遮断器を持つ） | UDP 1812 |
| **acct** | RADIUS 課金 (Accounting) | UDP 1813 |
| **admin**（任意） | 管理API（本PoC の Provisioning API。`/admin/v1`。加入者・RADIUSクライアント・認可ポリシー、監査ログ・セッションの参照）。aka-provisioner から mTLS で操作する。compose の profile `admin` で起動 | HTTPS 9444（既定 127.0.0.1 のみ） |
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
├── cmd/aka-radius/         # コマンド（サブコマンド auth・acct・admin の振り分け）
├── internal/
│   ├── auth/               # RADIUS 認証サーバー（サブコマンド auth。auth/source が取得先の登録簿とクライアント）
│   ├── acct/               # RADIUS 課金サーバー（サブコマンド acct）
│   ├── admin/              # 管理API（サブコマンド admin）
│   ├── radiussrv/          # auth と acct に共通の RADIUS の受け口（待ち受け、共有シークレット、Message-Authenticator、Proxy-State、Status-Server）
│   ├── envconf/            # 環境変数の読み込み（Valkey とログの共通の設定）
│   ├── apperr/             # エラー定義
│   ├── httputil/           # HTTP ユーティリティ
│   ├── logging/            # ログ設定
│   ├── model/              # 共通モデル
│   ├── validation/         # マスタデータの入力検証
│   ├── masterdata/         # マスタデータの Valkey アクセス
│   └── valkey/             # Valkey クライアント
├── configs/
│   └── fluent-bit/         # Fluent Bit 設定 (YAML)
├── deployments/
│   ├── docker-compose.yml  # Docker Compose 定義
│   ├── docker-compose.aka-av.yml  # aka-vecgen との共有ネットワーク用オーバーレイ
│   ├── .env.example        # 環境変数テンプレート
│   ├── sources/            # 取得先の登録簿と証明書の配置先（例の sources.example.json のほかは .gitignore 対象）
│   ├── certs/              # 管理API のサーバー証明書の配置先（中身は .gitignore 対象）
│   └── lnav_formats/       # lnav ログフォーマット定義
├── docs/                   # MVP の設計概要と、PoC の設計・運用ドキュメント（D-文書など）
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

# Docker Compose を起動
docker compose up -d
```

インターネットに公開する場合は、`RADIUS_FALLBACK_SECRET` を空のままにし、ファイアウォールで RADIUS（1812/1813/udp）の送信元をエッジのアドレスに限ってください。

### 主要な環境変数

compose の `.env` に書く変数です。アプリが読む環境変数（`RADIUS_VALKEY_HOST` など）は、compose がこれらから組み立てて渡します。

| 変数名 | 必須 | 説明 |
|---|---|---|
| `VALKEY_PASSWORD` | Yes | Valkey 接続パスワード（アプリには `RADIUS_VALKEY_PASSWORD` として渡す） |
| `RADIUS_FALLBACK_SECRET` | No | 予備の RADIUS 共有シークレット。設定すると、クライアント登録（送信元IP）のない送信元からのパケットもこの値で受け付ける。インターネットに公開する場合は空のままにする |
| `RADIUS_EAP_AKA_PRIME_NETWORK_NAME` | No | EAP-AKA' のネットワーク名 (デフォルト: `WLAN`) |
| `RADIUS_LOG_LEVEL` | No | ログレベル (`DEBUG` / `INFO` / `WARN` / `ERROR`、デフォルト: `INFO`) |
| `RADIUS_LOG_MASK_IMSI` | No | ログの IMSI をマスクするか (デフォルト: `false`。MVP の決定事項 26) |
| `RADIUS_ADMIN_CLIENTS` | No | 管理API の管理クライアント（`識別名=クライアント証明書のSHA-256フィンガープリント` のカンマ区切り。admin を使う場合は必須） |
| `RADIUS_ADMIN_BIND` | No | 管理API の 9444/tcp を公開するアドレス (デフォルト: `127.0.0.1`) |
| `RADIUS_ADMIN_NODE_NAME` | No | 管理API の `/status` が返すノード名 |
| `RADIUS_ADMIN_AUDIT_MAX` | No | 管理API の監査ログを Valkey に保持する件数の上限（おおよそ。デフォルト: `10000`。`GET /audit-logs` で参照する） |
| `PROVISIONING_SHARED_NETWORK` | No | 同じホストの呼び出し側と共有する Docker ネットワークの名前 (デフォルト: `eapaka-prov`。段階 2-5 で決め直す) |
| `RADIUS_VERSION` | No | イメージのタグと、起動ログの `build` に出すビルドの版 (デフォルト: `dev`) |
| `AKA_SHARED_NETWORK` | No | aka-vecgen の compose が作る共有ネットワークの名前（`docker-compose.aka-av.yml` 用。デフォルト: `aka-av`） |

詳細は `deployments/.env.example` を参照してください。

コンテナの中のアプリが読む環境変数は次のとおりです（compose を使わずに動かす場合）。

| サブコマンド | 環境変数 |
|---|---|
| 共通（auth・acct・admin） | `RADIUS_VALKEY_HOST`（既定 `valkey`）、`RADIUS_VALKEY_PORT`（既定 `6379`）、`RADIUS_VALKEY_PASSWORD`（必須）、`RADIUS_LOG_LEVEL`、`RADIUS_LOG_MASK_IMSI` |
| auth | `RADIUS_AUTH_LISTEN_ADDR`（既定 `:1812`）、`RADIUS_FALLBACK_SECRET`、`RADIUS_EAP_AKA_PRIME_NETWORK_NAME`（既定 `WLAN`。EAP-AKA' の `anId` と AT_KDF_INPUT）、`RADIUS_SOURCES_FILE`（既定 `/etc/aka-radius/sources/sources.json`） |
| acct | `RADIUS_ACCT_LISTEN_ADDR`（既定 `:1813`）、`RADIUS_FALLBACK_SECRET` |
| admin | `RADIUS_ADMIN_LISTEN_ADDR`（既定 `:9444`）、`RADIUS_ADMIN_TLS_CERT`・`RADIUS_ADMIN_TLS_KEY`（必須）、`RADIUS_ADMIN_CLIENTS`（必須）、`RADIUS_ADMIN_NODE_NAME`、`RADIUS_ADMIN_AUDIT_MAX` |

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

- 知らない項目や形の誤りがあると、auth は起動しません。動いている auth で登録簿を直したら、`docker compose kill -s HUP auth` で読み直します（失敗したら前の登録簿のまま続け、ログに `SOURCES_RELOAD_ERR` を出します）。
- 取得先ごとに遮断器を持ちます（一時的な誤りと契約違反が 5 回続くと開き、30 秒で半開き）。加入者がいない・止めてあるなどの拒否（403）は数えません。

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

段階 2-3 で加入者の口（管理API 2.0.0）ができるまでは、`source_id` は Valkey に直接入れます（例 `HSET sub:<IMSI> source_id vecgen`）。

### 管理API（任意）

aka-provisioner から加入者・RADIUSクライアント・認可ポリシーを操作する場合は、サーバー証明書を `deployments/certs/provisioning/`（`server.pem` / `server.key`）に置き、呼び出し側のクライアント証明書のフィンガープリントを `.env` の `RADIUS_ADMIN_CLIENTS` に登録して、profile を指定して起動します。

```bash
docker compose --profile admin up -d
```

同じホストで動かす呼び出し側は、共有の Docker ネットワーク（既定名 `eapaka-prov`）に参加して `https://provisioning-api:9444` で接続します（admin のサービスに別名 `provisioning-api` を付けています。サーバー証明書の SAN に `DNS:provisioning-api` を入れます）。

API 仕様は `docs/openapi/provisioning-api.yaml`、設計は D-13 を参照してください（段階 2-3〜2-5 で管理API 2.0.0 に改めます）。

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

`docs/` 配下に各種設計・運用ドキュメントがあります（全29件作成済み）。MVP の設計概要は [`docs/design-overview.md`](docs/design-overview.md) で、D-文書の体系の外に置いています。

### 設計ドキュメント (13件)

| No. | ドキュメント名 | 内容 |
|---|---|---|
| D-01 | 設計仕様書 | 全体アーキテクチャ・リポジトリ構成 |
| D-02 | Valkey データ設計仕様書 | データストア設計・キー設計 |
| D-03 | Vector-API IF 定義書 | API インターフェース・EAP-AKA ステートマシン |
| D-04 | ログ仕様設計書 | ログフォーマット・event_id 定義 |
| D-05 | Admin TUI 詳細設計書【前半】 | 画面設計・バリデーション |
| D-06 | エラーハンドリング詳細設計書 | 異常系処理・Circuit Breaker |
| D-07 | Admin TUI 詳細設計書【後半】 | モニタリング画面・ヘルプ |
| D-08 | インフラ設定・運用設計書 | Docker Compose・Valkey・Fluent Bit 設定 |
| D-09 | Auth Server 詳細設計書 | 認証サーバー詳細設計 |
| D-10 | Acct Server 詳細設計書 | 課金サーバー詳細設計 |
| D-11 | Vector API 詳細設計書 | Milenage 計算・SQN 管理 |
| D-12 | Vector Gateway 詳細設計書 | PLMN ルーティング・外部 API 連携 |
| D-13 | Provisioning API 詳細設計書 | マスタデータの REST API・mTLS・監査ログ |

### 開発ドキュメント (3件)

| No. | ドキュメント名 | 内容 |
|---|---|---|
| E-01 | 開発環境セットアップガイド | Go 環境構築・ローカル開発手順 |
| E-02 | コーディング規約（簡易版） | 命名規則・パッケージ構成 |
| E-03 | 共通ライブラリ(pkg)設計書 | pkg 配置方針・各パッケージ設計 |

### テストドキュメント (4件)

| No. | ドキュメント名 | 内容 |
|---|---|---|
| T-01 | テスト戦略書 | テスト方針・カバレッジ目標 |
| T-02 | 単体テスト仕様書 | 単体テストのケース（1,531 件） |
| T-03 | 結合テスト仕様書 | コンポーネント間連携テスト |
| T-04 | E2E テスト仕様書 | 実機テスト・擬似 E2E・実機異常系 (計 11 シナリオ)、接続方式01（aka-only-server）の E2E (16 シナリオ) |

### 構築・デプロイドキュメント (3件)

| No. | ドキュメント名 | 内容 |
|---|---|---|
| B-01 | ホストOS構築手順書 | Ubuntu Server インストール・セキュリティ設定 |
| B-02 | アプリケーションデプロイ手順書 | Docker Compose 起動・logrotate・バックアップ |
| B-03 | VPSデプロイ手順書（AWS Lightsail） | VPS への構築・デプロイ（ファイアウォール、SSH、RADIUS の公開範囲） |

### 運用ドキュメント (5件)

| No. | ドキュメント名 | 内容 |
|---|---|---|
| O-01 | 操作ガイド | 加入者登録・ポリシー設定・セッション監視の操作手順 |
| O-02 | ポリシー設定ガイド | NAS-ID/SSID 設定例・VLAN 割り当て例 |
| O-03 | 障害対応手順書 | 障害検知・切り分け・復旧手順・エスカレーション |
| O-04 | バックアップ・リストア手順書 | Valkey データのバックアップ/リストア・設定ファイル管理 |
| O-05 | ログ解析ガイド | lnav の使い方・頻出クエリ集・障害調査パターン |

### 補足資料 (1件)

| No. | ドキュメント名 | 内容 |
|---|---|---|
| S-01 | eapaka_test 利用ノウハウ | eapaka_test の設定・テストケース解説・トラブルシューティング |

詳細は [ドキュメント一覧](docs/EAP-AKA_RADIUS_PoC環境_ドキュメント一覧_r69.md) を参照してください。

## ライセンス

[MIT License](LICENSE)
