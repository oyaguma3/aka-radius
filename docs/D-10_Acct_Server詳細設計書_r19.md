# D-10 Acct Server詳細設計書 (r19)

## 1. 概要

### 1.1 目的

本書は、aka-radius の RADIUS Accounting サーバー（サブコマンド `aka-radius acct`。以下 acct）の設計を定義する。決定事項と段階の記録は設計概要（`docs/design-overview.md`）の §9・§10 にあり、本書はその作りを詳しく書く。正はコード（`internal/acct` と、共通のパッケージ `internal/radiussrv`・`internal/sessions`・`internal/usage`）である。

### 1.2 範囲

| 範囲 | 内容 |
|---|---|
| 受信と応答 | UDP 1813、共有シークレットの解決、Request Authenticator の検証、属性の取り出し、Accounting-Response（Proxy-State を返す）、Status-Server |
| 受け口の守り | 重複した要求への応答の再送（重複応答のキャッシュ）、要求の期限、同時に処理する上限（auth と共通の `radiussrv.Guard`） |
| Accounting の記録 | 重複と順序の判定、前回の累計からの増分、利用の集計への足し込み、セッションと索引の更新を 1 つの Lua で行う（§5） |
| テナントと IMSI | テナントの刻印（VSA）による判定、セッションと重複の判定のキーとの突き合わせ（§5.5） |
| 定期の処理 | セッションの索引の掃除とセッションの数え上げ（1 時間ごと。§6） |
| NAS の起動と停止 | Accounting-On / Off（ログだけ） |

### 1.3 関連

| 文書 | 参照する内容 |
|---|---|
| 設計概要 `docs/design-overview.md` | §6.1（キー）、§8.3（刻印）、§9（重複応答のキャッシュ）、§10（Accounting と利用の集計）、§17.1 の「段階 2-4 で決めたこと」（J1〜J12） |
| 運用ガイド `docs/operation-guide.md` | 6.3（Accounting と利用の集計）、9 章（ログ）、12 章（障害時の確かめ） |
| D-01 aka-radius 設計仕様書 | 全体の構成 |
| D-02 Valkey データ設計仕様書 | `acct:seen:`・`sess:`・`idx:sess:`・`usage:`・`stat:sessions` の形 |
| D-04 ログ仕様設計書 | event_id とログの項目 |
| D-06 エラーハンドリング詳細設計書 | 誤りの分類と RADIUS の応答 |
| D-08 インフラ設定・運用設計書 | compose、環境変数、公開範囲 |
| D-09 Auth Server詳細設計書 | セッションの作成（Class）と、利用の集計の `auth_ok` |
| aka-mvpset の課金レコード（`docs/charging-records.md`） | §5.2（利用の集計の項目と使い方） |

### 1.4 対象外

| 機能 | 扱い |
|---|---|
| Acct-Delay-Time による時刻の補正 | 使わない（日の割り当ては Event-Timestamp か受け取った時刻。§5.6） |
| 複数の Class | 最初に読めた UUID の 1 つだけを使う |
| Accounting-On / Off による NAS のセッションの一括の終了 | しない。セッションは期限（24 時間）で消える |
| CoA / Disconnect | 持たない（設計概要 §2） |

### 1.5 準拠する規格

| 規格 | 範囲 |
|---|---|
| RFC 2865 | Proxy-State、Class、Vendor-Specific |
| RFC 2866 | Accounting-Request / Response、Request Authenticator、Acct-Status-Type（Start・Stop・Interim-Update・Accounting-On・Accounting-Off） |
| RFC 2869 | Acct-Input/Output-Gigawords、Event-Timestamp |
| RFC 5080 §2.2.2 | 重複した要求の見分け方（送信元、Identifier、Request Authenticator） |
| RFC 5997 | Status-Server |

### 1.6 用語

| 用語 | 説明 |
|---|---|
| 累計 | Accounting-Request が運ぶ、セッションの始まりからの時間（Acct-Session-Time）と送受信の量（Acct-Input/Output-Octets と Gigawords を合わせた値） |
| 増分 | 今回の累計から、同じセッションの前回の累計を引いた値。利用の集計にはこれを足す |
| 重複の判定のキー | `acct:seen:{送信元の IP}:{NAS-Identifier か NAS-IP-Address}:{Acct-Session-Id}`。最後の種類と前回の累計、IMSI、テナントを持つ |
| セッション | auth が Access-Accept で作る `sess:{uuid}`。UUID は Access-Accept の Class で NAS に渡り、Accounting で返ってくる |
| 刻印 | エッジが付けるテナントの ID（Vendor-Specific。既定は Vendor-Id 32473、Vendor-Type 1） |

## 2. パッケージ構成

| パッケージ | 役目 |
|---|---|
| `internal/acct` | 入口（`Main`）。設定・ログ・Valkey・healthcheck の口・RADIUS サーバーを組み立て、セッションの索引の掃除（§6）を動かす |
| `internal/acct/config` | 環境変数の読み込みと固定値（§3） |
| `internal/acct/server` | RADIUS のハンドラー。Code の振り分け、Request Authenticator の検証、属性の取り出し、Accounting-Response の送信 |
| `internal/acct/radius` | 属性の取り出し（Gigawords・Event-Timestamp を含む）、Request Authenticator の計算、Accounting-Response の組み立て |
| `internal/acct/processor` | 1 つの Accounting-Request の処理。刻印のテナント、User-Name の IMSI、日の割り当てを決めて記録に渡し、結果をログに出す |
| `internal/acct/store` | 記録の Lua（`accountScript`）と、重複の判定のキー |
| `internal/radiussrv` | auth と共通の受け口（待ち受け、受けたパケットの goroutine への受け渡し、パニックの回復、`Guard`、共有シークレット、刻印の信頼の印、Proxy-State、Status-Server） |
| `internal/sessions` | セッションと索引のキー、掃除（auth・acct・admin で共通） |
| `internal/usage` | 利用の集計のキー、日の割り当て、期限（auth・acct・admin で共通） |
| `internal/health`・`internal/envconf`・`internal/logging`・`internal/valkey` | healthcheck の口、環境変数、ログ、Valkey の接続（共通） |

