# D-09 Auth Server詳細設計書 (r26)

## ■セクション1: 概要

### 1.1 目的

本書は、aka-radius の認証サーバー（サブコマンド `auth`。以下「auth」）の実装レベルの設計を定義する。

- 正はコード（`internal/auth/` と、auth が使う共有のパッケージ）である。本書はコードの写しを持たず、何をどうしているか・なぜそうしたかを表と短い説明で書く。型や関数の細部はコードを見る。
- MVP で決めたこと（決定事項と段階の記録）は設計概要（`docs/design-overview.md`）にある。運用の手順は運用ガイド（`docs/operation-guide.md`）にある。
- auth は、本PoC（eapaka-radius-server-poc）の auth-server に、vector-gateway（認証ベクターの取得）を取り込んだものである（設計概要の決定事項 2）。

### 1.2 スコープ

| 範囲 | 内容 | 節 |
|------|------|----|
| 設定と起動 | 環境変数、起動の順、`SIGHUP` の読み直し、停止、healthcheck | §3 |
| UDP の受け口 | 共有シークレットの解決、重複応答のキャッシュ、要求の期限、同時処理の上限、パニックの回復（auth と acct に共通の `internal/radiussrv`） | §4 |
| RADIUS 処理 | Message-Authenticator の検証、属性の取り出し、応答の組み立て、Proxy-State、Status-Server | §5 |
| EAP-AKA / EAP-AKA' | Identity の取り方、フル認証への誘導、状態機械、鍵の導出、Challenge の検証、再同期 | §6 |
| 認証ベクターの取得 | 取得先の登録簿、取得先ごとの mTLS の接続と遮断器、契約に合わせた要求と応答の確かめ | §7 |
| 認可 | ベクターを取る前の判定（加入者の有無・状態・テナントの刻印・ルール）、Access-Accept の属性 | §8 |
| セッションと利用の集計 | EAP の会話の状態、セッションと索引の作成、利用の集計の `auth_ok` | §9 |

### 1.3 関連ドキュメント

| 文書 | 参照内容 |
|------|---------|
| 設計概要（`docs/design-overview.md`） | 決定事項、データの一覧（§6）、取得先（§7）、認可（§8）、重複応答のキャッシュ（§9） |
| 運用ガイド（`docs/operation-guide.md`） | 取得先とつなぐ手順、RADIUS クライアント、証明書、障害時の確かめ |
| 認証ベクターAPI の契約（aka-mvpset の `docs/av-api-contract.md`） | 取得先との要求と応答、結果の分類（§9.3）、aka-radius の改修 R1〜R7（§16.2） |
| D-01 aka-radius 設計仕様書 | システムの構成、コンテナ、パッケージ |
| D-02 Valkey データ設計仕様書 | キーと項目 |
| D-04 ログ仕様設計書 | `event_id` の一覧、ログの形、IMSI のマスク |
| D-06 エラーハンドリング詳細設計書 | 誤りの分類、タイムアウト、遮断器 |
| D-08 インフラ設定・運用設計書 | compose、環境変数、ネットワーク |
| D-10 Acct Server詳細設計書 | セッションの更新と削除、Accounting の利用の集計 |
| D-13 管理API 詳細設計書 | 加入者の登録（状態・取得先・ルール）、`/sources`・`/status` |
| E-02 コーディング規約（簡易版） | 書き方 |
| T-01 テスト戦略書 | 試験の層と CI |

### 1.4 対象外

| 機能 | RFC | 扱い |
|------|-----|------|
| 仮名による認証 | RFC 4187 / RFC 5448 | 仮名を受けたら永続 ID を求めてフル認証にする（§6.4） |
| 高速再認証 | RFC 4187 / RFC 5448 | 再認証 ID を受けたら同上 |
| EAP-Notification | RFC 4187 | 使わない |
| RadSec | RFC 6614 | エッジ（radsecproxy）が受け、auth には RADIUS/UDP で届く |
| Chargeable-User-Identity | RFC 4372 | 扱わない |
| AT_KDF の交渉 | RFC 5448 | KDF=1 だけ |
| EAP-SIM | RFC 4186 | Identity の先頭が `1`・`3`・`5` なら拒否する |
| EAP-AKA と EAP-AKA' の切り替え（NAK） | RFC 3748 | 方式は Identity の先頭の文字で決める（設計概要の決定事項 3） |
| CoA / Disconnect、IPv6 の RADIUS クライアント | — | 扱わない（設計概要 §2） |

### 1.5 準拠規格

| 規格 | 対応範囲 |
|------|---------|
| RFC 2865 | Access-Request / Accept / Reject / Challenge、Proxy-State、Class、Session-Timeout、Vendor-Specific |
| RFC 2868 | Tunnel-Type・Tunnel-Medium-Type・Tunnel-Private-Group-Id（VLAN） |
| RFC 3579 | EAP-Message、Message-Authenticator（Access-Request に必須）、State |
| RFC 3748 | EAP の枠組み、EAP-Response/Identity |
| RFC 4187 | EAP-AKA（フル認証） |
| RFC 5448 | EAP-AKA'（フル認証、KDF=1） |
| RFC 5080 §2.2.2 | 重複した要求の見分け方（重複応答のキャッシュのキー） |
| RFC 5997 | Status-Server |
| 3GPP TS 29.503 | 認証ベクターの取得（Nudm_UEAU の GenerateAv。契約が範囲と違いを決める） |

### 1.6 用語

| 用語 | 説明 |
|------|------|
| 取得先 | 認証ベクターを返すサーバー（契約の「取得先」。MVP では aka-vecgen）。加入者ごとに `source_id` で決まる |
| 取得先の登録簿 | 取得先の ID・URL・証明書を並べたファイル（`RADIUS_SOURCES_FILE`。§7.2） |
| 遮断器 | 取得先ごとに持つサーキットブレーカー（gobreaker。§7.7） |
| 刻印 | エッジが付けるテナントの印（Vendor-Specific。既定 Vendor-Id 32473、Vendor-Type 1。値はテナントの ID の 10 進） |
| 信頼の印 | RADIUS クライアント（`client:{ip}`）の `trust_stamp`。`1` のクライアントの刻印だけを使う |
| トレースID | EAP の会話ごとの UUID。ログの `trace_id`、RADIUS の State、`eap:` のキー、取得先への `X-Trace-ID` で同じ値を使う |
| セッションの UUID | Access-Accept の Class に入れる UUID（`sess:` のキー） |
| RAND・AUTN・XRES・CK・IK | 認証ベクターの要素。EAP-AKA' では CK・IK の代わりに CK'・IK'（取得先が導出したもの）を受ける |
| K_aut・MSK | EAP の鍵（AT_MAC の計算、MS-MPPE-Recv-Key / Send-Key の元） |
| ネットワーク名（anId） | EAP-AKA' の Access Network Identity。取得先への `anId` と AT_KDF_INPUT に同じ値を使う |

### 1.7 本PoC からの主な変更

| 項目 | 本PoC | MVP |
|------|-------|-----|
| ベクターの取得 | 別のコンテナの vector-gateway に HTTP で頼み、その先の vector-api か外部の取得先が鍵を持つ | auth が取得先の登録簿から、加入者ごとの取得先に mTLS で直接要求する。取得先ごとに接続と遮断器を持つ（§7） |
| EAP-AKA' の CK'・IK' | auth-server が CK・IK から導出する | 取得先が導出して返す。auth は MK 以降だけを導出する（§6.8） |
| 認可の位置 | Challenge の検証の後（停止中・未登録でもベクターを取っていた） | ベクターを取る前。Accept の前に状態だけもう一度（§8） |
| テナント | NAS-Identifier の前方一致 | エッジの刻印（§8.4） |
| ルールに一致しないとき | 認可ポリシーの `default`（allow / deny） | 拒否する |
| IMSI の取り方 | RADIUS の User-Name | EAP の Identity（EAP-Response/Identity と AT_IDENTITY。§6.3） |
| 再同期の上限 | 32 回 | 1 回（§6.9） |
| 再送 | 新しい要求として処理する | 重複応答のキャッシュで前の応答を送り直す（§4.4） |
| 要求の期限・同時処理の上限 | なし | 既定 4 秒・256（§4.4） |
| healthcheck | `pgrep` | ループバックの HTTP の口（§3.6） |
| ログの IMSI | 既定でマスクする | 既定でマスクしない（設計概要 §3.1 の 7） |

---

## ■セクション2: パッケージ構成

### 2.1 パッケージ

コマンドは 1 つ（`cmd/aka-radius`）で、サブコマンド `auth` が `internal/auth.Main` を呼ぶ。

| パッケージ | 責務 |
|-----------|------|
| `internal/auth` | 入口（`Main`）。設定の読み込み、登録簿と Valkey の準備、healthcheck、`SIGHUP`、遮断器の状態の報告、部品の組み立て、停止 |
| `internal/auth/config` | 環境変数（§3.1）と定数（EAP の会話の TTL、再同期の上限、停止の待ち時間、Valkey のタイムアウト） |
| `internal/auth/server` | RADIUS のハンドラー。トレースID の決定、Message-Authenticator の検証、属性の取り出し、エンジンの結果から応答を作る、Status-Server（§5） |
| `internal/auth/radius` | RADIUS の属性の読み書きと、Access-Accept / Challenge / Reject の組み立て |
| `internal/auth/eap` | EAP のパケット、Identity の解析、状態機械の遷移表、誤りの定義（§6） |
| `internal/auth/eap/aka`、`internal/auth/eap/akaprime` | EAP-AKA / EAP-AKA' の鍵の導出、Challenge の組み立てと検証（go-eapaka の薄い包み） |
| `internal/auth/engine` | 認証エンジン。Identity の処理、ベクターを取る前の判定、ベクターの取得、Challenge の検証、再同期、セッションの作成（§6・§8・§9） |
| `internal/auth/session` | EAP の会話の状態（`eap:{traceId}`）の読み書きと、セッションの UUID の採番 |
| `internal/auth/store` | Valkey のクライアントと、構造体と Hash の変換 |
| `internal/auth/source` | 取得先の登録簿、取得先のクライアント（mTLS・遮断器）、遮断器の状態の報告（§7） |
| `internal/auth/source/sourcetest` | 試験用の偽の取得先 |
| `internal/auth/mocks` | ハンドラーの試験のためのモック（mockgen） |

auth が使う共有のパッケージ:

| パッケージ | auth での使い方 |
|-----------|----------------|
| `internal/radiussrv` | UDP の受け口、共有シークレットの解決、`Guard`（重複応答のキャッシュ・要求の期限・同時処理の上限）、Message-Authenticator、Proxy-State、Status-Server、刻印の読み取り（§4・§5・§8.4） |
| `internal/subscriber` | 加入者（`sub:{imsi}`）の読み出し、ルールの評価、取得先ごとの加入者の数（§8） |
| `internal/sessions` | セッションと、IMSI・テナントの索引の作成（§9.3） |
| `internal/usage` | 利用の集計の `auth_ok`（§9.4） |
| `internal/clients` | RADIUS クライアントのキーの名前（`client:`） |
| `internal/health` | ループバックの healthcheck の口（§3.6） |
| `internal/envconf` | 共通の環境変数（Valkey、ログ、healthcheck、刻印） |
| `internal/logging` | IMSI のマスク、ログのレベル、RADIUS ライブラリのログの受け口 |
| `internal/valkey` | Valkey のクライアントの作成 |

