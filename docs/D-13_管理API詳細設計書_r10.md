# D-13 管理API 詳細設計書 (r10)

**作成日:** 2026-10-07
**更新日:** 2026-10-29
**ステータス:** 実装済み（管理API 2.0.0。`internal/admin`。MVP セットの段階 2-3〜2-5）

## 1. 概要

### 1.1 目的

本書は、aka-radius の管理API 2.0.0（`/admin/v2`）の設計の方針を定める。口ごとの項目と形は OpenAPI（`docs/openapi/admin-api.yaml`）を正とし、本書には写さない。本書には、OpenAPI に書けない決まり（認証、ヘッダー、エラー、状態の規則、ページ送り、監査ログ、各口の作りの要点）を書く。

r9 までは、本PoC（eapaka-radius-server-poc）の Provisioning API 0.4.0（`/admin/v1`、任意のコンテナ `provisioning-api`）の設計書だった。MVP の段階 2 で管理API 2.0.0 に置き換えたので、番号を残して作り直した（設計概要 §11）。

### 1.2 位置づけ

| 項目 | 内容 |
|---|---|
| 呼び出し側 | aka-provisioner だけ（MVP セットの管理面）。全権として扱い、口ごとの権限は持たない |
| 置き場所 | コンテナ `admin`（サブコマンド `aka-radius admin`）。auth・acct と同じイメージ |
| 待ち受け | 9444/tcp（HTTPS）。公開先は `RADIUS_ADMIN_BIND`（既定 `127.0.0.1`。本番はサービス用の WireGuard のアドレス。D-08） |
| 本PoC との関係 | Provisioning API 0.4.0 の後継。パスを `/admin/v2` に改め、互換は持たない |
| 作法 | aka-vecgen の管理API にそろえる（ProblemDetails の `cause`、`X-Operator-Id`・`X-Trace-ID`、JSON Merge Patch、カーソルの一覧、状態の値と口、管理クライアントのピン、監査ログの形） |

### 1.3 扱うもの・扱わないもの

| 区分 | 内容 |
|---|---|
| 書き込み | 加入者（状態・取得先・認可ポリシーのルール）の登録・変更・状態の変更・戻し・削除 |
| 読み出し | 加入者（個別・一覧・一括）、セッション、利用の集計、RADIUS クライアント、取得先の登録簿、ノードの状態、監査ログ |
| 扱わない | 鍵（Ki・OPc。aka-radius は持たない）、RADIUS クライアントの書き込みと共有シークレット（ノードの上のコマンド `aka-radius client`）、取得先の登録簿の書き込み（ファイルと `SIGHUP`）、管理API のサーバー証明書（コマンド `aka-radius admin-cert`）、`Idempotency-Key`（再送の判定は aka-provisioner に任せる） |

本PoC の `/subscribers/{imsi}/keys`、`/policies` 系、`/clients` の書き込み、`/clients/{id}/secret` はなくした。

### 1.4 関連ドキュメント

| ドキュメント | 参照内容 |
|---|---|
| 設計概要（`docs/design-overview.md`） | §11（管理API）、§6（データ）、§17.1（段階 2-3〜2-5 で決めたこと） |
| OpenAPI（`docs/openapi/admin-api.yaml`） | 口ごとの項目と形（正） |
| 運用ガイド（`docs/operation-guide.md`） | 5 章（管理クライアントの登録と証明書の入れ替え）、7 章（サーバー証明書） |
| D-02 Valkey データ設計仕様書 | 加入者・セッション・利用の集計・監査ログのキー |
| D-04 ログ仕様設計書 | `ADMIN_*` などの `event_id` |
| D-06 エラーハンドリング詳細設計書 | 管理API の誤りの扱い |
| D-08 インフラ設定・運用設計書 | 公開先、証明書とピンの置き場所、共有ネットワーク |

## 2. 認証

### 2.1 mTLS と管理クライアントのピン

