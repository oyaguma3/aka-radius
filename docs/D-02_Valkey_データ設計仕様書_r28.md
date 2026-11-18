# D-02 Valkey データ設計仕様書 (r27)

- 対象: aka-radius が Valkey に置くデータ（キー、型、期限、項目、だれが書き・読むか、原子性）。
- 正: コード（下の表の「コード」の列）と設計概要 `docs/design-overview.md` §6。本書はその要約で、食い違えばコードを正とする。
- 関連: D-01 設計仕様書、D-09 Auth Server 詳細設計書（EAP の会話と判定）、D-10 Acct Server 詳細設計書（Accounting と利用の集計）、D-13 管理API 詳細設計書、運用ガイド `docs/operation-guide.md` の 10 章（バックアップと戻し）。

## 1. 全体方針

| 項目 | 内容 |
|---|---|
| 置き場所 | compose の `valkey`（`valkey/valkey:9.1.2`）。auth・acct・admin とコマンドが `valkey:6379` につなぐ（環境変数 `RADIUS_VALKEY_HOST`・`RADIUS_VALKEY_PORT`・`RADIUS_VALKEY_PASSWORD`）。ホストには `127.0.0.1:6379` だけに出す |
| 認証 | `requirepass`（`.env` の `VALKEY_PASSWORD`。空なら compose が起動を断る） |
| 永続化 | AOF（`appendonly yes`、`appendfsync everysec`）。バックアップはスナップショットを取るスクリプト（運用ガイドの 10 章） |
| メモリー | `maxmemory 1gb`、`maxmemory-policy noeviction`（あふれたら書き込みが誤りになる。黙って消さない）。見積もりは §5 |
| 構成 | 単一ノード。登録・変更・削除と Accounting の Lua は、スクリプトの中でキーを組み立てる（取得先ごとの数、利用の集計、索引）ので、クラスターでは動かない |
| 置かないもの | 鍵（Ki・OPc）と SQN（取得先が持つ）、取得先の登録簿と証明書（ファイル。D-09）、管理クライアントのピン（ファイル）、CK・IK（auth のメモリーの中だけで使い、Valkey には書かない） |
| 時刻の形 | 加入者と RADIUS クライアントの日時は RFC 3339（UTC、秒まで）。セッションと Accounting の時刻は Unix 秒。利用の集計の日は日本時間の `yyyymmdd` |

本PoC（eapaka-radius-server-poc）からの主な変更: 鍵と SQN の入った `sub:` と `policy:` を、状態・取得先・ルールを持つ `sub:` 1 つに置き換えた。加入者と RADIUS クライアントに索引（`subs`・`clients`）を足し、`SCAN` による一覧をやめた。セッションの索引を期限つきの Sorted Set にした。利用の集計・取得先ごとの数・遮断器の状態の報告・セッションの数を足した。監査ログの Stream を `audit` に改めた。

## 2. キーの一覧

| キー | 型 | 期限 | 書く | 読む | コード |
|---|---|---|---|---|---|
| `sub:{imsi}` | Hash | なし | admin（管理API） | auth、admin | `internal/subscriber` |
| `subs` | Sorted Set | なし | admin | admin | 同上 |
| `cnt:src:{sourceId}` | String（整数） | なし | admin | auth（登録簿の読み直し）、admin | 同上 |
| `client:{ip}` | Hash | なし | コマンド `client` | auth、acct（パケットごと）、admin | `internal/clients`、`internal/radiussrv` |
| `clients` | Set | なし | コマンド `client` | admin、コマンド | `internal/clients` |
| `eap:{traceId}` | Hash | 60 秒（書くたびに延ばす） | auth | auth | `internal/auth/session` |
| `sess:{uuid}` | Hash | 24 時間（Start・Interim で延ばす） | auth（作る）、acct（更新・削除） | acct、admin | `internal/sessions`、`internal/acct/store` |
| `idx:sess:imsi:{imsi}` | Sorted Set | なし（メンバーごとの期限をスコアに） | auth、acct | admin | 同上 |
| `idx:sess:tenant:{tenantId}` | Sorted Set | 同上 | auth、acct | admin | 同上 |
| `stat:sessions` | Hash | なし | acct（1 時間ごとの掃除） | admin | `internal/sessions` |
| `acct:seen:{送信元 IP}:{NAS}:{Acct-Session-Id}` | Hash | 24 時間（受けるたびに延ばす） | acct | acct | `internal/acct/store` |
| `usage:{yyyymmdd}:{tenantId}:{imsi}` | Hash | その日の始まりから 8 日 | auth（`auth_ok`）、acct | admin | `internal/usage`、`internal/acct/store` |
| `usage:idx:{yyyymmdd}` | Sorted Set | 同上 | auth、acct | admin | `internal/usage` |
| `stat:sources` | String（JSON） | なし | auth | admin | `internal/auth/source`（`status.go`） |
| `audit` | Stream | 件数の上限 | admin、コマンド | admin | `internal/audit` |