### 2.2 依存の向き

```
auth（Main）
  ├─ server ──> eap（Request / Result）、radius、radiussrv
  ├─ engine ──> eap、eap/aka、eap/akaprime、session、source、subscriber、sessions、radiussrv（刻印の読み取り）
  ├─ source（登録簿・クライアント・状態の報告）
  ├─ session ──> store ──> valkey
  └─ radiussrv（受け口・Guard・共有シークレット）、health
```

- ハンドラー（`server`）はエンジンをインターフェース `eap.EAPProcessor` で受ける。エンジンは取得先を `source.Fetcher`、加入者・刻印の信頼の印・セッション・利用の集計をそれぞれ小さなインターフェースで受ける（試験でメモリーの偽物に差し替えるため）。
- 共有のパッケージどうしは依存しない（E-02）。

### 2.3 外部パッケージ

| パッケージ | 用途 |
|-----------|------|
| `layeh.com/radius` | RADIUS の受信・解析・応答（`PacketServer`）、RFC 2865 / 2868 / 2869 と Microsoft の属性 |
| `github.com/oyaguma3/go-eapaka` | EAP-AKA / EAP-AKA' のパケット、属性、AT_MAC、鍵の導出 |
| `github.com/redis/go-redis/v9` | Valkey |
| `github.com/sony/gobreaker` | 取得先ごとの遮断器 |
| `github.com/google/uuid` | トレースID、セッションの UUID |

版は `go.mod` を正とする。取得先への HTTP は標準の `net/http`、ログは `log/slog`（JSON）。

### 2.4 イメージ

- イメージは 1 つ（`aka-radius`）で、auth・acct・admin はサブコマンドを変えて別々のコンテナで動かす（`Dockerfile`）。
- ランタイムは `gcr.io/distroless/static-debian13:nonroot`（UID 65532）。シェルも `pgrep` もないので、healthcheck はコマンド `aka-radius healthcheck` で行う（§3.6）。
- マウントする取得先の登録簿と鍵は UID 65532 で読めるようにする（運用ガイドの 2 章）。

---

## ■セクション3: 設定・初期化

### 3.1 環境変数

| 変数 | 既定 | 内容 |
|------|------|------|
| `RADIUS_VALKEY_HOST`・`RADIUS_VALKEY_PORT` | `valkey`・`6379` | Valkey の宛先 |
| `RADIUS_VALKEY_PASSWORD` | （必須） | Valkey のパスワード |
| `RADIUS_LOG_LEVEL` | `INFO` | ログのレベル（`DEBUG` / `INFO` / `WARN` / `ERROR`） |
| `RADIUS_LOG_MASK_IMSI` | `false` | ログの IMSI をマスクするか（§3.7） |
| `RADIUS_HEALTH_ADDR` | `127.0.0.1:9090` | healthcheck の口（§3.6） |
| `RADIUS_AUTH_LISTEN_ADDR` | `:1812` | RADIUS 認証の待ち受け |
| `RADIUS_FALLBACK_SECRET` | 空 | 予備の共有シークレット。空なら登録していない送信元のパケットを捨てる（§4.2） |
| `RADIUS_SOURCES_FILE` | `/etc/aka-radius/sources/sources.json` | 取得先の登録簿（§7.2） |
| `RADIUS_EAP_AKA_PRIME_NETWORK_NAME` | `WLAN` | EAP-AKA' のネットワーク名（`anId` と AT_KDF_INPUT）。空は誤り |
| `RADIUS_STAMP_VENDOR_ID` | `32473` | 刻印の Vendor-Id（1〜4294967295） |
| `RADIUS_STAMP_VENDOR_TYPE` | `1` | 刻印の Vendor-Type（1〜255） |
| `RADIUS_REQUEST_TIMEOUT` | `4s` | 要求 1 つの処理の期限（500ms〜30s。§4.4） |
| `RADIUS_MAX_INFLIGHT` | `256` | 同時に処理する要求の上限（§4.4） |

- compose が `.env` からこれらを組み立てて渡す（D-08）。値の形が違えば、ログの `error` に理由を出して終了する。

### 3.2 起動の順

`internal/auth/run.go` の `Main`:

1. 環境変数を読む（§3.1）。誤りなら終了。
2. JSON のロガーを作る（項目 `app` は `auth`）。起動のログに、ビルドの版・待ち受け・登録簿のパス・ネットワーク名・予備の共有シークレットの有無・刻印の Vendor-Id と Vendor-Type・要求の期限・同時処理の上限を出す（刻印の値はエッジの設定と突き合わせるため）。予備の共有シークレットがあれば WARN を出す。
3. 取得先の登録簿を読む（§7.2）。読めない・形が違う・証明書と鍵が読めないなら `SOURCES_LOAD_ERR` で終了する。読めたら `SOURCES_LOADED`（取得先ごとの ID・名前・URL・タイムアウト・クライアント証明書の期限。鍵のパスは出さない）。取得先が 1 つもなければ WARN（すべての認証を拒否する）。
4. Valkey につなぐ（PING で確かめる）。つながらなければ `VALKEY_CONN_ERR` で終了。
5. healthcheck の口を開く（§3.6）。開けなければ `SYS_LISTEN_ERR` で終了。
6. 部品を組み立てる（取得先の `Service`、エンジン、共有シークレットの解決、`Guard`、ハンドラー、サーバー）。
7. `SIGHUP` の受け手を動かす（§3.4）。
8. 遮断器の状態の報告を動かす（§7.7）。
9. RADIUS の待ち受けを始める。始められない（ポートが使われているなど）なら `SYS_LISTEN_ERR` で終了する。
10. `SIGTERM`・`SIGINT` で停止する（§3.5）。

- 終了したら、compose の `restart: always` が起動し直す。起動の失敗の直し方は運用ガイドの 2 章。

### 3.3 定数

| 定数 | 値 | 内容 |
|------|----|------|
| EAP の会話の TTL | 60 秒 | `eap:{traceId}` の期限。書くたびに延ばす（§6.6） |
| セッションの TTL | 24 時間 | `sess:` の期限（`internal/sessions`。acct が Start・Interim で延ばす） |
| 再同期の上限 | 1 回 | 1 回の EAP の会話で（§6.9） |
| 停止の待ち時間 | 5 秒 | 処理中のパケットを待つ上限（§3.5） |
| Valkey | 接続 3 秒、コマンド 2 秒、プール 10、最小のアイドル 2 | `internal/auth/config`・`internal/auth/store` |

### 3.4 `SIGHUP` の読み直し

- `SIGHUP` で取得先の登録簿を読み直す（`docker compose kill -s HUP auth`。admin も同じ登録簿を読むので、ふつうは `auth admin` の両方に送る）。
- 読み直しの規則は §7.2。成功すれば `SOURCES_RELOADED`、失敗すれば `SOURCES_RELOAD_ERR` で、前の登録簿のまま続ける。
- auth が `SIGHUP` で読み直すのは登録簿だけである（RADIUS クライアントと加入者は Valkey をパケットごとに引くので、読み直しは要らない）。

### 3.5 停止

- `SIGTERM`・`SIGINT` を受けたら、新しいパケットを受けるのをやめ、処理中のパケットが応答を送り終えるのを待ってから待ち受けを閉じる（最大 5 秒。§4.6）。
- 停止の間に届いたパケットは捨てる（エッジが送り直す。重複応答のキャッシュはプロセスとともに消える）。

### 3.6 healthcheck

- `internal/health` が `RADIUS_HEALTH_ADDR`（既定 `127.0.0.1:9090`）で HTTP の `GET /healthz` を待ち受け、Valkey の PING（1 秒まで）の結果を返す（200 `{"status":"ok",…}` か 503）。失敗は `HEALTH_VALKEY_ERR`。
- compose の healthcheck はコマンド `aka-radius healthcheck` で、同じコンテナのループバックの口を叩く（設計概要の決定事項 13）。
- 口はコンテナのループバックだけで、外には出さない。

### 3.7 IMSI のマスク

- `RADIUS_LOG_MASK_IMSI` が `true` のとき、ログの IMSI を先頭 6 桁と末尾 1 桁を残してマスクする（`internal/logging.MaskIMSI`）。Identity の形（種別の 1 文字 + IMSI + realm）は `MaskUserName` で IMSI の部分だけをマスクする。
- 既定は `false`（調査のトレーサビリティを優先する。設計概要 §3.1 の 7、運用ガイドの 9 章）。
- 認証ベクター（RAND・AUTN・XRES・CK・IK）、K_aut、MSK、共有シークレットは、設定によらずログに出さない。

---

## ■セクション4: UDP の受け口（`internal/radiussrv`）

### 4.1 構成

auth と acct は同じ受け口を使う。1 つのパケットは次の順に通る。

```
UDP 1812
  └─ layeh.com/radius の PacketServer（共有シークレットの解決: DynamicSecretSource）
       └─ dispatch（goroutine に渡してすぐ戻る。停止の待ちのために数える）
            └─ recoverHandler（パニックの回復）
                 └─ Guard（重複応答のキャッシュ、同時処理の上限、要求の期限）
                      └─ server.Handler（§5）
                           └─ engine（§6〜§9）
```

- `PacketServer` のパケットの認証は使わない（`InsecureSkipVerify`）。Message-Authenticator はハンドラーで確かめ、失敗したら送信元の IP つきのログを出して捨てる（§5.3）。ライブラリに任せると、素のテキストのログだけで捨てられるため。
- ライブラリが出すエラー（パケットの解析の失敗など）は JSON のログ `RADIUS_LIB_ERR` に流す。共有シークレットが決まらずに捨てたときは、アプリが `RADIUS_NO_SECRET` を出すので DEBUG にする。

### 4.2 共有シークレットの解決

送信元の IP で `client:{ip}` の `secret` を引く（RADIUS クライアントはコマンド `aka-radius client` で登録する。設計概要 §12）。

| 状況 | 予備の共有シークレットがない | ある |
|------|---------------------------|------|
| 登録あり | 登録の値 | 登録の値 |
| 登録なし | 捨てる（`RADIUS_NO_SECRET`） | 予備の値 |
| Valkey に届かない | 捨てる（`RADIUS_SECRET_ERR`） | 予備の値（`RADIUS_SECRET_ERR`） |
| 送信元の IP が取れない | 捨てる（`RADIUS_IP_EXTRACT_ERR`） | 予備の値 |

- 予備の共有シークレット（`RADIUS_FALLBACK_SECRET`）は既定で空にする（運用ガイドの 8 章）。予備の値で受けた送信元には `client:` がないので、刻印の信頼の印もない（§8.4）。
- パケットのたびに Valkey を引くので、RADIUS クライアントの変更はすぐに効く。

### 4.3 goroutine への受け渡し（dispatch）

- `PacketServer` は、同じ送信元と Identifier の要求を、ハンドラーが戻るまで黙って捨てる。ハンドラーは応答を送った後もログや後片付けで少し戻らないので、その間に届いた再送が `Guard` に届かず、前の応答を送り直せなかった（段階 2-6 の確認事項 M11。設計概要 §17.1 の K16）。
- そこで一番外のハンドラー（`dispatch`）は、処理を goroutine に渡してすぐにライブラリへ戻る。重複の判定はライブラリに任せず、`Guard` だけで行う。
- 渡した処理は `WaitGroup` で数え、停止のときに待つ（§4.6）。同時に処理する数の上限は `Guard` が持つ。

