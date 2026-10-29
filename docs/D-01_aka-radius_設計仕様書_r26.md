# D-01 aka-radius 設計仕様書 (r26)

- 対象: aka-radius（EAP-AKA / EAP-AKA' の RADIUS サーバー）の全体の設計。システムの位置づけ、コンテナと口、パッケージの地図、外部依存、データと配置の要約、文書の地図。
- 正: 決定事項と段階の記録は設計概要 `docs/design-overview.md`、動きはコード。本書は全体を見渡すための要約で、細部は各 D-文書に書く。
- 本PoC（eapaka-radius-server-poc）の「ミニPC版 EAP-AKA RADIUS PoC環境 設計仕様書」を、MVP セットの段階 2 の改修に合わせて書き直したもの（題も改めた）。

## 1. 位置づけ

aka-radius は MVP セット（[aka-mvpset](https://github.com/oyaguma3/aka-mvpset)）のデータ面のノードで、VPS に置いて複数の顧客（テナント）の Wi-Fi 認証（WPA2/WPA3-Enterprise）を収容する。

```
[AP] ──RadSec──> [エッジ（radsecproxy）] ──RADIUS/UDP（サービス用の WireGuard）──> auth（1812）・acct（1813）
                                                                     ────────── aka-radius ────────── Valkey
                                                             auth ──mTLS──> 取得先（aka-vecgen、他の実装）
[aka-provisioner] ──mTLS（サービス用の WireGuard）──────────> admin（管理API 9444）
[運用者] ──SSH（運用用の WireGuard と踏み台）──> コマンド（RADIUS クライアント、証明書、登録簿の確かめ）
```

- 鍵（Ki・OPc）と SQN を持たない。認証のたびに、IMSI ごとに決めた取得先から認証ベクターAPI の契約（aka-mvpset の `docs/av-api-contract.md`）に従って 1 ベクターを mTLS で取る。MVP では取得先は aka-vecgen で、同じ VPS に並べる。
- IMSI ごとに、状態（`active` / `inactive` / `terminated`）・取得先 ID・認可ポリシーのルールを持つ。テナントへの所属は持たない（aka-ossbss の台帳にある）。
- テナントは、エッジが付けた刻印（Vendor-Specific。既定 Vendor-Id 32473、Vendor-Type 1、値はテナントの ID）で判定する。RADIUS クライアントはエッジ（のトンネルの IP）だけ。
- 課金レコードのために、IMSI・テナント・日（日本時間）ごとの利用の集計を作り、管理API で取り出させる。
- 管理API の呼び出し側は aka-provisioner だけ（aka-ossbss の画面 → aka-provisioner → 管理API）。

## 2. 範囲

| 区分 | 内容 |
|---|---|
| 対象 | EAP-AKA / EAP-AKA' のフル認証、認証ベクターの取得（取得先の登録簿、取得先ごとの遮断器）、テナントの刻印による認可、Access-Accept の属性（MSK、VLAN、Session-Timeout、Class）、Accounting とセッション、重複応答のキャッシュ、利用の集計、管理API 2.0.0、運用のコマンド、監査ログ |
| 対象外 | 鍵と SQN（取得先の役目）、加入者の一括投入（aka-ossbss のジョブが 1 件ずつの管理API を呼ぶ）、仮名と高速再認証（永続 ID を求めてフル認証にする）、EAP-SIM、CoA / Disconnect、冗長化、IPv6 の RADIUS クライアント、RadSec の終端（エッジの役目） |

本PoC にあった Admin-TUI、vector-api（鍵を持つベクターの計算）、独立したコンテナの vector-gateway、Provisioning API 0.4.0 はなくした（行き先は設計概要 §16）。

## 3. コンテナと口

1 つのイメージ `aka-radius`（distroless、UID 65532）を、サブコマンドを変えて別々のコンテナで動かす。プロセスを分けて、Accounting の負荷や管理API の不具合が認証に及ばないようにする（設計概要の決定事項 1）。

| コンテナ | 役割 | 詳しくは |
|---|---|---|
| `auth` | RADIUS 認証。EAP-AKA / EAP-AKA' の状態機械、ベクターを取る前の判定（加入者・状態・刻印・ルール）、取得先のクライアント（登録簿、取得先ごとの接続と遮断器）、セッションの作成 | D-09 |
| `acct` | RADIUS Accounting。重複の判定、累計からの増分、利用の集計、セッションの更新、セッションの索引の掃除 | D-10 |
| `admin` | 管理API 2.0.0（`/admin/v2`、mTLS）。運用のコマンドもこのコンテナで動かす | D-13、OpenAPI `docs/openapi/admin-api.yaml` |
| `valkey` | データの保存先（AOF） | D-02 |
| `fluent-bit` | ログをアプリごとのファイルに書く（aka-vecgen のログも受ける） | D-04、D-08 |

| 口 | ポート | 公開先 | 認証 |
|---|---|---|---|
| 認証 | 1812/udp | 既定 `127.0.0.1`。本番はサービス用の WireGuard のアドレス（`RADIUS_UDP_BIND`） | RADIUS クライアントごとの共有シークレット、Message-Authenticator 必須 |
| Accounting | 1813/udp | 同上 | 共有シークレット（Request Authenticator） |
| 管理API | 9444/tcp | 既定 `127.0.0.1`（`RADIUS_ADMIN_BIND`） | mTLS（管理クライアントのピン） |
| healthcheck | 各コンテナの `127.0.0.1:9090`（`GET /healthz`） | コンテナの中だけ | なし（Valkey に届くかを返す） |
| Valkey | 6379/tcp | `127.0.0.1` | パスワード |
| Fluent Bit | 24224 | `127.0.0.1` | なし（Docker のログドライバーだけが使う） |
| 取得先へ | auth から（aka-vecgen は 8443） | 共有の Docker ネットワーク `aka-av` | mTLS（取得先ごとのクライアント証明書） |

サブコマンドの一覧:

| 種類 | サブコマンド | 内容 |
|---|---|---|
| サーバー | `auth`・`acct`・`admin` | 上のコンテナ |
| 運用のコマンド | `client add / list / update / remove` | RADIUS クライアント（エッジ）。共有シークレットと刻印を信頼する印 |
| | `sources check` | 取得先の登録簿の検査と、各取得先への接続の確かめ（auth のコンテナで動かす） |
| | `admin-cert`・`admin-cert create` | 管理API のサーバー証明書の表示と作成 |
| | `fingerprint` | 証明書の SHA-256 のフィンガープリント（ピンのファイルに書く値） |
| | `healthcheck` | 同じコンテナの healthcheck の口に問い合わせる（compose の healthcheck） |

使い方は運用ガイド `docs/operation-guide.md` と README。

## 4. 処理の概要

### 4.1 認証（auth）

1. Access-Request を受け、送信元の共有シークレットで検証する（Message-Authenticator がなければ捨てる）。重複応答のキャッシュに当たれば前の応答を返す。
2. EAP の Identity から IMSI と方式（先頭 `0` は EAP-AKA、`6` は EAP-AKA'）を決める。仮名・高速再認証の ID なら永続 ID を求める。
3. ベクターを取る前に、加入者がいる → 状態が `active` → 刻印が 1 つだけで信頼する送信元から → テナント・NAS-Identifier・SSID が一致するルールがある、を確かめる。どれかに当たれば取得先を呼ばずに拒否する。
4. 取得先から 1 ベクターを取り、Challenge を送る。再同期は 1 回の会話で 1 回まで。
5. 端末の応答を確かめ、状態をもう一度確かめてから、セッションを作り、利用の集計に認証の成功を足し、Access-Accept（MSK、Class、ルールの VLAN と Session-Timeout）を返す。

詳しくは D-09（状態機械、取得先のクライアント、判定）。

### 4.2 Accounting（acct）

Accounting-Request（Start・Interim・Stop）ごとに、1 つの Lua で重複の判定・前回の累計からの増分・利用の集計への足し込み・セッションと索引の更新を行う（Gigawords と Event-Timestamp を扱う）。Valkey に書けないときは Accounting-Response を返さない（NAS に送り直させ、集計の取りこぼしを防ぐ）。詳しくは D-10。

### 4.3 RADIUS の受け口（auth と acct に共通）

待ち受け、共有シークレットの解決（`client:{ip}`、なければ予備の共有シークレット、それもなければ捨てる）、Message-Authenticator、Proxy-State、Status-Server、重複応答のキャッシュ（送信元・Identifier・Request Authenticator ごとに 30 秒）、要求ごとの期限（既定 4 秒）、同時に処理する上限（既定 256）を `internal/radiussrv` にまとめた。重複応答のキャッシュは、エッジの再送を新しい要求として処理しないためにある（設計概要 §9）。

### 4.4 管理API（admin）と運用のコマンド

管理API は加入者（状態・取得先・ルール）の登録・変更・状態の変更・削除、一覧と一括取得、セッション、利用の集計、RADIUS クライアントと取得先の読み出し、ノードの状態、監査ログを持つ。RADIUS クライアントと取得先の登録簿は管理面から書けないようにし、ノードの上のコマンドとファイルで扱う（設計概要の決定事項 3・6）。変更の操作は監査ログ（Valkey の Stream と日ごとのファイル）に残す。詳しくは D-13。

## 5. パッケージの地図

1 つのモジュール `github.com/oyaguma3/aka-radius`。コマンドは `cmd/aka-radius`、ほかはすべて `internal/`。

| パッケージ | 役割 | 使うもの |
|---|---|---|
| `cmd/aka-radius` | サブコマンドの振り分けと、運用のコマンド（`client`・`sources`・`admin-cert`・`fingerprint`・`healthcheck`） | — |
| `internal/auth` | サブコマンド auth の入口（設定、起動、`SIGHUP` で登録簿の読み直し） | auth |
| `internal/auth/server` | RADIUS のハンドラー（Access-Request の検証、トレースID、エンジンの呼び出し） | auth |
| `internal/auth/engine` | 認証の流れ（判定、ベクターの取得、Challenge の検証、再同期、Accept） | auth |
| `internal/auth/eap`・`eap/aka`・`eap/akaprime` | EAP のパケット、Identity の解析、状態機械、方式ごとの鍵の導出と Challenge | auth |
| `internal/auth/session` | EAP の会話（`eap:{traceId}`）の読み書き | auth |
| `internal/auth/source` | 取得先の登録簿（ファイル）、取得先ごとの mTLS のクライアントと遮断器、遮断器の状態の報告（`stat:sources`）。`sourcetest` は試験用の偽の取得先 | auth（登録簿の読み出しは admin とコマンドも） |
| `internal/auth/radius`・`store`・`config` | Access-Accept などの組み立て、Valkey の接続、設定と定数 | auth |
| `internal/acct` と `acct/processor`・`server`・`store`・`radius`・`config` | サブコマンド acct。Accounting の処理、Lua による記録、属性の読み取り | acct |
| `internal/admin` と `admin/api`・`auth`・`config` | サブコマンド admin。管理API の口、mTLS とピンの読み直し、設定 | admin |
| `internal/radiussrv` | RADIUS の受け口（§4.3）、刻印の読み取りと信頼の印 | auth、acct |
| `internal/subscriber` | 加入者（`sub:`・`subs`・`cnt:src:`）の読み書き（Lua）とルールの評価 | auth、admin |
| `internal/sessions` | セッション（`sess:`）と IMSI・テナントの索引、掃除 | auth、acct、admin |
| `internal/usage` | 利用の集計のキー、日本時間の日の割り当て、認証の成功の足し込み、取り出し | auth、acct、admin |
| `internal/clients` | RADIUS クライアント（`client:`・`clients`）の読み書き | admin、コマンド |
| `internal/audit` | 監査ログ（Stream と日ごとのファイル、圧縮と削除） | admin、コマンド |
| `internal/health` | ループバックの healthcheck の口 | auth、acct、admin |
| `internal/envconf` | 環境変数（`RADIUS_`）の読み込みと共通の設定 | すべて |
| `internal/logging` | ログの設定と IMSI のマスク | すべて |
| `internal/valkey` | Valkey のクライアントの共通処理 | すべて |

- 共通のパッケージは、サブコマンドの間で同じ処理を重ねて書かないためにある。共通のパッケージどうしの依存は持たない（`internal/radiussrv` が `internal/envconf` を読むのは例外）。
- コーディングの決まりは E-02、試験の方針は T-01。

## 6. 技術と外部依存

| 区分 | 内容 |
|---|---|
| 言語 | Go 1.27.3（`go.mod`）。HTTP は `net/http`（メソッドつきの ServeMux）、JSON は `encoding/json/v2`、ログは `log/slog` |
| データ | Valkey 9.1.2 |
| ログ | Fluent Bit 4.2.8（Docker の `fluentd` のログドライバーから受ける） |
| イメージ | ビルドは `golang:1.27.3-bookworm`、実行は `gcr.io/distroless/static-debian13:nonroot` |
| 配置 | Docker Compose（ホストは Debian 13 を主に想定） |

外部依存（`go.mod` の直接の依存）:

| パッケージ | 用途 |
|---|---|
| `layeh.com/radius` | RADIUS のパケットと UDP の待ち受け |
| `github.com/oyaguma3/go-eapaka` | EAP-AKA / EAP-AKA' のパケットと鍵の導出 |
| `github.com/redis/go-redis/v9` | Valkey のクライアント |
| `github.com/sony/gobreaker` | 取得先ごとの遮断器 |
| `github.com/google/uuid` | トレースID とセッションの ID |
| `github.com/alicebob/miniredis/v2` | 試験用の Valkey（試験だけ） |
| `go.uber.org/mock` | 試験のモック（auth のハンドラーの試験だけ） |

本PoC から Gin、resty、tview・tcell、`wmnsk/milenage` などを外した。

## 7. データの要約

Valkey に、加入者（`sub:`）、RADIUS クライアント（`client:`）、EAP の会話（`eap:`、60 秒）、セッション（`sess:`、24 時間）と索引、Accounting の重複の判定（`acct:seen:`、24 時間）、利用の集計（`usage:`、8 日）、遮断器の状態の報告、セッションの数、監査ログの Stream を置く。`maxmemory 1gb`、`noeviction`、AOF（1 秒ごと）。キーと項目、書く・読むコンテナ、原子性は D-02。

Valkey に置かないもの:

| もの | 置き場所 |
|---|---|
| 取得先の登録簿と、取得先ごとのクライアント証明書・鍵・サーバーの CA | ディレクトリ `deployments/sources/`（auth が読み、admin は鍵を除いて読む）。`SIGHUP` で読み直す |
| 管理クライアントのピン | ファイル `deployments/secrets/admin-clients`（Docker の secrets）。`SIGHUP` で読み直す |
| 管理API のサーバー証明書と鍵 | ディレクトリ `deployments/certs/admin/`。`SIGHUP` で読み直す |
| 監査ログの日ごとのファイル | ディレクトリ `deployments/audit/` |

## 8. 守り

- RADIUS（UDP）は共有シークレットで守る。RADIUS クライアントはエッジだけで、送信元を nftables の `DOCKER-USER` でエッジのトンネルの IP に絞る（Docker の公開ポートにはホストの UFW などが及ばないため）。予備の共有シークレット `RADIUS_FALLBACK_SECRET` は空のままにし、登録していない送信元のパケットは捨てる。
- 刻印は、`trust_stamp` の印のある RADIUS クライアント（エッジ）が付けたものだけを使う。2 つ以上、10 進の数でない値は拒否する。
- 取得先とは mTLS だけでつなぐ（平文の HTTP は使わない）。取得先へのクライアント証明書の鍵は、このノードで作り外に出さない。
- 管理API は mTLS で、ピンのファイルに書いた管理クライアントだけを通す。RADIUS クライアントの共有シークレットと取得先の鍵は、管理API から読めない。
- 共有シークレット・認証ベクター（RAND・AUTN・XRES・CK・IK）・鍵はログにも監査ログにも出さない。IMSI はマスクしない（`RADIUS_LOG_MASK_IMSI` の既定 `false`。調査のトレーサビリティを優先する）。ログを取りに行く監視用の PC も、ノードと同じく人を限る。
- Valkey にはパスワードをかけ、ホストの外に出さない。共有シークレットは Valkey に平文で入っているので、バックアップの写しは運用者だけが読める場所に置く。
- 要求ごとの期限と同時に処理する上限で、取得先が遅いときに goroutine が積み上がらないようにする。

## 9. 配置の要約

- `deployments/docker-compose.yml` に auth・acct・admin・Valkey・Fluent Bit を置く。auth は `docker-compose.aka-av.yml` を重ねて、aka-vecgen の compose が作る共有ネットワーク `aka-av` に入る。admin は同じホストで試すときのための共有ネットワーク `aka-radius-admin` に入る。
- 1812/udp・1813/udp・9444/tcp は、本番ではサービス用の WireGuard のアドレスにだけ結び付ける。
- 設定は `deployments/.env`（項目は `deployments/.env.example` と README）。アプリが読む環境変数の接頭辞は `RADIUS_`。
- 更新はイメージを手元か CI でビルドして送る（VPS の上ではビルドしない）。

詳しくは D-08（インフラ設定・運用設計書）と運用ガイド。

## 10. 文書の地図

| 文書 | 内容 |
|---|---|
| 設計概要 `docs/design-overview.md` | MVP の決定事項と段階の記録（なぜそうしたか） |
| 運用ガイド `docs/operation-guide.md` | 導入、取得先とエッジとのつなぎ方、証明書、ログ、バックアップ、障害時の確かめ |
| README | 起動の手順、環境変数、コマンドの一覧 |
| OpenAPI `docs/openapi/admin-api.yaml` | 管理API 2.0.0 の口と項目（正） |
| D-01 設計仕様書（本書） | 全体の要約 |
| D-02 Valkey データ設計仕様書 | キー、項目、期限、原子性 |
| D-04 ログ仕様設計書 | ログの形と `event_id` の一覧、監査ログ |
| D-06 エラーハンドリング詳細設計書 | 誤りの分類と、コンテナごとの扱い |
| D-08 インフラ設定・運用設計書 | compose、ネットワーク、ログの経路、配置 |
| D-09 Auth Server 詳細設計書 | 認証の流れ、状態機械、取得先のクライアント、判定 |
| D-10 Acct Server 詳細設計書 | Accounting、重複の判定、利用の集計、セッション |
| D-13 管理API 詳細設計書 | 管理API の方針、認証、監査、状態の規則 |
| E-01・E-02・T-01 | 開発環境、コーディングの決まり、試験の方針 |
| O-05 ログ解析ガイド | ログの調べ方 |
| S-01 eapaka_test 利用ノウハウ | 試験の道具 eapaka_test の使い方 |

文書の一覧と版数、廃止した文書はドキュメント一覧にある。他の文書を参照するときは ID と名前だけを書き、版数を書かない。

## 改訂履歴

| 版数 | 日付 | 内容 |
|------|------|------|
| r1 | - | 初版 |
| r2 | - | パッケージ利用マップ追加、docker-compose.yml最終化 |
| r3 | 2025-12-30 | Valkey接続管理セクション追加（接続タイムアウト設定、再接続方針）、EAP-AKA'対応明記 |
| r4 | 2026-01-05 | Vector Gateway追加：アーキテクチャ図更新、ノード一覧にVector Gateway追加、パッケージ利用マップにGateway列追加、コンテナ構成更新、リポジトリ構成にvector-gateway追加、docker-compose.yml更新、Vector Gateway環境変数セクション追加 |
| r5 | 2026-01-20 | パッケージ利用マップ更新: google/uuidの用途説明をAuth Server（生成）とAcct Server（パース検証）で区別 |
| r6 | 2026-01-26 | インフラ基盤統一: Valkeyバージョン7.2→9.0、Fluent Bitイメージをfluent/fluent-bit:3.0に統一、LOG_OUTPUT_PATH環境変数削除、RADIUS_SECRET_KEY→RADIUS_SECRETに統一、docker-compose.ymlをD-08と整合化（x-logging/x-timezone追加、LOG_MASK_IMSI追加） |
| r7 | 2026-01-27 | docker-compose.yml相対パス修正: セクション7のbuildパスを`./apps/...`→`../apps/...`に修正（D-08との整合性確保） |
| r8 | 2026-01-27 | Fluent Bit設定ファイルの相対パス修正（`./configs/...`→`../configs/...`、deployments基準でD-08と整合） |
| r9 | 2026-02-16 | 実装コードとの不整合20件を修正: データモデル注記追加（RadiusClient/Subscriber/Policy）、パッケージ利用マップ更新（envconfig/resty/tcell/mock/miniredis）、Valkeyキースキーマ概要追加、コンテナ構成詳細更新（healthcheck/fluent-bitポート/テストベクターモード/healthエンドポイント）、docker-compose.ymlを実装ファイルと完全同期、セットアップ手順のenvconfig記述修正 |
| r10 | 2026-10-04 | Vector Gateway 接続方式ID `01`（外部の aka-only-server、mTLS、任意）対応: 2章構成図に aka-only-server を追加し注記追加、3.1 Vector Gateway の処理フロー更新、3.5に aka-only-server への接続設定追加、4章に Outbound 接続と証明書管理を追記、5章に vector-gateway の証明書マウントと docker-compose.aka-av.yml の注記追加、6章リポジトリ構成に docker-compose.aka-av.yml と certs/ を追加、7章 docker-compose.yml の vector-gateway に `VECTOR_GATEWAY_AKAONLY_*` と certs ボリュームを追加しオーバーレイを掲載、8章に `VECTOR_GATEWAY_AKAONLY_*`・`AKA_SHARED_NETWORK` と接続方式ID `01` を追加。既存記載の実装との不一致を修正（7章 docker-compose.yml を実ファイル全文と一致させた: build を `context: ..`＋`dockerfile: apps/<アプリ名>/Dockerfile` に、logging をアンカーのマージ＋サービスごとの `tag: "app.<サービス名>"` に、fluent-bit を `fluent/fluent-bit:4.2`・`command` 指定・`fluent-bit.yaml` マウントに修正し、vector-api に `TEST_VECTOR_ENABLED`／`TEST_VECTOR_IMSI_PREFIX` を追加。オーバーレイもコメント込みの全文に。5章の vector-api・fluent-bit 行と6章 configs/fluent-bit の構成を実装に合わせた）。Valkey 復旧検知ログ（`VALKEY_CONN_RESTORED`）は実装にないため記載を修正 |
| r11 | 2026-10-04 | テストベクターモードでも加入者登録を必須にした Vector API の実装修正の反映: 3.1 Vector API の設定読込の説明を「固定テストベクターを返却」から「Ki/OPc/AMF をテスト用固定値に置き換えて計算（加入者の登録・SQN管理は通常どおり必要）」に修正 |
| r12 | 2026-10-04 | auth-server の LOG_LEVEL 対応に伴う compose 修正の反映: §7 docker-compose.yml の auth-server / vector-gateway / vector-api の environment に `LOG_LEVEL: ${LOG_LEVEL:-INFO}` を追加（実ファイルと一致）し、対象サービス（acct-server は INFO 固定のため渡さない）の注記を追加 |
| r13 | 2026-10-04 | acct-server の LOG_LEVEL 対応に伴う compose 修正の反映: §7 docker-compose.yml の acct-server の environment に `LOG_LEVEL: ${LOG_LEVEL:-INFO}` を追加（実ファイルと一致）し、ログレベルの注記を4サーバー（auth-server / acct-server / vector-gateway / vector-api）とも `.env` の値を渡す形に修正（「acct-server は INFO 固定のため渡さない」を削除） |
| r14 | 2026-10-04 | §3.3 Valkeyキースキーマ概要を実装（D-02 r17）に合わせて修正: 実装に存在しない `audit:log`（監査ログ）と `acct:{ID}`（課金セッション）を削除し、`sess:{UUID}`（アクティブセッション）・`idx:user:{IMSI}`（ユーザー検索インデックス）・`acct:seen:{Acct-Session-Id}`（Accounting重複検出キャッシュ）を追加。`sub:{IMSI}` の使用コンポーネントから Auth Server を削除（参照するのは Vector API と Admin TUI）、`eap:{UUID}` の用途を EAP認証コンテキストに修正。監査ログは Valkey に保存せず Admin TUI の標準出力に JSON で出力する旨の注記を追加し、§3.1 Admin TUI の監査ログの説明も同様に修正 |
| r15 | 2026-10-04 | vector-gateway のバックエンド向けタイムアウトを auth-server より短くした実装修正の反映: §3.5 Vector Gateway接続設定の Vector Gateway → Vector API / aka-only-server のタイムアウトを 5秒 → 3秒に修正し、Auth Server（5秒）より短くする理由の注記を追加。§7 docker-compose.yml の `VECTOR_GATEWAY_INTERNAL_TIMEOUT` / `VECTOR_GATEWAY_AKAONLY_TIMEOUT` の既定値を `3s` に修正（実ファイルと一致）、§8 環境変数表の既定値を `3s` に修正 |
| r16 | 2026-10-04 | インターネット公開（VPS 等）に向けた安全面の実装修正の反映: §4 ファイアウォールの「Block: 上記以外全て」を、UFW の受信規則としての記載であり Docker の公開ポートには及ばない旨に訂正し、Docker と UFW の関係の注記（公開ポートは UFW を素通りする、内部向けは 127.0.0.1 にバインド、RADIUS の送信元はクラウド側のファイアウォールまたは `DOCKER-USER` で絞る）を追加。§4 に「インターネット越しに RADIUS を受ける場合（VPS 等）」を新設（共有シークレットの強度、RadSec はスコープ外、`RADIUS_SECRET` を空にしてクライアント登録、NAT 配下の AP と送信元IP、クラウド側のファイアウォール、同一ホストからの試験の送信元IP）。§4 シークレット管理の `RADIUS_SECRET` を任意（空を推奨）とし、空の場合の動作（`RADIUS_NO_SECRET` で破棄）、Valkey エラー時等のフォールバック、compose の `${RADIUS_SECRET:-}`、起動ログの `radius_secret_fallback` と WARN を追記。§5 の fluent-bit の公開ポートを `127.0.0.1:24224/tcp, 127.0.0.1:24224/udp` に修正し、公開ポートの注記を追加。§7 docker-compose.yml を実ファイルと一致させた（auth-server / acct-server の `RADIUS_SECRET: ${RADIUS_SECRET:-}`、fluent-bit の `127.0.0.1:24224` バインドとコメント）。§9 の FW設定・デプロイに注記を追加 |
| r17 | 2026-10-04 | §2 の Acct Server の検証を「Message-Authenticator検証」から実装どおり「Request Authenticator の検証（Accounting-Request。Status-Server は Message-Authenticator）」に訂正。§6 のディレクトリ構成のセットアップ手引書群を B-01〜B-03 に（VPS 向けのデプロイ手順書 B-03 を追加） |
| r18 | 2026-10-07 | Admin TUI の加入者・RADIUSクライアント・認可ポリシーの store と validation を pkg に移した実装修正（Provisioning API（D-13）と共通で使うため。E-03 r11）の反映: §6 開発リポジトリ構成の pkg に `validation/`・`masterdata/` を追加 |
| r19 | 2026-10-08 | Provisioning API（provisioning-api。D-13）の実装の反映: §2 の構成図に `8. provisioning-api`（任意）と注記、§3 の実装ノードを6つに（§3.1 に Provisioning API）、§3.2 のパッケージ利用マップに Prov 列（gin、go-redis、envconfig、slog）、§3.3 のキースキーマの使用コンポーネントに Provisioning API、§4 の公開ポートの注記に 9444/tcp（既定 127.0.0.1、`PROVISIONING_API_BIND`）、§5 のコンテナ一覧に provisioning-api（profile `provisioning`）、§6 のリポジトリ構成に `apps/provisioning-api`・`docs/openapi`・`certs/provisioning`、§7 の docker-compose.yml を実ファイルと一致させた（provisioning-api サービス）、ログレベルの注記の対象に provisioning-api |
| r20 | 2026-10-08 | 同じホストの BFF から provisioning-api に接続するための共有ネットワーク（既定名 `eapaka-prov`。D-13 r4）の反映: §2 の provisioning-api の注記、§5 のコンテナ一覧に共有ネットワークへの参加を追記し、§7 の docker-compose.yml を実ファイルと一致させた |
| r21 | 2026-10-08 | RADIUSクライアントにサーバー採番の ID を導入（D-02 r23、D-13 r5）: §3.1 のデータモデル注記の RadiusClient に `id`（索引 `idx:client:{ID}`、カウンター `seq:client`）を追加 |
| r22 | 2026-10-09 | Provisioning API の監査ログとセッションの参照（D-13 r6、API 0.3.0）の反映: コンポーネント一覧の Provisioning API に `/audit-logs`・`/sessions` と、監査ログの Stream（`audit:prov`）への保存を追記。compose の抜粋に `PROVISIONING_API_AUDIT_MAX` を追加 |
| r23 | 2026-10-12 | PoC セット（eapaka-pocset）の品質改善のステップ 3（Go 1.27.2 への更新とイメージの版の固定）の反映: compose の写しのイメージを実ファイルに同期（`valkey/valkey:9.0` → `9.1.2`、`fluent/fluent-bit:4.2` → `4.2.8`。パッチ版まで固定） |
| r24 | 2026-10-12 | PoC セット（eapaka-pocset）の品質改善のステップ 4（ビルドの版の埋め込み、`VALKEY_PASSWORD` が空なら compose が断ること、Valkey のヘルスチェック、停止の猶予）の反映: §7 の `docker-compose.yml` の全文を実ファイルに同期（5 つのアプリの build.args `VERSION: ${POC_VERSION:-dev}`、`REDIS_PASS` と `--requirepass` の `${VALKEY_PASSWORD:?…}`、Valkey の `VALKEYCLI_AUTH` と healthcheck `valkey-cli ping \| grep -q PONG`、vector-gateway / vector-api / provisioning-api の `stop_grace_period: 20s`）し、それぞれの注記を追加。§4 のシークレット管理の `VALKEY_PASSWORD` に空なら compose が断ることを追記。§8 の `VECTOR_GATEWAY_INTERNAL_TIMEOUT` に 0 以下なら起動しないことを、§3.5 の Auth Server → Vector Gateway に接続の確立を含む要求全体の上限であることを追記 |
| r25 | 2026-10-12 | PoC セット（eapaka-pocset）の品質改善のステップ 6（文書の整理）の反映: §3.4 の Valkey 障害の復旧検知の行の D-04 の参照から版数を外した（1 か所。今の版はドキュメント一覧で分かるため、参照に版を書かない）。§6 のリポジトリ構成の `docs/subdocs/`・`docs/testresults/` に、git の管理外（非公開）である旨を追記 |
| r26 | 2026-10-29 | MVP の段階 2-6 で改版（設計概要 §17.2）: 題を「aka-radius 設計仕様書」に改め、MVP の作りに合わせて全面的に書き直した（設計概要 §1〜§6・§14 を骨に、MVP セットのデータ面での位置づけ、範囲、コンテナと口とサブコマンド、処理の概要、`internal/` のパッケージの地図、外部依存（`go.mod`）、データの要約、守り、配置の要約、文書の地図）。ミニPC と Admin-TUI・vector-api・vector-gateway・Provisioning API・PLMN による振り分けの記述、compose の全文の写し、ホスト OS の設定を削った（配置は D-08 と運用ガイド） |