- 依存は `acct` → `server` → `processor` → `store` の向きで、共通のパッケージはサブコマンドのパッケージを読まない。
- イメージは auth・admin と同じ `aka-radius`（distroless、UID 65532）。compose の healthcheck は `aka-radius healthcheck`（同じコンテナのループバックの口に問い合わせる）。イメージと compose は D-08。

## 3. 設定

### 3.1 環境変数

| 環境変数 | 既定 | 内容 |
|---|---|---|
| `RADIUS_VALKEY_HOST`・`RADIUS_VALKEY_PORT` | `valkey`・`6379` | Valkey の場所 |
| `RADIUS_VALKEY_PASSWORD` | （必須） | Valkey のパスワード |
| `RADIUS_ACCT_LISTEN_ADDR` | `:1813` | 待ち受け（コンテナの中。公開先は compose の `RADIUS_UDP_BIND`） |
| `RADIUS_FALLBACK_SECRET` | 空 | 予備の共有シークレット。空なら、RADIUS クライアントとして登録していない送信元のパケットは捨てる。設定すると起動のときに WARN を出す |
| `RADIUS_STAMP_VENDOR_ID`・`RADIUS_STAMP_VENDOR_TYPE` | `32473`・`1` | 刻印の Vendor-Id と Vendor-Type（auth と同じ値にする） |
| `RADIUS_REQUEST_TIMEOUT` | `4s` | 要求 1 つの処理の期限（500ms〜30s） |
| `RADIUS_MAX_INFLIGHT` | `256` | 同時に処理する要求の上限 |
| `RADIUS_LOG_LEVEL` | `INFO` | ログのレベル |
| `RADIUS_LOG_MASK_IMSI` | `false` | ログの IMSI をマスクするか |
| `RADIUS_HEALTH_ADDR` | `127.0.0.1:9090` | healthcheck の口 |

### 3.2 固定値

| 項目 | 値 | 置き場所 |
|---|---|---|
| Valkey の接続・コマンドのタイムアウト | 3 秒・2 秒 | `internal/acct/config` |
| Valkey の再試行 | 3 回（100 ms〜1 秒） | 同上 |
| セッションの期限 | 24 時間（Start・Interim で延ばす） | `internal/sessions` |
| 重複の判定のキーの期限 | 24 時間（受けるたびに延ばす） | `internal/acct/store` |
| 利用の集計の取り出せる日数 | 7 日（キーはその日の始まりから 8 日で消える） | `internal/usage` |
| セッションの索引の掃除の間隔 | 1 時間（1 回の上限 1 分） | `internal/acct` |
| 重複応答のキャッシュ | 30 秒、50,000 件 | `internal/radiussrv` |
| 停止のときに処理中のパケットを待つ時間 | 5 秒 | `internal/acct/config` |

## 4. 受信から応答まで

### 4.1 流れ

1. `radiussrv.Server`（`layeh.com/radius` の `PacketServer`）がパケットを受け、送信元の IP で共有シークレットを決める（`client:{ip}` の `secret`、なければ予備の共有シークレット、どちらもなければ `RADIUS_NO_SECRET` を出して捨てる）。ライブラリによるパケットの検証は使わず、ハンドラーで検証する（送信元つきのログを出すため）。
2. 受けたパケットはすぐに goroutine に渡してライブラリに戻る（ライブラリは同じ送信元と Identifier の要求をハンドラーが戻るまで黙って捨てるので、重複は次の `Guard` だけで扱う。段階 2-6 の M11）。パニックは回復して `SYS_PANIC` を出し、そのパケットには応答しない。
3. `Guard` が重複応答のキャッシュ、同時に処理する上限、要求の期限を受け持つ（§4.4）。
4. Code が Accounting-Request でも Status-Server でもなければ捨てる（`RADIUS_UNKNOWN_CODE`）。Status-Server は §4.3。
5. Request Authenticator（RFC 2866）を確かめ、合わなければ捨てる（`RADIUS_AUTH_ERR`）。
6. 属性を取り出す（§4.2）。Acct-Status-Type がない、または Start・Stop・Interim で Acct-Session-Id がなければ捨てる（`RADIUS_PARSE_ERR`）。
7. Acct-Status-Type が Start・Stop・Interim-Update なら刻印を読み、記録する（§5）。Accounting-On・Off はログだけ（§5.9）。それ以外の値は捨てる（`RADIUS_UNKNOWN_CODE`）。
8. 記録できなかった（Valkey に書けない、期限を過ぎた）ときは応答しない（§5.10）。記録できれば、Proxy-State をそのまま返した Accounting-Response を送る。

- パケットごとにトレースID（UUID）を作り、ログの `trace_id` に出す（auth の EAP の会話のトレースID とは別のもの）。

### 4.2 使う属性

| 属性 | 使い方 |
|---|---|
| Acct-Status-Type（40） | 必須。1 Start、2 Stop、3 Interim-Update、7 Accounting-On、8 Accounting-Off |
| Acct-Session-Id（44） | Start・Stop・Interim では必須。重複の判定のキーとセッションの `acct_id` |
| Class（25） | UUID として読めればセッションの ID |
| User-Name（1） | セッションも重複の判定のキーも IMSI を持たないときに、永続 ID（`0<IMSI>@realm`・`6<IMSI>@realm`、または 15 桁）から IMSI を取る |
| NAS-Identifier（32）・NAS-IP-Address（4） | 重複の判定のキー（NAS-Identifier、なければ NAS-IP-Address）。NAS-Identifier はセッションとログにも残す |
| Acct-Session-Time（46） | 時間の累計 |
| Acct-Input/Output-Octets（42・43）と Gigawords（52・53） | 送受信の量の累計（Gigawords × 2^32 + Octets） |
| Event-Timestamp（55） | 利用の集計の日の割り当て（§5.6） |
| Calling-Station-Id（31）・Framed-IP-Address（8） | セッションに残す（`calling_station_id`・`client_ip`） |
| Vendor-Specific（26） | テナントの刻印（§5.5） |
| Proxy-State（33） | 応答にそのまま、同じ順で返す |