### 4.4 Guard

`radiussrv.Guard` は、auth と acct のハンドラーの前で次の 3 つを受け持つ（設計概要 §9、段階 2-3 の G7・G8）。

**重複応答のキャッシュ**

| 項目 | 内容 |
|------|------|
| キー | 送信元の IP とポート、Identifier、Request Authenticator（RFC 5080 §2.2.2） |
| 値 | 送った応答のパケット |
| 持つ時間 | 30 秒（エッジの再送の窓 5 秒 × 2 回より長く） |
| 件数の上限 | 50,000（見込みの約 5,000 件の 10 倍）。超えたら古いものから消す |
| キャッシュに当たった | 前の応答をそのまま送り直す（`AUTH_DUPLICATE_REPLAYED`） |
| 処理中の重複 | 捨てる（`AUTH_DUPLICATE_DROPPED`、DEBUG）。最初の要求の応答がキャッシュに入れば、次の再送に返る |
| 応えなかった要求 | 捨てた・パニックした要求は印を残さない（次の再送を新しい要求として処理する） |

- 応答は**キャッシュに入れてから送る**（送ってから入れると、その間に届いた再送を処理中として捨ててしまう。K16 の直し）。
- プロセスを再起動するとキャッシュは消える。その間の再送は新しい要求として処理される（害は、取得先の SQN が 1 つ進むことと、Accept の後の再送が EAP の会話の状態がなくて拒否になること）。
- これで、本PoC で起きていた「Accept の後に最後の要求の再送が届いて Reject になる」「Identity の再送でベクターをもう 1 つ取って SQN を進める」がなくなる。

**同時処理の上限**

- `RADIUS_MAX_INFLIGHT`（既定 256）を超えた要求は、応えずに捨てる（まだ何もしていないので、エッジの再送を待っても害がない）。ログ `RADIUS_OVERLOAD` は 1 秒に 1 回に間引き、その間に捨てた数を `dropped` に出す。

**要求の期限**

- 処理には `RADIUS_REQUEST_TIMEOUT`（既定 4 秒）の期限つきの context を渡す。取得先のタイムアウト（既定 3 秒）より長く、エッジの再送の間隔（5 秒）より短くする（設計概要 §4）。期限を過ぎた Valkey や取得先の呼び出しは誤りで返り、エンジンは拒否で終える。
- 本PoC は期限も上限もなく、取得先が遅いと goroutine が積み上がった。

### 4.5 パニックの回復

- パケットの処理中のパニックは `recoverHandler` が回復し、`SYS_PANIC`（送信元、Code、スタック）を出す。そのパケットには応答しない（NAS が送り直す）。回復しないと 1 つのパケットでプロセスごと落ちる。

### 4.6 停止の順

1. 停止の印を立て、それ以後に届いたパケットを捨てる。
2. 渡した処理が応答を送り終えるのを待つ（停止の待ち時間まで）。
3. `PacketServer` を閉じる（先に閉じると、処理中のパケットが応答を送れないため）。

### 4.7 ログ

| `event_id` | レベル | 内容 |
|-----------|--------|------|
| `RADIUS_NO_SECRET` | WARN | 登録していない送信元のパケットを捨てた（`src_ip`） |
| `RADIUS_SECRET_ERR` | WARN | 共有シークレットを Valkey から引けない |
| `RADIUS_IP_EXTRACT_ERR` | WARN | 送信元の IP が取れない |
| `RADIUS_LIB_ERR` | WARN / DEBUG | RADIUS ライブラリのエラー |
| `AUTH_DUPLICATE_REPLAYED` | INFO | 重複した要求に前の応答を送り直した |
| `AUTH_DUPLICATE_DROPPED` | DEBUG | 処理中の要求の重複を捨てた |
| `RADIUS_OVERLOAD` | WARN | 同時処理の上限を超えて捨てた（`server`、`max_in_flight`、`dropped`） |
| `SYS_PANIC` | ERROR | パニックを回復した |

---

## ■セクション5: RADIUS 処理層（`internal/auth/server`・`internal/auth/radius`）

### 5.1 流れ

1. トレースID を決める（§5.2）。受信のログ `PKT_RECV`（`src_ip`、`code`。Access-Request は `nas_identifier` も。Status-Server は DEBUG）。
2. Code で分ける。Access-Request は 3 へ、Status-Server は §5.7、ほかは `PKT_UNKNOWN_CODE` を出して応えない。
3. Message-Authenticator を確かめる（§5.3）。
4. EAP-Message を取り出す。なければ `PKT_NO_EAP` を出して応えない。
5. 属性を取り出してエンジンに渡す（§5.4）。context は `Guard` の期限つき。
6. エンジンの結果（Accept / Challenge / Reject / Drop）から応答を作って送る（§5.6）。Drop は `PKT_DROP` を出して応えない。送れなければ `PKT_SEND_ERR`。

- エンジンは中の誤りをログに出して Reject に変えるので、ハンドラーは常に結果を受け取る。
- auth は EAP-Request/Identity を送らない。最初の Access-Request には EAP-Response/Identity が入っている前提である（EAP-Start は NAS が受け持つ）。

### 5.2 トレースID

- 2 回目以降の Access-Request（Access-Challenge への応答）は、State の値（最初に採番したトレースID）が UUID の形なら、その文字列をそのままトレースID にする。1 回の認証のログを同じ `trace_id` でたどれるようにするため。
- State がない、UUID の形でない場合は、新しい UUID を採番する。
- 同じ値を、Access-Challenge の State、`eap:{traceId}` のキー、取得先への `X-Trace-ID`（§7.8）に使う。

### 5.3 Message-Authenticator

- Access-Request には Message-Authenticator が必須（RFC 3579）。ない・合わないときは `PKT_MA_INVALID`（`src_ip`）を出して応えない。
- 検証は、属性の値を 16 バイトのゼロにしてパケット全体の HMAC-MD5（鍵は共有シークレット）を計算し、定数時間で比べる。
- 応答（Accept / Challenge / Reject、Status-Server の応答）にも、Request Authenticator を使って計算した Message-Authenticator を付ける。

### 5.4 取り出す属性

| 属性 | 使い方 |
|------|--------|
| EAP-Message | すべてを受け取った順につないで 1 つの EAP パケットにする（RFC 3579） |
| State | 2 回目以降の要求でトレースID（EAP の会話のキー） |
| NAS-Identifier | ルールの評価（§8.5）、ログ、セッション |
| Called-Station-Id | SSID を取り出してルールの評価に使う（§8.5） |
| User-Name | 判定には使わない。EAP の Identity と違えばログに出す（`EAP_IDENTITY_MISMATCH`。§6.3） |
| Vendor-Specific（刻印） | `RADIUS_STAMP_VENDOR_ID`・`RADIUS_STAMP_VENDOR_TYPE` の値をすべて取り出す（信頼の印の確かめはエンジン。§8.4） |
| Proxy-State | 順を保って応答に写す（§5.5） |
| 送信元の IP | 刻印の信頼の印、セッションの `nas_ip`（エッジのトンネルの IP） |

### 5.5 Proxy-State

- 要求のすべての Proxy-State を順に覚え、Accept / Challenge / Reject と Status-Server の応答に同じ順で付ける（RFC 2865。エッジの radsecproxy が使う）。

### 5.6 応答

| 応答 | 付ける属性 |
|------|-----------|
| Access-Challenge | EAP-Message（AKA-Identity か AKA-Challenge。253 バイトごとに分ける）、State（トレースID）、Proxy-State、Message-Authenticator |
| Access-Accept | EAP-Message（EAP-Success）、MS-MPPE-Recv-Key・MS-MPPE-Send-Key、Class、Tunnel-Type・Tunnel-Medium-Type・Tunnel-Private-Group-Id（ルールに VLAN があるとき）、Session-Timeout（ルールにあるとき）、Proxy-State、Message-Authenticator（§8.7） |
| Access-Reject | EAP-Message（EAP-Failure）、Proxy-State、Message-Authenticator |

- どの拒否でも EAP-Failure の Access-Reject を返す（応えずに再送を待つことはしない。設計概要 §7.2）。応えないのは、パケットそのものを受け付けないとき（共有シークレット不明、Message-Authenticator の不一致、EAP-Message なし、最初の要求が Identity でない）と、`Guard` が捨てたときだけである。
- EAP の Identifier は、端末の応答の Identifier に 1 を足した値にする。

### 5.7 Status-Server

- Status-Server（RFC 5997）には、Message-Authenticator を確かめて Access-Accept を返す（EAP-Message なし、Proxy-State を写す）。合わなければ `RADIUS_STATUS_AUTH_FAIL`（WARN）を出して応えない。
- エッジから定期的に届くので、受信と応答のログ（`PKT_RECV`・`RADIUS_STATUS_OK`）は DEBUG にする。
- Status-Server も `Guard` を通る（重複応答のキャッシュの対象）。

### 5.8 ログ

| `event_id` | レベル | 内容 |
|-----------|--------|------|
| `PKT_RECV` | INFO / DEBUG | パケットを受けた |
| `PKT_UNKNOWN_CODE` | WARN | 扱わない Code |
| `PKT_MA_INVALID` | WARN | Message-Authenticator がない・合わない |
| `PKT_NO_EAP` | WARN | EAP-Message がない |
| `PKT_DROP` | INFO | エンジンが Drop を返した |
| `PKT_SEND_ERR` | ERROR | 応答を送れない |
| `RADIUS_STATUS_OK`・`RADIUS_STATUS_AUTH_FAIL` | DEBUG・WARN | Status-Server |

---

## ■セクション6: EAP 処理層（`internal/auth/eap`・`internal/auth/engine`）

### 6.1 概要

- エンジン（`engine.EngineImpl.Process`）が 1 つの Access-Request の EAP を処理し、結果（Accept / Reject / Challenge / Drop と、応答の EAP、State、Accept のときの MSK・VLAN・Session-Timeout・セッションの UUID）を返す。
- State がなければ新しい会話（§6.3）、あれば `eap:{traceId}` を読んで続きを処理する（§6.5）。`eap:` がない（期限切れ、知らない State）なら `EAP_CTX_NOT_FOUND` で拒否する。
- EAP-AKA / EAP-AKA' のパケットは go-eapaka で解析・組み立てる。EAP-Response/Identity（Type 1）は go-eapaka で解析できないので、バイト列から読む。

### 6.2 EAP のパケット

| 項目 | 内容 |
|------|------|
| Code | 1 Request、2 Response、3 Success、4 Failure |
| Type | 1 Identity、23 EAP-AKA、50 EAP-AKA' |
| Subtype（AKA） | 1 Challenge、2 Authentication-Reject、4 Synchronization-Failure、5 Identity、14 Client-Error |

- EAP-Success / EAP-Failure は 4 バイト（Code・Identifier・Length）で作る。
- 属性の取り出しは型で探す（`eap.GetAttribute`）。値の長さなどの形の検査は go-eapaka の解析に任せる。

### 6.3 Identity の取り方と形

IMSI と方式は、RADIUS の User-Name ではなく EAP の Identity から取る（設計概要の決定事項 8）。