- TLS 1.2 以上で、クライアント証明書を必須にする。証明書は CA で検証せず、SHA-256 のフィンガープリントをピンのファイルと照合する（aka-vecgen の管理API と同じ方式）。
- ピンのファイルは `RADIUS_ADMIN_CLIENTS_FILE`（既定 `/run/secrets/admin-clients`。compose の secrets）。1 行に `識別名=フィンガープリント` を書く。`#` の行と空行は読み飛ばす。フィンガープリントは大文字・コロン区切り（`openssl x509 -fingerprint -sha256` の出力）も受ける。
- 同じ識別名に 2 行（新旧 2 つの証明書）を書ける。証明書を入れ替える間は両方を受ける（aka-vecgen の決定事項 7）。同じフィンガープリントが 2 回出たら誤り。
- ピンが 1 つもないファイルは誤りにする（起動しない。読み直しでは前のまま）。aka-vecgen は空のファイルを受けるが、admin は管理API のためだけのコンテナなので、だれも使えない状態で動かさない（段階 2-5 の K7）。
- 照合はハンドシェイクの中で行い、証明書がない・登録されていない・有効期間の外のどれかなら、HTTP の応答を返さずに切る。ログに `ADMIN_CLIENT_REJECTED`（`reason`・`fingerprint`・`src_ip`）を出す。登録の誤りは、この `fingerprint` と比べて確かめる。
- `ClientAuth` は `RequestClientCert` にして、証明書のない接続もこちらの照合で断る（`RequireAnyClientCert` では照合の前に断られ、ログに残らないため）。秘密鍵の所持（CertificateVerify）はどちらでも確かめられる。

### 2.2 読み直し（`SIGHUP`）

`docker compose kill -s HUP admin` で、次の 3 つをそれぞれ読み直す。失敗したものは前のまま続ける。

| 対象 | 成功 | 失敗 | 効く時 |
|---|---|---|---|
| ピンのファイル | `ADMIN_CLIENTS_RELOADED` | `ADMIN_CLIENTS_RELOAD_ERR` | 次のハンドシェイクから（張ってある接続は切らない） |
| サーバー証明書と鍵（`RADIUS_ADMIN_TLS_CERT`・`RADIUS_ADMIN_TLS_KEY`） | `SERVER_CERT_RELOADED` | `SERVER_CERT_RELOAD_ERR` | 次の接続から |
| 取得先の登録簿（`RADIUS_SOURCES_FILE`。鍵は読まない） | `SOURCES_RELOADED` | `SOURCES_RELOAD_ERR` | すぐ（`sourceId` の確かめ、`/status`・`/sources`） |

- ピンのファイルはファイルごとマウントしているので、その場で書き換える。`mv` で置き換えたときは、コンテナを作り直す（運用ガイド 5.1）。
- `/status` の `adminClients`・`adminClientsLoadedAt`・`sourcesLoadedAt` で、読み直しが効いたかを確かめられる。

### 2.3 起動のときの確かめ

admin は次の順に読み、どれかが失敗したら終了する（compose の `restart` で起動し直す）。

1. 設定（環境変数）
2. ピンのファイル（`ADMIN_CLIENTS_LOAD_ERR`）
3. サーバー証明書と鍵（`SERVER_CERT_LOAD_ERR`）
4. 取得先の登録簿（`SOURCES_LOAD_ERR`）
5. 監査ログのディレクトリに書けるか（`AUDIT_DIR_ERR`）
6. Valkey への接続

## 3. ヘッダー

| ヘッダー | 向き | 内容 |
|---|---|---|
| `X-Operator-Id` | 要求 | 操作した人（aka-ossbss の利用者など）。任意。`^[A-Za-z0-9._@-]{1,64}$` に合わなければ 400（`OPTIONAL_IE_INCORRECT`）。監査ログの `operator` とログの `operator` に残す |
| `X-Trace-ID` | 要求・応答 | トレースID。印字できる ASCII の 1〜64 文字ならそのまま使い、なければ・合わなければ 16 バイトの乱数の 16 進で採番する。応答の同じヘッダーで返し、ログ（`ADMIN_REQUEST`）と監査ログの `traceId` に残す |

- 要求の本文は 256 KiB まで。未知の項目は断る（`INVALID_MSG_FORMAT`。PATCH は `OPTIONAL_IE_INCORRECT`）。
- 処理を終えた要求はすべて `ADMIN_REQUEST`（方法、パス、状態、時間、管理クライアントの識別名、操作者、送信元）としてログに出す。

## 4. エラー

エラーは ProblemDetails（`application/problem+json`。`title`・`status`・`detail`・`cause`・`invalidParams`）で返す。`cause` の名前は、本PoC の Provisioning API 0.4.0 の名前を残し、MVP で足した誤りは aka-vecgen と同じ名前にした（段階 2-3 の G11）。