- 「admin」は管理API、「コマンド」は `aka-radius client …` などの運用のコマンド（admin のコンテナで動かす。運用ガイドの 4 章）。
- 管理API の口と、どのキーを読むかの対応は D-13 にある。

## 3. 永続のデータ

### 3.1 加入者 `sub:{imsi}`

IMSI（15 桁の数字）ごとに、状態・取得先・認可ポリシーのルールを持つ。テナントへの所属は持たない（aka-ossbss の台帳にある）。

| 項目 | 形 | 内容 |
|---|---|---|
| `state` | `active` / `inactive` / `terminated` | 状態。規則は aka-vecgen と同じ（設計概要 §6.2） |
| `state_changed_at` | RFC 3339 | 状態を変えた日時（登録のときは登録の日時） |
| `source_id` | 文字列 | 認証ベクターの取得先の ID（取得先の登録簿の `id`。必須） |
| `rules` | JSON の配列 | 認可ポリシーのルール（1〜32 件。§3.2） |
| `created_at`、`updated_at` | RFC 3339 | 登録と最後の変更の日時 |

- `state` か `source_id` のない `sub:`（本PoC の形のデータ）は、形の誤りとして扱う。auth はベクターを取らずに拒否し（`AUTH_SUBSCRIBER_INVALID`）、管理API は 500 を返す（段階 2-3 の G2。移し替えはしない）。
- 値の範囲（IMSI 15 桁、ルールの件数、NAS-Identifier・SSID の長さ、VLAN、Session-Timeout）は管理API で確かめてから書く（D-13、OpenAPI）。

### 3.2 ルール（`rules` の要素）

| 項目 | 内容 |
|---|---|
| `tenantId` | 刻印のテナントの ID（1 以上。必須） |
| `nasId` | NAS-Identifier（253 バイトまで）。`*` はどれでも。大文字と小文字を区別する |
| `ssids` | SSID の配列（32 バイト・32 個まで）。`["*"]` はどれでも（ほかの SSID と混ぜられない）。大文字と小文字を区別しない |
| `vlanId` | 任意（1〜4094）。省くと Access-Accept に VLAN を付けない |
| `sessionTimeout` | 任意（1〜86400 秒）。省くと Session-Timeout を付けない |

評価（配列の順に見て、テナント・NAS-Identifier・SSID がすべて一致した最初のルールを採り、一致しなければ拒否する）は `subscriber.Match` で、D-09 に書く。本PoC の `default`（allow / deny）はない。

### 3.3 加入者の索引 `subs` と取得先ごとの数 `cnt:src:{sourceId}`

- `subs` は IMSI の Sorted Set（スコアはすべて 0）。`ZRANGEBYLEX` で辞書順の一覧と前方一致（管理API の `GET /subscribers` の `prefix` と `cursor`）、`ZLEXCOUNT` で数、`ZCARD` で全体の数（`/status`）を取る。
- `cnt:src:{sourceId}` は、その取得先を指している加入者の数。admin の `/status`・`/sources` に出し、auth は登録簿の読み直しで「加入者が指している取得先を消していないか」を確かめるのに使う（D-09）。

加入者・索引・数は、次の Lua で一度に書き換える（`internal/subscriber/store.go`）。

| 操作 | Lua の中ですること | 断る条件 |
|---|---|---|
| 登録 | `sub:` を書き、`subs` に足し、`cnt:src:{sourceId}` を 1 増やす | すでにいる |
| 変更（取得先・ルール） | 取得先が変われば、前の数を 1 減らし新しい数を 1 増やす。`updated_at` を書く | いない、`terminated` |
| 状態の変更 | `state`・`state_changed_at`・`updated_at` を書く（同じ状態なら何もしない） | いない、`terminated` から（戻しは reinstate だけ） |
| 戻し（reinstate） | `terminated` を `inactive` にする | いない、`terminated` でない |
| 削除 | `sub:` を消し、`subs` から外し、数を 1 減らす | いない、`active` |

- 取得先が登録簿にあるかは、Lua の前に admin が確かめる（登録簿はファイルで、Valkey にない）。
- 削除してもセッションと利用の集計は消さない（期限で消える）。

### 3.4 RADIUS クライアント `client:{ip}` と索引 `clients`

RADIUS クライアントは MVP ではエッジ（のトンネルの IP）だけで、コマンド `aka-radius client add / update / remove` で書く（設計概要 §12、運用ガイドの 4 章）。管理API は読み出しだけで、共有シークレットは返さない。