| 最初の要求の EAP | Identity |
|-----------------|----------|
| EAP-Response/Identity（Type 1） | Type-Data（RFC 3748 §5.1） |
| EAP-Response/AKA-Identity（Type 23・50、Subtype 5） | AT_IDENTITY（RFC 4187 §9.2）。なければ拒否 |
| Subtype が Identity でない AKA のパケット | `EAP_NOT_IDENTITY` を出して応えない（Drop） |
| 解析できない | 拒否（`EAP_PARSE_ERR`） |

Identity は `<種別の 1 文字><IMSI など>@<realm>` の形で、先頭の文字で種別を決める。

| 先頭 | 種別 | 方式 | 扱い |
|------|------|------|------|
| `0` | 永続 ID | EAP-AKA | 判定（§8）へ進む |
| `6` | 永続 ID | EAP-AKA' | 同上 |
| `2`・`4` | 仮名・再認証 ID | EAP-AKA | フル認証へ誘導（§6.4） |
| `7`・`8` | 仮名・再認証 ID | EAP-AKA' | 同上 |
| `1`・`3`・`5` | EAP-SIM | — | 拒否（`EAP_UNSUPPORTED_TYPE`） |
| ほか、空、realm がない、`@` の前が空 | 不正 | — | 拒否（`EAP_IDENTITY_INVALID`） |

- 永続 ID の IMSI は、先頭の 1 文字を除いた `@` の前の部分である。形（15 桁の数字）はここでは確かめず、`sub:{imsi}` がなければ未登録として拒否する（加入者は管理API で 15 桁の数字を確かめて登録する）。
- User-Name と EAP の Identity が違えば `EAP_IDENTITY_MISMATCH`（INFO）を出し、EAP の側を使う。外側の名前を変えない端末でも通るようにするため（本PoC は User-Name を使い、AT_IDENTITY を見なかった）。
- 鍵の導出に使う Identity（RFC 4187 §7）は、最後に受けた EAP の Identity である。`eap:` の `identity` に残し、再同期でも使う。

### 6.4 フル認証への誘導

1. 仮名・再認証 ID を受けたら、`eap:` を作り（`permanent_id_requested`、方式）、EAP-Request/AKA-Identity（AT_PERMANENT_ID_REQ）を Access-Challenge で送る。状態は `WAITING_IDENTITY`。
2. 端末の EAP-Response/AKA-Identity の AT_IDENTITY を読む。永続 ID なら判定（§8）へ進む。AT_IDENTITY がない・仮名のまま・不正なら拒否し、`eap:` を消す。
3. 誘導した後の方式は、2 の永続 ID の先頭の文字で決まる。

### 6.5 状態機械

`eap:{traceId}` の `stage` に状態を持つ（本PoC の状態機械を引き継いだ。もとは D-03 にあった）。

**状態**

| 状態 | 意味 | `stage` に書くか |
|------|------|-----------------|
| `NEW` | 仮名・再認証 ID を受けて `eap:` を作った直後 | 書く（すぐに `WAITING_IDENTITY` に変える） |
| `WAITING_IDENTITY` | AT_PERMANENT_ID_REQ を送り、永続 ID を待っている | 書く |
| `IDENTITY_RECEIVED` | 永続 ID を受け、判定を通った。ベクターを取る前 | 書く |
| `WAITING_VECTOR` | 取得先に要求している | 書かない（1 つの要求の処理の中で終わる） |
| `CHALLENGE_SENT` | AKA-Challenge を送り、端末の応答を待っている | 書く |
| `RESYNC_SENT` | 再同期のベクターを要求している | 書かない（同上） |
| `SUCCESS`・`FAILURE` | 終わり | 書かない（`eap:` を消す） |

- 書かない状態は、遷移表（`eap.ValidateTransition`）の検査のためだけにある。
- どの状態でも、`eap:` の TTL（60 秒）を過ぎれば会話は消え、次の要求は `EAP_CTX_NOT_FOUND` で拒否になる。

**遷移**

```
（State なし）
  ├─ 永続 ID（0・6）─> 判定（§8）─┬─ 通る ─> IDENTITY_RECEIVED ─> ベクター取得 ─┬─ 成功 ─> CHALLENGE_SENT
  │                               │                                            └─ 失敗 ─> FAILURE
  │                               └─ 当たる ─> FAILURE
  ├─ 仮名・再認証 ID（2・4・7・8）─> NEW ─> WAITING_IDENTITY
  └─ EAP-SIM・不正 ─> FAILURE

WAITING_IDENTITY
  ├─ AKA-Identity の AT_IDENTITY が永続 ID ─> 判定（§8）へ（上と同じ）
  └─ AT_IDENTITY なし・仮名・不正 ─> FAILURE

CHALLENGE_SENT
  ├─ AKA-Challenge ─┬─ AT_MAC・AT_RES が正しく、状態がまだ active ─> SUCCESS（Access-Accept）
  │                 └─ それ以外 ─> FAILURE
  ├─ Synchronization-Failure ─┬─ 再同期がまだ 0 回 ─ 取り直し ─┬─ 成功 ─> CHALLENGE_SENT（新しい Challenge）
  │                           │                              └─ 失敗 ─> FAILURE
  │                           └─ 1 回済み ─> FAILURE（AUTH_RESYNC_LIMIT）
  ├─ Authentication-Reject ─> FAILURE
  └─ Client-Error ─> FAILURE
```

**State ありの要求の振り分け**

| 受けた Subtype | 期待する状態 | 処理 | 状態が違うとき |
|---------------|-------------|------|---------------|
| Challenge | `CHALLENGE_SENT` | §6.7・§6.8 の検証 | 拒否（`EAP_STATE_ERR`） |
| Synchronization-Failure | `CHALLENGE_SENT` | §6.9 の再同期 | 拒否（`EAP_STATE_ERR`） |
| Identity | `WAITING_IDENTITY` | §6.4 の 2 | 拒否（`EAP_UNEXPECTED_IDENTITY`） |
| Authentication-Reject | どれでも | 拒否し `eap:` を消す（`EAP_AUTH_REJECT`） | — |
| Client-Error | どれでも | 拒否し `eap:` を消す（`EAP_CLIENT_ERROR`） | — |
| ほか | — | 拒否（`EAP_UNKNOWN_SUBTYPE`） | — |
| 解析できない | — | 拒否（`EAP_PARSE_ERR`） | — |

- 遷移表の内部の検査に失敗したときも `EAP_STATE_ERR`（ERROR）で拒否する。

### 6.6 EAP の会話の状態（`eap:{traceId}`）

| 項目 | 内容 | 書く時 |
|------|------|--------|
| `stage` | 状態（§6.5） | 毎回 |
| `eap_type` | 23（EAP-AKA）か 50（EAP-AKA'） | 作るとき |
| `permanent_id_requested` | フル認証へ誘導したか | 誘導のとき |
| `imsi`、`identity` | IMSI と、鍵の導出に使う Identity | 判定を通ったとき |
| `source_id` | 取得先の ID（再同期で同じ取得先に要求する） | 判定を通ったとき、ベクターを取ったとき |
| `tenant_id`、`rule_index` | 刻印のテナントと、採ったルールの番号 | 判定を通ったとき |
| `vlan_id`、`session_timeout` | 採ったルールの値（0 なら Accept に付けない） | 判定を通ったとき |
| `rand`、`autn`、`xres`、`k_aut`、`msk` | ベクターと導出した鍵（16 進） | ベクターを取ったとき |
| `resync_count` | 再同期の回数 | 再同期のとき |

- 作るときと書き足すたびに TTL を 60 秒に延ばす。Accept・拒否で消す（一部の拒否では消さずに TTL に任せる）。
- K_aut・MSK・XRES を Valkey に置くので、Valkey はホストの外に出さない（D-08）。

### 6.7 EAP-AKA

| 手順 | 内容 |
|------|------|
| 鍵の導出 | MK = SHA1(Identity ‖ IK ‖ CK) から FIPS 186-2 の PRF で K_encr・K_aut（16 バイト）・MSK（64 バイト）・EMSK（RFC 4187 §7。go-eapaka） |
| Challenge | AT_RAND、AT_AUTN、AT_MAC（K_aut で HMAC-SHA1-128） |
| 応答の検証 | AT_MAC → AT_RES の長さ → AT_RES の値（定数時間で比べる） |

### 6.8 EAP-AKA'

| 手順 | 内容 |
|------|------|
| CK'・IK' | **取得先が導出して返す**（`EAP_AKA_PRIME` で要求し、`anId` にネットワーク名を入れる。契約 §6.4）。auth は導出しない |
| 鍵の導出 | MK = PRF'(IK' ‖ CK', "EAP-AKA'" ‖ Identity) から K_encr・K_aut（32 バイト）・K_re・MSK・EMSK（RFC 5448 §3.3。go-eapaka） |
| Challenge | AT_RAND、AT_AUTN、AT_KDF_INPUT（ネットワーク名。`anId` と同じ値）、AT_KDF（1）、AT_MAC（K_aut で HMAC-SHA-256-128） |
| 応答の検証 | AT_KDF（ないか、1 つで値が 1 なら受ける。ほかは拒否）→ AT_MAC → AT_RES の長さ → AT_RES の値 |

- ネットワーク名は全体で 1 つ（`RADIUS_EAP_AKA_PRIME_NETWORK_NAME`、既定 `WLAN`。設計概要の決定事項 3）。取得先が許していない `anId` なら、取得先は 400 か 403 を返す（§7.6）。
- 本PoC は auth-server が CK・IK・AUTN とネットワーク名から CK'・IK' を導出していた（契約 R2 で改めた）。

### 6.9 再同期

1. `CHALLENGE_SENT` で Synchronization-Failure を受けたら、`resync_count` を見る。1 以上なら `AUTH_RESYNC_LIMIT` で拒否する（1 回の EAP の会話で 1 回まで。設計概要の決定事項 9）。
2. AT_AUTS を取り出す（なければ `EAP_PARSE_ERR` で拒否）。
3. 直前の Challenge の RAND（`eap:` の `rand`）と AUTS で、同じ取得先（`eap:` の `source_id`）からベクターを取り直す（契約 §8）。
4. 新しいベクターで鍵を導出し（Identity は `eap:` の `identity`）、`eap:` を更新して（`resync_count` を 1 足す）、新しい Challenge を送る（`EAP_RESYNC_CHALLENGE`）。

- 上限を 1 回にしたのは、SQN_MS に合わせた後のベクターは受け入れられるはずで、2 回目の同期の失敗は設定の誤り（取得先の食い違いなど）を示すため。何度も取り直すと取得先の SQN を無駄に進める（本PoC は 32 回）。
- 再同期でも、取得先の拒否・誤りは §7.6 のとおり拒否する。

### 6.10 ログ