| `cause` | 状態 | 当たる時 |
|---|---|---|
| `INVALID_MSG_FORMAT` | 400 | 本文が JSON でない、形が違う、未知の項目がある |
| `INVALID_QUERY_PARAM` | 400 | クエリの値が違う（`limit` の範囲、`cursor`、`prefix`、`date`、`tenantId` と `imsi` の両方を渡した・どちらもない など） |
| `MANDATORY_IE_MISSING` | 400 | 必須の項目がない（PATCH の空のオブジェクトを含む） |
| `MANDATORY_IE_INCORRECT` | 400 | 必須の項目の値が違う |
| `OPTIONAL_IE_INCORRECT` | 400 | 任意の項目の値が違う。`X-Operator-Id` の形、PATCH の項目（`state` を PATCH で変えようとしたときも） |
| `UNKNOWN_SOURCE` | 400 | `sourceId` が取得先の登録簿にない（aka-radius だけにある） |
| `USER_NOT_FOUND` | 404 | 加入者がいない（IMSI の形が違うときも） |
| `RESOURCE_NOT_FOUND` | 404 | 口がない |
| `SUBSCRIBER_ALREADY_EXISTS` | 409 | 登録で、同じ IMSI がもういる |
| `SUBSCRIBER_ACTIVE` | 409 | `active` の加入者を消そうとした |
| `SUBSCRIBER_TERMINATED` | 409 | `terminated` の加入者を PATCH で変えようとした |
| `STATE_TRANSITION_NOT_ALLOWED` | 409 | `terminated` からの `PUT …/state`、`terminated` でない加入者の `reinstate` |
| `SYSTEM_FAILURE` | 500 | Valkey に届かないなどの中の誤り。詳細は返さず、ログに `ADMIN_ERROR`（パニックは `SYS_PANIC`）を出す |

- 入力の誤りが複数あれば、`invalidParams` にまとめて返す。`cause` は、必須の欠落 → 必須の誤り → 任意の誤りの順に重いものを採る。
- `RESOURCE_NOT_FOUND` は設計概要 §11.1 の一覧にないが、口がないときの応答としてコードにある。

## 5. 加入者

### 5.1 項目

加入者は 1 つのリソース（本PoC の加入者と認可ポリシーを 1 つにした）で、Valkey の `sub:{imsi}` に置く（D-02）。

| 項目 | 内容 |
|---|---|
| `imsi` | 15 桁の数字。変えない |
| `state` | `active` / `inactive` / `terminated` |
| `stateChangedAt` | 状態を変えた時刻 |
| `sourceId` | 取得先の登録簿の ID（英小文字・数字・`-` の 1〜32 文字）。登録と変更のときに登録簿にあるかを確かめる |
| `rules` | 認可ポリシーのルール（1〜32 件）。項目と評価の規則は設計概要 §8.2・D-09。値の範囲は NAS-Identifier 253 バイト、SSID 32 バイト・32 個（`["*"]` はほかと混ぜない）、VLAN 1〜4094、Session-Timeout 1〜86400 秒 |
| `createdAt`・`updatedAt` | 作った時刻と変えた時刻 |

### 5.2 状態の規則

規則は aka-vecgen と同じ（設計概要 §6.2）。

| 今の状態 | `PATCH`（取得先・ルール） | `PUT …/state` | `POST …/reinstate` | `DELETE` |
|---|---|---|---|---|
| `active` | できる | `inactive`・`terminated` へ | 409 `STATE_TRANSITION_NOT_ALLOWED` | 409 `SUBSCRIBER_ACTIVE` |
| `inactive` | できる | `active`・`terminated` へ | 409 `STATE_TRANSITION_NOT_ALLOWED` | できる |
| `terminated` | 409 `SUBSCRIBER_TERMINATED` | 409 `STATE_TRANSITION_NOT_ALLOWED` | `inactive` へ（理由が必須） | できる |

- 登録（`POST /subscribers`）では `state` が必須で、`active` か `inactive` に限る。
- 状態は PATCH では変えられない（`PUT …/state` と `POST …/reinstate` だけ）。
- 同じ状態への `PUT …/state` は何もせずに 200 を返し、監査ログには `unchanged` の印をつけて残す（aka-vecgen と同じ）。
- 理由（`reason`）は 500 文字まで。`PUT …/state` では任意、`reinstate` では必須。
- 削除しても、その加入者のセッションと利用の集計は消さない（期限で消える）。
- 登録・変更・状態の変更・削除は Lua で `sub:`・索引 `subs`・取得先ごとの数 `cnt:src:{id}` を一度に書き換える（D-02）。`state` のない本PoC の形の `sub:` を読むと 500 になる（段階 2-3 の G2）。