### 4.3 Status-Server

Message-Authenticator を確かめ、合えば Accounting-Response（Proxy-State と Message-Authenticator つき）を返す。合わなければ捨てる（`RADIUS_AUTH_ERR`）。正常な応答のログは DEBUG（`RADIUS_STATUS_OK`）。

### 4.4 重複応答のキャッシュ、要求の期限、同時に処理する上限

auth と共通の `radiussrv.Guard` が、ハンドラーの前で受け持つ（設計概要 §9。段階 2-3 の G7・G8）。

| 項目 | 内容 |
|---|---|
| 重複の見分け | 送信元の IP とポート、Identifier、Request Authenticator が同じ要求（RFC 5080 §2.2.2） |
| 再送への応答 | 前に送った応答をそのまま送り直す（`ACCT_DUPLICATE_REPLAYED`）。応答はキャッシュに入れてから送る（段階 2-5 の K16） |
| 処理中の重複 | 捨てる（`ACCT_DUPLICATE_DROPPED`、DEBUG）。最初の要求の応答がキャッシュに入れば、次の再送に返る |
| 応答しなかった要求 | 印を残さない（次の再送を新しい要求として処理する。§5.10 の書けないときに効く） |
| 持つ時間・件数 | 30 秒・50,000 件（超えたら古いものから消す） |
| 同時に処理する上限 | `RADIUS_MAX_INFLIGHT`（既定 256）。超えた要求は応えずに捨てる（`RADIUS_OVERLOAD`。1 秒に 1 回に間引き、間の数を `dropped` に出す） |
| 要求の期限 | `RADIUS_REQUEST_TIMEOUT`（既定 4 秒）の期限つきの context を記録に渡す。期限を過ぎると Valkey の操作が失敗し、応答しない |

- Accounting の数え直しは、このキャッシュに加えて、重複の判定と累計からの増分（§5）で防ぐ。プロセスを再起動してキャッシュが消えても、同じ値の再送は増分が 0 になる。

## 5. Accounting の記録

### 5.1 1 つの Lua で行うこと

`internal/acct/store` の `accountScript` が、1 つの Accounting-Request（Start・Interim・Stop）につき 1 回、次をまとめて原子的に行う（設計概要 §10.2。段階 2-4 の J2）。Lua の中でキーを組み立てるので、Valkey は単一ノードで使う。

1. 重複の判定のキーから、最後の種類・前回の累計・IMSI・テナントを読む。
2. 重複と順序を判定する（§5.3）。Start の重複と Stop の後の Stop は、ここで何も書かずに終える。
3. Class のセッションがあれば、その IMSI とテナントを読む。
4. IMSI とテナントを決める（§5.5）。
5. 増分を計算する（§5.4）。
6. IMSI が分かり、足し込む日があり、重複でなければ、利用の集計に足し込む（§5.7）。
7. 重複の判定のキーに、今回の種類・累計・IMSI・テナントを書き、期限を 24 時間に延ばす。
8. セッションがあれば、更新して期限と索引を延ばす（Stop なら消す）（§5.8）。

- 呼び出し側（`processor`）が Lua の前に決めるのは、刻印のテナント（信頼の印は Valkey の `client:{ip}` を引く）、User-Name の IMSI、足し込む日の 3 つ。
- 本PoC（eapaka-radius-server-poc）は重複の判定を GET と SET の 2 回で行い、セッションの更新も別の操作だったので原子的でなかった。累計も持たず、利用の集計もなかった。

### 5.2 読み書きするキー

| キー | 読み書き | 内容 |
|---|---|---|
| `acct:seen:{送信元の IP}:{NAS}:{Acct-Session-Id}` | 読む・書く | Hash。`last`（`start` / `interim` / `stop`）、`sec`・`in`・`out`（前回の累計）、`imsi`、`tenant`。24 時間（受けるたびに延ばす） |
| `sess:{uuid}` | 読む・書く・消す | auth が作ったセッション。acct は作らない |
| `idx:sess:imsi:{imsi}`・`idx:sess:tenant:{id}` | 書く・消す | セッションの索引（Sorted Set。スコアは期限の時刻） |
| `usage:{yyyymmdd}:{tenantId}:{imsi}` | 足す | 利用の集計（Hash） |
| `usage:idx:{yyyymmdd}` | 足す | その日の集計の索引（Sorted Set。メンバーは `{tenantId}:{imsi}`） |
| `client:{ip}` | 読む | `trust_stamp`（刻印を信頼する印）。Lua の外で引く |

- `{NAS}` は NAS-Identifier、なければ NAS-IP-Address（段階 2-4 の J3）。送信元はエッジのトンネルの IP なので、エッジの奥の AP の間で Acct-Session-Id が重なっても混ざらないように、NAS で分ける。
- 形の詳細は D-02。

### 5.3 重複と順序の判定