| `event_id` | レベル | 内容 |
|-----------|--------|------|
| `EAP_IDENTITY_RECEIVED` | INFO | EAP-Response/Identity を受けた |
| `EAP_IDENTITY_MISMATCH` | INFO | EAP の Identity と User-Name が違う |
| `EAP_IDENTITY_INVALID`・`EAP_UNSUPPORTED_TYPE` | WARN | Identity が不正・EAP-SIM |
| `EAP_NOT_IDENTITY` | WARN | 最初の要求が Identity でない（Drop） |
| `EAP_CHALLENGE_SENT`・`EAP_RESYNC_CHALLENGE` | INFO | Challenge・再同期の Challenge を送った（`source_id`） |
| `AUTH_SUCCESS` | INFO | Access-Accept（IMSI、`tenant_id`、`rule_index`、`nas_identifier`、`session_id`） |
| `AUTH_MAC_INVALID`・`AUTH_RES_MISMATCH`・`AUTH_VERIFY_FAIL` | WARN | Challenge の応答の検証の失敗 |
| `AUTH_RESYNC_LIMIT` | WARN | 再同期の上限を超えた |
| `EAP_AUTH_REJECT`・`EAP_CLIENT_ERROR` | WARN | 端末が Authentication-Reject・Client-Error を返した |
| `EAP_STATE_ERR`・`EAP_UNEXPECTED_IDENTITY`・`EAP_UNKNOWN_SUBTYPE`・`EAP_PARSE_ERR` | WARN / ERROR | 状態の違う要求、解析の失敗 |
| `EAP_CTX_NOT_FOUND` | WARN | `eap:` がない |
| `EAP_CTX_CREATE_ERR`・`EAP_CTX_UPDATE_ERR`・`EAP_CTX_DECODE_ERR`・`EAP_CTX_INVALID`・`EAP_BUILD_ERR` | ERROR | `eap:` の読み書き・パケットの組み立ての失敗 |

### 6.11 RFC の参照

| RFC | 節 | 内容 |
|-----|----|------|
| RFC 3748 | §5.1 | EAP-Response/Identity |
| RFC 4187 | §4.1.6 | AT_PERMANENT_ID_REQ |
| RFC 4187 | §6.3.1 | 再同期（AT_AUTS） |
| RFC 4187 | §7 | EAP-AKA の鍵の導出 |
| RFC 4187 | §9.2 | AKA-Identity と AT_IDENTITY |
| RFC 5448 | §3.1〜§3.4 | AT_KDF_INPUT、AT_KDF、PRF'、EAP-AKA' の鍵の導出 |

---

## ■セクション7: 認証ベクターの取得（`internal/auth/source`）

### 7.1 概要

- auth は、加入者ごとに決めた取得先から、認証ベクターAPI の契約に従って 1 回の認証で 1 ベクターを取る（設計概要 §7）。鍵（Ki・OPc）と SQN は取得先が持つ。
- 本PoC の vector-gateway（別のコンテナ）は auth に取り込んだ（設計概要の決定事項 2）。IMSI ごとの取得先を引くには Valkey が要るが、vector-gateway は Valkey を持たなかった。取り込んで HTTP の 1 往復と 1 つのプロセスを減らし、遮断器を取得先ごとに分けた（本PoC は遮断器が 1 つで、ある取得先の障害がほかの取得先の認証も止めた）。

```
engine ── Fetch（IMSI、取得先の ID、方式、anId、再同期、トレースID）──> Service
  Service ── 登録簿から取得先のクライアントを引く ──> Client（取得先ごと）
     Client ── 遮断器 ──> mTLS の HTTP/1.1 ──> 取得先（POST …/generate-av）
```

- 取得先の ID は、判定（§8）で読んだ加入者の `source_id` をエンジンが渡す。空のとき `Service` が加入者から引き直す作りもあるが、ふだんは使わない。
- 取得先の ID が登録簿にない（加入者の `source_id` が古いなど）ときは、取得先を呼ばずに拒否する（`VECTOR_SOURCE_UNKNOWN`）。

### 7.2 取得先の登録簿

ファイル（`RADIUS_SOURCES_FILE`。JSON）に置き、`SIGHUP` で読み直す（設計概要の決定事項 3）。例は `deployments/sources/sources.example.json`、置き方は運用ガイドの 3 章。

| 項目 | 必須 | 内容 |
|------|------|------|
| `id` | 必須 | 取得先の ID（英小文字・数字・`-`、1〜32 文字、先頭は英小文字か数字）。加入者の `sourceId` が指す。重ならない |
| `name` | 任意 | 表示名（省くと `id`） |
| `baseUrl` | 必須 | `https://<ホスト>[:<ポート>]/…/nudm-ueau/v1`（契約 §5）。平文の HTTP、利用者情報・クエリ・フラグメントは誤り |
| `clientCertFile`・`clientKeyFile` | 必須 | aka-radius のクライアント証明書と鍵（PEM） |
| `serverCaFile` | 必須 | 取得先のサーバー証明書を確かめる CA（PEM、複数可。aka-vecgen では AV 用の自己署名のサーバー証明書そのもの） |
| `timeout` | 任意 | 要求 1 回のタイムアウト（Go の書き方。100ms〜30s、既定 `3s`） |

- 知らない項目は綴りの誤りとして拒否する。ファイルの中のパスが相対なら、登録簿のファイルのディレクトリから見る（compose は `deployments/sources/` をディレクトリごと読み取り専用でマウントする。D-08）。
- 読み込むときに、証明書と鍵の組とサーバーの CA を読み、取得先ごとのクライアントを作る。1 件でも誤りがあれば、全体を誤りにする。

| 時 | 誤りのとき |
|----|-----------|
| 起動 | 終了する（`SOURCES_LOAD_ERR`） |
| `SIGHUP` | 前の登録簿のまま続ける（`SOURCES_RELOAD_ERR`） |

- 読み直しで消える取得先があれば、取得先ごとの加入者の数（`cnt:src:{id}`）を読み、0 でない取得先を消す登録簿は断る（`SOURCES_RELOAD_ERR`。設計概要 §7.1）。数を読めないときも断る。先に加入者の `sourceId` を変えるか、加入者を消す。
- 読み直しが通ったら、全部の取得先のクライアントを作り直す（遮断器は閉じた状態から。古い接続は閉じる）。読み直しは 1 つずつ行い、処理中の要求は前のクライアントで終わる。
- admin も同じファイルを読み取り専用で読み、取得先の ID の確かめと `/sources` に使う（鍵は読まない。D-13）。

### 7.3 接続

| 項目 | 内容 | 理由 |
|------|------|------|
| 認証 | mTLS。登録簿のクライアント証明書を出す | 契約の決定事項（mTLS だけ） |
| サーバーの確かめ | 登録簿の CA のチェーンとホスト名（SAN）。システムの CA の束は使わない | 契約 §5 |
| TLS | 1.2 以上 | — |
| HTTP | HTTP/1.1（HTTP/2 は使わない）、keep-alive | 契約 §5（HTTP/2 は任意） |
| 何もしない接続 | 取得先ごとに 64 まで、50 秒で閉じる | 取得先は 60 秒は閉じない（契約 §5）ので、それより短くする |
| プロキシ | 使わない | 取得先へは直接つなぐ |
| タイムアウト | 取得先ごとの `timeout`（既定 3 秒）。接続と TLS のハンドシェイクを含む | 契約 §11.1 |
| 再試行 | しない | RADIUS の再送に任せる（契約 §11.1） |

- aka-vecgen とは、同じノードの共有の Docker ネットワーク（`aka-av`）で `https://aka-vecgen:8443/nudm-ueau/v1` につなぐ（`docker-compose.aka-av.yml`。D-08）。

### 7.4 要求

| 項目 | EAP-AKA | EAP-AKA' |
|------|---------|----------|
| パス | `POST {baseUrl}/imsi-{IMSI}/hss-security-information/eap-aka/generate-av` | `…/eap-aka-prime/generate-av` |
| `hssAuthType` | `EAP_AKA` | `EAP_AKA_PRIME` |
| `numOfRequestedVectors` | 1 | 1 |
| `anId` | 送らない | ネットワーク名 |
| `resynchronizationInfo` | 再同期のときだけ（`rand`、`auts`。16 進） | 同左 |

- ヘッダーは `Content-Type: application/json`、`Accept: application/json, application/problem+json`、`X-Trace-ID`（§7.8）。
- 1 回の認証で 1 ベクター、再同期のときにもう 1 回。先取りしない（契約 §7）。

### 7.5 応答の確かめ

200 の本文を次のとおり確かめ、合わなければ契約違反にする（契約 §6.3、R4）。

| 確かめること | 内容 |
|-------------|------|
| 大きさ | 64 KiB まで（ベクター 1 つの応答は 1 KiB に満たない） |
| 要素の数 | `hssAuthenticationVectors` がちょうど 1 つ |
| `avType` | 要求の `hssAuthType` と同じ |
| 値の長さ | `rand`・`autn` 16 バイト、`xres` 4〜16 バイト、`ck`・`ik`（EAP-AKA' は `ckPrime`・`ikPrime`）16 バイト。16 進（大文字も受ける） |

- 知らない項目は無視する。

### 7.6 結果の分類（契約 §9.3）

| 取得先の結果 | 分類（`source.Kind`） | ログ | 遮断器 |
|-------------|----------------------|------|--------|
| 200 で形が合う | 成功 | — | 成功 |
| 403（ProblemDetails つき） | 確定した拒否 | `VECTOR_REJECTED`（WARN） | 数えない |
| 400・501（ProblemDetails つき） | 確定した拒否 | `VECTOR_REJECTED`（ERROR。許していない `anId` などの設定の食い違い） | 数えない |
| 500・503、接続・TLS の失敗、タイムアウト | 一時的な誤り | `VECTOR_TEMPORARY_ERR` | 失敗 |
| 200 で形が合わない、本文（`cause`）のない 400・403・501、契約にないステータス | 契約違反 | `VECTOR_CONTRACT_VIOLATION` | 失敗 |
| 遮断器が開いている（半開きで枠を超えたときも） | — | `VECTOR_CIRCUIT_OPEN` | — |
| 加入者に取得先がない、登録簿にない取得先 | — | `VECTOR_SOURCE_UNKNOWN` | — |
| 加入者の取得先を読めない | — | `VECTOR_SOURCE_LOOKUP_ERR` | — |

- どの誤りでも、その認証は EAP-Failure の Access-Reject で拒否し、`eap:` を消す。応えずに再送を待つことはしない（設計概要 §7.2）。
- ログには取得先の ID（`source_id`）、`http_status`、ProblemDetails の `cause`、分類の理由（`reason`。例 `timeout`、`avType mismatch`）、`latency_ms` を出す。値（鍵、ベクター）は出さない。
- 本PoC の 404（`VECTOR_IMSI_NOT_FOUND`）の扱いはなくした。取得先は未登録・止めてある加入者にも 403 を返す（契約 R5）。

### 7.7 遮断器

取得先ごとに 1 つ持つ（gobreaker。設計概要 §7.2）。

| 設定 | 値 | 内容 |
|------|----|------|
| 開く条件 | 連続 5 回の失敗 | 一時的な誤りと契約違反を失敗に数える。確定した拒否は数えない（取得先の障害ではないため） |
| 閉じている間の数え直し | しない（`Interval` 0） | 連続の失敗の数は成功すればゼロに戻る。数え直すと、タイムアウトのように 1 回に数秒かかる失敗が間隔の中で 5 回に届かず、いつまでも開かない（段階 2-2 で分かった） |
| 半開きにするまで | 30 秒 | — |
| 半開きで通す数 | 3 | 3 回続けて成功すれば閉じる。1 回でも失敗すれば開き直す。枠を超えた要求は `VECTOR_CIRCUIT_OPEN` で拒否する |

- 状態が変わると `CB_OPEN`（WARN）・`CB_HALF_OPEN`・`CB_CLOSE`（`source_id`、`from`、`to`）を出す。
- auth は遮断器の状態を Valkey の `stat:sources`（JSON。取得先ごとの `state`・`since` と報告の時刻 `reportedAt`）に、状態が変わったときと 30 秒ごとに書く。admin が `/status` と `/sources` に出す（遮断器は auth のプロセスの中にあるため。段階 2-5 の K4）。書けなければ `SOURCES_STATUS_WRITE_ERR` を最初の 1 回だけ出す。報告は丸ごと置き換えるので、登録簿から消えた取得先は報告からも消える。

