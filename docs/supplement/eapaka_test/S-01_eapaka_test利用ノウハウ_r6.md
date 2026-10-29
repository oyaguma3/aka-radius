# S-01 eapaka_test 利用ノウハウ (r6)

**版数:** r6
**作成日:** 2026-02-24
**改版日:** 2026-10-29
**分類:** 補足資料

---

## 1. 概要

[eapaka_test](https://github.com/oyaguma3/eapaka_test) は、RADIUS で EAP-AKA / EAP-AKA' を流してサーバーを試す CLI である（EAP-AKA と EAP-AKA'、外側と内側の Identity、AT_PERMANENT_ID_REQ への応答、SQN の保存、MPPE の鍵の確かめ）。aka-radius では、手元の compose と検証機の通し（T-01 テスト戦略書 2.4、aka-mvpset の `verify/stage2/`）で AP の役に使う。

MVP の作りでは、次のことに気をつける。

- 鍵（Ki・OPc）と SQN は取得先（aka-vecgen）が持つ。aka-radius には鍵を入れず、加入者は管理API で `state`・`sourceId`・`rules` を登録する。本PoC のテストベクターモードはない。
- 認可のルールはテナントが必須で、テナントはエッジが付ける刻印（Vendor-Specific 32473/1）で決まる。**eapaka_test は刻印を付けられない**（任意の RADIUS 属性を足す設定がない）。そのため、eapaka_test はエッジの代わりの radsecproxy（`verify/stage2/` の `edge-t42`・`edge-t7`）に送り、radsecproxy が刻印を付けて auth に送る。
- Accounting と、刻印を細かく作る要求（2 つの刻印、信頼しないクライアントの刻印）は eapaka_test では送れないので、radclient を使う（7 章）。

`docs/supplement/eapaka_test/` のファイル:

```
docs/supplement/eapaka_test/
├── S-01_eapaka_test利用ノウハウ_r6.md
├── configs/
│   ├── config_stage2.yaml          # テナント 42 のエッジの代わり（127.0.0.1:11812）、IMSI A
│   ├── config_stage2_t7.yaml       # テナント 7 のエッジの代わり（127.0.0.1:21812）
│   ├── config_stage2_resync.yaml   # 再同期（UE の SQN を大きくして始める）
│   ├── config_direct.yaml          # エッジを通さずに auth（127.0.0.1:1812）へ
│   ├── config_testsim.yaml         # テストSIM（鍵は aka-vecgen に登録する）
│   ├── config_commercial.yaml      # 商用設備で確かめたテストSIM
│   └── example.yaml                # サンプル
└── testdata/cases/                 # 試験ケース（13 件。5 章）
```

`verify/stage2/stage2.sh` は、ここの設定と同じ中身を IMSI ごとに作って使う（ここのファイルは、手で試すときのための写しである）。

## 2. ビルド

```bash
git clone https://github.com/oyaguma3/eapaka_test.git
cd eapaka_test
CGO_ENABLED=0 go build -o eapaka_test ./cmd/eapaka_test
```

- Go のない検証機には、`CGO_ENABLED=0` でビルドした 1 つのファイルを送る。バイナリはリポジトリに入れない。
- 段階 2 の通しで使った版は `b3f8f25`。

## 3. 準備

### 3.1 構成

```
eapaka_test（ホスト） ─UDP 127.0.0.1:11812─> edge-t42（radsecproxy。刻印 "42" を付ける） ─UDP─> auth
                      ─UDP 127.0.0.1:21812─> edge-t7 （radsecproxy。刻印 "7" を付ける）  ─UDP─> auth
                      ─UDP 127.0.0.1:1812──────────────────────────────────────────────────> auth（刻印なし）
auth ─mTLS─> aka-vecgen
```

組み立て方（compose の重ね合わせ、radsecproxy の設定、RADIUS クライアントの共有シークレット）は `verify/stage2/` の README と `compose.radius.yaml`・`edge-t42.conf` を正にする。eapaka_test とエッジの代わりの間の共有シークレットは `ap-secret-0123456789`。

RADIUS クライアント（aka-radius の admin のコンテナで登録する。運用ガイド 4 章）:

| IP | 名前 | `-trust-stamp` | 用途 |
|---|---|---|---|
| `172.30.250.42` | edge-t42 | `true` | テナント 42 のエッジの代わり |
| `172.30.250.7` | edge-t7 | `true` | テナント 7 のエッジの代わり |
| compose のネットワークのゲートウェイ（`172.30.250.1`） | direct | `true` | `config_direct.yaml`（共有シークレット `direct-secret-0123456789`）。刻印がないので拒否になることを確かめる |

### 3.2 加入者

鍵は 3GPP TS 35.208 の試験セット 1（Ki `465b5ce8b199b49faa5f0a2ee238a6bc`、OPc `cd63cb71954a9f4e48a5994e37a02baf`）、AMF は `8000`。

| 記号 | IMSI | aka-vecgen | aka-radius | 使うケース |
|---|---|---|---|---|
| A | `440100000000001` | 登録 | `active`、ルール R1 | 成功、再同期、別のテナント、刻印なし、仮名 |
| B | `440100000000002` | 登録 | `active`、ルール R1 | EAP-AKA' の再同期 |
| C | `440100000000003` | 登録 | `inactive`、ルール R1 | `reject_inactive` |
| D | `440100000000004` | 登録 | `terminated`、ルール R1 | `reject_terminated` |
| E | `440100000000005` | 登録 | なし | `reject_subscriber_not_found` |
| F | `440100000000006` | なし | `active`、ルール R1 | `reject_source_rejected` |
| H | `440100000000008` | なし（取得先を呼ぶ前に拒否する） | `active`、ルール R2 | `reject_rule_nas`・`reject_rule_ssid` |

- ルール R1: `[{"tenantId":42,"nasId":"*","ssids":["*"],"vlanId":100,"sessionTimeout":3600}]`
- ルール R2: `[{"tenantId":42,"nasId":"ap-01","ssids":["ssid1"],"vlanId":100}]`

aka-vecgen への登録（aka-vecgen の作業ディレクトリで。`-clients` は aka-radius の AV クライアントの番号）:

```bash
docker compose exec -T aka-vecgen /aka-vecgen subscriber add -imsi 440100000000001 -ki 465b5ce8b199b49faa5f0a2ee238a6bc -opc cd63cb71954a9f4e48a5994e37a02baf -clients 1
```

aka-radius への登録（管理API。`terminated` は `inactive` で登録してから `PUT …/state` で変える）:

```bash
curl -sS --cacert certs/admin/server.pem --cert prov.pem --key prov.key -H X-Operator-Id:test -H Content-Type:application/json \
  -X POST https://localhost:9444/admin/v2/subscribers \
  -d '{"imsi":"440100000000001","state":"active","sourceId":"vecgen","rules":[{"tenantId":42,"nasId":"*","ssids":["*"],"vlanId":100,"sessionTimeout":3600}]}'
```

## 4. 設定ファイル

| ファイル | 送り先 | `sim.imsi` | `sqn_initial_hex` | SQN の保存ファイル |
|---|---|---|---|---|
| `config_stage2.yaml` | 127.0.0.1:11812（テナント 42） | A | `000000000000` | `/tmp/eapaka_test-sqn-stage2.json` |
| `config_stage2_t7.yaml` | 127.0.0.1:21812（テナント 7） | A | `000000000000` | 同上 |
| `config_stage2_resync.yaml` | 127.0.0.1:11812 | A | `00000F000000` | `/tmp/eapaka_test-sqn-stage2-resync.json` |
| `config_direct.yaml` | 127.0.0.1:1812（auth に直に） | A | `000000000000` | `/tmp/eapaka_test-sqn-direct.json` |
| `config_testsim.yaml`・`config_commercial.yaml` | 127.0.0.1:11812 | テストSIM の IMSI | SIM に合わせる | SIM ごと |

- `radius_attrs` は NAS-Identifier `ap-01`、Called-Station-Id `00-11-22-33-44-55:ssid1`（SSID は最初の `:` より後ろ）。ケースの `radius.attributes` で上書きできる。
- EAP-AKA' の鍵の導出には、aka-radius が AT_KDF_INPUT で送るネットワーク名（`RADIUS_EAP_AKA_PRIME_NETWORK_NAME`、既定 `WLAN`）が `eap.aka_prime.net_name` より優先される。設定は `WLAN` にそろえてある。
- 方式は Identity の先頭の文字で決まる（`0` は EAP-AKA、`6` は EAP-AKA'）。realm は aka-radius の判定に使わない。
- テストSIM を使うときは、その SIM の鍵を aka-vecgen に登録し、IMSI を aka-radius に登録する（ルール R1 など）。

## 5. 試験ケース

| ケース | 設定 | IMSI | 期待 | auth のログ |
|---|---|---|---|---|
| `success_aka.yaml` | `config_stage2.yaml` | A | Accept（MPPE の鍵あり） | `AUTH_SUCCESS` |
| `success_aka_prime.yaml` | 同上 | A（`6` で始める） | Accept | `AUTH_SUCCESS` |
| `perm_id_req_from_pseudonym.yaml` | 同上 | 仮名 → A | Accept | `AUTH_SUCCESS` |
| `resync_aka.yaml` | `config_stage2_resync.yaml` | A | Accept | `EAP_RESYNC_CHALLENGE` が 1 回 |
| `resync_aka_prime.yaml` | 同上 | B | Accept | `EAP_RESYNC_CHALLENGE` が 1 回 |
| `reject_inactive.yaml` | `config_stage2.yaml` | C | Reject | `AUTH_SUBSCRIBER_INACTIVE` |
| `reject_terminated.yaml` | 同上 | D | Reject | `AUTH_SUBSCRIBER_TERMINATED` |
| `reject_subscriber_not_found.yaml` | 同上 | E | Reject | `AUTH_SUBSCRIBER_NOT_FOUND` |
| `reject_source_rejected.yaml` | 同上 | F | Reject | `VECTOR_REJECTED` |
| `reject_rule_nas.yaml` | 同上 | H（NAS-Identifier `ap-02`） | Reject | `AUTH_POLICY_DENIED` |
| `reject_rule_ssid.yaml` | 同上 | H（SSID `ssid2`） | Reject | `AUTH_POLICY_DENIED` |
| `reject_other_tenant.yaml` | `config_stage2_t7.yaml` | A | Reject | `AUTH_POLICY_DENIED` |
| `reject_stamp_missing.yaml` | `config_direct.yaml` | A | Reject | `AUTH_STAMP_MISSING` |

```bash
./eapaka_test -c docs/supplement/eapaka_test/configs/config_stage2.yaml run docs/supplement/eapaka_test/testdata/cases/success_aka.yaml
```

- 終了コードは 0 が PASS、1 が FAIL（期待と違う）、2 が ERROR（設定の誤り、届かないなど）。
- Reject のケースは理由を見分けない。理由は auth のログの `event_id`（D-04 ログ仕様設計書）で確かめる。状態・刻印・ルールの拒否と未登録の拒否では、取得先を呼ばないこと（aka-vecgen の払い出しのログが増えないこと）も確かめられる。
- `--trace-radius-attrs` を付けると、Accept の属性（Class 25、Tunnel-Type 64・Tunnel-Medium-Type 65・Tunnel-Private-Group-Id 81、Session-Timeout 27）が見える。

## 6. SQN と再同期

- aka-vecgen の SQN の方式は既定で `inc32`（IND を 0 に固定して SEQ を進める）。登録したばかりの加入者の SQN は小さい。
- eapaka_test は SQN を **設定の `sim.imsi` ごと**に、IND ごとの配列で保存する。ケースの `identity` で別の IMSI を使っても、保存のキーは `sim.imsi` のままである。
- 再同期は、UE（eapaka_test）の SQN が aka-vecgen の SQN 以上のときに起きる。`config_stage2_resync.yaml` は `sqn_initial_hex` を `00000F000000`（IND 0）にし、保存ファイルを分けてある。
  - `resync_aka.yaml`（A）: AUTS を送り、aka-radius が AUTS で取り直して成功する。
  - `resync_aka_prime.yaml`（B）: 保存のキーは A のままなので、大きな SQN と B の小さな SQN が比べられて再同期になる。
- 一度再同期すると aka-vecgen の SQN が UE に追いつくので、同じ設定で 2 回目を流しても再同期は起きずに Accept になる（**PASS するが再同期は確かめられていない**）。試すたびに `sqn_initial_hex` を上げる（例 `00001F000000`）か、保存ファイルを消して別の IMSI を使い、auth のログの `EAP_RESYNC_CHALLENGE` が増えたことで確かめる。
- 再同期は 1 回の EAP の会話で 1 回まで（設計概要 §7.3）。2 回目の Synchronization-Failure は拒否になる。
- 通常の設定で SQN が食い違ったら、保存ファイル（`/tmp/eapaka_test-sqn-*.json`）を消す。

## 7. eapaka_test で送れない要求（radclient）

AP の役のコンテナ（`verify/stage2/` の `ap`）の radclient で送る。送信元は RADIUS クライアントに登録しておく。

- **刻印**: `Attr-26 = 0x00007ed901<長さ><値>`（Vendor-Id 32473 = `0x00007ed9`、Vendor-Type 1、長さは値 + 2）。値 "42" なら `Attr-26 = 0x00007ed901043432`。
- **刻印のない要求・2 つの刻印・信頼しないクライアント**: EAP-Response/Identity だけの Access-Request を作って送る（`verify/stage2/stage2.sh` の `ident_req`）。刻印なしは `AUTH_STAMP_MISSING`、2 つは `AUTH_STAMP_MULTIPLE`、`-trust-stamp false` のクライアントの刻印は付いていないものとして `AUTH_STAMP_MISSING` になる。
- **Accounting**: Start・Interim・Stop を radclient で送る（`Class` に Accept の Class、Gigawords は `Acct-Input-Gigawords`、時刻は `Event-Timestamp`。`Attr-55 = 0x<秒の 16 進>` でも書ける）。エッジの代わりの 1813（127.0.0.1:11813）に送れば刻印が付く。
- radclient の属性は、`echo` のパイプよりファイルからの入力のほうが崩れにくい。

```bash
printf 'Message-Authenticator = 0x00\n' > status.attrs
radclient -x -r 1 -t 3 127.0.0.1:11812 status ap-secret-0123456789 < status.attrs
```

## 8. うまくいかないとき

| 症状 | 確かめること |
|---|---|
| 終了コード 2、応答がない | 送り先のポート（エッジの代わりが起動しているか）、共有シークレット、auth のログの `RADIUS_NO_SECRET`（送信元が RADIUS クライアントにない） |
| どのケースも Reject | auth のログの `AUTH_STAMP_*`（エッジの代わりの刻印、`trust_stamp`、Vendor-Id・Vendor-Type）、`AUTH_SUBSCRIBER_*`、`AUTH_POLICY_DENIED`（ルールのテナント） |
| `VECTOR_REJECTED` | aka-vecgen に加入者がいない、AV クライアントの許可（`-clients`）、aka-vecgen の側の状態 |
| `amf mismatch` で中断 | 設定の `sim.amf` と aka-vecgen の AMF（既定 `8000`） |
| AT_MAC の誤り | 設定の Ki・OPc と aka-vecgen の登録の食い違い。EAP-AKA' ならネットワーク名（4 章） |
| 再同期が起きない | 6 章（UE の SQN が aka-vecgen より大きいか、IND が 0 か） |
| radsecproxy が起動しない、古い IP に送る | `verify/stage2/` の README（`rewrite` を `client` より前に書く、上流の名前を起動のときに引いたまま使う） |

---

## 改版履歴

| 版数 | 日付 | 内容 |
|:----:|:-----|:-----|
| r1 | 2026-02-24 | 初版作成 |
| r2 | 2026-10-04 | Vector Gateway 接続方式01（aka-only-server）対応: 6.5「IND と再同期（aka-only-server 相手の場合）」・9.4「aka-only-server 相手での利用パターン」を追加、3.5 に AKA' の `net_name` は AT_KDF_INPUT が優先される旨を追記、4.3 の INT-GW-PLMN-010 の前提を `441999:02` に変更、8.3 に再同期不発の行を追加、1.2/1.3 を更新。テストベクターモードの実動作（2026-10-04 確認）に合わせて修正: 3.2 にテストベクターモードの動作（Ki/OPc/AMF は Test Set 1 固定・AMF `B9B9`、`sub:{IMSI}` は SQN 管理のみ）を追記し、IMSI 003 のケースもテストベクターモードでは `config_testvector.yaml` を使い `config_testvector_imsi003.yaml` は実計算モード専用とした（1.3・3.3・4.3・5.4・6.3・7.1・7.2・8.1・8.2・9.1・9.2）。4.3 で `reject_imsi_not_found.yaml` の IMSI をテストベクター対象外の `001029999999999` に変更した旨と、`mismatch_strict_fail.yaml` は現実装では IMSI 未登録で Reject になり方式ミスマッチを検証できない旨を追記。Valkey 操作を `valkey-cli -a "$VALKEY_PASSWORD" --no-auth-warning` 形式に統一、`EAP_RESYNC` を `EAP_RESYNC_CHALLENGE` に修正 |
| r3 | 2026-10-04 | テストベクターモードでも加入者登録を必須にした Vector API の実装修正の反映: 3.2 のテストベクターモードの動作を修正（テストベクター対象の IMSI も `sub:{IMSI}` の登録が必要で未登録なら 404。既定 SQN へのフォールバック・`sqn` だけの書き込みは廃止）。4.3 `reject_imsi_not_found.yaml`（テストベクター対象外の IMSI）は引き続き 404 を期待する旨を追記。8.1 の加入者データ未登録の行、9.1 の未登録 IMSI・テストデータ投入の記述を修正。9.4 の T-03 参照を r9 に更新。`testdata/cases/reject_imsi_not_found.yaml`・`policy_not_found_testvector.yaml` のコメントを更新 |
| r4 | 2026-10-04 | 内部 Vector API 相手の再同期の手順を訂正: 6.5 の見出しを「IND と再同期」に改め、T-03 G3（INT-003 系）で再同期を起こすにはサーバー側 SQN を `sqn_initial_hex`（`FF9BB4D0B607`、IND=7）と同じ IND の少し古い値（`FF9BB4D0B587`）にすることを追記。`000000000001`（IND=1）では再同期が起きないこと（2026-10-04 simwifi 実機で確認）、デルタ検証との関係を追記し、「2026-10-04 の実施時は INT-003-01/02 とも再同期が発生した」の記述を訂正。8 のトラブルシューティング表の対処に内部 Vector API の場合を追記 |
| r5 | 2026-10-12 | PoC セット（eapaka-pocset）の品質改善のステップ 6（文書の整理）の反映: §9.4 の T-03 の参照から版数を外した（1 か所。今の版はドキュメント一覧で分かるため、参照に版を書かない）。§1.3 のファイル構成の本書のファイル名を r5 に |
| r6 | 2026-10-29 | MVP の段階 2-6 で改版（設計概要 §17.2）: 全体を MVP の作り（鍵と SQN は aka-vecgen、テストベクターモードはない、加入者は管理API で状態・取得先・ルール、ルールはテナントが必須）に合わせて書き直した。eapaka_test は刻印を付けられないので、エッジの代わりの radsecproxy（aka-mvpset の `verify/stage2/`）を通す前提にし、刻印の細かい要求と Accounting は radclient にした。設定を `config_stage2*.yaml`・`config_direct.yaml` に入れ替え（`config_testvector*.yaml` を消し、テストSIM とサンプルはエッジの代わりへ）、試験ケースを状態・刻印・ルール・未登録・取得先の拒否のもの（13 件）に入れ替えた（`policy_*`・`reject_plmn_not_implemented`・`mismatch_strict_fail` などを消した） |