| 項目 | 内容 |
|---|---|
| `name` | 名前（英数字と `.` `_` `-`、1〜64 文字） |
| `secret` | 共有シークレット（空白と制御文字を除く ASCII で 16〜128 文字）。平文で持つ（RADIUS の検証に要るため）。Valkey のバックアップの扱いに注意する |
| `trust_stamp` | `1` / `0`。`1` のクライアントが付けたテナントの刻印だけを使う（設計概要 §8.3） |
| `created_at`、`updated_at` | RFC 3339 |

- キーの IP は、auth・acct が送信元を引くときと同じ形に直してから書く（IPv4 は 4 つの数、IPv6 は短い形。IPv4 に写した IPv6 は IPv4。ゾーンつきは誤り）。
- `clients` は IP の Set で、一覧（`GET /clients`、`client list`）と数（`/status` の `clientCount`）に使う。追加・削除は `client:` と同じ Lua で行う。Valkey に直接書いたクライアントは索引にないので一覧に出ない（消して入れ直す）。
- auth・acct はパケットのたびに `HGET client:{ip} secret`と `trust_stamp`（刻印を使ってよいか）を引くので、変更はすぐに効く。登録のない送信元は、予備の共有シークレット（`RADIUS_FALLBACK_SECRET`。既定は空）がなければ捨てる（`RADIUS_NO_SECRET`）。予備の共有シークレットで受けた送信元は、刻印を信頼しない。
- 本PoC の `idx:client:{id}`・`seq:client`（ID の採番）と `vendor` の項目はなくした。

### 3.5 監査ログ `audit`

管理API とコマンドの変更の操作を 1 件ずつ `XADD` する（`MAXLEN ~ RADIUS_AUDIT_MAX_LEN`、既定 10000）。同じ内容を日ごとのファイル（`RADIUS_AUDIT_DIR` の `audit-YYYYMMDD.jsonl`）にも書く（D-04、運用ガイドの 9.2）。

| 項目 | 内容 |
|---|---|
| エントリ ID | Stream の ID（`{ミリ秒}-{連番}`）。時刻はここから求める |
| `operator` | 操作者（管理API は `X-Operator-Id`、コマンドは `-operator`） |
| `mgmt_client` | 管理クライアントの識別名（コマンドは `cli`） |
| `action` | 操作（例 `subscriber.create`） |
| `target` | 対象（IMSI、RADIUS クライアントの IP など） |
| `detail` | 変更の内容（JSON の文字列） |
| `trace_id` | トレースID |

- 管理API の `GET /audit-logs` は `XREVRANGE` で新しい順に返す。
- Stream に書けなくても操作は成功のまま（`AUDIT_WRITE_ERR`）。コマンドは Valkey に届かないときもファイルには書く。

## 4. 一時のデータ

### 4.1 EAP の会話 `eap:{traceId}`

EAP の会話（Access-Request の往復）の間、状態とベクターから導いた値を持つ。キーのトレースID は、State 属性の値・ログの `trace_id`・取得先への `X-Trace-ID` に同じ値を使う（UUID。最初の Access-Request で作る）。

| 項目 | 内容 |
|---|---|
| `imsi` | IMSI（仮名などから永続 ID を求めている間は空） |
| `identity` | 鍵の導出に使う端末の Identity（最後の AT_IDENTITY か EAP-Response/Identity。再同期でも使う） |
| `eap_type` | `23`（EAP-AKA）/ `50`（EAP-AKA'） |
| `stage` | 状態機械の状態（`NEW`・`WAITING_IDENTITY`・`IDENTITY_RECEIVED`・`CHALLENGE_SENT`。D-09） |
| `permanent_id_requested` | 永続 ID を求めたか |
| `source_id` | ベクターを取った取得先（再同期で同じ取得先に要求する） |
| `tenant_id`、`rule_index` | ベクターを取る前の判定で使った刻印のテナントと、採ったルールの番号 |
| `vlan_id`、`session_timeout` | 採ったルールの値（Access-Accept に付ける。0 なら付けない） |
| `rand`、`autn`、`xres` | 取得先から受けた値（16 進） |
| `k_aut`、`msk` | 導いた K_aut と MSK（16 進）。CK・IK（CK'・IK'）は書かない |
| `resync_count` | 再同期の回数（上限 1。設計概要 §7.3） |

- 書くたびに期限を 60 秒に戻す。Access-Accept を返すとき、拒否するとき（判定・検証・再同期の上限など）は消す。それ以外は期限で消える。
- 会話の流れと、いつどの項目を書くかは D-09。

### 4.2 セッション `sess:{uuid}` と索引