### 7.8 トレースID

- 取得先への要求の `X-Trace-ID` に、認証のトレースID（§5.2。UUID の 36 文字）をそのまま入れる（契約 §11.2）。aka-vecgen はこの値をログの `trace_id` に残すので、auth と取得先のログを同じ値でたどれる（運用ガイドの 9.3）。

### 7.9 接続の確かめ（`sources check`）

- コマンド `aka-radius sources check`（auth のコンテナで `docker compose run --rm --no-deps auth sources check`）は、登録簿を起動と同じ規則で読み、取得先ごとに証明書の期限（30 日より近ければ警告）とクライアント証明書のフィンガープリントを出し、接続を確かめる（設計概要 §12、段階 2-5 の K6）。
- 接続の確かめは、遮断器を通さずに基底の URL へ GET を 1 回送り、HTTP の応答が返るかを見る（応答の状態は問わない）。TLS 1.3 では、取得先がクライアント証明書を断ったことはハンドシェイクの後でしか分からないため、ハンドシェイクだけでは足りない。

### 7.10 契約の改修（R1〜R7）との対応

| # | 契約 §16.2 の改修 | auth の作り |
|---|-----------------|------------|
| R1 | EAP-AKA' は `EAP_AKA_PRIME` と `anId` | §7.4 |
| R2 | CK'・IK' を受け取り、MK 以降を導出。AT_KDF_INPUT は `anId` と同じ値 | §6.8 |
| R3 | ネットワーク名の置き場所、方式の切り替え | 全体で 1 つ（`RADIUS_EAP_AKA_PRIME_NETWORK_NAME`）。NAK による切り替えは扱わない（§1.4） |
| R4 | 応答の要素の数・`avType`・値の長さ | §7.5 |
| R5 | 結果の分類を §9.3 に | §7.6 |
| R6 | 再同期の上限 | 1 回（§6.9） |
| R7 | 登録簿にタイムアウト、mTLS に限る | §7.2・§7.3 |

### 7.11 ログ

| `event_id` | レベル | 内容 |
|-----------|--------|------|
| `SOURCES_LOADED`・`SOURCES_RELOADED` | INFO（空なら WARN） | 登録簿を読んだ・読み直した |
| `SOURCES_LOAD_ERR`・`SOURCES_RELOAD_ERR` | ERROR | 登録簿の誤り |
| `SOURCES_STATUS_WRITE_ERR` | WARN | 遮断器の状態を報告できない |
| `VECTOR_REJECTED`・`VECTOR_TEMPORARY_ERR`・`VECTOR_CONTRACT_VIOLATION`・`VECTOR_CIRCUIT_OPEN`・`VECTOR_SOURCE_UNKNOWN`・`VECTOR_SOURCE_LOOKUP_ERR`・`VECTOR_UNKNOWN_ERR` | WARN / ERROR | ベクターの取得の誤り（§7.6） |
| `CB_OPEN`・`CB_HALF_OPEN`・`CB_CLOSE` | WARN・INFO | 遮断器の状態の変化 |

---

## ■セクション8: 認可

### 8.1 判定の位置

- 加入者の有無・状態・テナントの刻印・認可ポリシーのルールを、**ベクターを取る前**（IMSI が分かった最初の要求）に確かめる（設計概要の決定事項 4）。どれかに当たれば、取得先を呼ばずに EAP-Failure の Access-Reject を返す。
- 拒否する IMSI で取得先を呼ばないので、取得先の SQN を進めない。未登録の IMSI を総当たりされても取得先に負荷がかからない。本PoC は Challenge の検証の後に評価していたので、停止中・未登録でもベクターを取っていた。
- Access-Accept の前に、状態だけもう一度確かめる（§8.6）。

### 8.2 判定の順

| 順 | 確かめること | 当たったとき（`event_id`） |
|----|-------------|--------------------------|
| 1 | `sub:{imsi}` がある | `AUTH_SUBSCRIBER_NOT_FOUND`（WARN） |
| 1' | `sub:` の形が正しい（`state`・`source_id`・`rules` が読める） | `AUTH_SUBSCRIBER_INVALID`（ERROR） |
| 1'' | Valkey から読める | `AUTH_SUBSCRIBER_LOOKUP_ERR`（ERROR） |
| 2 | `state` が `active` | `AUTH_SUBSCRIBER_INACTIVE`・`AUTH_SUBSCRIBER_TERMINATED`（WARN） |
| 3 | 信頼する刻印が 1 つだけあり、値が 10 進の 1 以上の数（§8.4） | `AUTH_STAMP_MISSING`・`AUTH_STAMP_MULTIPLE`・`AUTH_STAMP_INVALID`（WARN）、`AUTH_STAMP_LOOKUP_ERR`（ERROR） |
| 4 | テナント・NAS-Identifier・SSID がすべて一致するルールがある（§8.5） | `AUTH_POLICY_DENIED`（WARN。`tenant_id`、`nas_identifier`、`ssid`） |

- 通ったら、取得先の ID・テナント・ルールの番号・VLAN・Session-Timeout を `eap:` に書き（§6.6）、ベクターを取る（§7）。
- フル認証へ誘導した会話（§6.4）では、AT_IDENTITY で永続 ID を受けた時点で判定する。

### 8.3 加入者のデータ

`sub:{imsi}`（Hash。D-02）のうち、auth が使う項目:

| 項目 | 内容 |
|------|------|
| `state` | `active` / `inactive` / `terminated`（規則は aka-vecgen と同じ。設計概要 §6.2） |
| `source_id` | 取得先の登録簿の ID |
| `rules` | 認可ポリシーのルールの配列（JSON。1〜32 件） |

- 加入者は管理API（aka-provisioner から）で登録・変更する（D-13）。auth は読むだけ。
- 本PoC の形の `sub:`（鍵と SQN）は移し替えない。`state` や `source_id` のない `sub:` は、ベクターを取らずに拒否する（段階 2-3 の G2）。

### 8.4 刻印

| 手順 | 内容 |
|------|------|
| 取り出し | Vendor-Specific（26）のうち Vendor-Id が `RADIUS_STAMP_VENDOR_ID` のものから、Vendor-Type が `RADIUS_STAMP_VENDOR_TYPE` の属性の値をすべて取り出す（RFC 2865 §5.26 の形。1 つの Vendor-Specific に複数あってもよい。形の壊れた Vendor-Specific は、その後ろを読まない） |
| 信頼の印 | 刻印があれば、送信元の `client:{ip}` の `trust_stamp` を読む。`1` でなければ（登録のない送信元を含む）刻印はないものとして扱う（`AUTH_STAMP_UNTRUSTED`、DEBUG）。読めなければ拒否（`AUTH_STAMP_LOOKUP_ERR`） |
| 数 | 0 なら拒否（`AUTH_STAMP_MISSING`。ルールはどれもテナントを持つため）、2 つ以上なら拒否（`AUTH_STAMP_MULTIPLE`） |
| 値 | 10 進の数字だけで 1 以上（`int64`）。違えば拒否（`AUTH_STAMP_INVALID`） |

- 刻印はエッジ（radsecproxy）が付ける（設計概要の決定事項 4）。RADIUS クライアントはエッジのトンネルの IP だけで、`trust_stamp` はコマンド `client add -trust-stamp true` で付ける（運用ガイドの 4 章）。
- Vendor-Id・Vendor-Type はエッジの設定と同じにする。起動のログと `/status` で確かめられる。
- acct も同じ部品（`radiussrv.VendorAttributes`・`ParseTenantStamp`）で刻印を読む（D-10）。

### 8.5 ルールの評価

`subscriber.Match` が、ルールの配列を順に見て、次がすべて一致した最初のルールを採る。一致しなければ拒否する（本PoC の `default` はなくした。設計概要の決定事項 5）。

| ルールの項目 | 一致の条件 |
|-------------|-----------|
| `tenantId` | 刻印のテナントの ID と等しい |
| `nasId` | `*` か、NAS-Identifier と完全一致（大文字と小文字を区別する） |
| `ssids` | `*` を含むか、SSID のどれかと一致（大文字と小文字を区別しない） |

- SSID は Called-Station-Id の最初の `:` より後ろ（`:` がなければ全体。本PoC と同じ）。
- ルールの値の範囲（NAS-Identifier 253 バイト、SSID 32 バイト・32 個、VLAN 1〜4094、Session-Timeout 1〜86400 秒、`*` はほかの SSID と混ぜない）は登録のときに管理API が確かめる（D-13、設計概要 §17.1 の「段階 2-3 で決めたこと」）。
- ルールのテナントを台帳の所属のテナントに限るのは aka-ossbss の役目で、auth はルールのとおりに評価する。

### 8.6 Accept の前の再確認

- Challenge の応答の AT_MAC・AT_RES が正しかったら、Access-Accept の前に `sub:{imsi}` をもう一度読み、`active` でなければ拒否する（§8.2 の 1〜2 と同じログ）。判定から応答までの数秒の間に止めた場合のため（設計概要 §8.1 の 6）。
- 刻印とルールはもう一度は評価しない（判定のときの値を `eap:` から使う）。

### 8.7 Access-Accept の属性

| 属性 | 値 |
|------|----|
| EAP-Message | EAP-Success |
| MS-MPPE-Recv-Key | MSK の先頭 32 バイト（RFC 2548 の形で暗号化。Request Authenticator を使う） |
| MS-MPPE-Send-Key | MSK の次の 32 バイト |
| Class | セッションの UUID（acct が Accounting でセッションを引く） |
| Tunnel-Type・Tunnel-Medium-Type・Tunnel-Private-Group-Id | 13（VLAN）・6（IEEE 802）・ルールの `vlanId`（タグ 0）。ルールに VLAN があるときだけ |
| Session-Timeout | ルールの `sessionTimeout`。あるときだけ |
| Proxy-State、Message-Authenticator | §5.5・§5.3 |

---

## ■セクション9: セッションと利用の集計

### 9.1 概要

| データ | auth がすること | 相手 |
|--------|----------------|------|
| EAP の会話（`eap:{traceId}`） | 作る・書き足す・消す（§6.6） | auth だけ |
| セッション（`sess:{uuid}`）と索引 | Accept で作る（§9.3） | acct が更新・削除、admin が読む |
| 利用の集計（`usage:`） | Accept で `auth_ok` を足す（§9.4） | acct が足す、admin が読む |
| 加入者（`sub:`）、RADIUS クライアント（`client:`）、取得先ごとの数（`cnt:src:{id}`） | 読むだけ | admin とコマンドが書く |
| 遮断器の状態（`stat:sources`） | 書く（§7.7） | admin が読む |

キーと項目の詳細は D-02 と設計概要 §6.1。

### 9.2 Valkey のキー（auth が触るもの）