### 5.3 一覧と一括取得

- `GET /subscribers`: IMSI の辞書順。`prefix`（1〜15 桁）で前方一致に絞れる。`total` は `prefix` に当たる数。索引 `subs` を使う（本PoC の SCAN をやめた）。
- `POST /subscribers/batch-get`: `imsis`（1〜500 件）の加入者を返し、いないものは `notFound` に並べる。aka-ossbss の突き合わせのため。

## 6. ページ送り

| 口 | 並び | `limit`（既定・上限） | 続き |
|---|---|---|---|
| `GET /subscribers` | IMSI の辞書順 | 50・500 | `nextCursor`（最後の IMSI）を `cursor` に渡す |
| `GET /sessions` | 期限の早い順 | 100・1,000 | `nextCursor`（位置）を `cursor` に渡す |
| `GET /usage` | テナント・IMSI の辞書順 | 1,000・1,000 | `nextCursor`（`テナント:IMSI`）を `cursor` に渡す |
| `GET /audit-logs` | 新しい順 | 100・500 | `nextBefore`（Stream のエントリID）を `before` に渡す |
| `GET /clients` | IP の順 | なし（全件） | なし |
| `GET /sources` | 登録簿のファイルの順 | なし（全件） | なし |

- 続きがあるときだけ `nextCursor`・`nextBefore` を返す。カーソルは前の応答の値だけを受け、形が違えば 400（`INVALID_QUERY_PARAM`）。

## 7. 読み出しの口の作り

### 7.1 `GET /status`

| 項目 | 出どころ |
|---|---|
| `version`（`2.0.0`）・`build`・`nodeName`・`startedAt` | admin のプロセス（`nodeName` は `RADIUS_ADMIN_NODE_NAME`、なければホスト名） |
| `subscriberCount` | 索引 `subs` の数 |
| `clientCount` | RADIUS クライアントの索引 `clients` の数 |
| `sessionCount`・`sessionsCountedAt` | acct が 1 時間ごとの掃除で数えた値（`stat:sessions`）。まだ数えていなければ返さない |
| `sources`・`sourcesLoadedAt` | admin が読んだ登録簿（ID、名前、基底の URL、タイムアウト、クライアント証明書の期限）と、取得先ごとの加入者の数（`cnt:src:{id}`）、遮断器の状態 `circuitBreaker` |
| `stampVendorId`・`stampVendorType` | `RADIUS_STAMP_VENDOR_ID`・`RADIUS_STAMP_VENDOR_TYPE`（エッジの設定と同じかを確かめるため） |
| `adminClients`・`adminClientsLoadedAt` | ピンの識別名と、読んだ時刻 |

- 遮断器の状態は auth の中にあるので、auth が状態の変わったときと 30 秒ごとに `stat:sources` に書き、admin はそれを読む（`state`・`since`・`reportedAt`。段階 2-5 の K4）。報告がなければ `circuitBreaker` を返さない。`reportedAt` が 1 分より古ければ、auth が止まっているか Valkey に書けていない。

### 7.2 `GET /sources`

取得先の登録簿をファイルの順に返す。クライアント証明書の主体名・期限・フィンガープリント（aka-vecgen の AV クライアントのフィンガープリントと突き合わせられる）、サーバーの CA の主体名と期限、加入者の数、遮断器の状態を出す。鍵とファイルのパスは返さない。admin は登録簿のディレクトリを読み取り専用でマウントし、証明書だけを読む（段階 2-3 の G4、2-5 の K5）。aka-ossbss が一括投入で取得先 ID を選ぶために使う。

### 7.3 `GET /clients`

RADIUS クライアント（`client:{ip}` と索引 `clients`）を IP の順に全件返す。IP、名前、`trustStamp`、作った時刻と変えた時刻。共有シークレットは返さない。書き込みはノードの上のコマンド（`aka-radius client`）だけ（設計概要 §3.2 の 6）。

### 7.4 `GET /sessions`