Access-Accept のときに auth が作り、Class 属性に UUID を入れる。acct が Accounting で更新し、Stop で消す。

| 項目 | 書く時 | 内容 |
|---|---|---|
| `imsi`、`tenant_id` | Accept | IMSI と刻印のテナント |
| `nas_ip` | Accept、Start・Interim | 送信元（エッジのトンネルの IP） |
| `nas_identifier` | Accept、Start・Interim | AP の NAS-Identifier（エッジの奥の AP を見分ける） |
| `start_time` | Accept、Start | 開始の時刻（Unix 秒） |
| `last_update` | Accept、Start・Interim | 最後に更新した時刻 |
| `calling_station_id`、`client_ip`、`acct_id` | Start・Interim | Calling-Station-Id、Framed-IP-Address、Acct-Session-Id |
| `seconds`、`input_octets`、`output_octets` | Start・Interim | Accounting の最後の累計（Gigawords を含む。表示用） |

- 期限は 24 時間で、Start・Interim のたびに延ばす。
- 索引 `idx:sess:imsi:{imsi}`・`idx:sess:tenant:{tenantId}` のメンバーはセッションの UUID、スコアはセッションの期限の時刻（Unix 秒）。auth の作成は `MULTI` で `sess:` と両方の索引を書き、acct の更新・削除は §4.3 の Lua の中で索引も直す。
- 期限を過ぎた索引のメンバーは、管理API が読むとき（`ZREMRANGEBYSCORE`）と、acct の起動のときと 1 時間ごとの掃除で消す。掃除はテナントの索引のメンバーを数えて `stat:sessions`（`count`、`counted_at`）に書く（`/status` の `sessionCount`）。メンバーがなくなった索引は Valkey が消す。
- Class のない・セッションの消えた Accounting では、セッションを作らない（利用の集計だけを行う。D-10）。
- 本PoC の `idx:user:{imsi}`（期限も掃除もない Set）はなくした。

### 4.3 Accounting の重複の判定 `acct:seen:…`

キーは `acct:seen:{送信元の IP}:{NAS-Identifier（なければ NAS-IP-Address）}:{Acct-Session-Id}`（エッジの奥の AP の間で Acct-Session-Id が重ならないように）。

| 項目 | 内容 |
|---|---|
| `last` | 最後に受けた種類（`start` / `interim` / `stop`） |
| `sec`、`in`、`out` | 前回の累計（時間、受信・送信のオクテット。Gigawords を含む） |
| `imsi`、`tenant` | 集計に使った IMSI とテナント（次の Accounting で、刻印やセッションがないときに使う） |

1 つの Accounting-Request につき 1 つの Lua（`internal/acct/store/record.go` の `accountScript`）で、次をまとめて原子的に行う（段階 2-4 の J2）。

1. `last` と今回の種類で重複と順序の誤りを判定する（Start の重複、Stop の後の Stop、同じ累計の Interim は重複）。
2. Class のセッションがあれば、その IMSI とテナントを読む。IMSI はセッション → `acct:seen` → User-Name の順、テナントは信頼する刻印 → `acct:seen` → セッション → `0` の順に決める。
3. 今回の累計から前回の累計を引いて増分を出す（減っていれば今回の値をそのまま増分にする）。
4. 重複でなく、IMSI と日が決まれば、利用の集計（§4.4）に足し込み、索引に足す。
5. `acct:seen` の累計・種類・IMSI・テナントを書き、期限を 24 時間に戻す。
6. セッションがあれば、Start・Interim では項目と期限と索引のスコアを延ばし、Stop ではセッションを消して索引から外す。

判定の規則と、増分・日の決め方の細部は D-10。累計は Lua の数（倍精度）で扱うので、1 つのセッションで 2^53 まで正確である。

### 4.4 利用の集計 `usage:{yyyymmdd}:{tenantId}:{imsi}` と索引 `usage:idx:{yyyymmdd}`

課金レコードの材料として、日本時間の日・テナント・IMSI ごとに数える（課金レコード §5.2）。

| 項目 | 足す時 | 値 |
|---|---|---|
| `auth_ok` | auth の Access-Accept | 1 |
| `sessions` | Accounting-Start（重複でないもの） | 1 |
| `seconds` | Interim・Stop | Acct-Session-Time の増分 |
| `in_octets`、`out_octets` | Interim・Stop | 受信・送信のオクテット（Gigawords を合わせた値）の増分 |