| キー | 型 | TTL | 読み書き |
|------|----|-----|---------|
| `client:{ip}` | Hash | なし | 読む（`secret`、`trust_stamp`） |
| `sub:{imsi}` | Hash | なし | 読む |
| `cnt:src:{id}` | String | なし | 読む（登録簿の読み直しのとき） |
| `eap:{traceId}` | Hash | 60 秒（書くたびに延ばす） | 読み書き |
| `sess:{uuid}` | Hash | 24 時間 | 作る |
| `idx:sess:imsi:{imsi}`・`idx:sess:tenant:{id}` | Sorted Set | なし（期限はスコア） | 足す |
| `usage:{yyyymmdd}:{tenantId}:{imsi}`・`usage:idx:{yyyymmdd}` | Hash・Sorted Set | その日の始まりから 8 日 | 足す |
| `stat:sources` | String（JSON） | なし | 書く |

### 9.3 セッションの作成

- Accept のときに、セッションの UUID を採番し、`internal/sessions` の `Create` で次を 1 つのトランザクション（MULTI）で書く（設計概要 §10.1）。
  - `sess:{uuid}` に `imsi`・`tenant_id`・`nas_ip`（送信元。エッジのトンネルの IP）・`nas_identifier`・`start_time`・`last_update` を書き、期限を 24 時間後にする。
  - `idx:sess:imsi:{imsi}` と `idx:sess:tenant:{tenantId}` に、スコアを期限の時刻（Unix 秒）にして足す。
- Calling-Station-Id・Accounting の累計などは acct が Accounting で書き足す（D-10）。期限の延長、Stop での削除、期限切れの索引の掃除も acct が行う。
- セッションを作れなければ、Accept を返さずに拒否する（`SESSION_CREATE_ERR`）。Class で引けないセッションを NAS に渡さないため。

### 9.4 利用の集計の `auth_ok`

- Accept のときに、`usage:{日}:{テナント}:{IMSI}` の `auth_ok` を 1 足し、その日の索引 `usage:idx:{日}` に足す（課金レコード §5.2、段階 2-4 の J7）。日は受け取った時刻の日本時間の日付。キーはその日の始まりから 8 日で消える。
- 書けなくても Accept は返す（`USAGE_WRITE_ERR`）。利用者の認証を、集計の都合で止めないため。

### 9.5 Valkey に届かないとき

| 場面 | 動き |
|------|------|
| 共有シークレットを引く | 予備の共有シークレットがなければ捨てる（§4.2） |
| 加入者・信頼の印を読む | 拒否（`AUTH_SUBSCRIBER_LOOKUP_ERR`・`AUTH_STAMP_LOOKUP_ERR`） |
| `eap:` を読み書きする | 拒否（`EAP_CTX_*`） |
| セッションを作る | 拒否（`SESSION_CREATE_ERR`） |
| `auth_ok` を足す | Accept は返す（`USAGE_WRITE_ERR`） |
| 遮断器の状態を報告する | 続ける（`SOURCES_STATUS_WRITE_ERR`） |
| healthcheck | 503（compose が unhealthy にする） |

- コマンドのタイムアウトは 2 秒で、要求の期限（§4.4）の中で終わる。go-redis の再接続に任せ、アプリは再試行しない（エッジの再送に任せる）。

### 9.6 ログ

| `event_id` | レベル | 内容 |
|-----------|--------|------|
| `SESSION_CREATE_ERR` | ERROR | セッションを作れない |
| `USAGE_WRITE_ERR` | ERROR | `auth_ok` を足せない |
| `VALKEY_CONN_ERR` | ERROR | 起動のときに Valkey につながらない |
| `HEALTH_VALKEY_ERR`・`HEALTH_SERVE_ERR` | WARN・ERROR | healthcheck の口 |
| `SYS_LISTEN_ERR` | ERROR | 待ち受けを始められない |

---

## 改訂履歴