| 受けたもの | 前の `last` | 判定 | 集計 | ログ |
|---|---|---|---|---|
| Start | なし | 正常 | 前回の累計を 0 とする | `ACCT_START` |
| Start | `start`・`interim` | 重複。何も書かない | しない | `ACCT_DUPLICATE_START`（WARN） |
| Start | `stop` | 順序の誤り `start_after_stop`。0 から数え直す | する | `ACCT_SEQUENCE_ERR`（WARN）と `ACCT_START` |
| Interim | なし | 順序の誤り `no_start_received` | する | `ACCT_SEQUENCE_ERR` と `ACCT_INTERIM` |
| Interim | `start` | 正常 | する | `ACCT_INTERIM` |
| Interim | `interim`（時間・受信・送信の累計がすべて同じ） | 重複。集計しない（キーとセッションの期限は延ばす） | しない | `ACCT_DUPLICATE_INTERIM`（WARN） |
| Interim | `interim`（どれかの累計が違う） | 正常 | する | `ACCT_INTERIM` |
| Interim | `stop` | 順序の誤り `interim_after_stop` | する | `ACCT_SEQUENCE_ERR` と `ACCT_INTERIM` |
| Stop | `stop` | 重複。何も書かない | しない | `ACCT_DUPLICATE_STOP`（DEBUG） |
| Stop | なし | 順序の誤り `no_start_received` | する | `ACCT_SEQUENCE_ERR` と `ACCT_STOP` |
| Stop | `start`・`interim` | 正常 | する | `ACCT_STOP` |

- 順序の誤りは、課金の取りこぼしを避けるためにログを出して数える（段階 2-4 の J4）。

### 5.4 増分

- 時間・受信・送信のそれぞれで、今回の累計から前回の累計を引く。Start と Stop の後の Start は、前回を 0 とする。
- 送受信の量は Gigawords を合わせた値（Gigawords × 2^32 + Octets）で扱い、4 GiB で折り返さない。
- 引いた値が負（累計が減った。NAS の作り直しなど）なら、今回の累計をそのまま増分とし、`USAGE_COUNTER_RESET`（WARN）を出す。
- 同じ値の再送は増分が 0 になるので、重複応答のキャッシュが消えた後でも数え直さない。
- 累計は Lua の数（倍精度）で扱うので、1 つのセッションで 2^53 オクテット（約 9 PB）までは正確である。

### 5.5 テナントと IMSI の決め方

テナント（段階 2-4 の J6）:

| 順 | 出どころ | `tenant_source` |
|---|---|---|
| 1 | 信頼する送信元の刻印 | `stamp` |
| 2 | 重複の判定のキーに残したテナント（0 でないもの） | `seen` |
| 3 | セッションのテナント（0 でないもの） | `session` |
| 4 | どれもなければ `0`（`ACCT_TENANT_UNKNOWN`、WARN） | `none` |

- 刻印は、Vendor-Specific の中の `RADIUS_STAMP_VENDOR_ID`・`RADIUS_STAMP_VENDOR_TYPE` の属性。送信元の RADIUS クライアントの `trust_stamp` が `1` のときだけ使う。2 つ以上（`ACCT_STAMP_MULTIPLE`）、10 進の 1 以上の数でない（`ACCT_STAMP_INVALID`）、信頼の印を読めない（`ACCT_STAMP_LOOKUP_ERR`、ERROR）ときは、刻印がないものとして 2 以降に進む（auth と違い、Accounting は拒否しない）。
- テナント `0` の集計は、エッジを通らないパケットの取りこぼしを見えるようにするため（設計概要 §10.2）。

IMSI:

| 順 | 出どころ |
|---|---|
| 1 | セッションの IMSI |
| 2 | 重複の判定のキーに残した IMSI |
| 3 | User-Name の永続 ID（`0`・`6` に続く 15 桁、または 15 桁。`@` より後は見ない） |
| 4 | どれもなければ集計しない（`ACCT_IMSI_UNKNOWN`、WARN。User-Name をログに出す） |

- 一度決まった IMSI とテナントは重複の判定のキーに残るので、Class のない Interim・Stop や、セッションが消えた後の Accounting でも同じ値で集計する。

### 5.6 日の割り当て

段階 2-4 の J5。`internal/usage` の `Place`。

- Event-Timestamp があればその時刻、なければ受け取った時刻を使う。Event-Timestamp が受け取った時刻より 1 時間以上先なら、受け取った時刻を使う（`ACCT_EVENT_TIMESTAMP_FUTURE`、WARN）。
- 日は日本時間の日付（`yyyymmdd`）。
- 今日の日本時間の始まりから 7 日より前の日には足し込まない（`ACCT_USAGE_TOO_OLD`、WARN）。このときも重複の判定のキーとセッションは更新する。
- 集計のキーと索引は、その日の始まりから 8 日で消える（7 日分を取り出せるように 1 日余裕を持たせる）。

### 5.7 利用の集計への足し込み

| 項目 | 足す時 | 値 |
|---|---|---|
| `sessions` | Start（重複でないもの） | 1 |
| `seconds` | 増分が正のとき | 時間の増分 |
| `in_octets`・`out_octets` | 増分が正のとき | 送受信の量の増分 |
| `auth_ok` | auth の Access-Accept（acct は足さない。D-09） | 1 |

- 何かを足したときは、キーと索引の期限を付け、索引 `usage:idx:{日}` にメンバー `{tenantId}:{imsi}` を足す。
- 集計は aka-ossbss が管理API の `GET /admin/v2/usage?date=YYYY-MM-DD` で毎日取り出す（課金レコード §5.2）。

### 5.8 セッションと索引

- Class の UUID で `sess:{uuid}` を引く。セッションは auth が Access-Accept で作り（D-09）、acct は作らない。
- Start・Interim: `nas_ip`（送信元）、`nas_identifier`、`calling_station_id`、`client_ip`（Framed-IP-Address）、`acct_id`、`last_update` と最後の累計（`seconds`・`input_octets`・`output_octets`。表示用）を書き、Start では `start_time` も書く。期限を受けた時刻から 24 時間に延ばし、IMSI とテナントの索引のスコアも同じ期限にする。
- Stop: セッションを消し、索引から外す。
- Class がない、またはセッションが消えている（期限切れ、auth の作成の失敗）ときは、利用の集計と重複の判定のキーだけを更新し、`ACCT_SESSION_NOT_FOUND`（WARN）を出す。
- 期限切れの索引は、admin が読むときと acct の定期の掃除（§6）で消す。

