# 運用ガイド

- 対象: aka-radius を MVP セットのデータ面の VPS で動かす人。
- 関連: [README](../README.md)（起動と基本操作、コマンドと設定の一覧）、[設計概要](design-overview.md)（決定事項と段階の記録）、[管理API](openapi/admin-api.yaml)、[認証ベクターAPI の契約](https://github.com/oyaguma3/aka-mvpset/blob/main/docs/av-api-contract.md)（aka-mvpset）、aka-vecgen の[運用ガイド](https://github.com/oyaguma3/aka-vecgen/blob/main/docs/operation-guide.md)。細かい仕様は D-文書（[ドキュメント一覧](aka-radius_ドキュメント一覧_r70.md)）にある。

## 1. 構成と置き場所

`deployments/` で `docker compose` を使い、5 つのコンテナを動かす。auth・acct・admin は同じイメージ `aka-radius` をサブコマンドを変えて動かす（distroless、UID 65532）。

| コンテナ | 内容 |
|---|---|
| `auth` | RADIUS 認証（EAP-AKA / EAP-AKA'）。認証ベクターを取得先（aka-vecgen）から mTLS で取る |
| `acct` | RADIUS Accounting。利用の集計、セッションの索引の掃除 |
| `admin` | 管理API（aka-provisioner から）と、運用のコマンド（`docker compose exec admin aka-radius …`） |
| `valkey` | データの保存先（加入者、RADIUS クライアント、EAP の会話、セッション、利用の集計、監査ログの Stream）。AOF は 1 秒ごとに同期する |
| `fluent-bit` | ログをアプリごとのファイル（`logs_on_host/`）に書く。aka-vecgen のログも受ける |

| 口 | ポート | つなぐ相手 | 認証 |
|---|---|---|---|
| 認証 | 1812/udp | エッジ（サービス用の WireGuard のトンネルの IP） | RADIUS クライアントごとの共有シークレット、Message-Authenticator 必須 |
| Accounting | 1813/udp | 同上 | 共有シークレット |
| 管理API | 9444/tcp | aka-provisioner（管理面。サービス用の WireGuard を通す） | mTLS。管理クライアントのピン（ファイル） |
| healthcheck | `127.0.0.1:9090`（各コンテナの中） | コンテナの中だけ | なし（Valkey に届くか） |
| 取得先 | auth から aka-vecgen の 8443 | 同じ VPS の共有の Docker ネットワーク `aka-av` | mTLS（取得先ごとのクライアント証明書） |

```
[エッジ（radsecproxy）] ──RADIUS（WireGuard）──> 1812/1813 ┐
                                                           ├ auth・acct ── Valkey
[aka-provisioner] ──mTLS（WireGuard）──────────> 9444 ── admin ┘
auth ──mTLS（aka-av）──> aka-vecgen 8443
[運用者] ──SSH（運用用の WireGuard と踏み台）──> コマンド（RADIUS クライアント、証明書、登録簿の確かめ）
```

ノードの上に置くもの（`deployments/` の下）:

| ファイル | 内容 | 持ち主と権限 |
|---|---|---|
| `.env` | Valkey のパスワードと設定 | 運用者、600 |
| `sources/` | 取得先の登録簿 `sources.json` と、取得先ごとのクライアント証明書・鍵・サーバーの CA（3 章） | UID 65532。鍵は 600 |
| `certs/admin/` | 管理API のサーバー証明書 `server.pem`・`server.key`（7 章） | UID 65532。鍵は 600 |
| `secrets/admin-clients` | 管理クライアントのピン（5 章）。Docker の secrets で渡す | UID 65532、600 |
| `audit/` | 監査ログの日ごとのファイル（9.2） | UID 65532 |
| `logs_on_host/` | アプリごとのログのファイル（9.1） | root（Fluent Bit が書く） |
| ボリューム `<プロジェクト名>_valkey_data` | Valkey のデータ（AOF とスナップショット） | — |

## 2. 導入

ホストに要るのは Docker Engine と compose プラグインだけである（Debian 13 を主に想定する）。手順は README の「起動手順」と同じで、ここでは注意する点を書く。

1. `.env` を作り、`VALKEY_PASSWORD` を長い乱数にする（`openssl rand -hex 24` など）。`RADIUS_FALLBACK_SECRET` は空のままにする（8 章）。
2. 取得先の登録簿を用意する（3 章）。
3. 管理クライアントのピンのファイルを作る（5 章）。**admin のコンテナを一度でも動かす前に作る**（ないと Docker が同じ名前のディレクトリを作る。できてしまったら、そのディレクトリを消してからファイルを作る）。
4. 管理API のサーバー証明書を作る（7.1）。SAN には、aka-provisioner がつなぐ名前か WireGuard のアドレスを入れる。
5. `sources/`・`certs/admin/`・`secrets/admin-clients`・`audit/` を UID 65532 で読み書きできるようにする（`sudo chown -R 65532:65532 …`。鍵とピンは 600）。
6. 公開先を決める（8 章。`.env` の `RADIUS_UDP_BIND`・`RADIUS_ADMIN_BIND`）。
7. `docker compose up -d` で起動し、`docker compose ps` で auth・acct・admin が healthy になることを確かめる。
8. RADIUS クライアント（エッジ）を登録する（4 章）。

次のどれかに当たると起動しない（ログの `event_id`。終了して、compose の `restart` で起動し直す）。

| `event_id` | コンテナ | 原因 | 直し方 |
|---|---|---|---|
| `SOURCES_LOAD_ERR` | auth・admin | 登録簿が読めない、形が違う、証明書と鍵の組が合わない（auth） | `docker compose run --rm --no-deps auth sources check` で確かめる（3 章） |
| `ADMIN_CLIENTS_LOAD_ERR` | admin | ピンのファイルがない・読めない・形が違う・1 つもない | 5 章 |
| `SERVER_CERT_LOAD_ERR` | admin | 管理API のサーバー証明書か鍵が読めない、組が合わない | 7.1 |
| `AUDIT_DIR_ERR` | admin | `audit/` に UID 65532 で書けない | 持ち主と権限を直す |
| `VALKEY_CONN_ERR` など | すべて | Valkey に届かない、パスワードが違う | `docker compose logs valkey` を見る |
| `SYS_LISTEN_ERR` | すべて | ポートが使われている | 同じポートを使うほかのものを止める |
| 設定の誤り（`error` だけ） | すべて | 環境変数の値の形が違う | ログの `error` を見る |

## 3. aka-vecgen とつなぐ

auth は、加入者の `sourceId` が指す取得先から認証ベクターを 1 つずつ取る。取得先は登録簿 `sources/sources.json` に並べる（項目は README の「取得先の登録簿」）。同じ VPS の aka-vecgen には、共有の Docker ネットワーク `aka-av` で `https://aka-vecgen:8443/nudm-ueau/v1` につなぐ。

1. aka-radius のクライアント証明書と鍵を、このノードで作る（鍵はノードの外に出さない）。手順は README の「取得先の登録簿」。
2. 証明書（鍵ではない）を aka-vecgen に AV クライアントとして登録する（aka-vecgen の `client add`）。aka-vecgen の AV 用サーバー証明書（`av-cert`）をもらい、`sources/` に `serverCaFile` として置く。
3. `sources.json` を書く。
4. `docker compose run --rm --no-deps auth sources check` で確かめる。形、証明書と鍵の組、期限（30 日より近ければ警告）、各取得先へ TLS でつながり HTTP の応答が返るか、を見る。`connection: FAIL` なら、aka-vecgen に登録していない証明書か、サーバーの CA の取り違え、名前（SAN）の不一致、共有ネットワークに入っていない、のどれかである。
5. aka-radius を共有ネットワークに入れて起動する（`docker-compose.aka-av.yml` を重ねる。`.env` の `COMPOSE_FILE=docker-compose.yml:docker-compose.aka-av.yml` にすると素の `docker compose` でも重なる）。共有ネットワークは aka-vecgen の compose が作るので、aka-vecgen を先に起動する。

- `GET /admin/v2/sources` の `clientCertificate.fingerprint` を、aka-vecgen の AV クライアントのフィンガープリントと突き合わせられる。
- 登録簿を直したら `docker compose kill -s HUP auth admin` で読み直す（ログの `SOURCES_RELOADED`）。読めない・形が違うときは前の登録簿のまま続ける（`SOURCES_RELOAD_ERR`）。
- 加入者が指している取得先（`/status` の `sources[].subscriberCount` が 0 でない）を消す読み直しは断る（`SOURCES_RELOAD_ERR`）。先に加入者の `sourceId` を変えるか、加入者を消す。
- 取得先ごとに遮断器を持つ。一時的な誤り（タイムアウト、5xx、契約に合わない応答）が 5 回続くと開き、30 秒で半開きにして 3 回通し、成功すれば閉じる。加入者がいない・止めてあるなどの拒否（403）は数えない。状態は `/status`・`/sources` の `circuitBreaker` に出る（auth が状態の変わったときと 30 秒ごとに報告する。`reportedAt` が 1 分より古ければ auth が止まっているか、Valkey に書けていない）。
- 要求 1 回のタイムアウトは取得先ごとに `timeout`（既定 3 秒）で、再試行はしない。EAP の会話全体の期限は `RADIUS_REQUEST_TIMEOUT`（既定 4 秒）。

## 4. エッジ（RADIUS クライアント）

RADIUS クライアントは、エッジのトンネルの IP ごとに、admin のコンテナのコマンドで登録する。エッジを足すときに、WireGuard のピアの設定と一緒に行う。auth・acct はパケットのたびに Valkey を引くので、変更はすぐに効く。

```bash
docker compose exec admin aka-radius client add -ip 10.10.0.11 -name edge-01 -trust-stamp true -generate
```

- `-trust-stamp` は必須で、エッジは `true` にする。`true` のクライアントが付けたテナントの刻印（Vendor-Specific 32473/1）だけを使う。`false` のクライアントの刻印は付いていないものとして扱うので、認証は通らない（MVP の加入者のルールはテナントが必須のため）。
- 共有シークレットは、`-generate` で作って 1 回だけ表示するか、標準入力の 1 行で渡す（引数に書かない）。16〜128 文字（空白と制御文字を除く ASCII）。エッジの radsecproxy の `server` のブロックに同じ値を書く。
- 作り直しは `client update -ip … -generate`。エッジの側と同時に変える（変える間、そのエッジからの要求は通らない）。
- 一覧は `client list`（共有シークレットは出さない）か `GET /admin/v2/clients`。消すのは `client remove -ip …`。
- 操作は監査ログに残る（管理クライアント `cli`、操作者は `-operator`、既定 `cli`）。
- 刻印の Vendor-Id・Vendor-Type（`RADIUS_STAMP_VENDOR_ID`・`RADIUS_STAMP_VENDOR_TYPE`）は、エッジの設定と同じにする。起動のログと `/status` で確かめられる。

## 5. 管理API と aka-provisioner

管理API は aka-provisioner だけが使う（加入者の登録・変更・状態、セッションと利用の集計の取り出し、RADIUS クライアントと取得先の読み出し、監査ログ）。

### 5.1 管理クライアントを登録する

ピンのファイル `secrets/admin-clients` に、1 行に 1 つ、`識別名=クライアント証明書の SHA-256 のフィンガープリント` を書く。

```
# aka-provisioner（管理面の VPS）
provisioner=0123…cdef
```

- フィンガープリントは `docker compose exec -T admin aka-radius fingerprint < client.pem` か `openssl x509 -in client.pem -noout -fingerprint -sha256`（コロン区切り・大文字のままでよい）。
- 識別名は監査ログの `mgmtClient` に残る。
- 1 つもないファイルでは admin は起動しない（読み直しでは前のまま続ける）。
- 直したら `docker compose kill -s HUP admin` で読み直す（ログの `ADMIN_CLIENTS_RELOADED`、`/status` の `adminClients`・`adminClientsLoadedAt`）。ファイルはファイルごとマウントしているので、その場で書き換える。`mv` で置き換えたときは `docker compose up -d --force-recreate admin`。
- 登録していない証明書の接続は TLS のハンドシェイクで断り、ログに `ADMIN_CLIENT_REJECTED`（`fingerprint`・`src_ip`）を出す。登録の誤りは、この `fingerprint` と比べて確かめる。

### 5.2 管理クライアントの証明書を入れ替える

同じ識別名に新旧 2 行を書いて読み直し、aka-provisioner を新しい証明書に替えてから、古い行を消して読み直す。

### 5.3 同じホストで試すとき

本番の aka-provisioner は別の VPS から WireGuard でつなぐ。同じホストで試すときは、aka-provisioner を共有の Docker ネットワーク `aka-radius-admin`（`RADIUS_ADMIN_SHARED_NETWORK`）に入れ、`https://aka-radius-admin:9444/admin/v2` でつなぐ（サーバー証明書の SAN に `DNS:aka-radius-admin`）。

## 6. 加入者と認可

加入者は aka-ossbss の画面から aka-provisioner を通して管理API で登録する。加入者は、状態、認証ベクターの取得先（`sourceId`）、認可ポリシーのルール（`rules`、1〜32 件）を持つ。鍵は持たない（取得先が持つ）。

### 6.1 状態

| 状態 | 認証 | 変更 | 物理削除 |
|---|---|---|---|
| `active` | 通す | できる | できない（先に `inactive` にする） |
| `inactive` | 断る（`AUTH_SUBSCRIBER_INACTIVE`） | できる | できる |
| `terminated` | 断る（`AUTH_SUBSCRIBER_TERMINATED`） | できない（`POST …/reinstate` で `inactive` に戻す。理由が必須） | できる |

規則は aka-vecgen と同じ。aka-vecgen 側の状態とは別に持つ（どちらかで止めれば認証は通らない）。

### 6.2 ルールの評価

auth は最初の要求（IMSI が分かった時点）で、認証ベクターを取る前に次の順に確かめ、どれかに当たれば取得先を呼ばずに拒否する。Access-Accept の前に状態だけもう一度確かめる。

1. 加入者がいる（`AUTH_SUBSCRIBER_NOT_FOUND`）
2. 状態が `active`（`AUTH_SUBSCRIBER_INACTIVE`・`AUTH_SUBSCRIBER_TERMINATED`）
3. テナントの刻印が 1 つだけあり、10 進の 1 以上の数（`AUTH_STAMP_MISSING`・`AUTH_STAMP_MULTIPLE`・`AUTH_STAMP_INVALID`）
4. テナント・NAS-Identifier・SSID がすべて一致するルールがある（`AUTH_POLICY_DENIED`）。上から順に見て、最初に一致したルールを使う。一致しなければ拒否する

| ルールの項目 | 内容 |
|---|---|
| `tenantId` | テナント（刻印の値）。必須 |
| `nasId` | NAS-Identifier（253 バイトまで）。`*` はどれでも |
| `ssids` | SSID（Called-Station-Id の `:` の後。32 バイト・32 個まで）。`["*"]` はどれでも（ほかの SSID と混ぜられない） |
| `vlanId` | 一致したときに Access-Accept で返す VLAN（1〜4094。Tunnel-Type・Tunnel-Medium-Type・Tunnel-Private-Group-Id）。省くと返さない |
| `sessionTimeout` | Access-Accept の Session-Timeout（1〜86400 秒）。省くと返さない |

例（テナント 42 の SSID `corp` では VLAN 100、それ以外の SSID では VLAN 200）:

```json
[{"tenantId":42,"nasId":"*","ssids":["corp"],"vlanId":100},
 {"tenantId":42,"nasId":"*","ssids":["*"],"vlanId":200}]
```

- IMSI と鍵の導出には、RADIUS の User-Name ではなく EAP の Identity（EAP-Response/Identity と AT_IDENTITY）を使う（違えば `EAP_IDENTITY_MISMATCH`）。
- 方式は Identity の先頭の文字で決まる（`0` は EAP-AKA、`6` は EAP-AKA'）。EAP-AKA' のネットワーク名は全体で 1 つ（`RADIUS_EAP_AKA_PRIME_NETWORK_NAME`、既定 `WLAN`）。
- 再同期は 1 回の EAP の会話で 1 回まで（`EAP_RESYNC_CHALLENGE`）。

### 6.3 Accounting と利用の集計

acct は Accounting-Request（Start・Interim・Stop）ごとに、重複の判定・前回の累計からの増分・利用の集計への足し込み・セッションの更新を 1 つの Lua で行う（Gigawords と Event-Timestamp を扱う。詳しくは D-10）。テナントは信頼する送信元の刻印から決め、分からなければ `0` で集計する（`ACCT_TENANT_UNKNOWN`）。IMSI が分からなければ集計しない（`ACCT_IMSI_UNKNOWN`）。Valkey に書けないときは Accounting-Response を返さない（NAS に送り直させて、集計の取りこぼしを防ぐ）。集計は日本時間の日ごとに 8 日持ち、aka-ossbss が `GET /admin/v2/usage?date=YYYY-MM-DD` で毎日取り出す。

## 7. 証明書

| 証明書 | 置き場所 | 期限の確かめ | 入れ替え |
|---|---|---|---|
| 管理API のサーバー証明書 | `certs/admin/` | `docker compose exec admin aka-radius admin-cert` | 7.1 |
| 取得先へのクライアント証明書 | `sources/` | `sources check`、`/sources` | 7.2 |
| 取得先のサーバーの CA | `sources/` | 同上 | aka-vecgen が作り直したら差し替えて読み直す |
| 管理クライアントの証明書 | aka-provisioner | aka-provisioner の側 | 5.2 |

### 7.1 管理API のサーバー証明書を作り直す

```bash
docker compose run --rm --no-deps -v ./certs/admin:/out admin admin-cert create -hosts aka-radius-admin,10.10.0.2 -out /out -force
docker compose kill -s HUP admin
```

- 自己署名（ECDSA P-256、10 年）。aka-provisioner はこの証明書そのものを信頼の起点にするので、作り直したら aka-provisioner の側の証明書も替える。
- 読み直すと、次の接続から新しい証明書を出す（`SERVER_CERT_RELOADED`）。読めない・組が合わないときは前の証明書のまま（`SERVER_CERT_RELOAD_ERR`）。
- 作ったことは監査ログに残る（`admin-server-certificate.create`。Valkey に届かないときは日ごとのファイルにだけ）。

### 7.2 取得先へのクライアント証明書を入れ替える

1. 新しい証明書と鍵を、別の名前で `sources/` に作る。
2. aka-vecgen に新しい証明書を登録する（aka-vecgen の `client replace-cert` か、新しいクライアントとして登録）。
3. `sources.json` の `clientCertFile`・`clientKeyFile` を新しい名前にし、`sources check` で確かめてから `docker compose kill -s HUP auth admin`。
4. 古い証明書を aka-vecgen から外し、古いファイルを消す。

読み直すと取得先のクライアントを作り直す（遮断器は閉じた状態から。古い接続は閉じる）。

## 8. 公開範囲

| ポート | 既定の公開先 | 本番 |
|---|---|---|
| 1812・1813/udp | `127.0.0.1`（`RADIUS_UDP_BIND`） | サービス用の WireGuard のアドレス。nftables の `DOCKER-USER` で送信元をエッジのトンネルの IP に絞る |
| 9444/tcp | `127.0.0.1`（`RADIUS_ADMIN_BIND`） | サービス用の WireGuard のアドレス。送信元を aka-provisioner に絞る |
| Valkey 6379 | `127.0.0.1` | そのまま（ホストの外に出さない） |
| Fluent Bit 24224 | `127.0.0.1` | そのまま（Docker のログドライバーだけが使う） |

- nftables の規則は段階 5 の配置の文書で決める。
- 予備の共有シークレット `RADIUS_FALLBACK_SECRET` は空のままにする。設定すると、登録していない送信元のパケットもその値で受ける（起動のログに WARN）。

## 9. ログと監査ログ

| 記録 | 内容 | 置き場所 | 持つ期間 |
|---|---|---|---|
| ログ | 認証・Accounting・管理API の要求、起動と読み直し、誤り | `logs_on_host/` の `auth.log`・`acct.log`・`admin.log`・`valkey.log`・`vecgen.log`・`vecgen-valkey.log` | ノードで 2 週間（9.1） |
| 監査ログ | 管理API とコマンドの変更の操作 | Valkey の Stream `audit` と `audit/` の日ごとのファイル | Stream は `RADIUS_AUDIT_MAX_LEN` 件（既定 10000）、ファイルは `RADIUS_AUDIT_RETENTION_DAYS` 日（既定 14） |

共有シークレット、認証ベクター（RAND・AUTN・XRES・CK・IK）、鍵はログにも監査ログにも出さない。

> **IMSI はマスクしない。** 運用上、調査・解析のトレーサビリティを優先するため、IMSI のマスクの既定はオフである（`RADIUS_LOG_MASK_IMSI=false`。aka-vecgen も同じ）。ログを取りに行く監視用の PC の保管とアクセスできる人も、ノードと同じく限る。

### 9.1 ログ

JSON（1 行に 1 つ）で、Docker の `fluentd` のログドライバーから Fluent Bit に送り、アプリごとのファイルに書く。`docker compose logs` でも見られる。aka-vecgen は `compose.fluent-bit.yaml` を重ねて、同じ Fluent Bit に送る。ファイルはホストの logrotate で日ごとに圧縮し、2 週間持つ。監視用の PC が取りに行く。

`/etc/logrotate.d/aka-radius`（パスは置き場所に合わせる）:

```
/opt/aka-radius/deployments/logs_on_host/*.log {
    daily
    rotate 14
    compress
    delaycompress
    missingok
    notifempty
    create 0644 root root
    dateext
    dateformat -%Y%m%d
}
```

lnav で読むときの書式は `deployments/lnav_formats/eap_aka_log.json`。`event_id` の一覧は D-04、調べ方は O-05 にある。よく見るもの:

| `event_id` | 内容 |
|---|---|
| `AUTH_SUCCESS` | Access-Accept（IMSI、テナント、ルール、NAS-Identifier） |
| `AUTH_SUBSCRIBER_*`・`AUTH_STAMP_*`・`AUTH_POLICY_DENIED` | ベクターを取る前の拒否（6.2） |
| `VECTOR_REJECTED`・`VECTOR_TEMPORARY_ERR`・`VECTOR_CIRCUIT_OPEN` | 取得先が断った・一時的な誤り・遮断器が開いている |
| `CB_OPEN`・`CB_HALF_OPEN`・`CB_CLOSE` | 遮断器の状態の変化 |
| `RADIUS_NO_SECRET` | 登録していない送信元のパケットを捨てた（4 章） |
| `RADIUS_OVERLOAD` | 同時に処理する上限（`RADIUS_MAX_INFLIGHT`）を超えて捨てた |
| `ACCT_*`・`USAGE_*`・`SESSION_SWEPT` | Accounting と利用の集計、セッションの掃除 |
| `ADMIN_REQUEST`・`ADMIN_ERROR`・`ADMIN_CLIENT_REJECTED` | 管理API の要求・中の誤り・断った証明書 |
| `*_RELOADED`・`*_RELOAD_ERR` | `SIGHUP` の読み直し（登録簿、ピン、サーバー証明書） |
| `AUDIT`・`AUDIT_WRITE_ERR`・`AUDIT_MAINTAIN_ERR` | 監査ログを書いた・書けなかった（操作は成功のまま）・ファイルの圧縮か削除に失敗した |

### 9.2 監査ログ

- 日ごとのファイル `audit/audit-YYYYMMDD.jsonl`（日付は日本時間）。項目は `time`・`operator`・`mgmtClient`・`action`・`target`・`detail`・`traceId`・`streamId`（aka-vecgen と同じ）。前の日までのファイルは admin が gzip で圧縮し、持つ日数を過ぎたら消す。監視用の PC が取りに行く。
- 管理API の `GET /admin/v2/audit-logs` は Stream を新しい順に返す。
- 管理API の操作は `mgmtClient` が管理クライアントの識別名、`operator` が `X-Operator-Id`。コマンドの操作は `mgmtClient` が `cli`。

### 9.3 トレースID で記録をたどる

auth は EAP の会話ごとにトレースID を決め、ログの `trace_id` に出し、取得先への要求の `X-Trace-ID` で送る（aka-vecgen のログの `trace_id` と同じ値）。管理API は `X-Trace-ID` で受けた値をログ（`ADMIN_REQUEST`）と監査ログの `traceId` に残す。

## 10. バックアップと戻し

| 守るもの | 取り方 |
|---|---|
| Valkey（加入者、RADIUS クライアントと共有シークレット、利用の集計、セッション、監査ログの Stream） | `scripts/backup-valkey.sh`（止めずにスナップショットを取る） |
| `sources/`（取得先の鍵を含む）、`certs/admin/`、`secrets/admin-clients`、`.env` | ファイルの写し（運用者だけが読める場所に） |
| `audit/` | 監視用の PC が取りに行く |

```bash
cd deployments
../scripts/backup-valkey.sh backups/valkey-$(date +%Y%m%d).rdb
```

戻し方（今のデータは消える）:

```bash
../scripts/restore-valkey.sh backups/valkey-20261029.rdb
docker compose up -d
```

- スクリプトは素の `docker compose` を使う。プロジェクト名や重ね合わせを変えて起動しているときは、`COMPOSE_PROJECT_NAME`・`COMPOSE_FILE` を付けて動かす。動いている valkey が見つからなければ何もしない（別のボリュームを触らないため）。
- AOF が有効な Valkey は、AOF がないとスナップショットを読まずに空で始まる。戻しのスクリプトは、AOF を切った一時の Valkey で読み込んでから AOF を作り直す（aka-vecgen と同じ）。
- 戻すと、写しの後の加入者の変更・利用の集計・セッションは消える。aka-ossbss の毎日の突き合わせで加入者は台帳に合わせ直される。利用の集計は、写しの後の分が消えたままになる（Accounting は送り直されない）。

## 11. 更新

1. 新しいイメージを手元（または CI）でビルドして送る（VPS の上ではビルドしない）。
2. `docker compose up -d` で作り直す（auth・acct・admin は数秒止まる。エッジは再送する）。
3. `docker compose ps` で healthy、`/status` の `build` を確かめる。

Valkey のデータの形を変える版では、その版の README と設計概要の記録に従う。

## 12. 障害時の確かめ

| 症状 | 確かめること |
|---|---|
| どの認証も通らない | `docker compose ps`（healthy か）、auth のログの `RADIUS_NO_SECRET`（エッジの IP が RADIUS クライアントにない、共有シークレットが違う）、`AUTH_STAMP_*`（エッジの刻印、`trust_stamp`、Vendor-Id・Vendor-Type） |
| ある加入者だけ通らない | auth のログの IMSI で `AUTH_SUBSCRIBER_*`・`AUTH_POLICY_DENIED`（状態とルール）、`VECTOR_REJECTED`（aka-vecgen の側の状態・許可のクライアント）。aka-vecgen のログを同じ `trace_id` で見る |
| 断続的に通らない、遅い | `VECTOR_TEMPORARY_ERR`・`CB_OPEN`（`/status` の `circuitBreaker`）、`sources check`、aka-vecgen の負荷。`RADIUS_OVERLOAD`（同時処理の上限） |
| 再同期が続く | UE の SQN と aka-vecgen の SQN の方式（aka-vecgen の運用ガイド） |
| 利用の集計が増えない | acct のログの `ACCT_IMSI_UNKNOWN`・`ACCT_TENANT_UNKNOWN`、エッジが Accounting を送っているか、`VALKEY_CONN_ERR`（応答を返していない） |
| 管理API につながらない | admin のログの `ADMIN_CLIENT_REJECTED`（ピン）、サーバー証明書の SAN と期限（`admin-cert`）、`RADIUS_ADMIN_BIND` と nftables |
| 読み直しが効かない | ログの `*_RELOAD_ERR`、ピンのファイルを `mv` で置き換えていないか（5.1） |
| コンテナが unhealthy | `docker compose exec <サービス> aka-radius healthcheck`（Valkey に届くか）、`docker compose logs valkey` |
| 監査ログのファイルが増えない | admin のログの `AUDIT_WRITE_ERR`・`AUDIT_MAINTAIN_ERR`、`audit/` の持ち主 |