- 日は、auth は受け取った時刻、acct は Event-Timestamp（なければ・1 時間以上先なら受け取った時刻）の日本時間の日付。7 日より前の日には足し込まない。
- テナントの分からない Accounting はテナント `0` で数える。
- 集計のキーと索引の期限は、その日の始まりから 8 日（7 日分を取り出せるように 1 日余裕を持たせる）。
- 索引は `{tenantId}:{imsi}` の Sorted Set（スコア 0）で、管理API の `GET /usage` が辞書順のページ送りに使う。
- auth の `auth_ok` は `MULTI` で `HINCRBY`・`EXPIREAT`・`ZADD` を行う。acct の足し込みは §4.3 の Lua の中で行う。

### 4.5 遮断器の状態 `stat:sources`

auth のプロセスの中にある取得先ごとの遮断器の状態を、admin が `/status`・`/sources` に出すための報告（段階 2-5 の K4）。auth が状態の変わったときと 30 秒ごとに、まるごと書き換える。

```json
{"reportedAt":"2026-10-29T01:23:45Z","sources":{"vecgen":{"state":"closed","since":"2026-10-29T00:00:00Z"}}}
```

`reportedAt` が古ければ、auth が止まっているか Valkey に書けていない（運用ガイドの 3 章）。

## 5. メモリーの見積もり

規模（加入者 20 万、同時のセッション 20,000、ピーク 50 TPS）での見込み。規模の試験（段階 6）で測って直す。

| データ | 件数の目安 | 見込み |
|---|---|---|
| `sub:` と `subs` | 20 万件（ルール数件） | 約 100 MB |
| `usage:` と索引 | 1 日に使った IMSI の数 × 8 日（多くて 160 万件） | 数百 MB（約 250 バイト/件） |
| `sess:` と索引 | 20,000 件 | 約 10〜20 MB |
| `acct:seen:` | 24 時間に始まったセッションの数 | 数十 MB |
| `eap:` | ピーク 50 TPS × 数秒 | 1 MB 未満 |
| `audit` | 10,000 件 | 数 MB |

- 合計で 1 GB（`maxmemory`）に収まる見込み。`noeviction` なので、あふれると書き込みが誤りになり、auth は拒否、acct は Accounting-Response を返さない（D-06）。`INFO memory` の `used_memory` を監視する。

## 6. コンテナごとの使い方

| コンテナ | 主な操作 |
|---|---|
| auth | `client:` の `HGET`（共有シークレットと刻印の印）、`sub:` の `HGETALL`（判定と Accept の前の再確認）、`eap:` の読み書き、Accept で `sess:`・索引・`usage:` を書く、`stat:sources` を書く、登録簿の読み直しで `cnt:src:` を読む |
| acct | `client:` の `HGET`、Accounting ごとに `accountScript`（`acct:seen:`・`sess:`・索引・`usage:`）、1 時間ごとに索引の掃除と `stat:sessions` |
| admin | 加入者の Lua と読み出し、`sess:`・索引・`usage:`・`clients`・`client:`・`stat:sources`・`stat:sessions`・`cnt:src:` の読み出し、`audit` の読み書き |
| コマンド | `client` が `client:`・`clients` の Lua と `audit`。`admin-cert create` が `audit` |

- 接続はプロセスごとの go-redis の接続プール（auth・acct は接続 3 秒・コマンド 2 秒のタイムアウト、プール 10）。Valkey に届かないときの動きは D-06。

## 改訂履歴