- `tenantId`（0 以上の整数）か `imsi`（15 桁、カンマ区切りで 100 件まで）のどちらか 1 つが必須（段階 2-4 の J9）。
- 索引 `idx:sess:tenant:{id}`・`idx:sess:imsi:{imsi}`（Sorted Set。スコアは期限）を読み、期限を過ぎたものは返さない。
- セッションは Accept で作り、Accounting の Start・Interim で期限を延ばし、Stop で消す（D-10）。

### 7.5 `GET /usage`

- `date`（日本時間の `YYYY-MM-DD`）が必須（段階 2-4 の J10）。その日の索引 `usage:idx:{yyyymmdd}` から、IMSI・テナントごとの集計（認証の成功、セッションの数、時間、送受信の量）を返す。
- 集計は 8 日で消えるので、取り出せるのは 7 日前まで。aka-ossbss が毎日取り出して課金レコードの材料にする。
- 刻印のないパケットの集計はテナント `0` に入る（設計概要 §10.2）。

## 8. 監査ログ

- 書き込みの操作だけを残す。読み出し（一括取得を含む）は残さない。
- 書く先は Valkey の Stream `audit`（件数の上限 `RADIUS_AUDIT_MAX_LEN`、既定 10000。おおよその上限）と、日ごとのファイル `audit-YYYYMMDD.jsonl`（`RADIUS_AUDIT_DIR`。日本時間で区切り、前の日までを gzip で圧縮し、`RADIUS_AUDIT_RETENTION_DAYS` 日（既定 14）で消す）。形は aka-vecgen と同じ（設計概要 §13、段階 2-5 の K9）。
- 項目は `time`・`operator`・`mgmtClient`・`action`・`target`・`detail`・`traceId`（ファイルには `streamId` も）。管理API の操作は `mgmtClient` が管理クライアントの識別名、`operator` が `X-Operator-Id`。コマンドの操作は `mgmtClient` が `cli`、`operator` が `-operator`（既定 `cli`）。
- 書けなくても操作は成功のまま返し、ログに `AUDIT_WRITE_ERR`（`to` が `stream` か `file`）を出す。Stream に書けなくてもファイルには書く。書いた監査ログは、ログにも `AUDIT` として出る。

| `action` | 書く口・コマンド | `target` | `detail` |
|---|---|---|---|
| `subscriber.create` | `POST /subscribers` | IMSI | 状態・取得先・ルール |
| `subscriber.update` | `PATCH /subscribers/{imsi}` | IMSI | 指定した項目の前後の値 |
| `subscriber.state` | `PUT /subscribers/{imsi}/state` | IMSI | 状態の前後、`unchanged`、理由 |
| `subscriber.reinstate` | `POST /subscribers/{imsi}/reinstate` | IMSI | 状態の前後、理由 |
| `subscriber.delete` | `DELETE /subscribers/{imsi}` | IMSI | 消す前の状態・取得先・ルール |
| `client.create`・`client.update`・`client.delete` | コマンド `client add`・`update`・`remove` | IP | 名前と `trustStamp`（変更は前後の値）。共有シークレットは値を残さず、変えたことだけを残す |
| `admin-server-certificate.create` | コマンド `admin-cert create` | 証明書の主体名 | フィンガープリント・SAN・期限。Valkey に届かないときはファイルにだけ書く |

- `GET /audit-logs` は Stream を新しい順に返す。長い保管は aka-ossbss の側の役目で、ノードの上のファイルは 14 日。

## 9. 実装の構成

| 置き場所 | 内容 |
|---|---|
| `internal/admin/run.go` | 起動（2.3）、`SIGHUP` の読み直し、監査ログのファイルの圧縮と削除（起動のときと 1 時間ごと）、healthcheck の口、停止（処理中の要求を 10 秒まで待つ。compose の `stop_grace_period` は 20 秒） |
| `internal/admin/auth` | ピン（`Pins`）、サーバー証明書（`CertFile`）、ハンドシェイクでの照合（`Verifier`） |
| `internal/admin/api` | `net/http` のメソッドつき ServeMux の口、ProblemDetails、入力の確かめ、監査ログ |
| `internal/admin/config` | 環境変数（`RADIUS_ADMIN_*`、共通の `RADIUS_VALKEY_*` など。一覧は README） |
| `internal/subscriber`・`internal/sessions`・`internal/usage`・`internal/clients`・`internal/audit` | Valkey の読み書き（auth・acct・コマンドと共通） |