### 5.9 Accounting-On / Off

ログ（`ACCT_ON`・`ACCT_OFF`、INFO。`nas_ip_address` つき）を出して Accounting-Response を返すだけで、記録しない。その NAS のセッションは期限で消える（段階 2-4 の J12）。

### 5.10 記録できないとき

Lua が失敗した（Valkey に届かない、要求の期限を過ぎた）ときは `VALKEY_CONN_ERR`（ERROR）を出し、Accounting-Response を返さない。NAS（エッジ）は応答がなければ送り直すので、Valkey が戻った後の再送で記録される。判定と書き込みは 1 つの Lua で原子的に行い、`Guard` は応答しなかった要求の印を残さないので、送り直しで数え直すことも、取りこぼすこともない。

- 本PoC は書けなくても応答していたので、Valkey が止まっている間の課金データは失われた。
- 予備の共有シークレットが空（推奨）のときは、Valkey が止まっていると共有シークレットも引けないので、パケットはその前に捨てられる（`RADIUS_SECRET_ERR`）。どちらにしても NAS の再送で記録される。

## 6. セッションの索引の掃除

acct は、起動のときと 1 時間ごとに `sessions.Store.Sweep` を動かす（設計概要 §10.1。段階 2-4 の J8・J11）。

1. `idx:sess:imsi:*` と `idx:sess:tenant:*` を SCAN し、スコア（期限の時刻）が今より前のメンバーを消す。
2. テナントの索引のメンバーの数を足し、`stat:sessions`（`count`・`counted_at`）に書く。管理API の `/status` の `sessionCount`・`sessionsCountedAt` はこれを返す。
3. 済めば `SESSION_SWEPT`（INFO。`sessions`・`latency_ms`）、失敗すれば `SESSION_SWEEP_ERR`（ERROR）。1 回の上限は 1 分。

- 本PoC の索引（`idx:user:`）は TTL も掃除もなく、Stop が届かなかったセッションの分が残り続けた。

## 7. 誤りの扱い

| 状況 | 応答 | ログ |
|---|---|---|
| 送信元の共有シークレットがない | なし（捨てる） | `RADIUS_NO_SECRET`（WARN） |
| 共有シークレットを Valkey から引けない | 予備の共有シークレットがあればそれで続ける。なければ捨てる | `RADIUS_SECRET_ERR`（WARN） |
| パケットの形が壊れている | なし（ライブラリが捨てる） | `RADIUS_LIB_ERR`（WARN） |
| Request Authenticator・Message-Authenticator が合わない | なし | `RADIUS_AUTH_ERR`（WARN） |
| 必須の属性がない | なし | `RADIUS_PARSE_ERR`（WARN） |
| 対応しない Code・Acct-Status-Type | なし | `RADIUS_UNKNOWN_CODE`（WARN） |
| 同時に処理する上限を超えた | なし | `RADIUS_OVERLOAD`（WARN） |
| 記録できない（Valkey、期限） | なし | `VALKEY_CONN_ERR`（ERROR） |
| 重複・順序の誤り・テナントや IMSI が分からない | Accounting-Response | §5.3・§5.5 の表 |
| パケットの処理中のパニック | なし | `SYS_PANIC`（ERROR） |
| 応答を送れない | — | `PKT_SEND_ERR`（ERROR） |

ノード全体の誤りの分類と、起動のときの誤りは D-06。

## 8. ログ

JSON（1 行に 1 つ）で標準出力に出し、`app` は `acct`。項目と書式は D-04。acct の主なログ:

| event_id | レベル | 内容 |
|---|---|---|
| `ACCT_START`・`ACCT_INTERIM`・`ACCT_STOP` | INFO | 記録した（`imsi`・`tenant_id`・`tenant_source`・`session_id`・`day`・`delta_seconds`・`delta_in_octets`・`delta_out_octets`） |
| `ACCT_ON`・`ACCT_OFF` | INFO | NAS の起動・停止の通知 |
| `ACCT_DUPLICATE_START`・`ACCT_DUPLICATE_INTERIM` | WARN | 重複（§5.3） |
| `ACCT_DUPLICATE_STOP` | DEBUG | Stop の後の Stop |
| `ACCT_SEQUENCE_ERR` | WARN | 順序の誤り（`reason`） |
| `ACCT_SESSION_NOT_FOUND` | WARN | Class がない、セッションがない（集計だけ） |
| `ACCT_TENANT_UNKNOWN`・`ACCT_IMSI_UNKNOWN` | WARN | テナント `0` で集計した・集計しなかった |
| `ACCT_STAMP_MULTIPLE`・`ACCT_STAMP_INVALID` | WARN | 刻印を使わなかった |
| `ACCT_STAMP_LOOKUP_ERR` | ERROR | 刻印の信頼の印を読めなかった |
| `ACCT_USAGE_TOO_OLD`・`ACCT_EVENT_TIMESTAMP_FUTURE` | WARN | 日の割り当て（§5.6） |
| `USAGE_COUNTER_RESET` | WARN | 累計が減った |
| `ACCT_DUPLICATE_REPLAYED` | INFO | 再送に前の応答を送り直した |
| `ACCT_DUPLICATE_DROPPED` | DEBUG | 処理中の重複を捨てた |
| `SESSION_SWEPT`・`SESSION_SWEEP_ERR` | INFO・ERROR | セッションの索引の掃除 |
| `VALKEY_CONN_ERR` | ERROR | 起動のときに Valkey に届かない、記録できない |

- 課金のログには `trace_id`・`src_ip`・`nas_identifier`・`acct_session_id` を付ける。送信元はエッジなので、AP は `nas_identifier` で見分ける。
- IMSI は既定でマスクしない（`RADIUS_LOG_MASK_IMSI=false`。設計概要の決定事項 7）。