| 版数 | 日付 | 内容 |
|------|------|------|
| r1 | - | 初版 |
| r2 | - | Shared Secret優先順位、Trace ID統合 |
| r3 | 2025-12-30 | EAPコンテキストにresync_count/permanent_id_requestedフィールド追加、セッションTTL超過時の動作明記、Go構造体更新 |
| r4 | 2026-01-12 | EAPコンテキスト構造体の変更: eap_type/autn/k_aut/msk追加、ck/ik削除。CK/IK非保存方針（セキュリティ上の理由）を明記。セクション4のデータアクセスフローに鍵導出処理の説明追加。 |
| r5 | 2026-01-20 | Accounting重複検出キャッシュ追加: セクション1プレフィックス一覧に`acct:seen:`追加、セクション3にサブセクションG新設 |
| r6 | 2026-01-20 | UUID仕様明記: セクション3.D/3.EにUUIDフォーマット（RFC 4122準拠、36文字）・生成パッケージ・検証方法を追記。Session UUIDとTrace IDの関係を明記。セッションTTL更新タイミングを「Interim受信ごと」から「Start/Interim受信時にリセット」に修正。 |
| r7 | 2026-01-21 | stageフィールド値明記: セクション3.Dにstageフィールドで使用する状態名一覧を追記（D-03/D-09との整合性確保）。旧仕様値（"identity"/"challenge"）の非推奨を明記。 |
| r8 | 2026-01-26 | SQN競合制御明記: セクション2.Aの`sqn`フィールド備考にCAS更新を追記、WATCH/MULTIによる原子更新方式の補足説明を追加 |
| r9 | 2026-01-27 | idx:userクリーンアップ方針追記: セクション3 F項にAdmin TUIでの読み取り時クリーンアップ方針を明記 |
| r10 | 2026-02-18 | 実装との整合: PolicyDoc→Policy型名変更、PolicyRuleフィールド更新（SSID/Action/TimeMin/TimeMax）、rulesのJSONサンプル更新、stage値を小文字に変更しmodel.Stage型として定義されている旨を明記、Go構造体定義例からredisタグ除去（ストア層変換方式の補足追記）、全コンストラクタシグネチャ追記 |
| r11 | 2026-02-27 | PolicyRule構造を実装コードに合わせて修正: フィールドをNasID/AllowedSSIDs/VlanID/SessionTimeoutに変更、JSONサンプルをNAS-ID/SSIDマッチング＋VLAN・セッションパラメータ形式に更新、Go構造体定義例も同期 |
| r12 | 2026-10-04 | 実装コードとの突き合わせによる修正: (1) 2.C 認可ポリシー: `nas_id` はワイルドカード不可の完全一致（大文字小文字区別）に訂正し、JSON例の `nas_id` "*" を削除。`allowed_ssids` の `"*"`・大文字小文字無視、評価順序（配列順で最初の一致）、SSID抽出、`default` の欠落・不正値（deny扱い）、ポリシー不在・JSON不正時のReject、`vlan_id`（JSON文字列、Tunnel属性3種）・`session_timeout`（>0で付与）、default allow時は属性なし、を明記 (2) 2.A: SQN更新は CAS ではなく単純な HSET（+32、再同期は SQN_MS+32、12桁小文字hex）であることに訂正し、テストベクターモード時のSQN扱い（`sqn` のみの Hash 作成を含む）を追記。`created_at` の形式を明記 (3) 2.B: name/vendor はサーバー未使用、Valkeyエラー時もフォールバックすることを明記 (4) 3.D: stage は大文字の `EAPState` 値で保存されることに訂正（実際に書かれる値を明記）、格納形式・TTLリセット・削除タイミング・書き込みの流れを追記 (5) 3.E: 各フィールドの書き込みタイミングを Auth Accept 時の作成を含めて訂正、実装に無い `ACCT_SESSION_EXPIRED` を削除し Start/Interim/Stop の不在時動作を実装どおりに記載 (6) 3.F: SADD は Auth Server の Accept 時であることを明記、Admin TUI の SCAN フォールバックを追記 (7) 3.G: 重複検出ロジックを実装どおりに訂正（Interim の `no_start_received` が実質出力されない点、Stop重複時は以降の処理をスキップ） (8) 4: 各サーバーのフローを実装に合わせて修正（Acct Server は idx:user に SADD しない、Accounting-On/Off を追記） (9) 1: 接続・認証・永続化設定を compose に合わせて修正、Type列追加、`stats:global` 未使用を注記 (10) 5: `pkg/model` の利用状況を明記し、Valkey と直接対応する各アプリの redis タグ付き構造体を追記。D-04 r19 の event_id 全面整合に合わせて修正（2.E の不在時動作の注記から実装に存在しない event_id 名 `ACCT_SESSION_EXPIRED` を削除し、TTL超過・未作成とも `ACCT_SESSION_NOT_FOUND` である旨に修正） |
| r13 | 2026-10-04 | acct-server の Interim シーケンス判定修正の反映: 3.E Acct Server のセッション処理で、Interim も `sess:{UUID}` の存在を確認し、不在時は `ACCT_SESSION_NOT_FOUND` を出力してセッションを作成しない（`imsi` を持たない Hash が作られる問題を解消）ことに修正、TTLの記述を補足。3.G 重複検出ロジックの Interim を実装どおりに修正（1回の GET で判定してから SET、値なしは `no_start_received`、`stop` は新設の `interim_after_stop` として `ACCT_SEQUENCE_ERR` を出力し処理継続。「`no_start_received` が出力されない」実装上の制約の記載を削除）。4 Acct Server の Interim フローを更新。D-10 の参照セクション番号を修正（5.6→5.8） |
| r14 | 2026-10-04 | テストベクターモードでも加入者登録を必須にした Vector API の実装修正の反映（2.A）: テストベクターモードで置き換えるのは Ki/OPc/AMF だけで、`sub:{IMSI}` の取得・`sqn` の解析と書き戻し・エラー処理は通常モードと同じ（未登録は404、Valkeyエラー・書き戻し失敗は500、`sqn` 解析エラーも500）に修正。既定 SQN `ff9bb4d0b607` へのフォールバック、`sqn` だけを持つ Hash の作成、`TEST_SQN_PERSIST_ERR` の記述を削除し、旧実装で作られた `sqn` だけの Hash の扱いを注記。SQN更新方式の注記に、書き戻し失敗時・未登録時の扱いがテストベクターモードでも同じである旨を追記 |
| r15 | 2026-10-04 | ポリシーの `nas_id` で `"*"` を任意の NAS に一致させた Auth Server の実装修正の反映（2.C）: `nas_id` の説明を「`"*"` 単独は任意の NAS-Identifier（NAS-Identifier が無い場合を含む）に一致、部分一致は行わない、それ以外は完全一致（大文字小文字区別）」に改め、r12 で記載した「ワイルドカード不可」を削除。JSON 例に `nas_id` `"*"` のルールを追加し、評価順（個別のNASのルールを前に置く）を説明。評価ロジック2に `nas_id` / `allowed_ssids` の一致条件を追記。5 の `PolicyRule.NasID` のコメントを `pkg/model` の更新後のコメントに合わせて修正 |
| r16 | 2026-10-04 | auth-server の trace_id 引き継ぎの実装修正の反映: 3.D の UUID の「生成」を、State属性の無い初回 Access-Request で生成し、以降は UUID 形式の State属性の値をそのまま Trace ID とする（ハンドラー層のログを含め1回の認証で同じ値。UUID 形式でない State は新規 UUID）記述に修正、4章の処理フロー 1. の Trace ID の「生成」を「決定」に修正 |
| r17 | 2026-10-04 | acct-server の重複 Interim の event_id 分離の実装修正の反映（3.G）: 重複・順序異常の検出ロジックで、直前と同一の `interim:{input}:{output}` の Interim（重複）に出力する event_id を `ACCT_DUPLICATE_START` から `ACCT_DUPLICATE_INTERIM` に変更（重複 Start は従来どおり `ACCT_DUPLICATE_START`） |
| r18 | 2026-10-04 | SQN競合制御の実装（Lua による `sqn` の比較・置き換え、競合時のやり直し最大3回、HTTP 409）の反映（2.A、5.2）: `sqn` フィールドの備考と「SQN更新方式（現行実装）」を、単純な HSET による後勝ちから、Lua スクリプト（`CompareAndSetSQN`。EVALSHA）で読んだ値と一致するときだけ書き換える方式に改めた。スクリプト本体、比較は `sqn` フィールドのみ・大文字小文字を正規化しない・キー不在時は作成しないこと、ベクター計算を書き換え成功後に行う順序、競合時のやり直し（1〜10ms待機、最大3回、`SQN_CONFLICT_RETRY`）と上限超過・期限切れ時の 409（`SQN_CONFLICT_ERR`）、再同期のやり直しで同期済みとみなす場合、書き換え後のベクター計算失敗時は SQN が進んだままになること、守る性質（IMSIごとに一意・単調増加）、WATCH/MULTI 設計からの経緯を記載。「WATCH/MULTI による CAS・リトライ・HTTP 409 は実装していない」の記述を削除。残っている制約として Admin TUI の加入者編集が `sqn` を上書きし巻き戻りうること（別PRで対応予定）を追記。5.2 ストア層変換方式の補足の Vector API の書き戻し方法を更新 |
| r19 | 2026-10-04 | Admin TUI の加入者編集による `sqn` の上書き（巻き戻り）を解消した実装修正の反映（2.A、4、5.2）: r18 で「残っている制約」として記載した Admin TUI による `sqn` の上書きを解消済みとし、「Admin TUI からの書き込み（加入者編集）」に改めた。Lua スクリプト（`updateSubscriberScript`）で存在チェックと更新をまとめて行うこと、SQN を変更していないとき（大文字小文字の違いだけを含む）は `ki` / `opc` / `amf` だけを更新し `sqn` を書き換えないこと、変更したときは編集開始時の値と一致する場合だけ更新し一致しなければ何も更新しないこと、加入者が削除されていればキーを作らないこと、新規作成・CSVインポートは例外であることを記載。`sqn` フィールドの備考を補足。Hex表記の「入力どおりに保存する」を「大文字に正規化して保存する」に訂正。4 の Admin TUI のデータアクセスと 5.2 ストア層変換方式の補足に編集時の Lua スクリプトを追記 |
| r20 | 2026-10-06 | `sess:{UUID}` に `nas_identifier`（NAS-Identifier）を追加した実装修正の反映: radsecproxy 等のプロキシ経由では `nas_ip`（送信元IP）がプロキシのIPになり NAS を区別できないため。Auth Server が Accept 時にポリシー評価に使った NAS-Identifier を書き、Acct Server が Start / Interim で NAS-Identifier があれば上書きする（なければ残す）。§2 のフィールド表・処理フロー・§5 の構造体を更新し、`nas_ip` がプロキシ経由ではプロキシのIPになる旨を追記 |
| r21 | 2026-10-07 | Admin TUI の加入者・RADIUSクライアント・認可ポリシーの store と validation を pkg に移した実装修正（Provisioning API（D-13）と共通で使うため。E-03 r11）の反映: §2.A の Admin TUI の加入者編集の実装箇所を `pkg/masterdata/subscriber.go` に、§5.2 のストア層変換方式の補足を `pkg/masterdata` に修正し、作成・変更を Lua スクリプトで原子的に行う旨を追記。Admin TUI の `internal/model` は廃止して `pkg/model` に統合 |
| r22 | 2026-10-08 | Provisioning API（D-13）の実装の反映: §2 の冒頭に Provisioning API も同じキー・形式で読み書きすること、§2.A に Provisioning API からの加入者の変更（`SubscriberStore.Patch`。`sqn` を指定しなければ触れない、指定した場合は比較せずに書き換える）、§4 に「Provisioning API」のアクセスパターン（CRUD、PATCH・PUT、`ListPage` による一覧、`/status` の件数、連動しない操作）を追加 |
| r23 | 2026-10-08 | RADIUSクライアントにサーバー採番の ID を導入（D-13 r5）: §1 のキー一覧に `idx:client:`（ID → IP の索引、String）と `seq:client`（採番のカウンター）を追加。§2.B に `id` フィールドと「IDと索引」（作成・上書き・IP の変更・削除の Lua、ID からの取得、起動時の採番 `EnsureIDs`、simwifi での確認）を追加。§4 の Admin TUI / Provisioning API のアクセスパターン、§5.1 の `RadiusClient` に `ID` を追加。Auth / Acct Server の `client:{IP}` の参照は変更なし |
| r24 | 2026-10-09 | Provisioning API の監査ログとセッションの参照（D-13 r6、API 0.3.0）: §1 のキー一覧に `audit:prov`（Stream）を追加し、§2.H（監査ログの写し。フィールド、XADD の MAXLEN、読み出し、メモリの目安）を新設。§3.F の注記に Provisioning API は索引を掃除しないことを、§5.1 の `pkg/model` の利用元に Provisioning API と `Policy` / `PolicyRule`（`pkg/masterdata` で使用。r21 以降の実態に合わせる）を、§4 の Provisioning API に監査ログの読み書きと `GET /sessions` の読み出し（`pkg/masterdata.SessionStore`）、`/status` の `sess:*` の件数を追記 |
| r25 | 2026-10-10 | 加入者の停止の印（一時停止・再開）を追加: §2.C の `policy:{IMSI}` にフィールド `status`（`active` / `suspended`。ないときは `active`）を加え、Auth Server の扱い（`suspended` はルールを評価せずに Reject・`AUTH_SUBSCRIBER_SUSPENDED`、不正値は安全側に Reject）と「停止の印」の節（書き込むのは停止・再開の操作だけ、ポリシーの保存・置き換えでは残る、CSV の取り込みの扱い、削除で消える、次の認証から効く、停止中も SQN は進む）を追加。§3 の Auth Server の処理フロー、Admin TUI・Provisioning API の操作（`PolicyStore.SetStatus`、PUT の応答の `status`）、§4 の `pkg/model.Policy` と Auth Server の `Policy` に `Status` を追記 |
| r26 | 2026-10-12 | PoC セット（eapaka-pocset）の品質改善のステップ 3（Go 1.27.2 への更新とイメージの版の固定）の反映: 接続方式の Valkey コンテナのイメージを `valkey/valkey:9.0` → `valkey/valkey:9.1.2` に |
| r27 | 2026-10-29 | MVP の段階 2-6 で改版（設計概要 §17.2）: MVP の作りに合わせて全面的に書き直した。キーの一覧を設計概要 §6.1 とコードに合わせ（`sub:` を状態・取得先・ルールに、`policy:`・`idx:user:`・`idx:client:`・`seq:client`・`audit:prov` をなくし、`subs`・`cnt:src:`・`clients`・`idx:sess:imsi:`・`idx:sess:tenant:`・`stat:sessions`・`stat:sources`・`usage:`・`usage:idx:`・`audit` を足した）、キーごとに書く・読むコンテナと原子性（Lua・MULTI）を書いた。`acct:seen:` を NAS ごとの Hash（前回の累計を持つ）に、`eap:` に取得先・テナント・ルール・Identity の項目を、`sess:` にテナントと累計を足した。Valkey の `maxmemory` を 1 GB にし、メモリーの見積もりを書いた。Go の構造体の写しと、Admin TUI・Provisioning API・Vector Gateway のアクセスの流れを削った |