- HTTP サーバーの期限: ヘッダーの読み取り 10 秒、読み取りと書き込み 30 秒、待機 120 秒。
- Gin と resty は使わない（段階 2-3 で外した）。JSON は `encoding/json/v2`。

## 10. 試験

- 単体: `internal/admin/api`（miniredis で口ごとの成功と誤り、状態の規則、ページ送り、監査ログ）、`internal/admin/auth`（ピンのファイルの形、照合、読み直し）。
- CI の「README の手順の通し確認」: compose の admin に mTLS でつなぎ、`/status`・`/clients`・`/sources`・加入者の登録、登録していない証明書の拒否、監査ログのファイルの `action` を確かめる。
- aka-provisioner との契約の試験は、aka-provisioner を作り直す MVP の段階 3-1 で行う。

## 改版履歴

| 版数 | 日付 | 内容 |
|------|------|------|
| r1 | 2026-10-07 | 初版作成。Admin TUI の加入者・RADIUSクライアント・認可ポリシーの CRUD を REST API として提供する provisioning-api の設計（位置づけ、リソースモデル、aka-only-server の管理API に揃えた作法、秘密の値の読み出しと監査、mTLS 認証、ログ・監査、共通ライブラリへの移動、設定、テスト方針、将来拡張）。拡張案 X-01 を置き換える |
| r2 | 2026-10-07 | 共通ライブラリへの移動（§7.2）の実装の反映: §7.2 を実装済みとし、`internal/model` の `pkg/model` への統合、ポリシーの変更も原子的にしたこと、`SubscriberStore.Patch` / `PolicyStore.Put` / `PolicyStore.Count` の追加、センチネルエラー、E-03 の依存ルールの例外を追記。§2.3 の注記を過去形に、§11 の手順2を実装済みに |
| r3 | 2026-10-08 | provisioning-api の実装の反映: ステータスを実装済みに。§3.2 に IP アドレスの表記の規則（先頭の 0 を不可）、§3.3 に `vlanId` は数字だけ・正規化・`rules` の検証エラーの区分、§4.1 に PATCH の `application/json`・本文の上限・要求の解釈（未知の項目等は `INVALID_MSG_FORMAT`）・トレースIDの形式・ページングの実装（SCAN）・404/405、§4.2 に書き込み前の読み出しと PATCH の応答、§4.3 に `cause` の優先順・`X-Operator-Id`・500 の扱い、§5 に有効期間外の拒否と実装（`RequestClientCert` + `VerifyConnection`、`GetConfigForClient`）、§6.1 をログの表に（`PROV_REQUEST_ERR`、`PROV_CLIENT_REJECTED`、`src_ip` がゲートウェイIPになる注記と O-05 §11.6 への参照）、§6.2 に `trace_id`・`msg`・`details` の形式・ログレベルによらない出力、§6.3 を反映済みに、§7.1 を実装の構成に（`pkg/masterdata` の `ListPage`・`ClientStore.Patch` の追加、Dockerfile・Makefile・CI）、§8.1.1 に `GIN_MODE`、§8.2 にヘルスチェック（`pgrep -f`）・終了条件・鍵のパーミッション、§9 に実施結果、§11 の手順3を実装済みに |
| r4 | 2026-10-08 | 同じホストの BFF（別の compose。web-gui-for-eapaka-radius）から接続するための共有ネットワークの追加: §2.2 の公開範囲を、同じホストの BFF は共有ネットワーク（既定名 `eapaka-prov`）経由で `https://provisioning-api:9444` に接続する形に改め（コンテナからはホストの 127.0.0.1 に届かないため）、注記を追加。§6.1 の `src_ip` の注記に共有ネットワーク経由では BFF のコンテナの IP になることを追記。§8.1.2 に `PROVISIONING_SHARED_NETWORK`、§8.2 に共有ネットワークの定義・起動と停止の順序、サーバー証明書の SAN（`DNS:provisioning-api`）を追加。いずれも simwifi で確認 |
| r5 | 2026-10-08 | RADIUSクライアントにサーバー採番の ID を導入（API 0.2.0。provisioning-api のバージョン 0.2.0）: §3 の識別子を ID に、§3.2 に `id` と、識別子の設計（`client:{IP}` の Hash の `id`、索引 `idx:client:{ID}`、カウンター `seq:client`、Lua による作成・IP の変更・削除、起動時の採番 `EnsureIDs`、`?ip=` による検索、ID に改めた理由）、PATCH での IP の変更（キーの付け替え、409）を追記。§3.4・§4.2 のパスを `/clients/{clientId}` に、§4.3 の `CLIENT_ALREADY_EXISTS` の条件に PATCH を追加。§6.1 に `assigned client ids`、§6.2 に `target_id` と details の `ip`。§10 の IPv6 に注記。simwifi で、変更前の版で登録したクライアントへの採番、IP の変更の認証への反映、Admin TUI の ID 表示を確認 |
| r6 | 2026-10-09 | 監査ログとセッションの参照を追加（API 0.3.0。provisioning-api のバージョン 0.3.0）: §1.1・§1.4 の対象に監査ログの参照とセッションの参照を加え、一括操作（CSV）は Admin TUI だけで行い本APIでは扱わないことにした。§3.6（監査ログ。Valkey の Stream `audit:prov` への保存、上限 `PROVISIONING_API_AUDIT_MAX`、保存の失敗の扱い、aka-only-server に揃えた項目と `action` の命名）と §3.7（セッション。読み取りだけ、新しい順・`limit`・`total`、`?imsi=` と索引を掃除しないこと）を新設。§4.1 に一覧の作法、§4.2 に `GET /audit-logs`・`GET /sessions`、§4.4 に `sessionCount`、§6.1 に `audit_max` と `PROV_AUDIT_STORE_ERR`、§6.2 に Stream への保存、§7.1 に `SessionStore` と `audit` の Stream、§7.2 にセッションの読み出しの `pkg/masterdata` への移動、§8.1 に `PROVISIONING_API_AUDIT_MAX`、§9 に実施結果、§10・§11 を更新 |
| r7 | 2026-10-10 | 加入者の停止・再開を追加（API 0.4.0。provisioning-api のバージョン 0.4.0）: §1.1 の責務に追加。§3.3 に `status`（応答だけ。`active` / `suspended`）と、専用の `PUT /policies/{imsi}/status`（404 `POLICY_NOT_FOUND`、200 で認可ポリシー全体、同じ状態への変更は書き込まず監査ログなし、検証、`PolicyStore.SetStatus`、次の認証から効く）、置き換え（PUT）では `status` を変えないこと・本文の `status` は `INVALID_MSG_FORMAT`、不正値はそのまま返すことを追加。§4.2 のエンドポイント一覧、§6.2 の監査ログ（`operation` の `suspend` / `resume`、`msg`、`action`、`details`）、§7.1（`version`、共通ライブラリの追加）、§9 の実施結果、§10（停止した加入者のセッションは切らない、停止中の一覧・件数）、§11 を更新 |
| r8 | 2026-10-12 | PoC セット（eapaka-pocset）の品質改善のステップ 4（ビルドの版の埋め込み、停止の猶予、`VALKEY_PASSWORD` が空なら compose が断ること）の反映: §6.1 の起動ログ `starting provisioning-api` に `build` を追加。§7.1 に `build`（`main.build`。Dockerfile の `ARG VERSION=dev` と `-X main.build=${VERSION}`、compose の `POC_VERSION`）と API の版の `version` との違いを追記し、§4.4 の `version` に注記。§8.1.2 に `POC_VERSION` を追加し、`VALKEY_PASSWORD` が空なら compose が断ることを追記。§8.2 に build.args と `stop_grace_period: 20s`（停止の上限 10秒が Docker の既定の猶予と同じだったため）を追加。provisioning-api のバージョン（API 0.4.0）は変えていない |
| r9 | 2026-10-12 | PoC セット（eapaka-pocset）の品質改善のステップ 6（文書の整理）の反映: §9 のテスト方針の単体・結合の記録で、T-02・T-03 の参照から版数を外した（4 か所。今の版はドキュメント一覧で分かるため、参照に版を書かない） |
| r10 | 2026-10-29 | MVP の段階 2-6 で改版（設計概要 §17.2）: 番号を残して「管理API 詳細設計書」に作り直した（Provisioning API 0.4.0 の後継の管理API 2.0.0、`/admin/v2`）。口の項目と形は OpenAPI（`docs/openapi/admin-api.yaml`）を正にして写さず、方針、認証（mTLS とピンのファイル、`SIGHUP`、`ADMIN_CLIENT_REJECTED`）、ヘッダー、エラーと `cause`、状態の規則、ページ送り、読み出しの口の作り、監査ログの `action`、実装の構成と試験を書いた |