## 9. 起動と停止

1. 環境変数を読む。誤りなら `error` だけのログを出して終了コード 1 で終える。
2. 起動のログ（`build`・`listen_addr`・`fallback_secret`（設定の有無）・`stamp_vendor_id`・`stamp_vendor_type`・`request_timeout_ms`・`max_in_flight`）を出す。予備の共有シークレットがあれば WARN。
3. Valkey につなぐ。届かなければ `VALKEY_CONN_ERR` を出して終了コード 1。
4. healthcheck の口（`RADIUS_HEALTH_ADDR`。`GET /healthz` が Valkey の PING を返す）を開く。開けなければ `SYS_LISTEN_ERR` で終了コード 1。
5. セッションの索引の掃除（§6）を始め、RADIUS の待ち受けを始める。待ち受けを開始できない（ポートが使われている）か、シグナルの前に終わったときは `SYS_LISTEN_ERR` で終了コード 1（compose の `restart` で起動し直す）。
6. SIGTERM・SIGINT を受けたら、新しいパケットを受けるのをやめ、処理中のパケットが応答を送り終えるのを最大 5 秒待ってから待ち受けを閉じる。

## 10. 試験

| 層 | 置き場所 | 内容 |
|---|---|---|
| 単体 | `internal/acct/processor`（偽物の記録と信頼の印）、`internal/acct/radius`、`internal/acct/server` | 刻印の扱い、User-Name の IMSI、日の割り当て、ログ、属性の取り出し、Authenticator、応答 |
| 単体（miniredis） | `internal/acct/store`、`internal/sessions`、`internal/usage` | Lua の判定・増分・集計・セッションと索引の更新、掃除 |
| 受け口 | `internal/radiussrv` | 重複応答のキャッシュ（応答を入れてから送る）、上限、期限、本物の UDP での再送への応答 |
| 結合（本物の Valkey と UDP） | `internal/acct/integration_test.go` | Start・Interim・Stop と再送、集計の値 |

試験の方針と CI は T-01。

## 11. 未決・将来の検討

| 項目 | 内容 |
|---|---|
| Acct-Delay-Time | 日の割り当ての補正に使うか |
| 複数の Class | 1 つより多い UUID を扱うか |
| Accounting-On / Off | その NAS のセッションを閉じるか |
| セッションの開始の時刻の索引 | 20,000 件で一覧の時間を測ってから決める（aka-mvpset の概要 §19 の 5） |

---

## 改訂履歴

