# aka-radius 設計概要（下書き）

- 作成日: 2026-10-25
- 状態: 下書き。§18 の確認事項は 2026-10-26 にすべて推奨案で合意した（§3.2 の決定事項 1〜13）。
- 置き場所: 2026-10-27（MVP セットの段階 1）に、aka-mvpset の `docs/nodes/aka-radius.md` から本リポジトリの `docs/design-overview.md` に移した。本PoC の D-文書の体系は履歴ごと引き継ぎ、MVP の変更は本書にまとめる（段階 0 の進め方 S4。§17.2）。
- 対象: EAP-AKA / EAP-AKA' の RADIUS サーバー aka-radius（もとは eapaka-radius-server-poc。以下「本PoC」）。
- 関連:
  - aka-mvpset の[要件と構成の概要](https://github.com/oyaguma3/aka-mvpset/blob/main/docs/requirements-overview.md)（以下「概要」）: §5（配置）、§6.2〜§6.4（加入者のデータ・状態・台帳）、§8（テナントの刻印と認可）、§13（ログ）、§17.1（改修）、§19 の 5（未決。§19 にあった 3 と 4 は本書の決定事項 2・10 で決めた）
  - 認証ベクターAPI の契約（以下「契約」）: [av-api-contract.md](https://github.com/oyaguma3/aka-mvpset/blob/main/docs/av-api-contract.md)。とくに §16.2（aka-radius の改修 R1〜R7）
  - 課金レコード: [charging-records.md](https://github.com/oyaguma3/aka-mvpset/blob/main/docs/charging-records.md) §5.2・§8.1（利用の集計）
  - aka-vecgen の設計概要: aka-vecgen の [`docs/design-overview.md`](https://github.com/oyaguma3/aka-vecgen/blob/main/docs/design-overview.md)（状態の値と口、管理クライアントのピン、監査ログの書き出しをそろえる）
  - 本PoC: D-01（設計仕様書）、D-02（Valkey のデータ）、D-04（ログ）、D-08（インフラと運用）、D-09（auth-server）、D-10（acct-server）、D-12（vector-gateway）、D-13（Provisioning API 0.4.0）

## 1. 位置づけ

```
[エッジ（radsecproxy）] ──RADIUS/UDP（サービス用の WireGuard）──> auth（1812）・acct（1813）
                                                                  ────────── aka-radius ────────── Valkey
                                                          auth ──mTLS──> 取得先（aka-vecgen、他の実装）
[aka-provisioner] ──mTLS（サービス用の WireGuard）────────> 管理API（9444）
[運用者] ──SSH（運用用の WireGuard と踏み台）──> コマンド（RADIUS クライアント、取得先の登録簿）
```

- aka-radius は鍵（Ki・OPc）を持たない。認証のたびに、IMSI ごとに決めた取得先から契約に従って 1 ベクターを取る（概要の決定事項 5、契約の決定事項 3）。
- IMSI ごとに、状態・取得先 ID・認可ポリシーを持つ（概要 §6.2）。テナントへの所属は持たない（aka-ossbss の台帳にある）。
- テナントはエッジが付けた刻印（VSA）で判定する（概要 §8）。RADIUS クライアントはエッジ（のトンネルのアドレス）だけ。
- 課金レコードのために、IMSI・テナント・日ごとの利用の集計を作る（課金レコード §5.2）。
- 管理API の呼び出し側は aka-provisioner だけで、全権として扱う。

## 2. 範囲

| 区分 | 内容 |
|---|---|
| 対象 | EAP-AKA / EAP-AKA' のフル認証、認証ベクターの取得（取得先の登録簿、取得先ごとの遮断器）、テナントの刻印による認可、Access-Accept の属性（MSK、VLAN、Session-Timeout、Class）、Accounting とセッション、重複応答のキャッシュ、利用の集計、管理API、監査ログ |
| 対象外 | 鍵と SQN（aka-vecgen などの取得先の役目）、Admin-TUI と CSV の一括投入（一括投入は aka-ossbss のジョブが 1 件ずつの管理API を呼ぶ。概要 §10）、仮名と高速再認証（概要 §2）、EAP-SIM、CoA / Disconnect、冗長化、IPv6 の RADIUS クライアント |

## 3. 決定事項

### 3.1 概要・契約・課金レコードで決めたこと

| # | 内容 | 出どころ |
|---|---|---|
| 1 | 鍵を持たない（vector-api を廃止）。Admin-TUI を廃止し、管理API で運用する（一部はコマンド）。Gin を外す。aka-vecgen 以外の取得先も並べて使える | 概要の決定事項 5 |
| 2 | 状態は Active / Inactive / Terminated（aka-vecgen と同じ規則。独立に管理） | 決定事項 6・7、概要 §6.3 |
| 3 | 取得先は IMSI ごとに必須。PLMN ごとの既定値は持たない。取得先とのつなぎ方は mTLS に限る | 決定事項 17 |
| 4 | テナントは VSA（Vendor-Id は環境変数で既定 32473、Vendor-Type 1、値はテナントの ID の 10 進の文字列）で判定する。「刻印を信頼する」の印のある RADIUS クライアントの刻印だけを使い、2 つ以上なら拒否する。テナントの条件があるルールで刻印がなければ拒否する。NAS-Identifier の前方一致は使わない | 決定事項 11・29、概要 §8 |
| 5 | 認可ポリシーはルールの配列。ルールはテナント（必須）、NAS-Identifier（`*` 可）、SSID、VLAN、Session-Timeout を持ち、テナント・NAS-Identifier・SSID がすべて一致した最初のルールを採る | 決定事項 12、概要 §6.2・§8.2 |
| 6 | セッションに刻印のテナントの ID を残す。重複応答のキャッシュを持つ。一覧の索引、一括取得、セッションの複数 IMSI の取得を持つ | 概要 §6.2・§6.4・§17.1 |
| 7 | ログの IMSI のマスクの既定をオフにする | 決定事項 26 |
| 8 | EAP-AKA' では `EAP_AKA_PRIME` で CK'・IK' を受け取り、自分では導出しない。`anId` を送る。応答の値の長さを確かめる。結果の分類は契約 §9.3。再同期の回数に上限を設ける。取得先ごとのタイムアウトは既定 3 秒で、再試行しない | 契約の決定事項 3・4・6・9・10、§16.2 |
| 9 | 利用の集計（IMSI・テナント・日ごと）を Valkey に 7 日持ち、管理API で取り出せるようにする | 課金レコードの決定事項 20、§5.2 |
| 10 | 状態の値は小文字の `active` / `inactive` / `terminated`、変更は `PUT …/state`、戻しは `POST …/reinstate`（理由が必須）。管理クライアントのピンはファイルにして `SIGHUP` で読み直す。監査ログは Stream と日ごとの JSONL | aka-vecgen の決定事項 2・7・9 |

### 3.2 本書で決めたこと

| # | 内容 | 決定日 |
|---|---|---|
| 1 | 1 つのモジュール `github.com/oyaguma3/aka-radius` にまとめ、コマンド `aka-radius` のサブコマンド `auth`・`acct`・`admin` を別々のコンテナで動かす（同じイメージ）。環境変数の接頭辞は `RADIUS_`（§4。確認事項 C1） | 2026-10-26 |
| 2 | vector-gateway は auth に取り込み、取得先ごとに接続と遮断器を持つ（§7.2。C2。概要 §19 で未決だったもの） | 2026-10-26 |
| 3 | 取得先の登録簿はファイルにして `SIGHUP` で読み直し、管理API は読み出しだけにする。使用中の取得先を消す読み直しは受け付けない。EAP-AKA' のネットワーク名は全体で 1 つ（`RADIUS_EAP_AKA_PRIME_NETWORK_NAME`、既定 `WLAN`）。方式は Identity の先頭の文字で決め、NAK による切り替えは扱わない（§7.1・§7.2。C3） | 2026-10-26 |
| 4 | 状態・刻印・認可ポリシーは、ベクターを取る前（最初の要求）に確かめる。Accept の前に状態だけもう一度確かめる（§8.1。C4） | 2026-10-26 |
| 5 | ルールに一致しないときは拒否する。本PoC の `default`（allow / deny）はなくす（§6.2・§8.2。C5） | 2026-10-26 |
| 6 | RADIUS クライアントはコマンドで管理し、管理API は読み出しだけにする（共有シークレットは返さない）（§11.2・§12。C6） | 2026-10-26 |
| 7 | 重複応答のキャッシュは auth と acct のプロセスのメモリーに持ち、30 秒保つ（§9。C7） | 2026-10-26 |
| 8 | IMSI は RADIUS の User-Name ではなく、EAP の Identity（EAP-Response/Identity と AT_IDENTITY）から取る（§8.1。C8） | 2026-10-26 |
| 9 | 再同期は 1 回の EAP の会話で 1 回まで（§7.3。C9。契約の決定事項 9） | 2026-10-26 |
| 10 | ログは Docker の `fluentd` のログドライバーと Fluent Bit でアプリごとのファイルに書き、日ごとに圧縮してノードで 2 週間持つ。データ面の aka-vecgen も同じ Fluent Bit に送る。監査ログは Stream と日ごとの JSONL で、Fluent Bit を通さない（§13。C10。概要 §19 で未決だったもの） | 2026-10-26 |
| 11 | 利用の集計は、Accounting の累計をセッションに持ち、増分の計算と足し込みを 1 つの Lua で行う。Gigawords と Event-Timestamp を扱う。刻印のないパケットはテナント `0` として集計する（§10.2。C11） | 2026-10-26 |
| 12 | セッションの IMSI とテナントの索引は Sorted Set（スコアは期限）にし、読むときと 1 時間ごとに期限切れを消す（§10.1。C12） | 2026-10-26 |
| 13 | 各コンテナのループバックに healthcheck の口（Valkey に届くか）を置く（§5。C13） | 2026-10-26 |

## 4. 技術方針

- 言語は Go 1.27.3。Go Workspace の 7 つのモジュールを、1 つのモジュール `github.com/oyaguma3/aka-radius` にまとめる。コマンドは 1 つ（`aka-radius`）で、サブコマンド `auth`・`acct`・`admin` をそれぞれ別のコンテナで動かす（同じイメージ。§18 の C1）。
- 環境変数の接頭辞は `RADIUS_`（§18 の C1）。
- 外部依存: `layeh.com/radius`、`github.com/oyaguma3/go-eapaka`、`github.com/redis/go-redis/v9`、`github.com/sony/gobreaker`、`github.com/google/uuid`。Gin（管理API・vector-gateway・vector-api・`pkg/httputil`）、resty（`net/http` に置き換える）、tview・tcell（Admin-TUI）、`wmnsk/milenage`（vector-api）は外す。
- HTTP は `net/http` のメソッドつき ServeMux、JSON は `encoding/json/v2`、ログは `log/slog`。
- auth と acct の中で重なっている部分（RADIUS の受信と検証、共有シークレットの解決、Valkey の接続）を 1 つのパッケージにまとめる。
- 要求ごとの期限（既定 4 秒。取得先のタイムアウト 3 秒より長く、エッジの再送の間隔 5 秒より短く）と、同時に処理する要求の上限（既定 256）を設ける。本PoC はどちらも持たず、取得先が遅いと goroutine が積み上がる。
- ブランチの流儀（feature → develop（squash）→ main）と D-文書の改版の規則は、本PoC のまま引き継ぐ。

## 5. 待ち受け

| 口 | ポート | 待ち受けるアドレス | 認証 |
|---|---|---|---|
| 認証（auth） | 1812/udp | サービス用の WireGuard のアドレス（エッジのトンネルの IP からだけ受ける） | 共有シークレット、Message-Authenticator 必須 |
| Accounting（acct） | 1813/udp | 同上 | 共有シークレット、Request Authenticator |
| 管理API（admin） | 9444/tcp | サービス用の WireGuard のアドレス（aka-provisioner から） | mTLS（管理クライアントのピン） |
| healthcheck | 各コンテナのループバック（`127.0.0.1:9090`、HTTP の `GET /healthz`） | コンテナの中だけ | なし（Valkey に届くかを返す。コマンド `aka-radius healthcheck` が叩く。段階 2-5 の K10） |

- 予備の共有シークレット（本PoC の `RADIUS_SECRET`）は既定で空にし、登録していない送信元のパケットは捨てる（概要 §5.3）。
- 取得先へは、auth のコンテナから mTLS でつなぐ。aka-vecgen とは、データ面のノードの中の共有の Docker のネットワークを使う。
- healthcheck の口を足すのは、本PoC が UDP と mTLS のために `pgrep` でしか確かめられなかったため。

## 6. データ（Valkey）

### 6.1 キーの一覧

| キー | 型 | TTL | 内容 | 本PoC からの変更 |
|---|---|---|---|---|
| `sub:{imsi}` | Hash | なし | `state`、`state_changed_at`、`source_id`、`rules`（JSON）、`created_at`、`updated_at` | もとの `sub:`（鍵と SQN）と `policy:` を置き換える |
| `subs` | Sorted Set | なし | IMSI の索引（辞書順。一覧と前方一致） | 新しく足す（本PoC は SCAN） |
| `cnt:src:{id}` | String | なし | 取得先ごとの加入者の数（登録・変更・削除の Lua で増減） | 新しく足す |
| `client:{ip}` | Hash | なし | RADIUS クライアント（`name`、`secret`、`trust_stamp`（`0`/`1`）、`created_at`、`updated_at`） | `trust_stamp` を足し、`vendor` を外す |
| `clients` | Set | なし | RADIUS クライアントの IP の索引（一覧と数。段階 2-5 の K2） | 本PoC の `idx:client:{id}`・`seq:client`（ID の採番）を置き換える |
| `stat:sources` | String（JSON） | なし | auth が報告する取得先ごとの遮断器の状態（`state`・`since`）と報告の時刻。状態が変わったときと 30 秒ごとに書く（`/status`・`/sources`。段階 2-5 の K4） | 新しく足す |
| `eap:{traceId}` | Hash | 60 秒 | EAP の会話の状態。本PoC の項目に、`tenant_id`、採ったルールの番号、`source_id`、`identity` を足す | 項目を足す |
| `sess:{uuid}` | Hash | 24 時間（Start・Interim で延ばす） | セッション。`imsi`、`tenant_id`、`nas_ip`（エッジ）、`nas_identifier`、`calling_station_id`、`client_ip`、`acct_id`、`start_time`、`last_update`、Accounting の最後の累計（`seconds`・`input_octets`・`output_octets`。表示用） | `tenant_id` と累計を足す |
| `idx:sess:imsi:{imsi}`、`idx:sess:tenant:{id}` | Sorted Set | なし | セッションの索引。スコアはセッションの期限の時刻で、読むときと定期の処理で期限を過ぎたものを消す | 本PoC の `idx:user:`（TTL も掃除もない Set）を置き換える |
| `acct:seen:{送信元の IP}:{NAS-Identifier か NAS-IP-Address}:{Acct-Session-Id}` | Hash | 24 時間（受けるたびに延ばす） | Accounting の重複の判定（最後の種類）と前回の累計（時間・送受信の量）、IMSI、テナント（段階 2-4 の J2・J3） | NAS ごとに分け、累計を持たせる |
| `stat:sessions` | Hash | なし | acct が 1 時間ごとの掃除で数えたセッションの数と時刻（`/status`） | 新しく足す |
| `usage:{yyyymmdd}:{tenantId}:{imsi}` | Hash | 8 日 | 利用の集計（§10） | 新しく足す |
| `usage:idx:{yyyymmdd}` | Sorted Set | 8 日 | その日の集計のキーの索引（取り出しのページ送り） | 新しく足す |
| `audit` | Stream | 件数の上限 | 管理API とコマンドの監査ログ | `audit:prov` を改める |

- 鍵・SQN・vector-api のための `sub:` の項目はなくなる。
- 取得先の登録簿は Valkey に置かず、ファイルに置く（§7.1）。
- Valkey は AOF（`everysec`。本PoC のまま）、`noeviction`、`requirepass`。`maxmemory` は 1 GB に上げる（加入者 20 万件で約 100 MB、利用の集計 7 日分で数百 MB の見込み。規模の試験で測る）。

### 6.2 加入者

| 項目 | 内容 |
|---|---|
| `state` | `active` / `inactive` / `terminated`。規則は aka-vecgen の設計概要 §7 と同じ（物理削除は `active` ではできない、`terminated` では変更できない、戻しは理由が必須） |
| `source_id` | 取得先の登録簿の ID（必須）。登録・変更のときに登録簿にあるかを確かめる |
| `rules` | 認可ポリシーのルールの配列（1〜32 件。§8.2） |

- 本PoC の `policy:` の `default`（ルールに一致しないときの allow / deny）はなくし、一致しなければ拒否する（§18 の C5）。

## 7. 認証ベクターの取得

### 7.1 取得先の登録簿

- ファイル（`RADIUS_SOURCES_FILE`。JSON）に置き、`SIGHUP` で読み直す（§18 の C3）。証明書と鍵はファイル（Docker の secrets）で渡し、登録簿からはパスで指す。

| 項目 | 内容 |
|---|---|
| `id` | 取得先の ID（英小文字・数字・`-`。例 `vecgen`）。加入者の `source_id` が指す。変えない |
| `name` | 表示名 |
| `baseUrl` | `https://<ホスト>:<ポート>/nudm-ueau/v1`（契約 §5） |
| `clientCertFile`、`clientKeyFile` | aka-radius のクライアント証明書と鍵 |
| `serverCaFile` | 取得先のサーバー証明書を確かめる CA（aka-vecgen では自己署名のサーバー証明書そのもの） |
| `timeout` | 要求 1 回のタイムアウト（既定 `3s`） |

- 読み直しのとき、形が違う・ファイルが読めない場合は前の登録簿のまま続け、ログに出す。加入者が指している取得先（`cnt:src:{id}` が 0 でない）を消した登録簿も受け付けず、ログに出す。
- 管理API は登録簿を読み出すだけ（aka-ossbss が一括投入で取得先 ID を選ぶため。§11）。

### 7.2 取得の作り

- vector-gateway は auth に取り込む（§18 の C2）。auth の中の「取得先のクライアント」が、取得先ごとに HTTP の接続（keep-alive）と遮断器（gobreaker。連続 5 回の失敗で開き、30 秒で半開き）を持つ。
- 1 回の認証で 1 ベクターを取る。再同期のときにもう 1 回取る。先取りしない（契約 §7）。
- EAP-AKA は `eap-aka`、EAP-AKA' は `eap-aka-prime` と `anId` で要求する。`anId` は `RADIUS_EAP_AKA_PRIME_NETWORK_NAME`（既定 `WLAN`）で、同じ値を AT_KDF_INPUT に使う（契約 §6.4。§18 の C3）。
- 応答の要素の数・`avType`・値の長さを確かめる（契約 §6.3）。
- `X-Trace-ID` には認証のトレースID（State の値）を入れる。
- 結果の扱いは契約 §9.3 のとおり。どの拒否でも EAP-Failure の Access-Reject を返す（本PoC と同じ。応えずに再送を待つことはしない）。一時的な誤りと契約違反は遮断器に数える。

### 7.3 再同期

- 端末の Synchronization-Failure を受けたら、直前の Challenge の RAND と AUTS で取り直し、新しい Challenge を送る。
- 再同期は 1 回の EAP の会話で 1 回まで（本PoC は 32 回。§18 の C9）。2 回目の Synchronization-Failure は拒否する。

## 8. 認証の流れと認可

### 8.1 流れ

1. Access-Request を受け、送信元の共有シークレットで検証する（Message-Authenticator は必須。なければ捨てる）。重複応答のキャッシュ（§9）に当たれば、前の応答をそのまま返す。
2. State がなければ新しい会話。EAP の Identity（EAP-Response/Identity、または AKA-Identity の AT_IDENTITY）から IMSI と方式（先頭 `0` は EAP-AKA、`6` は EAP-AKA'）を決める（§18 の C8）。仮名・高速再認証の ID なら、本PoC と同じく永続 ID を求める。
3. **ベクターを取る前に**、次を確かめる（§18 の C4）。どれかに当たれば、ベクターを取らずに EAP-Failure の Access-Reject を返す。
   - `sub:{imsi}` がある（未登録なら `AUTH_SUBSCRIBER_NOT_FOUND`）。
   - `state` が `active`（`AUTH_SUBSCRIBER_INACTIVE` / `AUTH_SUBSCRIBER_TERMINATED`）。
   - 刻印を読み（§8.3）、認可ポリシーで一致するルールがある（`AUTH_POLICY_DENIED`、`AUTH_STAMP_MISSING`、`AUTH_STAMP_MULTIPLE`）。
4. 取得先から 1 ベクターを取り、Challenge を送る。`eap:` に、テナントの ID と採ったルールの番号を残す。
5. 端末の応答の AT_MAC と AT_RES を確かめる。
6. Accept の前に `state` をもう一度読み、`active` でなくなっていれば拒否する（3 と 6 の間の数秒に止めた場合のため）。
7. セッションを作り、利用の集計に認証の成功を 1 足し（§10）、Access-Accept を返す。

- 本PoC は認可ポリシーを Challenge の検証の後に評価していたので、停止中・未登録の IMSI でもベクターを取り、取得先の SQN を進めていた。3 で先に確かめると、拒否する IMSI では取得先を呼ばない。

### 8.2 ルール

| 項目 | 内容 |
|---|---|
| `tenantId` | 必須。刻印のテナントの ID と一致すること |
| `nasId` | `*` か完全一致（大文字と小文字を区別）。テナントの中の NAS-Identifier |
| `ssids` | `["*"]` か SSID の配列（大文字と小文字を区別しない）。SSID は Called-Station-Id の最初の `:` より後ろ（本PoC と同じ） |
| `vlanId` | 任意（1〜4094）。Access-Accept の Tunnel-Type・Tunnel-Medium-Type・Tunnel-Private-Group-Id |
| `sessionTimeout` | 任意（秒）。Access-Accept の Session-Timeout |

- 配列の順に評価し、テナント・NAS-Identifier・SSID がすべて一致した最初のルールを採る。一致しなければ拒否する。
- ルールのテナントを、台帳の所属のテナントに限るかは aka-ossbss の設計概要で決める（aka-radius はルールのとおりに評価する）。

### 8.3 刻印

- Vendor-Specific（26）のうち、Vendor-Id が `RADIUS_STAMP_VENDOR_ID`（既定 32473）で Vendor-Type が `RADIUS_STAMP_VENDOR_TYPE`（既定 1）のものを読む。起動のときに両方の値をログに出す（概要 §8.1）。
- 送信元の RADIUS クライアントの `trust_stamp` が `1` のときだけ使う。`0` のクライアントの刻印は、ないものとして扱う。
- 2 つ以上あれば拒否する。値が 10 進の数字でなければ拒否する。
- Accounting でも同じように読み、セッションと利用の集計のテナントにする。

### 8.4 Access-Accept

本PoC と同じ: EAP-Success、MS-MPPE-Recv-Key / Send-Key（MSK）、Class（セッションの UUID）、ルールの VLAN と Session-Timeout、Proxy-State、Message-Authenticator。使われていない `policy/avp.go` は消す。

## 9. 重複応答のキャッシュ

- エッジ（radsecproxy）は、応答が 5 秒のうちに届かなければ同じ要求を送り直す。本PoC には応答のキャッシュがないので、送り直された要求を新しい要求として処理してしまう。その結果、Accept の後に最後の要求が届くと Reject になり、Identity の要求が届くとベクターをもう 1 つ取って SQN を進めていた。
- auth と acct のそれぞれが、プロセスのメモリーに応答のキャッシュを持つ（§18 の C7）。

| 項目 | 内容 |
|---|---|
| キー | 送信元の IP とポート、Identifier、Request Authenticator（RFC 5080 §2.2.2） |
| 値 | 送った応答のバイト列 |
| 持つ時間 | 30 秒（エッジの再送の窓 5 秒 × 2 回より長く） |
| 処理中の重複 | 捨てる（最初の要求の応答がキャッシュに入れば、次の再送に返る） |
| 大きさ | ピーク 50 TPS × 1 回の認証あたり 2〜3 往復 × 30 秒で、約 5,000 件・数 MB |

- プロセスを再起動するとキャッシュは消える（その間の再送は新しい要求として扱われる。害は SQN が 1 つ進むことと、Accept の後の再送が拒否になること）。
- Accounting の重複は、このキャッシュに加えて、`acct:seen` と累計からの増分の計算（§10.2）で数え直しを防ぐ。

## 10. Accounting と利用の集計

### 10.1 セッション

- Accept のときに `sess:{uuid}` を作り、Class に UUID を入れる。Accounting-Start・Interim では Class でセッションを引き、項目と期限を更新する。Stop で消す。
- 索引 `idx:sess:imsi:` と `idx:sess:tenant:` のスコアは期限の時刻にし、更新のたびに延ばす。読むときと 1 時間ごとの処理で、期限を過ぎたものを消す（本PoC は Stop が届かなかった分の索引が残り続けた）。
- Class がない・セッションが消えている Accounting は、User-Name の IMSI と刻印のテナントで利用の集計だけを行う（セッションは作らない。本PoC と同じく `ACCT_SESSION_NOT_FOUND` をログに出す）。
- セッションの開始の時刻の索引は、20,000 件で一覧の時間を測ってから決める（概要 §19 の 5）。

### 10.2 利用の集計（課金レコード §5.2）

| 項目 | 足し込む時 | 値 |
|---|---|---|
| `auth_ok` | auth の Access-Accept | 1 |
| `sessions` | Accounting-Start（重複でないもの） | 1 |
| `seconds` | Interim・Stop | Acct-Session-Time の前回の累計からの増分 |
| `in_octets`、`out_octets` | Interim・Stop | Acct-Input/Output-Octets と Gigawords（× 2^32）を合わせた値の、前回の累計からの増分 |

- 前回の累計は重複の判定のキー（`acct:seen:…`）に持ち、重複の判定・増分の計算・累計の更新・集計への足し込み・セッションと索引の更新を 1 つの Lua で原子的に行う（段階 2-4 の J2 で、セッションに持つ案から改めた。Class がない・セッションが消えている Accounting でも増分を計算できるように）。同じ値の再送は増分が 0 になるので、数え直しにならない。累計が減ったとき（NAS の作り直しなど）は、その値をそのまま増分とみなし、ログに出す（`USAGE_COUNTER_RESET`）。
- 日は Event-Timestamp があればその時刻、なければ受け取った時刻の、日本時間の日付。キーは `usage:{yyyymmdd}:{tenantId}:{imsi}` で、8 日で消える（7 日分を取り出せるように 1 日余裕を持たせる）。
- 刻印のないパケットはテナントの ID を `0` として集計し、ログに出す（エッジを通らないパケットは来ない前提だが、取りこぼしを見えるようにする）。
- 本PoC の acct が持たなかった Gigawords と Event-Timestamp を扱うようにする。

### 10.3 Accounting の重複の判定

- `acct:seen:…`（§6.1）に最後の種類（`start` / `interim` / `stop`）を持ち、判定と更新を §10.2 の 1 つの Lua で行う（本PoC は GET と SET の 2 回で、原子的でなかった）。
- 判定の規則（Start の重複、Stop の後の Interim など）は本PoC の D-10 のまま。

## 11. 管理API（9444、mTLS）

項目の詳細は OpenAPI（`docs/openapi/admin-api.yaml`、2.0.0）で、段階 2 の実装のときに書く。本PoC の Provisioning API 0.4.0（D-13）の後継で、互換は持たない。

### 11.1 認証・ヘッダー・エラー

- mTLS 必須。管理クライアントのピンはファイル（`RADIUS_ADMIN_CLIENTS_FILE`）で、`SIGHUP` で読み直す。同じ識別名に新旧 2 つのフィンガープリントを書ける（aka-vecgen の決定事項 7）。
- `X-Operator-Id`、`X-Trace-ID` は本PoC と同じ。エラーは ProblemDetails で `cause` を入れる。`cause` は本PoC の Provisioning API 0.4.0 の名前を残し（`INVALID_MSG_FORMAT`・`INVALID_QUERY_PARAM`・`MANDATORY_IE_MISSING`・`MANDATORY_IE_INCORRECT`・`OPTIONAL_IE_INCORRECT`・`USER_NOT_FOUND`・`SUBSCRIBER_ALREADY_EXISTS`・`SYSTEM_FAILURE`）、MVP で足す誤りは aka-vecgen と同じ名前（`SUBSCRIBER_ACTIVE`・`SUBSCRIBER_TERMINATED`・`STATE_TRANSITION_NOT_ALLOWED`）、aka-radius だけにある `UNKNOWN_SOURCE` を足す（段階 2-3 の G11。前の版の `VALIDATION_ERROR`・`SUBSCRIBER_NOT_FOUND`・`SUBSCRIBER_EXISTS` は、aka-vecgen 2-1 で「もとの名前を残す」と決める前に書いたもの）。
- `Idempotency-Key` は扱わない。再送の判定は aka-provisioner に任せる（aka-vecgen の決定事項 11 と同じ）。

### 11.2 口

すべて `/admin/v2` の下。

| 口 | 内容 | 本PoC からの変更 |
|---|---|---|
| `GET /status` | 版、起動日時、加入者の数、RADIUS クライアントの数、セッションの数、取得先ごとの加入者の数と遮断器の状態（auth の報告）、刻印の Vendor-Id と Vendor-Type、管理クライアントの識別名と読み直しの日時 | 取得先・刻印・管理クライアントを足す |
| `GET /subscribers` | 一覧（カーソル、1 ページ 500 件まで、IMSI の前方一致）。`total` を返す | `subs` の索引を使う（本PoC は SCAN） |
| `POST /subscribers/batch-get` | 一括取得（`imsis` 500 件まで）。`items` と `notFound` | 新しく足す |
| `POST /subscribers` | 登録（`imsi`、`state`（`active` / `inactive`）、`sourceId`、`rules`） | 加入者と認可ポリシーを 1 つにする |
| `GET /subscribers/{imsi}` | 個別の取得 | 同上 |
| `PATCH /subscribers/{imsi}` | 変更（`sourceId`、`rules`）。`terminated` では 409 | 同上 |
| `PUT /subscribers/{imsi}/state` | 状態の変更（`state`、任意の `reason`） | `/policies/{imsi}/status` を置き換える |
| `POST /subscribers/{imsi}/reinstate` | `terminated` から `inactive` に戻す（`reason` 必須） | 新しく足す |
| `DELETE /subscribers/{imsi}` | 物理削除。`active` では 409。セッションは消さない（期限で消える） | 状態の検査を足す |
| `GET /sessions` | セッションの一覧（絞り込み: `tenantId`、`imsi`（複数可、100 件まで）。カーソル） | テナントと複数 IMSI の絞り込みを足す |
| `GET /usage` | 利用の集計（`date`（日本時間の日付）必須、カーソル、1 ページ 1,000 件まで） | 新しく足す |
| `GET /clients` | RADIUS クライアントの一覧（IP の順、ページ送りなし。IP、名前、`trustStamp`、作成と更新の日時。共有シークレットは返さない） | 書き込みと `/secret` をなくす |
| `GET /sources` | 取得先の登録簿（ファイルの順。クライアント証明書の主体名・期限・フィンガープリント、サーバーの CA の主体名と期限、加入者の数、遮断器の状態。鍵とファイルのパスは返さない） | 新しく足す |
| `GET /audit-logs` | 監査ログ | — |

- 本PoC の `/subscribers/{imsi}/keys`、`/policies` 系、`/clients` の書き込み、`/clients/{id}/secret` はなくす。
- 加入者の応答の形（例）: `imsi`、`state`、`stateChangedAt`、`sourceId`、`rules`、`createdAt`、`updatedAt`。

## 12. コマンド

`docker compose exec admin aka-radius <サブコマンド>` のように、動いているコンテナの中で使う。書き込みのコマンドは監査ログに残す（管理クライアントは `cli`、操作者は `-operator`。既定 `cli`）。読むだけのコマンドは残さない。

| サブコマンド | コンテナ | 内容 |
|---|---|---|
| `client add / list / update / remove` | admin | RADIUS クライアント（エッジのトンネルの IP、名前、共有シークレット、`trust_stamp`）。エッジを足すときに、WireGuard のピアの設定と一緒に行う（§18 の C6）。`-trust-stamp` は必須。共有シークレットは標準入力の 1 行から読むか、`-generate` で作って 1 回だけ表示する（16〜128 文字）。auth・acct はパケットのたびに引くので、すぐに効く |
| `sources check` | auth（`docker compose run --rm --no-deps auth sources check`） | 取得先の登録簿のファイルの検査（形、証明書と鍵の組、証明書の期限。30 日より近ければ警告）と、各取得先への接続の確かめ（TLS でつながり、HTTP の応答が返るか。応答の状態は問わない） |
| `admin-cert [show]`・`admin-cert create` | admin | 管理API のサーバー証明書の表示（主体名・SAN・期限・フィンガープリント）と、自己署名の証明書と鍵（ECDSA P-256、10 年）の作成 |
| `fingerprint` | どれでも | 標準入力の証明書（PEM）の SHA-256 のフィンガープリントの表示（ピンのファイルに書く値） |
| `healthcheck` | どれでも | 同じコンテナのループバックの healthcheck の口に問い合わせる（compose の healthcheck） |

## 13. ログと監査ログ

- ログの経路は本PoC のまま、Docker のログドライバー `fluentd` から Fluent Bit に送り、アプリごとのファイルに書く（§18 の C10）。ホストの logrotate で日ごとに圧縮し、ノードで 2 週間持つ（本PoC は 30 世代）。監視用の PC が取りに行く（概要 §13）。データ面の aka-vecgen も同じ Fluent Bit に送る。
- `event_id` は本PoC の D-04 を引き継ぎ、次を足す・改める: `AUTH_SUBSCRIBER_NOT_FOUND`・`AUTH_SUBSCRIBER_INACTIVE`・`AUTH_SUBSCRIBER_TERMINATED`（`AUTH_SUBSCRIBER_SUSPENDED` を置き換える）、`AUTH_STAMP_MISSING`・`AUTH_STAMP_MULTIPLE`・`AUTH_STAMP_INVALID`・`AUTH_STAMP_LOOKUP_ERR`、`AUTH_SUBSCRIBER_INVALID`・`AUTH_SUBSCRIBER_LOOKUP_ERR`、`EAP_IDENTITY_MISMATCH`、`AUTH_DUPLICATE_REPLAYED`・`ACCT_DUPLICATE_REPLAYED`、`RADIUS_OVERLOAD`、`VECTOR_*`（取得先の ID つき。`VECTOR_IMSI_NOT_FOUND` をなくし `VECTOR_REJECTED` に）、`SOURCES_RELOADED`、`ADMIN_CLIENTS_RELOADED`、`USAGE_COUNTER_RESET`・`USAGE_WRITE_ERR`、`ACCT_TENANT_UNKNOWN`・`ACCT_IMSI_UNKNOWN`・`ACCT_USAGE_TOO_OLD`・`ACCT_EVENT_TIMESTAMP_FUTURE`・`ACCT_STAMP_MULTIPLE`・`ACCT_STAMP_INVALID`・`ACCT_STAMP_LOOKUP_ERR`、`SESSION_SWEPT`・`SESSION_SWEEP_ERR`。
- `RADIUS_LOG_MASK_IMSI` の既定を `false` にする（決定事項 26）。
- 監査ログは Valkey の Stream（件数の上限つき）と、日ごとの追記だけの JSONL のファイル（`RADIUS_AUDIT_DIR`）に書く（aka-vecgen の決定事項 9）。本PoC の「標準出力が正本、Stream が写し」と、Admin-TUI のリダイレクト頼みの監査ログは、この 1 系統にまとめる。ファイルの形・日本時間の区切り・前の日の gzip・14 日での削除は aka-vecgen と同じ（段階 2-5 の K9）。

## 14. 配備

- compose に auth・acct・admin・Valkey・Fluent Bit を置く。データ面のノードで aka-vecgen と並べる。
- 1812/udp・1813/udp と 9444/tcp は、サービス用の WireGuard のアドレスにだけ結び付け、nftables の `DOCKER-USER` で送信元を絞る（概要 §5.3）。
- auth は、aka-vecgen が作る共有の Docker のネットワークに参加する（本PoC の `aka-av` を vector-gateway から auth に移す）。
- 取得先のクライアント証明書と鍵、管理クライアントのピンのファイル、取得先の登録簿は Docker の secrets で渡す。読み直しは `docker compose kill -s HUP auth admin`。

## 15. 試験

| 層 | 内容 |
|---|---|
| 単体 | EAP の状態機械（AT_IDENTITY からの IMSI、再同期の上限 1）、刻印（信頼の印、2 つ以上、数字でない値）、ルールの評価、増分の計算（Gigawords、累計の減少、再送）、重複応答のキャッシュ、登録簿の読み直し（形の誤り、使用中の取得先の削除） |
| 結合（実際の Valkey、取得先の模型） | 認証の流れ（拒否する IMSI で取得先を呼ばないこと）、Accounting と利用の集計、索引の掃除、管理API（状態、一括取得、セッションの絞り込み、利用の取り出し）、遮断器 |
| 結合（aka-vecgen） | aka-vecgen を相手に、EAP-AKA と EAP-AKA' の認証と再同期（eapaka_test）。本PoC の手順書の結合試験（T-03）は、Go のコードで動く試験に置き換えて CI で走らせる |
| E2E | aka-mvpset の `scripts/e2e.sh`（エッジを通す経路、刻印、状態による拒否。概要 §16.3） |
| 規模 | 加入者 20 万件・セッション 20,000 件で、ピーク 50 TPS を 30 分（概要 §16.1）。一覧とセッションの絞り込みの時間 |

## 16. Admin-TUI の機能の行き先

| 本PoC の Admin-TUI | MVP |
|---|---|
| 加入者・認可ポリシーの登録・編集・削除・停止と再開 | aka-ossbss の画面 → aka-provisioner → 管理API |
| CSV の取り込みと書き出し（鍵とシークレットを含む） | aka-ossbss の一括投入（概要 §10）。書き出しはなくす |
| RADIUS クライアント | コマンド（§12） |
| 統計 | `GET /status`、監視 |
| セッションの一覧と検索 | aka-ossbss の画面（`GET /sessions`） |
| セッションの索引の掃除 | 自動（§10.1） |

## 17. 進め方

### 17.1 段階

| 段階 | 内容 | 終わりの確かめ |
|---|---|---|
| 1 | リポジトリ `aka-radius` を作り、本PoC の履歴ごと移す。改名だけ（module パス、コマンド、環境変数の接頭辞、Docker のネットワーク名、イメージ名、文書） | 今の CI が通る |
| 2-1 | 1 つのモジュールと 1 つのコマンドにまとめ、Admin-TUI と vector-api を外す。auth と acct の共通の部分をまとめる（Gin はこの手順では外さない。2-2 と 2-3 で口ごと消える） | 単体、ビルド |
| 2-2 | 取得先の登録簿と取得先のクライアント（vector-gateway の取り込み、契約の R1〜R7。gateway の Gin が消える） | 単体、aka-vecgen との結合、検証機での最初の通し（eapaka_test で EAP-AKA・EAP-AKA'・再同期） |
| 2-3 | 加入者（状態、取得先、ルール）、刻印、判定の順の変更、Identity の取り方、重複応答のキャッシュ、要求の期限と上限、管理API 2.0.0 の加入者の口（provisioning-api を置き換える。Gin がすべて消える） | 単体、結合。**済み（2026-10-28）**: 下の「段階 2-3 で決めたこと」 |
| 2-4 | Accounting の作り直し（セッションの索引、増分、Gigawords、利用の集計）、`/sessions` と `/usage` の口 | 単体、結合。**済み（2026-10-28）**: 下の「段階 2-4 で決めたこと」 |
| 2-5 | 管理API 2.0.0 の残り（RADIUS クライアントと取得先の読み出し）、コマンド、ピンのファイル、監査ログの書き出し、healthcheck、共有のネットワークの名前 `eapaka-prov` を決め直す | 結合。**済み（2026-10-28）**: 下の「段階 2-5 で決めたこと」 |
| 2-6 | D-文書の改版、運用ガイド、README | 検証機で全体を通す（刻印はエッジの代わりの radsecproxy で付け、Accounting は radclient で送る） |

- 手順の組み替え（2026-10-27。aka-mvpset の概要 §18.5 の H3）: データの形と、それを読み書きする口は同じ手順で変える（管理API の加入者の口を 2-3 に、`/sessions`・`/usage` を 2-4 に移した）。
- 2-1・2-2 は aka-vecgen の 2-1 の後に行う（概要 §18.5 の H1）。
- 手順ごとに feature ブランチと develop への PR（squash）を作り、develop → main は段階 2 の終わりに 1 回だけ行う。D-文書は 2-6 でまとめて改版し、それまでは本書を正とする（概要 §18.5 の H6）。
- aka-provisioner との契約の試験は、aka-provisioner を作り直す段階 3-1 で行う（概要 §18.5 の H4）。

段階 2-3 で決めたこと（2026-10-28。確認事項 G1〜G12 はすべて推奨案で合意）:

- G1: 1 つの feature ブランチと 1 つの PR にした（加入者のデータの形を変えると auth と admin が同時に変わるため）。
- G2: 本PoC の形のデータ（鍵と SQN の入った `sub:`、`policy:`）は移し替えない。`state` のない `sub:` は、ベクターを取らずに拒否する（`AUTH_SUBSCRIBER_INVALID`）。管理API では 500 になる。
- G3: `masterdata`・`model`・`validation`（と使われていない `apperr`・`httputil`）を、`internal/subscriber` に置き換えた。登録・変更・状態の変更・削除は Lua で `sub:`・`subs`・`cnt:src:{id}` を一度に書き換える（Lua の中で取得先ごとの数のキーを組み立てるので、Valkey は単一ノード）。ルールの評価（`subscriber.Match`）も同じパッケージにし、`internal/auth/policy` を消した。
- G4: admin は取得先の登録簿のディレクトリを読み取り専用でマウントし、ID と証明書の期限だけを読む（鍵は読まない。`source.ReadInfos`）。`SIGHUP` で読み直す。auth は、加入者が指している取得先（`cnt:src:{id}` が 0 でない）を消す読み直しを断る（2-2 の B3）。
- G5: RADIUS クライアントの「刻印を信頼する」の印（`client:{ip}` の `trust_stamp`）は auth が読むだけ。印を付けるのは、2-5 のコマンドができるまで Valkey に直接書く。予備の共有シークレットで受けた送信元は印なしとして扱う。
- G6: IMSI は EAP-Response/Identity の Type-Data と AKA-Identity の AT_IDENTITY から取り、User-Name は判定に使わない（違えば `EAP_IDENTITY_MISMATCH`）。鍵の導出に使う Identity は `eap:` の `identity` に残し、再同期でも使う（RFC 4187 §7）。
- G7: 要求の期限（`RADIUS_REQUEST_TIMEOUT`、既定 4 秒）と同時に処理する上限（`RADIUS_MAX_INFLIGHT`、既定 256）を、auth と acct に共通の受け口（`radiussrv.Guard`）に入れた。上限を超えた要求は応えずに捨て（`RADIUS_OVERLOAD`。1 秒に 1 回に間引く）、期限を過ぎた処理は拒否で終える。
- G8: 重複応答のキャッシュは `radiussrv.Guard` に作り、auth と acct の両方に入れた（§9 のとおり。件数の上限 50,000、超えたら古いものから消す）。処理中の重複は捨て、応えなかった要求（捨てた、パニック）は印を残さない。
- G9: Accept のときに `sess:` に `tenant_id` を書く。`eap:` に `identity`・`tenant_id`・`rule_index`・`vlan_id`・`session_timeout` を足した。セッションの索引の作り直しと利用の集計は 2-4。
- G10: 管理API は `net/http` の `/admin/v2` で作り直し、`/status`・加入者の 8 つの口・`/audit-logs` にした。`/admin/v1` はすべてなくした（Gin が依存から消えた）。管理クライアントのピンは 2-5 まで `RADIUS_ADMIN_CLIENTS` のまま。監査ログの Stream は `audit` に改め、項目を aka-vecgen にそろえた（日ごとのファイルは 2-5）。RADIUS クライアントの ID の採番（本PoC の起動のときの処理）もなくした（RADIUS クライアントは 2-5 のコマンド）。
- G11: §11.1 の `cause` の名前を直した。
- G12: OpenAPI は `docs/openapi/admin-api.yaml`（2.0.0-draft）。`provisioning-api.yaml` は D-13 と一緒に 2-6 で廃止の記録をする。刻印を付けた通しは 2-6（エッジの代わりの radsecproxy）。
- そのほか: IMSI は本PoC と同じ 15 桁、ルールの値の範囲（NAS-Identifier 253 バイト、SSID 32 バイト・32 個、VLAN 1〜4094、Session-Timeout 1〜86400 秒）も本PoC と同じ。SSID の `*` はほかの SSID と混ぜられない。`/status` に取得先ごとの加入者の数を出す（遮断器の状態は auth の中にあるので 2-5 で決める）。

段階 2-4 で決めたこと（2026-10-28。確認事項 J1〜J12 はすべて推奨案で合意）:

- J1: 1 つの feature ブランチと 1 つの PR にした。
- J2: 前回の累計を、セッションではなく重複の判定のキー（`acct:seen:…`。Hash）に持つ（§10.2 を改めた）。1 つの Accounting につき 1 つの Lua（`internal/acct/store` の `accountScript`）で、重複の判定・増分・累計の更新・集計への足し込み・セッションと索引の更新を行う。
- J3: 重複の判定のキーは `acct:seen:{送信元の IP}:{NAS-Identifier（なければ NAS-IP-Address）}:{Acct-Session-Id}`（エッジの奥の AP の間で Acct-Session-Id が重ならないように）。
- J4: 増分は今回の累計から前回の累計を引いたもの。Start の重複・Stop の後の Stop・同じ値の Interim は数えない。累計が減ったら、その値をそのまま増分にする。順序の誤り（`no_start_received`・`interim_after_stop`・`start_after_stop`）はログを出して数える。Stop の後の Start は 0 から数え直す。
- J5: 日は Event-Timestamp（受け取った時刻より 1 時間以上先なら受け取った時刻。`ACCT_EVENT_TIMESTAMP_FUTURE`）の日本時間の日付。7 日より前の日には足し込まない（`ACCT_USAGE_TOO_OLD`）。集計のキーは、その日の始まりから 8 日で消える。
- J6: テナントは、信頼する刻印 → 重複の判定のキーに残したテナント → セッションのテナントの順。どれもなければ `0`（`ACCT_TENANT_UNKNOWN`）。IMSI は、セッション → 重複の判定のキー → User-Name の永続 ID の順。分からなければ集計しない（`ACCT_IMSI_UNKNOWN`）。
- J7: auth は Access-Accept で集計に `auth_ok` を 1 足す（`internal/usage`。書けなくても Accept を返し、`USAGE_WRITE_ERR`）。
- J8: セッションと索引は `internal/sessions`（auth・acct・admin で共通）。auth が Accept で作って索引に足し、acct が Start・Interim で期限を延ばし、Stop で消す。acct が起動のときと 1 時間ごとに期限切れの索引を消す（`SESSION_SWEPT`）。本PoC の `idx:user:` はなくした。
- J9: `GET /sessions` は `tenantId` か `imsi`（カンマ区切り 100 件まで）のどちらか 1 つが必須。期限の早い順、1 ページ 1,000 件まで、カーソルは位置。
- J10: `GET /usage` は `date`（日本時間の `YYYY-MM-DD`）が必須。テナントと IMSI の辞書順、1 ページ 1,000 件まで。
- J11: `/status` に、acct が数えたセッションの数と時刻を出す（`sessionCount`・`sessionsCountedAt`）。
- J12: Accounting-On/Off はログだけ。acct の試験は、gomock をやめて偽物と miniredis で書き直した（`internal/acct/mocks` を消した）。
- そのほか: acct は、記録できない（Valkey に書けない）ときは Accounting-Response を返さない（NAS に送り直させて、利用の集計の取りこぼしを防ぐ。本PoC は書けなくても応答していた）。acct も刻印の Vendor-Id・Vendor-Type（`RADIUS_STAMP_VENDOR_ID`・`RADIUS_STAMP_VENDOR_TYPE`）を読む。累計は Lua の数（倍精度）で扱うので、1 つのセッションで 2^53 オクテット（約 9 PB）までは正確。

段階 2-5 で決めたこと（2026-10-28。確認事項 K1〜K15 はすべて推奨案で合意）:

- K1: 1 つの feature ブランチと 1 つの PR にした。aka-vecgen の Fluent Bit の重ね合わせ（K14）は aka-vecgen の main に直接コミットした。
- K2: RADIUS クライアントは `client:{ip}`（`name`・`secret`・`trust_stamp`・`created_at`・`updated_at`）と索引の Set `clients`（`internal/clients`）。§6.1 の `idx:client:{id}`・`seq:client` は消した。コマンド `client add / list / update / remove`（admin のコンテナ）。`-trust-stamp` は必須、共有シークレットは標準入力の 1 行か `-generate`（16〜128 文字、空白と制御文字を除く ASCII）。IP は auth・acct が引く形に直す（IPv4 に写した IPv6 は IPv4、ゾーンつきは誤り）。Valkey に直接書いたクライアントは索引にないので、一覧に出ない（消して入れ直す）。
- K3: `GET /clients` は全件を IP の順に返す（共有シークレットは返さない）。`/status` に `clientCount`。
- K4: auth が遮断器の状態を `stat:sources`（JSON）に、状態が変わったときと 30 秒ごとに書く。admin は `/status` と `/sources` に `circuitBreaker`（`state`・`since`・`reportedAt`）を出す。報告がなければ返さない。
- K5: `GET /sources` はクライアント証明書の主体名・期限・フィンガープリントと、サーバーの CA の主体名と期限を返す（鍵とファイルのパスは返さない）。
- K6: `sources check` は auth のコンテナで動かす（`docker compose run --rm --no-deps auth sources check`）。接続の確かめは、遮断器を通さずに基底の URL へ GET を 1 回送り、HTTP の応答が返るか（TLS 1.3 では、取得先がクライアント証明書を断ったことはハンドシェイクの後でしか分からないため、ハンドシェイクだけでは足りない）。
- K7: 管理クライアントのピンは `RADIUS_ADMIN_CLIENTS_FILE`（既定 `/run/secrets/admin-clients`。compose の secrets）。形は aka-vecgen と同じ。環境変数 `RADIUS_ADMIN_CLIENTS` はなくした。`SIGHUP` でピン・サーバー証明書・取得先の登録簿を読み直す（それぞれ、失敗したら前のまま）。ピンが 1 つもないファイルは、起動でも読み直しでも誤りにした（aka-vecgen は空のファイルを受ける。aka-radius の admin は管理API のためだけのコンテナなので、だれも使えない状態で動かさない）。`/status` に `adminClientsLoadedAt`。
- K8: `admin-cert`（表示）と `admin-cert create -hosts … -out <dir>`（自己署名、`server.pem` 644・`server.key` 600、上書きは `-force`、監査ログ `admin-server-certificate.create`）。`fingerprint` は標準入力の PEM。
- K9: 監査ログは `internal/audit`（admin とコマンドで共通）。`RADIUS_AUDIT_DIR`（既定 `/var/lib/aka-radius/audit`）・`RADIUS_AUDIT_RETENTION_DAYS`（14）・`RADIUS_AUDIT_MAX_LEN`（10000。`RADIUS_ADMIN_AUDIT_MAX` を改めた）。admin は起動のときにディレクトリに書けるかを確かめ、起動のときと 1 時間ごとに圧縮と削除をする。
- K10: healthcheck の口は `internal/health`（`RADIUS_HEALTH_ADDR`、既定 `127.0.0.1:9090`、`GET /healthz`、Valkey の PING を 1 秒まで）。compose の healthcheck は `aka-radius healthcheck`。
- K11: イメージは `gcr.io/distroless/static-debian13:nonroot`（UID 65532）。マウントするファイルは 65532 で読めるようにする（README の「起動手順」）。
- K12: admin の profile を外し、既定の `docker compose up` で起動する。
- K13: 共有のネットワークは `aka-radius-admin`（`RADIUS_ADMIN_SHARED_NETWORK`）、admin の別名も `aka-radius-admin`。証明書は `deployments/certs/admin/`。aka-provisioner の重ね合わせの付け替えは段階 3-1、D-文書の直しは 2-6。
- K14: Fluent Bit の出力に `app.vecgen`（`vecgen.log`）と `app.vecgen-valkey`（`vecgen-valkey.log`）を足した。aka-vecgen 側は `compose.fluent-bit.yaml`。
- K15: OpenAPI を 2.0.0 にした。
- K16（2-5 のマージの後の develop の CI で見つけた）: 2-3 の重複応答のキャッシュ（`radiussrv.Guard`）は、応答を送ってからキャッシュに入れていたので、その間に届いた再送を処理中として捨てていた（acct の結合試験がたまに落ちた）。応答はキャッシュに入れてから送るように直した。

### 17.2 D-文書の扱い（S4）

| 区分 | 文書 |
|---|---|
| 改版する | D-01（設計仕様書）、D-02（Valkey のデータ）、D-04（ログ）、D-06（エラーハンドリング。管理API の章を足す）、D-08（インフラと運用）、D-09（auth）、D-10（acct）、D-13（管理API 2.0.0 に改める）、E-01・E-02・E-03、O-01〜O-05、T-01〜T-04、B-02・B-03、ドキュメント一覧 |
| 廃止として記録する | D-03（vector-api の IF 定義書と状態機械。状態機械の部分は D-09 に移す）、D-05・D-07（Admin-TUI）、D-11（vector-api）、D-12（vector-gateway。取得先のクライアントは D-09 に書く） |

- 文書とコードがずれている所（E-03 の pkg の利用表、D-09 の古い条件、D-03 の `VECTOR_API_URL` の例など）は、改版のときにコードを正として直す。

## 18. 確認事項（2026-10-25 に出し、2026-10-26 にすべて推奨案で合意。§3.2 の決定事項 1〜13）

| # | 事項 | 推奨 | 理由 |
|---|---|---|---|
| C1 | 名前と作り（段階 1・2-1） | module `github.com/oyaguma3/aka-radius`、コマンド `aka-radius` 1 つにサブコマンド `auth`・`acct`・`admin`、別々のコンテナ。環境変数の接頭辞 `RADIUS_` | 7 つのモジュールと Dockerfile の重複をなくす。プロセスを分けたままにして、Accounting の負荷や管理API の不具合が認証に及ばないようにする |
| C2 | vector-gateway（概要 §19 で未決だったもの） | auth に取り込む。取得先ごとに接続と遮断器を持つ | 独立させた理由（auth を変えずに差し込む、vector-api をそのまま使う）は MVP でなくなる。IMSI ごとの取得先を引くには Valkey が要るが、gateway は Valkey を持たない。HTTP の 1 往復と 1 つのプロセスが減り、遮断器を取得先ごとに分けられる（本PoC は 1 つの遮断器で、外部の取得先の障害が他の取得先の認証も止めた） |
| C3 | 取得先の登録簿とネットワーク名 | 登録簿はファイルで `SIGHUP` で読み直し、管理API は読み出しだけ。使用中の取得先を消す読み直しは受け付けない。ネットワーク名は全体で 1 つ（`RADIUS_EAP_AKA_PRIME_NETWORK_NAME`、既定 `WLAN`）。EAP-AKA と EAP-AKA' の切り替え（NAK）は扱わない（方式は Identity の先頭の文字で決める。本PoC と同じ） | 取得先は鍵（クライアント証明書の鍵）を伴う設定で、変えるのはまれ。aka-vecgen の AV クライアントと同じく管理面から変えられないようにする。ネットワーク名は接続網（WLAN）で決まり、取得先によらない |
| C4 | 判定の順 | 状態・刻印・認可ポリシーを、ベクターを取る前（最初の要求）に確かめる。Accept の前に状態だけもう一度確かめる（§8.1） | 拒否する IMSI で取得先を呼ばず、取得先の SQN を進めない。未登録の IMSI を総当たりされても取得先に負荷がかからない |
| C5 | ルールに一致しないとき | 本PoC の `default`（allow / deny）をなくし、拒否する | ルールごとにテナントが必須なので、`allow` の既定はテナントの判定を飛ばしてしまう |
| C6 | RADIUS クライアントの管理 | コマンドで行い、管理API は読み出しだけ（共有シークレットは返さない） | RADIUS クライアントはエッジだけで、足すのはエッジを足すとき（WireGuard のピアの設定で SSH が要る）。共有シークレットを管理面に通さずに済む |
| C7 | 重複応答のキャッシュの置き場所 | auth と acct のプロセスのメモリー。30 秒（§9） | プロセスは 1 つずつで、Valkey に置くと 1 回の要求あたりの往復が増える。再起動で消えても害は小さい |
| C8 | Identity の取り方 | RADIUS の User-Name ではなく、EAP の Identity（EAP-Response/Identity と AT_IDENTITY）から IMSI を取る | 本PoC は User-Name を使い、永続 ID を求めた後の応答でも AT_IDENTITY を見ないので、外側の名前を変えない端末で失敗しうる。EAP-AKA の正本は EAP の中の Identity |
| C9 | 再同期の回数の上限（契約の決定事項 9） | 1 回 | SQN_MS に置き換えた後のベクターは受け入れられるはずで、2 回目の同期の失敗は設定の誤り（取得先の食い違いなど）を示す。何度も取り直すと取得先の SQN を無駄に進める |
| C10 | ログの経路（概要 §19 で未決だったもの） | Docker の `fluentd` のログドライバーと Fluent Bit を残し、アプリごとのファイルを日ごとに圧縮して 2 週間持つ。データ面の aka-vecgen も同じ Fluent Bit に送る。監査ログは Stream と JSONL（Fluent Bit を通さない） | 本PoC で動いている経路で、ファイルに書くので監視用の PC が rsync で取りに行きやすい。監査ログは経路の障害（`fluentd-async` のバッファーあふれ）で落ちないよう、アプリが直接書く |
| C11 | 利用の集計の作り | 累計をセッションに持ち、増分の計算と足し込みを 1 つの Lua で行う。テナントのないパケットはテナント `0` として集計する。キーは日・テナント・IMSI ごと（§10.2） | 再送や重複した Interim で数え直さない。Gigawords を扱い、4 GiB で折り返さない。取りこぼしを集計の上で見えるようにする |
| C12 | セッションの索引 | IMSI とテナントの索引を Sorted Set（スコアは期限）にし、読むときと 1 時間ごとに期限切れを消す | 本PoC の索引は TTL も掃除もなく、Stop が届かなかった分が残り続けた（掃除は Admin-TUI が読むときだけだった） |
| C13 | healthcheck | 各コンテナのループバックに healthcheck の口（Valkey に届くか）を置く | UDP と mTLS のため、本PoC は `pgrep` でしか確かめられなかった |