| 版数 | 日付 | 内容 |
|------|------|------|
| r1 | 2026-01-12 | 初版作成 |
| r2 | 2026-01-12 | D-03 r3との整合: 状態定義をNEW/WAITING_IDENTITY/IDENTITY_RECEIVED/WAITING_VECTOR/CHALLENGE_SENT/RESYNC_SENT/SUCCESS/FAILUREに変更、状態遷移図をD-03準拠に更新、Post-Auth Policy評価のデフォルトポリシー分岐を反映。AT_BIDDINGのPoC非サポートを明記（RFC 9048準拠）。CK/IK非保存方針（セキュリティ）を追記。セクション9.2.1のStage定数をセクション6.5.1の状態定義に統一。 |
| r3 | 2026-01-18 | IMSIマスキング設定追加: セクション3.1に環境変数LOG_MASK_IMSI追加、セクション3.2/10.3.1の設定構造体更新、セクション3.5新設（マスキング仕様・実装・適用箇所）、関連ドキュメント参照バージョン更新 |
| r4 | 2026-01-20 | UUID仕様明記: セクション1.6にSession UUID用語追加、セクション8.6.4にRFC 4122準拠フォーマット・生成パッケージ・Acct Server連携を追記 |
| r4 | 2026-01-20 | セッション管理セクション追加（セクション9新設）、UUID仕様明記（RFC 4122準拠、36文字）、IMSIマスキング対応 |
| r5 | 2026-01-21 | 互換性エイリアス削除: セクション9.2.1から旧仕様の互換性エイリアス（StageIdentity, StageChallenge）を削除。新規実装では新状態定数のみを使用する方針に統一。 |
| r6 | 2026-01-26 | インフラ基盤統一: セクション2.6新設（Dockerfile方針 - ベースイメージdebian:bookworm-slim、curl/ca-certificates導入）、環境変数RADIUS_SECRETの統一に関する注記追加。これに伴い、旧 2.6 ファイル別責務詳細 のセクション番号を 2.7 に移行。 |
| r7 | 2026-01-27 | API接続設計統一: VECTOR_API_URL環境変数の説明にD-03参照と設定例を追加、関連ドキュメント版数更新（D-03 r3→r4） |
| r8 | 2026-01-27 | ヘルスチェック整合性修正: セクション2.6.1 Dockerfileに`procps`パッケージ追加、セクション2.6.3必須パッケージに`procps`追記（pgrep用） |
| r9 | 2026-02-18 | ディレクトリ構造全面更新、ポリシー評価ロジック更新、関連ドキュメント版数更新 |
| r10 | 2026-10-04 | 既存記載の実装との不一致を修正: Vector Gateway 404時のevent_idを `AUTH_IMSI_NOT_FOUND` → `VECTOR_IMSI_NOT_FOUND` に修正（§3.5.4、§7.8.3）、§7.8.3の呼び出し元でのevent_id表を実装（engine.go `logVectorError`）に合わせて更新（`VECTOR_CONN_ERR` / `VECTOR_CB_OPEN` / `VECTOR_UNKNOWN_ERR` 追加、HTTPクライアント層との2行出力を注記）、§7.8.1に403/409を追加、§7.10のCBログ属性を実装に合わせて修正（`CB_CLOSE` の `recovery_time_ms` 削除、`failure_count` は常に0）、§7.4.2のエラー応答例（404）のdetailを実装の文言に修正、関連ドキュメント版数更新（D-01 r10、D-03 r6、D-04 r19、D-06 r7、D-08 r14、D-12 r5、E-02 r3）。D-04 r19 の event_id 全面整合に合わせて修正（§3.5.4 マスキング適用表・実装例の `AUTH_OK` を `AUTH_SUCCESS`（属性 `trace_id`, `imsi`, `session_id`）に修正し実装にない `SESSION_CREATED` を削除、imsi を出力する event_id を追加、§4.6 の `AUTH_OK` を `AUTH_SUCCESS` に修正、§5.2 Secret解決ログに `RADIUS_IP_EXTRACT_ERR` / `RADIUS_SECRET_ERR` を追加、§5.10 の `RADIUS_AUTH_ERR` / `RADIUS_UNKNOWN_CODE` を `PKT_MA_INVALID` / `PKT_UNKNOWN_CODE` に修正し `PKT_RECV` の属性を `code` に修正、`PKT_NO_EAP` / `PKT_DROP` / `PKT_SEND_ERR` / `EAP_ENGINE_ERR` / `RADIUS_STATUS_OK` / `RADIUS_STATUS_AUTH_FAIL` を追加、§6.5.3 の `EAP_INVALID_STATE` を `EAP_STATE_ERR` / `EAP_UNEXPECTED_IDENTITY` に修正、§6.10 の `EAP_PSEUDONYM_FALLBACK` を削除（専用ログなし）、§8.7〜§8.10 の `POLICY_PARSE_ERR` を `AUTH_POLICY_NOT_FOUND` に統合し `AUTH_POLICY_NOT_FOUND` / `AUTH_POLICY_DENIED` を WARN・実装の属性に修正、§9.5.3 の `EAP_CONTEXT_NOT_FOUND` / `ACCT_SESSION_EXPIRED` を `EAP_CTX_NOT_FOUND` / `ACCT_SESSION_NOT_FOUND` に修正、§9.8.2/§9.9 の `EAP_CONTEXT_NOT_FOUND` / `EAP_CONTEXT_INVALID` / 実行時 `VALKEY_CONN_ERR` / `SESSION_CREATED` / `VALKEY_CONN_RESTORED` を実装の event_id に修正、起動時 `VALKEY_CONN_ERR` の msg を実装に合わせ、`slog.With` による trace_id 付与の記述を D-04 §4.1 に合わせて修正） |
| r11 | 2026-10-04 | ログのIMSIマスク漏れ修正の反映: §3.5.2にUser-Name（EAP Identity）のマスク規則（pkg/logging.MaskUserName）を追記、§3.5.3の実装を pkg/logging の MaskIMSI / MaskUserName を使う認証エンジンのラッパーメソッド（maskIMSI / maskUserName）に更新、§3.5.4適用箇所にEAP_UNSUPPORTED_TYPE / EAP_IDENTITY_INVALIDの `user_name` を追加し「マスクせず出力」の注記を修正。関連ドキュメント参照版数更新（D-04 r19→r20、D-06 r7→r8）。あわせて、廃止済みの internal/logging（mask.go）のパッケージ構成記載を削除し、pkg/logging を使う旨に修正 |
| r12 | 2026-10-04 | §9.5 のセッションTTL超過時の Acct Server の挙動を、Interim 時も `ACCT_SESSION_NOT_FOUND` を出力し不在のキーを作らない実装に合わせて修正 |
| r13 | 2026-10-04 | ポリシーの `nas_id` で `"*"` を任意の NAS に一致させた実装修正の反映（セクション8）: 8.3.2/8.3.3 のルール構造・8.4.2 と 10.4.3 の `PolicyRule` 型・8.5 の評価入力・評価ロジック・注意点・8.6.1 の注記・8.8 の NAS-ID/SSID 不在時の扱い・8.11 を、実装の構造（`nas_id` / `allowed_ssids` / `vlan_id` / `session_timeout`）と評価（`nas_id` は `"*"` 単独で任意の NAS に一致・部分一致なし・それ以外は完全一致、`allowed_ssids` は `"*"` で全SSID・大文字小文字無視、両方に一致した最初のルールで許可、VLAN・Session-Timeout は一致したルールから付与）に修正。r9 で記載した `ssid` / `action` / `time_min` / `time_max` によるルール評価は実装に存在しないため削除。関連ドキュメントの D-02 参照版数を更新（r10→r15） |
| r14 | 2026-10-04 | trace_id の認証単位での引き継ぎ・LOG_LEVEL 対応の実装修正の反映: §4.2 / §4.3 / §4.4 / §5.3 / §5.9 の Trace ID 生成を「決定（`resolveTraceID`）」に修正し、§5.3 に決め方（2回目以降の Access-Request は UUID 形式の State 属性をそのまま Trace ID とし、State がない・UUID 形式でない場合と Access-Request 以外は新規 UUID を生成）と、ハンドラー層のログ・エンジンのログ・X-Trace-ID が1回の認証を通して同じ値になる旨を追記、§7.7.2 に再同期時も同じ Trace ID である旨を追記。EAPエンジンの `Process` が error を返さなくなったことに合わせ、§5.10 から `EAP_ENGINE_ERR` を削除して注記を追加、§10.5.6 に `eap.EAPProcessor`（`Process(ctx, req) *Result`）を追加、§2 の `types.go` / `engine.go` の主要型と §10.7.4 / §10.7.11 のエクスポート一覧を実装（`Request` / `Result` / `Action` / `EAPProcessor`、`EngineImpl` / `NewEngine()`）に合わせて修正。§3.1 環境変数一覧・§3.2 / §10.3.1 設定構造体に `LOG_LEVEL`（既定 INFO）を追加、§3.3 初期化シーケンス・§3.4 main.go のロガー初期化を `pkg/logging.ParseLevel(cfg.LogLevel)` と起動ログの `log_level` に修正、§3.8 にログレベルの説明を追加、§7.10 の `vector api success`（DEBUG）が `LOG_LEVEL=DEBUG` 時のみ出力される旨を追記。§1.3 参照版数更新（D-04 r20→r23） |
| r15 | 2026-10-04 | vector-gateway のバックエンド向けタイムアウトを auth-server より短くした実装修正の反映: §7.2 タイムアウト設定に、Vector Gateway 側のタイムアウト（既定 3秒）を `VectorRequestTimeout`（5秒）より短くしている理由と、Vector Gateway の定数 `authServerVectorTimeout` を `VectorRequestTimeout` と同じ値に保つ必要がある旨の注記を追加（Auth Server の実装は変更なし） |
| r16 | 2026-10-04 | インターネット公開（VPS 等）に向けた安全面の修正の反映: §3.1 環境変数一覧の `RADIUS_SECRET` を任意（既定は空）・空を推奨とし、フォールバックの意味（登録のない送信元IPのパケットも受け付ける。インターネットに公開するサーバーでは空にする。空なら `RADIUS_NO_SECRET` で破棄）の注記を追加。§3.2 / §10.3.1 設定構造体に `RadiusSecret` が任意で空ならフォールバック無効である旨を注記。§3.3 初期化シーケンス・§3.4 main.go に起動ログの `radius_secret_fallback` と `warnFallbackSecret`（`RADIUS_SECRET` 設定時の WARN）を追加。§3.8 起動ログの例に `radius_secret_fallback` を追加し、WARN の msg と公開サーバーでの確認方法を追記。§4.2 / §4.3 / §5.2 Shared Secret解決にフォールバックが空のときの動作（パケット破棄）とフォールバックの注意を追記。§1.3 参照版数更新（D-01 r10→r16、D-04 r23→r30、D-06 r8→r16、D-08 r14→r19） |
| r17 | 2026-10-04 | RADIUSパケットの検証をハンドラーに一本化し、RADIUSライブラリのログをJSONにした実装修正の反映: §4.3 に「PacketServerの設定（パケットの認証とライブラリのログ）」を新設（`InsecureSkipVerify: true` と `ErrorLog: logging.NewRADIUSLibraryLogger()`、`NewServer` のコード、方針と理由）。§4.2 / §5.9 の処理フローのライブラリ内部処理を「デコード（失敗時は `RADIUS_LIB_ERR`）、認証は行わない」に修正し、その他のCodeを `PKT_UNKNOWN_CODE` と明記。§5.3 Code別処理に、Accounting-Request や未知のCodeもハンドラーに届く旨の注記を追加。§4.6 / §5.10 のログ表に `RADIUS_LIB_ERR`（WARN、Secret不明で破棄したときはDEBUG）を追加し、`PKT_UNKNOWN_CODE` の条件を修正、§5.10 注記の「デコード失敗はログを出力しない」を修正。§2.7 / §4.7 の `server.go` の記述を更新。§1.3 参照版数更新（D-04 r30→r31、D-06 r16→r17） |
| r18 | 2026-10-04 | §3.4 の main.go の例で、RADIUS サーバーの生成を実装どおり `server.NewServer(cfg.ListenAddr, handler, secretSource)` に訂正（r17 までは `server.New(cfg.ListenAddr, secretSource, handler)` と、関数名と引数の順序が実装と異なっていた） |
| r19 | 2026-10-05 | Status-Server に正常に応答したときのログを DEBUG に下げた実装修正（D-04 r32）の反映: §4.6 / §5.10 の `PKT_RECV` を Status-Server のときだけ DEBUG、§5.10 の `RADIUS_STATUS_OK` を DEBUG に変更。§5.8 の注意点に、DEBUG とする理由（radsecproxy 等の複数のプロキシが定期的に送る構成で INFO ログが埋まる）と `PKT_SEND_ERR` を追記。検証失敗の `RADIUS_STATUS_AUTH_FAIL`（WARN）は変更なし |
| r20 | 2026-10-06 | NAS-Identifier を NAS の識別情報としてセッションとログに加えた実装修正（D-04 r33、D-02 r20）の反映（radsecproxy 等のプロキシ経由では送信元IPがプロキシのIPになり NAS を区別できないため）: §4.6 / §5.10 の `PKT_RECV` に `nas_identifier`（Access-Request のとき）、§8.10 の `AUTH_POLICY_NOT_FOUND`・`AUTH_POLICY_DENIED`、§3.5 の `AUTH_SUCCESS` の例に `nas_identifier` を追加。§9.3 / §9.7.3 / §10.4.2 のセッション構造体・初期値・作成処理に `nas_identifier`（ポリシー評価に使った NAS-Identifier）を追加 |
| r21 | 2026-10-10 | 加入者の停止の印（D-02 r25 の `policy:{IMSI}` の `status`）の反映: §8.2 の処理フロー、§8.3.1 のフィールド定義、§8.4.1 の取得（`status` の読み込み、不正値は `ErrPolicyInvalid`）、§8.4.2 の主要型（`Status`、`IsSuspended`）を更新し、§8.4.4（停止の判定。ポリシー取得の後・ルール評価の前に判定し、`suspended` なら `AUTH_SUBSCRIBER_SUSPENDED`（WARN）で EAP-Failure を含む Access-Reject。停止中も SQN は進む、次の認証から効く）を新設。§8.8 のエラー表、§8.9 の処理フロー図、§8.10 のログ出力仕様、§3.5.4 の IMSI マスキングの適用箇所の表に `AUTH_SUBSCRIBER_SUSPENDED` を追加 |
| r22 | 2026-10-12 | PoC セット（eapaka-pocset）の品質改善のステップ 3（Go 1.27.2 への更新とイメージの版の固定）の反映: Dockerfile 方針のベースイメージを実ファイルに同期（ビルド `golang:1.25-bookworm` → `golang:1.27.2-bookworm`（Go 1.27.2）、ランタイム `debian:bookworm-slim` → `debian:12.15-slim`（Debian 12 bookworm のポイントリリース）） |
| r23 | 2026-10-12 | PoC セット（eapaka-pocset）の品質改善のステップ 4（待ち受けの失敗での終了、パニックの回復、ビルドの版の埋め込み、使っていない定数の削除）の反映: §3.4 の main.go を実装に同期（`build`、起動ログの `build`、`runServer` に分けた起動・シグナル待機・停止、シグナルの前にサーバーが終わったら `SYS_LISTEN_ERR` を出して終了コード 1）し、注記を追加。§3.3 の初期化シーケンス、§3.9 のシャットダウン処理に待ち受けの失敗時の終了を追加。§4.3 の PacketServer の設定に `Handler: recoverHandler(handler)` を加えてコードを実装に同期し、「パニックの回復」（パケットの処理中のパニックを回復して `SYS_PANIC` を出し、そのパケットには応答しない）を新設。§4.4 の「パニックリカバリは `PacketServer` が処理」を訂正。§4.5・§4.6 に `recoverHandler`・`runServer`・`SYS_LISTEN_ERR`・`SYS_PANIC` を追加。§2.1 のディレクトリ構造に `main_test.go` を追加。§2.6.1 の Dockerfile に `ARG VERSION=dev` と `-X main.build=${VERSION}` を追加し、§2.6.3 の注記に待ち受けの失敗で終了するため `pgrep` が通り続けないことを追記。§3.8 の起動ログの例に `build` を追加。§7.2 のタイムアウト設定から「接続タイムアウト 2秒」を削除し（実装は要求全体の 5秒だけで、接続の確立も含む）、§3.2・§10.2.1 の定数と §10.7.1 のエクスポート一覧から使っていない `VectorConnectTimeout` を削除 |
| r24 | 2026-10-12 | PoC セット（eapaka-pocset）の品質改善のステップ 6（文書の整理）の反映: §1.3 の関連ドキュメントの表から、他の文書の版数を外した（11 か所。今の版はドキュメント一覧で分かるため、参照に版を書かない）。あわせて表の誤りを訂正（Acct Server詳細設計書の ID を D-05 → D-10 に、D-07 の名前を Admin TUI詳細設計書【後半】に、「E-03 テストガイドライン」を T-01 テスト戦略書に） |
| r25 | 2026-10-23 | PoC セット（eapaka-pocset）の pocset-v1.0.1（Go 1.27.3 への更新。crypto/x509 の CVE-2026-111943（GO-2026-6809）の修正）の反映: Dockerfile 方針のビルドのイメージを実ファイルに同期（§2.6.1 の Dockerfile の例と §2.6.2 のベースイメージ選定の表を `golang:1.27.2-bookworm` → `golang:1.27.3-bookworm`（Go 1.27.3）に） |
| r26 | 2026-10-29 | MVP の段階 2-6 で改版（設計概要 §17.2）: aka-radius のサブコマンド auth の設計に書き直した（コードを正にし、コードの写しを削った。4817 行 → 約 900 行）。§2 を 1 つのモジュールのパッケージ（`internal/auth/...` と共有の `internal/radiussrv`・`subscriber`・`sessions`・`usage` など）に、§3 を環境変数 `RADIUS_`・起動の順・`SIGHUP`・ループバックの healthcheck に、§4 を共通の受け口（goroutine への受け渡し、重複応答のキャッシュ・要求の期限・同時処理の上限（Guard）、パニックの回復、停止の順）に改めた。§6 に、本PoC の D-03 の状態機械（状態・遷移・振り分け）をコードに合わせて取り込み（§6.5）、IMSI を EAP の Identity から取ること、再同期を 1 回までにしたこと、EAP-AKA' の CK'・IK' を取得先から受けることを書いた。§7 を vector-gateway 連携から取得先のクライアントに作り直した（本PoC の D-12 の登録簿・mTLS・起動の検証・HTTP の状態の扱いを取り込み、契約 §6・§9.3・R1〜R7 との対応、取得先ごとの遮断器と `stat:sources` の報告、`VECTOR_*`、トレースID、`sources check`）。§8 をベクターを取る前の判定（有無 → 状態 → 刻印 → ルール、Accept の前に状態をもう一度）に作り直した。§9 にセッションと索引の作成、利用の集計の `auth_ok` を書いた。§10（Go 実装定義）と、各節のコードの写し（main.go、構造体、Dockerfile など）を削った |