| 版数 | 日付 | 内容 |
|------|------|------|
| r1 | 2026-01-20 | 初版作成 |
| r2 | 2026-01-21 | Status-Server対応追加: セクション1.4からPoC対象外削除、セクション1.5にRFC 5997追加、セクション2.1/2.3にstatus.go追加、セクション4.7新設（Status-Server処理フロー・実装）、セクション7.1.2にMessage-Authenticator検証失敗追加、セクション8.1にPKT_RECV追加、セクション10.1ハンドラー更新、セクション11からStatus-Server削除 |
| r3 | 2026-01-26 | インフラ基盤統一: セクション2.5新設（Dockerfile方針 - ベースイメージdebian:bookworm-slim、curl/ca-certificates導入）、環境変数RADIUS_SECRETの統一に関する注記追加 |
| r4 | 2026-01-27 | ヘルスチェック整合性修正: セクション2.5.1 Dockerfileに`procps`パッケージ追加、セクション2.5.3必須パッケージに`procps`追記（pgrep用） |
| r5 | 2026-02-18 | ディレクトリ構造全面更新、関連ドキュメント版数更新 |
| r6 | 2026-03-05 | Accounting-On/Off対応: §1.2スコープ追加、§1.4対象外から削除、§1.5/§1.6更新、§2.1/§2.3更新、§4.1処理フロー拡張、§4.4属性抽出更新（NAS-Identifier追加・Acct-Session-Id On/Off省略許容・実装コード整合）、§5.6/§5.7新設（ProcessOn/ProcessOff）、§7.1.2/§8.1/§8.2更新、§9.2インターフェース更新、§10.1ハンドラー更新、§11から削除 |
| r7 | 2026-10-04 | D-04 r19 の event_id 全面整合に合わせて修正: §8.1 event_id一覧から実装に存在しない `VALKEY_CONN_RESTORED` / `ACCT_SESSION_EXPIRED` を削除し、`SYS_ERR` / `PKT_SEND_ERR` / `RADIUS_SECRET_ERR` を追加、各説明を実装の出力条件に修正。§7.1.1〜§7.1.3 のエラー表を修正（起動時 `VALKEY_CONN_ERR`、書き込み系 `DB_WRITE_ERR`、`RADIUS_SECRET_ERR`、`RADIUS_PARSE_ERR` は属性抽出失敗でありパケットデコード失敗はログなし、未知のRADIUS Code、`PKT_SEND_ERR` を追加。セッションTTL超過は `ACCT_SESSION_NOT_FOUND` と区別しない旨を明記）。§10.2 Valkey接続復旧検知を「実装しない」に改め実装例を削除。§4.2 Shared Secret解決、§5.3〜§5.5 Start/Interim/Stop処理のコード例を実装（`trace_id` 付与、`RADIUS_SECRET_ERR`、重複チェック時の `VALKEY_CONN_ERR`、Stop後Startの `ACCT_SEQUENCE_ERR`、停止マーク失敗の `DB_WRITE_ERR`）に合わせて更新。§4.7 Status-Server処理のログ（msg・`trace_id`、`packet_code` 削除）を実装に合わせて修正。§8.2 ログ出力例に `trace_id` を追加し、StartなしInterimの msg を `interim without start` に修正。§1.3 関連ドキュメントの版数を現行版に更新（D-01 r10、D-02 r12、D-03 r6（文書名も現行名に修正）、D-04 r19、D-06 r7、D-08 r14、D-09 r10、E-02 r3）。関連ドキュメント表の文書名を実在の文書に修正（存在しない D-05「Valkeyキー・TTL設計書」を削除、D-07「AKA Vector Server」→ D-11 Vector API詳細設計書、E-03 → 共通ライブラリ(pkg)設計書） |
| r8 | 2026-10-04 | ログのIMSIマスク漏れ修正の反映: §6.1 IMSI取得優先順位の優先度3（IMSI抽出失敗時のUser-Name）を「そのまま」からマスク済み（pkg/logging.MaskUserName）に修正、§6.2のResolveIMSIのコードを実装（`internal/session/identifier.go`。引数 userName / classUUID、pkg/logging の MaskIMSI / MaskUserName を使用）に合わせ更新、§6.3を pkg/logging の MaskIMSI / MaskUserName による実装に更新。関連ドキュメント参照版数更新（D-04 r19→r20、D-06 r7→r8、D-09 r10→r11、E-03 r3→r4）。あわせて、廃止済みの internal/logging（mask.go）をディレクトリ構成から削除（IMSIマスキングは pkg/logging を使用） |
| r9 | 2026-10-04 | Interimのシーケンス判定修正の反映: §5.4 Acct-Interim処理を実装（`CheckInterim` による重複・順序異常の一括判定、順序異常の msg `interim sequence error`、`sess:{UUID}` の存在確認と不在時の `ACCT_SESSION_NOT_FOUND`）に合わせて説明とコードを更新。§5.8 重複・順序異常検出を実装（`internal/acct/duplicate.go`）のコードに差し替え、`acct:seen` の値による判定表を追加（Stop後のInterim `interim_after_stop` を新設。削除した `CheckInterimDuplicate` / `HasSeenStart` / `MarkAsStart` の記載を削除し、旧実装でStartなしのInterimを検出できなかった旨を注記）。§5.7 `UpdateOnInterim` に存在確認後に呼び出す旨を注記。§7.1.3 データエラー表（セッション不在にInterimを追加、StartなしでInterimの対処を「受信値を記録して処理継続」に修正、Stop後のInterimを新設）、§8.1 event_id一覧（`VALKEY_CONN_ERR`、`ACCT_SESSION_NOT_FOUND`、`ACCT_DUPLICATE_START`、`ACCT_SEQUENCE_ERR` の説明）、§8.2 ログ出力例（StartなしInterimの msg）、§9.2 インターフェース定義（`DuplicateDetector`、`InterimCheckResult` を追加）を更新。関連ドキュメント参照版数更新（D-02 r12→r13、D-04 r20→r21） |
| r10 | 2026-10-04 | acct-server の重複 Interim の event_id 分離・SYS_ERR 削除・LOG_LEVEL 対応の実装修正の反映: §5.4 / §5.8 / §7.1.3 / §8.1 で重複Interimの event_id を `ACCT_DUPLICATE_START` から `ACCT_DUPLICATE_INTERIM` に変更し、§8.2 にログ出力例を追加（重複Startは従来どおり `ACCT_DUPLICATE_START`）。Accounting処理（`ProcessStart` / `ProcessInterim` / `ProcessStop` / `ProcessOn` / `ProcessOff`）の戻り値から error を外したことに合わせ、§5.3〜§5.7 のコード、§9.2 `AccountingProcessor` の定義、§10.1 ハンドラー（`procErr` と `SYS_ERR`（`処理エラー`）の分岐を削除し、`PKT_SEND_ERR` を実装どおり追記。常にAccounting-Responseを返す旨を注記）を更新し、§8.1 から `SYS_ERR` を削除。`LOG_LEVEL` 対応: §3.1 環境変数一覧に `LOG_LEVEL`（と既存の `LISTEN_ADDR`）を追加、§3.2 設定構造体を実装に合わせ更新、§10.3 起動・シャットダウンのコードを実装の `main.go`（`logging.ParseLevel(cfg.LogLevel)`、起動ログの `log_level`）に差し替え、§2.3 `logging` を共通ライブラリ `pkg/logging` に修正。関連ドキュメント参照版数更新（D-04 r21→r25、D-06 r8→r13、D-08 r14→r17） |
| r11 | 2026-10-04 | インターネット公開（VPS 等）に向けた安全面の修正の反映: §3.1 環境変数一覧の `RADIUS_SECRET` を任意（既定は空）・空を推奨とし、フォールバックの意味（登録のない送信元IPのパケットも受け付ける。インターネットに公開するサーバーでは空にする。空なら `RADIUS_NO_SECRET` で破棄）の注記を追加。§3.2 設定構造体に `RadiusSecret` が任意で空ならフォールバック無効である旨を注記。§4.2 Shared Secret解決にフォールバックの注意（空のときはパケット破棄、`RADIUS_NO_SECRET` は未登録の送信元で出る正常な動作）を追記。§10.3 起動・シャットダウンの説明とコードを実装の `main.go` に合わせて更新（起動ログの `radius_secret_fallback`、`RADIUS_SECRET` 設定時の WARN を出す `warnFallbackSecret`）。§1.3 参照版数更新（D-01 r10→r16、D-04 r25→r30、D-06 r13→r16、D-08 r17→r19、D-09 r11→r16） |
| r12 | 2026-10-04 | RADIUSパケットの検証をハンドラーに一本化し、RADIUSライブラリのログをJSONにした実装修正（`PacketServer` に `InsecureSkipVerify: true` と `ErrorLog: logging.NewRADIUSLibraryLogger()` を設定）の反映: §4.1 の処理フローにデコード（失敗時 `RADIUS_LIB_ERR`）・Secret不明・`RADIUS_AUTH_ERR` を追記し、パケットの認証をハンドラーで行う方針と理由の注記を追加。§4.2 注記にライブラリの DEBUG ログを追記。§4.3 の `VerifyAccountingAuthenticator` を実装のコードに置き換え、`MarshalBinary` による検証が受信バイト列での検証と等価であること・ライブラリが検証しないことを注記。§7.1.2 のパケットパース失敗のログを `RADIUS_LIB_ERR` に修正し、Authenticator検証失敗・未知のRADIUS Codeの条件を実装に合わせて修正。§8.1 の `RADIUS_AUTH_ERR` / `RADIUS_UNKNOWN_CODE` の説明を修正し `RADIUS_LIB_ERR` を追加、以前は Accounting-Request のシークレット不一致で `RADIUS_AUTH_ERR` が出ていなかった旨を注記。§10.3 に `internal/server/server.go` の実装を追加し「DEBUGログはない」を修正。§2.1 / §2.3 に `server_test.go`・`NewRADIUSLibraryLogger` を追加。§1.3 参照版数更新（D-04 r30→r31、D-06 r16→r17、D-09 r16→r17、E-03 r4→r9） |
| r13 | 2026-10-05 | Status-Server に正常に応答したときのログを DEBUG に下げた実装修正（D-04 r32）の反映: §4.7 の `HandleStatusServer` の例の `slog.Info` を `slog.Debug` に、§4.7.4 注意点を DEBUG とその理由（radsecproxy 等の複数のプロキシが定期的に送る構成で INFO ログが埋まる）に修正、§8.1 の `PKT_RECV` を DEBUG に変更。あわせて event_id を `PKT_RECV` から Auth Server と同じ `RADIUS_STATUS_OK` に変更（§4.7 の例、§4.7.4、§8.1）。検証失敗の `RADIUS_AUTH_ERR`（WARN）と `PKT_SEND_ERR`（ERROR）は変更なし |
| r14 | 2026-10-06 | NAS-Identifier を NAS の識別情報としてセッションとログに加えた実装修正（D-04 r33、D-02 r20）の反映（radsecproxy 等のプロキシ経由では送信元IPがプロキシのIPになり NAS を区別できないため）: §5 の Start / Interim / Stop のコードで `src_ip` を持つログに `nas_identifier` を追加し、Start / Interim のセッション更新に `NasIdentifier`（NAS-Identifier があるときだけ `nas_identifier` を上書き、なければ Auth Server が書いた値を残す）を追加。§5.7（セッション状態操作）の UpdateOnStart / UpdateOnInterim、§9.1 の型定義を更新。§8.1 に `nas_identifier` の注記を追加 |
| r15 | 2026-10-12 | PoC セット（eapaka-pocset）の品質改善のステップ 3（Go 1.27.2 への更新とイメージの版の固定）の反映: Dockerfile 方針のベースイメージを実ファイルに同期（ビルド `golang:1.25-bookworm` → `golang:1.27.2-bookworm`（Go 1.27.2）、ランタイム `debian:bookworm-slim` → `debian:12.15-slim`（Debian 12 bookworm のポイントリリース）） |
| r16 | 2026-10-12 | PoC セット（eapaka-pocset）の品質改善のステップ 4（待ち受けの失敗での終了、パニックの回復、ビルドの版の埋め込み）の反映: §10.3 の main.go を実装に同期（`build`、起動ログの `build`、`runServer` に分けた起動・シグナル待機・停止、シグナルの前にサーバーが終わったら `SYS_LISTEN_ERR` を出して終了コード 1）し、`internal/server/server.go` を実装に同期（`Handler: recoverHandler(handler)`、パケットの処理中のパニックを回復して `SYS_PANIC` を出し、そのパケットには応答しない）して説明を追加。§7.1.4（システムエラー）を新設し、§8.1 の event_id 一覧に `SYS_LISTEN_ERR`・`SYS_PANIC` を追加。§2.1 のディレクトリ構造に `main_test.go` を追加。§2.5.1 の Dockerfile に `ARG VERSION=dev` と `-X main.build=${VERSION}` を追加し、§2.5.3 の注記に待ち受けの失敗で終了するため `pgrep` が通り続けないことを追記 |
| r17 | 2026-10-12 | PoC セット（eapaka-pocset）の品質改善のステップ 6（文書の整理）の反映: §1.3 の関連ドキュメントの表から、他の文書の版数を外した（10 か所。今の版はドキュメント一覧で分かるため、参照に版を書かない） |
| r18 | 2026-10-23 | PoC セット（eapaka-pocset）の pocset-v1.0.1（Go 1.27.3 への更新。crypto/x509 の CVE-2026-111943（GO-2026-6809）の修正）の反映: Dockerfile 方針のビルドのイメージを実ファイルに同期（§2.5.1 の Dockerfile の例と §2.5.2 のベースイメージ選定の表を `golang:1.27.2-bookworm` → `golang:1.27.3-bookworm`（Go 1.27.3）に） |
| r19 | 2026-10-29 | MVP の段階 2-6 で改版（設計概要 §17.2）: 全面的に書き直した。§5 を、1 つの Lua（`internal/acct/store` の `accountScript`）による重複と順序の判定・前回の累計からの増分（Gigawords）・利用の集計への足し込み・セッションと索引の更新、重複の判定のキー `acct:seen:{送信元}:{NAS}:{Acct-Session-Id}`、テナントと IMSI の決め方、Event-Timestamp による日の割り当て、記録できないときは応答しないこと、Accounting-On/Off に作り直した。重複応答のキャッシュ・要求の期限・同時処理の上限（`radiussrv.Guard`）と受けたパケットの goroutine への受け渡し（§4.4）、セッションの索引の掃除（§6）を足した。環境変数を `RADIUS_` に改め、パッケージ構成・設定構造体・Dockerfile・ハンドラーや起動の処理などのコードの写しを要約にした。D-03・D-11・E-03 への参照を外した |
