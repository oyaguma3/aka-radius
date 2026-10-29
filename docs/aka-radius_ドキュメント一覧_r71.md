# aka-radius ドキュメント一覧 (r71)

**作成日:** 2025-12-30
**最終更新:** 2026-10-30
**ステータス:** MVP セットの段階 2 の終わり（2-6 で改版）

> **版数の書き方:** 他の文書を参照するときは、文書の ID と名前だけを書き、版数を書かない（今の版はこの一覧で分かる）。各文書の改訂履歴の記述は、当時の版を書く（記録のため、後から書き換えない）。

> **MVP での扱い:** 本リポジトリは、PoC セットの eapaka-radius-server-poc（以下「本PoC」）の D-文書の体系を履歴ごと引き継いだ。MVP セットの段階 2-6（2026-10-29）で、残す文書を改版し、要らなくなった文書を廃止した（§7）。改版した文書は、コード・OpenAPI・運用ガイドを正とし、その写し（compose の全文、構造体の定義、試験の一覧など）を持たない。MVP の決定事項と段階の記録は[設計概要](design-overview.md)にある。

---

## 1. MVP の文書（D-文書の体系の外）

| 文書 | 内容 |
|------|------|
| [設計概要](design-overview.md) | MVP のための改修の決定事項（1〜13）と、段階 2（2-1〜2-6）で決めたことの記録 |
| [運用ガイド](operation-guide.md) | 導入、aka-vecgen とのつなぎ方、RADIUS クライアント、管理API と aka-provisioner、加入者と認可、証明書、公開範囲、ログと監査ログ、バックアップと戻し、更新、障害時の確かめ |
| [管理API の OpenAPI](openapi/admin-api.yaml) | 管理API 2.0.0（`/admin/v2`）の口と項目の正本 |
| [README](../README.md) | 起動手順、環境変数、取得先の登録簿、コマンドの一覧 |

---

## 2. 設計ドキュメント

| No. | ドキュメント名 | 版数 | 最終更新 | 内容 |
|-----|---------------|------|---------|------|
| D-01 | aka-radius 設計仕様書 | r26 | 2026-10-29 | MVP セットでの位置づけ、コンテナと口、パッケージの地図、外部依存、データと配置の要約、文書の地図 |
| D-02 | Valkey データ設計仕様書 | r27 | 2026-10-29 | キーの一覧（加入者と索引、RADIUS クライアント、EAP の会話、セッションと索引、Accounting の重複の判定、利用の集計、遮断器とセッションの数の報告、監査ログ）、Lua による原子的な更新、Valkey の設定、メモリーの見積もり |
| D-04 | ログ仕様設計書 | r40 | 2026-10-30 | 共通の項目、`event_id` の一覧（auth・acct・admin・コマンド。コードと照合）、IMSI のマスク、出さないもの、ログの経路（Fluent Bit）、監査ログの形 |
| D-06 | エラーハンドリング詳細設計書 | r22 | 2026-10-30 | auth・acct・admin・コマンドの誤りの扱い、管理API の ProblemDetails と `cause`、取得先の遮断器、要求の期限と同時処理の上限、起動と読み直しの誤り |
| D-08 | インフラ設定・運用設計書 | r30 | 2026-10-29 | コンテナ・ネットワーク・ボリューム・マウント・secrets・healthcheck、公開先、Valkey と Fluent Bit の設定、ログの置き場所、取得先の証明書の置き場所、バックアップ |
| D-09 | Auth Server詳細設計書 | r26 | 2026-10-29 | 認証の流れ、EAP の状態機械（D-03 から取り込んだ）、ベクターを取る前の判定と認可、テナントの刻印、取得先の登録簿とクライアント（D-12 から取り込んだ）、重複応答のキャッシュ、セッションと利用の集計 |
| D-10 | Acct Server詳細設計書 | r19 | 2026-10-29 | Accounting の処理（1 つの Lua による重複の判定・増分・利用の集計・セッションの更新）、テナントと IMSI の決め方、Gigawords と Event-Timestamp、セッションの索引の掃除 |
| D-13 | 管理API 詳細設計書 | r10 | 2026-10-29 | 管理API 2.0.0 の方針・認証（mTLS とピンのファイル）・ヘッダー・エラー・状態の規則・ページ送り・監査ログ（口の詳細は OpenAPI）。Provisioning API 詳細設計書から改題 |

### 2.1 コードと設計ドキュメントの対応

| サブコマンド・パッケージ | 関連設計ドキュメント |
|-----------|-------------------|
| `auth`（`internal/auth`、`internal/radiussrv`） | **D-09**、D-02、D-04、D-06 |
| `acct`（`internal/acct`、`internal/radiussrv`） | **D-10**、D-02、D-04、D-06 |
| `admin`（`internal/admin`）とコマンド（`cmd/aka-radius`） | **D-13**、D-02、D-04、D-06、運用ガイド |
| 共有のパッケージ（`internal/subscriber`・`sessions`・`usage`・`clients`・`audit`・`health`・`envconf`・`logging`・`valkey`） | D-01（パッケージの地図）、D-02、E-02 |
| 配置（`deployments/`、`configs/`、`Dockerfile`） | **D-08**、運用ガイド |

---

## 3. 開発ドキュメント

| No. | ドキュメント名 | 版数 | 最終更新 | 内容 |
|-----|---------------|------|---------|------|
| E-01 | 開発環境セットアップガイド | r13 | 2026-10-29 | Go と Docker、`make` のターゲット、手元の compose で試すときの注意、結合試験の Valkey、mockgen と golangci-lint |
| E-02 | コーディング規約（簡易版） | r8 | 2026-10-29 | 命名、パッケージの構成と依存の向き、誤りの扱い、ログ（`log/slog` と `event_id`） |

---

## 4. テストドキュメント

| No. | ドキュメント名 | 版数 | 最終更新 | 内容 |
|-----|---------------|------|---------|------|
| T-01 | テスト戦略書 | r8 | 2026-10-29 | 単体・結合（本物の Valkey、偽の取得先、本物の UDP）・CI・検証機の通し（aka-mvpset の `verify/stage2/`）・E2E と規模の試験（aka-mvpset）の分担 |

---

## 5. 運用ドキュメント

運用の手順は[運用ガイド](operation-guide.md)にまとめた（O-01〜O-04 は廃止。§7）。

| No. | ドキュメント名 | 版数 | 最終更新 | 内容 |
|-----|---------------|------|---------|------|
| O-05 | ログ解析ガイド | r28 | 2026-10-30 | lnav の使い方、症状ごとの調べ方、トレースID で auth と aka-vecgen をたどる（`event_id` の意味は D-04） |

---

## 6. 補足資料

| No. | ドキュメント名 | 版数 | 最終更新 | 内容 |
|-----|---------------|------|---------|------|
| S-01 | eapaka_test利用ノウハウ | r6 | 2026-10-29 | eapaka_test の設定と試験ケース（状態・刻印・ルール・未登録）、SQN の扱い、刻印をエッジの代わりの radsecproxy で付ける前提 |

> **格納場所**: `docs/supplement/eapaka_test/` 配下

---

## 7. 廃止した文書

MVP セットの段階 2-6 で廃止し、ファイルを消した。最後の版は、本PoC の固定のコミット（`1f8aad1`。PoC セットの `pocset-v1.0.1`）のリンクで読める。

| No. | ドキュメント名 | 最後の版 | 廃止した日 | 移した先 |
|-----|---------------|---------|-----------|---------|
| D-03 | [Vector-APIインターフェース定義書およびEAP-AKAステートマシン設計書](https://github.com/oyaguma3/eapaka-radius-server-poc/blob/1f8aad11c9dcaee783689fd9c78e4f419fee672f/docs/D-03_Vector-APIインターフェース定義書およびEAP-AKAステートマシン設計書_r9.md) | r9 | 2026-10-29 | 状態機械は D-09。ベクターの取得の口は aka-mvpset の[認証ベクターAPI の契約](https://github.com/oyaguma3/aka-mvpset/blob/main/docs/av-api-contract.md) |
| D-05 | [Admin TUI詳細設計書【前半】](https://github.com/oyaguma3/eapaka-radius-server-poc/blob/1f8aad11c9dcaee783689fd9c78e4f419fee672f/docs/D-05_Admin_TUI詳細設計書_前半_r16.md) | r16 | 2026-10-29 | Admin-TUI を廃止した（機能の行き先は設計概要 §16。RADIUS クライアントはコマンド、加入者は管理API） |
| D-07 | [Admin TUI詳細設計書【後半】](https://github.com/oyaguma3/eapaka-radius-server-poc/blob/1f8aad11c9dcaee783689fd9c78e4f419fee672f/docs/D-07_Admin_TUI詳細設計書_後半_r15.md) | r15 | 2026-10-29 | 同上 |
| D-11 | [Vector API詳細設計書](https://github.com/oyaguma3/eapaka-radius-server-poc/blob/1f8aad11c9dcaee783689fd9c78e4f419fee672f/docs/D-11_Vector_API詳細設計書_r16.md) | r16 | 2026-10-29 | vector-api を廃止した（鍵とベクターの生成は取得先。MVP では [aka-vecgen](https://github.com/oyaguma3/aka-vecgen)） |
| D-12 | [Vector Gateway詳細設計書](https://github.com/oyaguma3/eapaka-radius-server-poc/blob/1f8aad11c9dcaee783689fd9c78e4f419fee672f/docs/D-12_Vector_Gateway_詳細設計書_r12.md) | r12 | 2026-10-29 | auth に取り込んだ。取得先の登録簿とクライアントは D-09、証明書の置き場所は D-08 |
| D-13 の OpenAPI | [Provisioning API（`provisioning-api.yaml`）](https://github.com/oyaguma3/eapaka-radius-server-poc/blob/1f8aad11c9dcaee783689fd9c78e4f419fee672f/docs/openapi/provisioning-api.yaml) | 0.4.0 | 2026-10-29 | 管理API 2.0.0（[`admin-api.yaml`](openapi/admin-api.yaml)）と D-13。互換は持たない |
| E-03 | [共通ライブラリ(pkg)設計書](https://github.com/oyaguma3/eapaka-radius-server-poc/blob/1f8aad11c9dcaee783689fd9c78e4f419fee672f/docs/E-03_共通ライブラリ_pkg_設計書_r18.md) | r18 | 2026-10-29 | `pkg/` は `internal/` の共有のパッケージになった。地図は D-01、使い方は D-09・D-10・E-02 |
| O-01 | [操作ガイド](https://github.com/oyaguma3/eapaka-radius-server-poc/blob/1f8aad11c9dcaee783689fd9c78e4f419fee672f/docs/O-01_操作ガイド_r9.md) | r9 | 2026-10-29 | 運用ガイドの 4〜6 章 |
| O-02 | [ポリシー設定ガイド](https://github.com/oyaguma3/eapaka-radius-server-poc/blob/1f8aad11c9dcaee783689fd9c78e4f419fee672f/docs/O-02_ポリシー設定ガイド_r5.md) | r5 | 2026-10-29 | 運用ガイドの 6 章 |
| O-03 | [障害対応手順書](https://github.com/oyaguma3/eapaka-radius-server-poc/blob/1f8aad11c9dcaee783689fd9c78e4f419fee672f/docs/O-03_障害対応手順書_r15.md) | r15 | 2026-10-29 | 運用ガイドの 12 章、O-05 |
| O-04 | [バックアップ・リストア手順書](https://github.com/oyaguma3/eapaka-radius-server-poc/blob/1f8aad11c9dcaee783689fd9c78e4f419fee672f/docs/O-04_バックアップ・リストア手順書_r3.md) | r3 | 2026-10-29 | 運用ガイドの 10 章 |
| T-02 | [単体テスト仕様書](https://github.com/oyaguma3/eapaka-radius-server-poc/blob/1f8aad11c9dcaee783689fd9c78e4f419fee672f/docs/T-02_単体テスト仕様書_r28.md) | r28 | 2026-10-29 | Go の試験のコードと CI（T-01） |
| T-03 | [結合テスト仕様書](https://github.com/oyaguma3/eapaka-radius-server-poc/blob/1f8aad11c9dcaee783689fd9c78e4f419fee672f/docs/T-03_結合テスト仕様書_r21.md) | r21 | 2026-10-29 | Go の結合試験と CI、検証機の通し（aka-mvpset の `verify/stage2/`）（T-01） |
| T-04 | [E2Eテスト仕様書](https://github.com/oyaguma3/eapaka-radius-server-poc/blob/1f8aad11c9dcaee783689fd9c78e4f419fee672f/docs/T-04_E2Eテスト仕様書_r10.md) | r10 | 2026-10-29 | aka-mvpset の E2E と受入試験（段階 5・6） |
| B-01 | [ホストOS構築手順書](https://github.com/oyaguma3/eapaka-radius-server-poc/blob/1f8aad11c9dcaee783689fd9c78e4f419fee672f/docs/B-01_ホストOS構築手順書_r6.md) | r6 | 2026-10-29 | VPS の作りは aka-mvpset の段階 5 の配置の文書 |
| B-02 | [アプリケーションデプロイ手順書](https://github.com/oyaguma3/eapaka-radius-server-poc/blob/1f8aad11c9dcaee783689fd9c78e4f419fee672f/docs/B-02_アプリケーションデプロイ手順書_r25.md) | r25 | 2026-10-29 | README の起動手順、運用ガイドの 2 章、D-08 |
| B-03 | [VPSデプロイ手順書（AWS Lightsail）](https://github.com/oyaguma3/eapaka-radius-server-poc/blob/1f8aad11c9dcaee783689fd9c78e4f419fee672f/docs/B-03_VPSデプロイ手順書_r5.md) | r5 | 2026-10-29 | aka-mvpset の段階 5 の配置の文書 |

---

## 8. 進捗サマリ

| カテゴリ | 残した文書 | 廃止した文書 |
|---------|-----------|-------------|
| 設計ドキュメント | 8（D-01・02・04・06・08・09・10・13） | 5（D-03・05・07・11・12） |
| 開発ドキュメント | 2（E-01・02） | 1（E-03） |
| テストドキュメント | 1（T-01） | 3（T-02〜04） |
| 構築・デプロイドキュメント | 0 | 3（B-01〜03） |
| 運用ドキュメント | 1（O-05）と運用ガイド | 4（O-01〜04） |
| 補足資料 | 1（S-01） | 0 |
| **合計** | **13** | **16** |

---

## 改訂履歴

| 版数 | 日付 | 内容 |
|------|------|------|
| r1 | 2025-12-30 | 初版作成。設計ドキュメント6件作成済み、全23件のドキュメント体系を定義。 |
| r2 | 2026-01-04 | D-07完了反映。Go実装ノード別詳細設計書（D-09〜D-11）を追加。総ドキュメント数23→26件。進捗率26%→27%。 |
| r2 | 2026-01-05 | D-12（Vector Gateway実装レベル検討書）追加。Vector Gatewayのノードマッピング追加。総ドキュメント数26→27件。進捗率27%→30%。 |
| r3 | 2026-01-14 | 各設計ドキュメントの版数更新反映: D-01(r4), D-02(r4), D-03(r3), D-04(r5), D-06(r3)。D-08（インフラ設定・運用設計書）、D-09（Auth Server詳細設計書）作成完了反映。設計ドキュメント進捗率67%→83%。全体進捗率30%→37%。 |
| r4 | 2026-01-18 | IMSIマスキング機能追加に伴う整合性更新: D-04(r6→r7), D-08(r1→r2), D-09(r2→r3), D-12(r1→r2/名称変更)。D-11(r2)を未作成→作成済みに移動。設計ドキュメント進捗率83%→92%、全体進捗率37%→41%。 |
| r5 | 2026-01-20 | 設計フェーズ完了: D-10(r1)作成完了、設計ドキュメント進捗率92%→100%。各ドキュメント版数更新: D-01(r4→r5), D-02(r4→r6), D-04(r7→r8), D-08(r2→r3), D-09(r3→r4)。D-10想定目次セクション削除。全体進捗率41%→44%。 |
| r6 | 2026-01-21 | 各ドキュメント版数更新: D-07(r1→r2), D-10(r1→r2)。D-10 Status-Server対応追加。|
| r7 | 2026-01-25 | 開発ドキュメント完了: E-01(r1), E-02(r1), E-03(r1)作成完了。開発ドキュメント進捗率0%→100%、全体進捗率44%→56%。各設計ドキュメント版数更新: D-02(r6→r7), D-04(r8→r9), D-08(r3→r4), D-09(r4→r5)。ステータスを「開発準備フェーズ」に変更。 |
| r8 | 2026-01-27 | レビューコメント対応（Phase 1-5）完了反映: T-01(r2)作成完了、テストドキュメント進捗率0%→25%、全体進捗率56%→59%。各ドキュメント版数更新: D-01(r5→r6), D-02(r7→r9), D-03(r3→r4), D-04(r9→r11), D-05(r2→r3), D-06(r3→r5), D-07(r2→r3), D-08(r4→r7), D-09(r5→r7), D-10(r2→r3), D-11(r2→r5), D-12(r2→r3), E-01(r1→r2)。主な更新内容: SQN競合制御、インフラ基盤統一、API接続設計統一、IMSIマスキング適用範囲明確化、idx:userクリーンアップ、テストベクターモード運用注記。 |
| r9 | 2026-01-27 | レビュー指摘対応（Phase 6）: D-09/D-10のDockerfileにprocps追加（ヘルスチェック整合性）、D-08にpgrep依存パッケージ明記、D-04にIDX_USER_CLEANUP event_id追加、D-01のdocker-compose.yml buildパス修正。各ドキュメント版数更新: D-01(r6→r7), D-04(r11→r12), D-08(r7→r8), D-09(r7→r8), D-10(r3→r4)。 |
| r10 | 2026-01-27 | D-01のFluent Bit設定ファイル相対パス修正に伴う版数更新（D-01 r8）。依存関係図の版数反映。 |
| r11 | 2026-02-07 | D-05版数更新（r3→r5）：全マスタデータHash形式統一。T-03結合テスト仕様書（r1）作成完了、テストドキュメント進捗率25%→50%、全体進捗率59%→63%。 |
| r12 | 2026-02-07 | テストドキュメント完了: T-02単体テスト仕様書（r1）作成完了（全876テストケース）、T-04 E2Eテスト仕様書（r2）作成完了（実機3件+擬似E2E 5件+実機異常系3件=計11シナリオ）。テストドキュメント進捗率50%→100%、全体進捗率63%→70%。 |
| r13 | 2026-02-07 | 構築・デプロイドキュメント完了: B-01ホストOS構築手順書（r1）、B-02アプリケーションデプロイ手順書（r1）作成完了。D-08版数更新（r8→r9、B-02スコープ修正）。構築・デプロイドキュメント進捗率0%→100%、全体進捗率70%→78%。 |
| r14 | 2026-02-16 | D-01版数更新（r8→r9）: 実装コードとの不整合20件修正（データモデル注記追加、パッケージマップ更新、Valkeyキースキーマ概要追加、docker-compose.yml完全同期、healthcheck・テストベクターモード・healthエンドポイント記載追加）。B-01版数更新（r1→r2）、B-02版数更新（r1→r2）: 関連ドキュメント参照版数をD-01 r9に整合。 |
| r15 | 2026-02-16 | テスト仕様書ファイル名に版数付与: T-02（r1）、T-03（r1→r3）、T-04（r2→r3）。T-03: Secret体系明確化、SQN再同期手順改訂、IMSI 003専用config追加、障害系PASS条件修正。T-04: SQN管理注意事項追加、Valkey再起動後データ残存確認追加。 |
| r16 | 2026-02-18 | 全文書版数を最新に更新 |
| r17 | 2026-02-22 | O-05ログ解析ガイド（r1）作成完了。運用ドキュメント進捗率0%→17%、全体進捗率78%→81%。 |
| r18 | 2026-02-22 | T-03結合テスト仕様書 版数更新（r3→r5）: go-eapaka PRF修正後の再テストで得られた知見を反映（identityオーバーライドIMSIのSQNリセット運用補足、Dockerイメージ再ビルド注意事項追加）。 |
| r19 | 2026-02-23 | lnavフォーマット・Fluent Bit設定修正に伴う版数更新: D-04(r13→r14)、D-08(r10→r11)、B-02(r2→r4)、O-05(r1→r2)。lnavプロパティ名をハイフン区切りに修正、Fluent Bitログルーティング修正（rewrite_tag廃止→parserフィルター＋タグベースルーティング）。 |
| r20 | 2026-02-23 | Fluent Bit 3.0→4.2アップグレード＋YAML移行に伴う版数更新: D-08(r11→r12)、O-05(r2→r3)。Classic config廃止に伴いYAML形式に移行、Distrolessベースイメージ対応、lnavフォーマットapp値定義追加。 |
| r21 | 2026-02-23 | README.md更新に伴う改版: 実装状況セクション新規追加、ドキュメント一覧を全22件カテゴリ別記載に拡充、環境変数説明・テスト規模情報追加、リポジトリ構成にpkg/httputil・deployments/.env.example・lnav_formats追加、Fluent Bit 4.2バージョン明記。B-01(r2→r3): D-08参照版数更新（r9→r12）。B-02(r4→r5): D-04参照版数更新（r13→r14）、D-08参照版数更新（r9→r12）、Fluent Bit設定ファイル参照をYAML形式に修正。 |
| r22 | 2026-02-23 | lnavフォーマット定義appフィールド欠落修正: D-04(r14→r15)、B-02(r5→r6) |
| r23 | 2026-02-23 | Admin TUI ドキュメント改善（実装スクリーンショットとのASCIIレイアウト整合性修正）: D-05(r8→r9、前半12画面)、D-07(r6→r7、後半5画面) |
| r24 | 2026-02-24 | eapaka_test利用ノウハウ補足資料作成: S-01(r1)新規作成、「6. 補足資料」セクション新設。T-03(r5→r6)・T-04(r3→r4)のeapaka_testパス参照をsupplement配下に一般化。進捗サマリ更新（総数27→28、完了22→23、進捗率81%→82%）。 |
| r25 | 2026-02-27 | O-01 操作ガイド(r1)作成完了。運用ドキュメント進捗率17%→33%、全体進捗率82%→86%（完了23→24件）。 |
| r26 | 2026-02-27 | O-02 ポリシー設定ガイド(r1)作成完了。運用ドキュメント進捗率33%→50%、全体進捗率86%→89%（完了24→25件）。 |
| r27 | 2026-02-27 | O-06（顧客説明ガイドライン資料）および「5.3 対外説明資料」セクション除去。総数28→27、未作成3→2、進捗率89%→93%。 |
| r28 | 2026-02-27 | O-03 障害対応手順書(r1)作成完了。障害検知・切り分け・コンポーネント別対応・復旧手順・エスカレーション・チェックリストを体系化。運用ドキュメント進捗率60%→80%、全体進捗率93%→96%（完了25→26件）。 |
| r29 | 2026-02-28 | O-04 バックアップ・リストア手順書(r1)作成完了。Valkeyデータの自動・手動バックアップ、リストア手順（6ステップ）、設定ファイルのバックアップと復元、トラブルシューティング、運用チェックリストを体系化。運用ドキュメント進捗率80%→100%、全体進捗率96%→100%（完了26→27件、全ドキュメント完了）。 |
| r30 | 2026-02-28 | lnavフォーマットtimestamp-format修正に伴う版数更新: D-04(r15→r16)、B-02(r7→r8)、O-05(r3→r4)。Go slog実出力形式（RFC3339Nano+数値TZオフセット）にマッチする`%N%z`パターンを追加。 |
| r31 | 2026-02-28 | lnavフォーマット定義全面改訂に伴う版数更新: D-04(r16→r17)、B-02(r8→r9)、O-05(r4→r5)。bunyan競合回避（フォーマット名変更）、timestamp-format削除、file-pattern追加、sample追加。B-02に§13.7トラブルシューティング新設、O-05に§13運用ノウハウ新設。 |
| r32 | 2026-03-01 | 開発ドキュメント版数更新: E-01(r2→r3)、E-02(r1→r2)、E-03(r2→r3)。実装・現行ドキュメントとの整合（IMSIマスキングD-04 r17準拠、pkg構成更新、golangci-lint/CI反映、Makefile/テストベクターモード追加、構造体タグjsonのみ化）。 |
| r33 | 2026-03-04 | ヘルスチェックログ分離＋全ログ重複解消: D-08(r12→r13)。rewrite_tagフィルタによるヘルスチェックログのアプリ専用ログ除外、match: "*"キャッチオール廃止→match: "healthcheck.*"に変更しapp.*タグの重複出力解消。 |
| r34 | 2026-03-05 | Accounting-On/Off対応＋ヘルスチェックログ分離のドキュメント反映: D-04(r17→r18)、D-10(r5→r6)、T-03(r6→r7)、O-05(r5→r6)。 |
| r35 | 2026-10-04 | Vector Gatewayに接続方式01（aka-only-server）を追加したことに伴う版数更新: D-01(r9→r10)、D-03(r5→r6)、D-04(r18→r19)、D-06(r6→r7)、D-08(r13→r14)、D-12(r4→r5)、T-02(r1→r2)、T-03(r7→r8)、T-04(r4→r5)、B-02(r9→r10)、O-01(r1→r2)、O-03(r1→r2)、S-01(r1→r2)。README.md も更新（アーキテクチャ表・環境変数表）。 あわせて既存記載の実装との不一致を修正: Vector Gateway/Auth Server の event_id（GW_ROUTE/GW_OK/GW_ERR、VECTOR_IMSI_NOT_FOUND 等）、エラー応答の detail 文言、docker-compose.yml 掲載内容（vector-api に TEST_VECTOR_* を渡すよう compose も修正）、T-02 の件数（削除済み logging テストの除去、全1,196件）、lnav クエリ（aka_radius_log テーブル等）。追加の版数更新: D-09(r9→r10)、E-02(r2→r3)、O-05(r6→r7)、T-01(r2→r3)。 さらに、D-04 の event_id を実装と全件突き合わせて整合し、参照文書（D-03/D-06/D-07/D-08/D-09/D-10/D-11/E-01/E-02/O-03/O-05/T-03）に反映。D-02 を実装に整合（nas_id は完全一致、SQN 競合制御は設計のみで現行未実装）。T-03/S-01 をテストベクターモードの実動作に整合（2026-10-04 実機確認）。追加の版数更新: D-02(r11→r12)、D-07(r7→r8)、D-10(r6→r7)、D-11(r6→r7)、E-01(r3→r4)。 |
| r36 | 2026-10-04 | ログのIMSIマスク漏れ修正（Auth ServerのEAP_UNSUPPORTED_TYPE/EAP_IDENTITY_INVALIDのuser_name、Vector APIのTEST_SQN_FALLBACK/TEST_SQN_PERSIST_ERRのimsi、Acct ServerのIMSI抽出不可時のUser-Name。pkg/logging.MaskUserName追加）＋lnavフォーマット修正（value整理・sample差し替え）に伴う版数更新: D-04(r19→r20)、B-02(r10→r11)、O-05(r7→r8)、D-06(r7→r8)、D-09(r10→r11)、D-10(r7→r8)、E-03(r3→r4)、T-02(r2→r3、全1,196件→1,216件)。 |
| r37 | 2026-10-04 | acct-serverのInterimシーケンス判定修正（StartなしのInterimで ACCT_SEQUENCE_ERR（no_start_received）を出力、Stop後のInterimを interim_after_stop として検出、Interimでセッション不在時は sess:{UUID} を作らず ACCT_SESSION_NOT_FOUND を出力）に伴う版数更新: D-10(r8→r9)、D-04(r20→r21)、D-02(r12→r13)、O-03(r2→r3)、O-05(r8→r9)、T-02(r3→r4、全1,216件→1,224件)。README.md も更新（ドキュメント一覧へのリンク、テスト件数）。 あわせて D-06(r8→r9)、D-09(r11→r12) を更新（Interim時のセッション不在・順序異常の記述を実装に整合）。 |
| r38 | 2026-10-04 | Vector APIのテストベクターモードでも加入者登録を必須にした実装修正（Ki/OPc/AMFだけをテスト用固定値に置き換え、加入者の取得・SQN管理・エラー処理は通常モードと同じ。未登録IMSIは404、既定SQN ff9bb4d0b607 へのフォールバックと TEST_SQN_FALLBACK / TEST_SQN_PARSE_ERR / TEST_SQN_PERSIST_ERR、GetDefaultSQN を削除し、未登録IMSIに sqn だけの sub:{IMSI} を作らない）に伴う版数更新: D-11(r7→r8)、D-02(r13→r14)、D-04(r21→r22)、T-01(r3→r4)、T-02(r4→r5、全1,224件→1,220件)、T-03(r8→r9)、T-04(r5→r6)、S-01(r2→r3)。あわせて D-01(r10→r11)、D-06(r9→r10)、D-08(r14→r15)、O-05(r9→r10) を更新（テストベクターモードの記述を実装に整合）。推奨作成順序の D-11 の版数を現行版（r8）に修正。README.md も更新（ドキュメント一覧へのリンク、テスト件数）。 |
| r39 | 2026-10-04 | ポリシーの nas_id で "*" を任意の NAS に一致させた Auth Server の実装修正（"*" 単独は任意の NAS-Identifier（空を含む）に一致、部分一致は行わない、それ以外は従来どおり完全一致。D-02 の当初設計と Admin TUI のバリデーションに合わせたもの）に伴う版数更新: D-02(r14→r15)、O-02(r1→r2)、T-04(r6→r7)、D-09(r12→r13)、T-02(r5→r6、全1,220件→1,228件)。あわせて D-03(r6→r7)、E-03(r4→r5)、O-01(r2→r3) を更新（D-03・D-09 のポリシー評価、E-03 の PolicyRule に残っていた実装に存在しない ssid/action/time_min/time_max の記述を実装の nas_id/allowed_ssids/vlan_id/session_timeout に整合）。README.md も更新（ドキュメント一覧へのリンク、テスト件数）。 |
| r40 | 2026-10-04 | auth-server の trace_id を認証単位で引き継ぐ実装修正（2回目以降の Access-Request は UUID 形式の State 属性を trace_id とし、PKT_RECV 等のハンドラー層のログもエンジンのログ・X-Trace-ID と同じ値になる）、EAPエンジンの Process が error を返さなくなったことによる EAP_ENGINE_ERR の削除、LOG_LEVEL 対応（既定 INFO、pkg/logging.ParseLevel 新設、compose で auth-server / vector-gateway / vector-api に LOG_LEVEL を渡す）に伴う版数更新: D-01(r11→r12)、D-02(r15→r16)、D-04(r22→r23)、D-06(r10→r11)、D-08(r15→r16)、D-09(r13→r14)、D-12(r5→r6)、E-01(r4→r5)、E-02(r3→r4)、E-03(r5→r6)、T-02(r6→r7、全1,228件→1,241件)、T-03(r9→r10)、B-02(r11→r12)、O-03(r3→r4)、O-05(r10→r11)。subdoc（T-02_UT-PKG / T-02_UT-AUTH、T-03_INT-006、T-04_E2E-001）と T-02 の他の subdoc の親リンクも更新。README.md も更新（ドキュメント一覧へのリンク、テスト件数、環境変数表に LOG_LEVEL）。 |
| r41 | 2026-10-04 | vector-api のログ整理と未使用コード削除の実装修正（SQN_RESYNC に trace_id とマスク済み imsi を追加（ハンドラーが usecase.ContextWithTraceID で context に載せる）、SQN_RESYNC_DELTA_ERR とテストモードの CALC_OK を1行に（SQN値はエラー文、CALC_OK に test_mode 属性）、ProblemError 経路のログに error 属性、未使用の GetWithRetry と ErrInvalidIMSI を削除、vector-api / vector-gateway の LOG_LEVEL 変換を pkg/logging.ParseLevel に統一、VectorUseCaseInterface に IsTestMode を追加）に伴う版数更新: D-04(r23→r24)、D-06(r11→r12)、D-11(r8→r9)、D-12(r6→r7)、E-02(r4→r5)、E-03(r6→r7)、T-02(r7→r8、全1,241件→1,245件)、T-03(r10→r11)、O-03(r4→r5)、O-05(r11→r12)。subdoc（T-02_UT-VAPI、T-03_INT-003、T-03_INT-006、T-04_pseudo_e2e_eapaka_test_scenarios、T-04_troubleshooting_guide）と T-02 の他の subdoc の親リンクも更新。README.md も更新（ドキュメント一覧へのリンク、テスト件数）。 |
| r42 | 2026-10-04 | acct-server の重複 Interim の event_id 分離・到達しない SYS_ERR の削除・LOG_LEVEL 対応の実装修正（重複 Interim は新設の ACCT_DUPLICATE_INTERIM（重複 Start は従来どおり ACCT_DUPLICATE_START）、AccountingProcessor の ProcessStart / ProcessInterim / ProcessStop / ProcessOn / ProcessOff の戻り値から error を外しハンドラーの SYS_ERR の分岐を削除（常に Accounting-Response を返す）、LOG_LEVEL 環境変数に対応（既定 INFO、pkg/logging.ParseLevel、起動ログに log_level）、compose / .env.example で acct-server にも LOG_LEVEL を渡し4サーバーとも対応）に伴う版数更新: D-01(r12→r13)、D-02(r16→r17)、D-04(r24→r25)、D-06(r12→r13)、D-08(r16→r17)、D-10(r9→r10)、D-12(r7→r8)、E-01(r5→r6)、E-02(r5→r6)、E-03(r7→r8)、T-02(r8→r9、全1,245件のまま（acct-server は1件追加・1件削除）)、T-03(r11→r12)、T-04(r7→r8)、B-02(r12→r13)、O-03(r5→r6)、O-05(r12→r13)。subdoc（T-02_UT-ACCT、T-03_INT-005、T-03_INT-FAULT、T-04_E2E-002）と T-02 の他の subdoc の親リンク、リネームした文書を参照する subdoc のパスも更新。README.md も更新（ドキュメント一覧へのリンク、環境変数表の LOG_LEVEL の対象サービス）。 |
| r43 | 2026-10-04 | Admin TUI の監査ログに件数と検索IMSIを正しく記録する実装修正（import / export に record_count（インポート/エクスポートしたレコード件数。0件も出力）、Session Detail の検索（search）で検索したIMSIを details ではなく target_imsi に記録し、検索の実行後に result_count（結果件数）を記録。検索に失敗した場合は result_count を出さず details に理由。LogSearch に検索エラーの引数を追加）と、D-05 のバリデーション規則を実装に合わせた修正（NAS ID 1-64文字→1〜253文字の印字可能ASCII・`*` ワイルドカード、NAS ID は NAS IPアドレスではなく NAS-Identifier と比較、SQN・Client Name 必須、Vendor 0〜64文字、エラーメッセージ等）に伴う版数更新: D-04(r25→r26)、D-05(r9→r10)、D-07(r8→r9)、T-02(r9→r10)、O-05(r13→r14)。T-02 は全1,250件（ID付与済み1,253件）に更新。推奨作成順序の D-07 の版数（r1 のままだった）を r9 に修正。あわせて、O-01 の入力規則を実装と D-05 r10 に合わせた修正（SQN・Client Name 必須、Vendor 0〜64文字、入力中の文字種制限・自動大文字化はなく保存時に検証・大文字化、ルールの入力規則、既存キーの新規登録はエラー、インポートは既存キーを上書き、エクスポートのファイル名に既定値なし、ステータスバーの表示）と、D-01 §3.3 Valkeyキースキーマを実装（D-02 r17）に合わせた修正（存在しない audit:log・acct:{ID} を削除、sess:・idx:user:・acct:seen: を追加、sub: の使用コンポーネントから Auth Server を削除、監査ログは Admin TUI の標準出力）に伴う版数更新: O-01(r3→r4)、D-01(r13→r14)。O-01 は画面説明も実装に合わせた（削除確認ダイアログは `Confirm Delete` の Yes / No、終了確認・変更破棄確認ダイアログはない、Default allow 警告は Continue / Cancel、VALKEY_ADDR は参照しない、ヘルプは F1 のみ、一覧の Enter・← / →・F6 は使わない、Session List のソートは s キー、Session Search へは Session List の Enter で移る） |
| r44 | 2026-10-04 | vector-gateway のバックエンド向けタイムアウトを auth-server より短くした実装修正（VECTOR_GATEWAY_INTERNAL_TIMEOUT / VECTOR_GATEWAY_AKAONLY_TIMEOUT の既定値を 5s → 3s。auth-server の Vector Gateway 呼び出しタイムアウト（5秒）と同じだと、バックエンド障害時に auth-server のログが VECTOR_API_ERR（502）になるか VECTOR_CONN_ERR になるかが環境次第で変わっていたため。設定値が5秒以上なら起動時に WARN「backend timeout should be shorter than the auth-server timeout; auth-server may time out before receiving 502」を出力）に伴う版数更新: D-01(r14→r15)、D-04(r26→r27)、D-06(r13→r14)、D-08(r17→r18)、D-09(r14→r15)、D-12(r8→r9)、E-01(r6→r7)、T-02(r10→r11)、T-03(r12→r13)、B-02(r13→r14)、O-03(r6→r7)、O-05(r14→r15)。T-02 は全1,254件（ID付与済み1,257件）に更新。T-03 の INT-FAULT-001（Vector API停止）の PASS 条件を VECTOR_API_ERR（http_status=502）のみに変更 |
| r45 | 2026-10-04 | Admin TUI のキー配線漏れを修正した実装修正（加入者・クライアント・ポリシーの一覧で Enter を押すと編集画面（ポリシーは Policy Details）を開く、? でヘルプを開く（入力欄にフォーカスがあるときは文字として入力）、加入者・クライアント・ポリシー・セッションの一覧で F6 でもフィルタを開く、Session List の s キーのソートの向きを項目ごとに固定（Start Time は新しい順 ▼、NAS IP と IMSI は昇順 ▲。同値は開始時刻の新しい順→UUID順。以前は常に降順）、フィルタ・IMSI検索の入力ダイアログを Esc でも閉じられる）に伴う版数更新: O-01(r4→r5)、D-05(r10→r11)、D-07(r9→r10)、T-02(r11→r12)、O-02(r2→r3)。O-01 は r4 で入れた「? ではヘルプを開かない」「一覧の Enter では開かない・F6 は使わない」「ソートはいずれも降順」の記述を修正後の動作に改めた。D-07 は Session List のソート仕様を元設計の2モード（start_time 降順 / IMSI 昇順、i / t キー）から現行の3項目の切り替えに改めた。T-02 は全1,269件（ID付与済み1,272件）に更新（admin-tui に ui パッケージのテスト15件を追加）。あわせて、キー操作・ボタン名・ダイアログの記述を実装（main.go、internal/ui）に合わせて修正: D-05（終了確認・変更破棄確認・上書き確認ダイアログを削除、Default allow 警告を Continue / Cancel に、ポリシーフォームの Ctrl+S を削除、ページ切替を ← / → から PgUp / PgDn に、フィルタ・ステータスバー・起動時の接続エラーの記述を修正）、D-07（Session Search へは Session List の Enter、Session Search の PgUp / PgDn・r / F5 を削除し Esc / q は Session List へ、ページ切替を PgUp / PgDn に、Statistics・Session Search のエラー表示を修正）、O-01（ルール編集ダイアログは Esc で閉じない、確認ダイアログの Esc）、O-02（Ctrl+S を削除、Default allow 警告を Continue / Cancel に、F6 はフォーム→ルールリストの一方向、ルール追加ダイアログのタイトル・初期値、マウス操作は無効、バリデーション規則を実装に整合）。また、Session Search の検索結果を開始時刻の新しい順（同じなら UUID 順）に並べる実装修正（画面側の sortByStartTimeDesc。テスト TestSortByStartTimeDesc を追加）に合わせて D-07 の処理フロー・コード例を修正し、D-07 の PoC 対象外・将来課題の「NAS-IP / Client-IP フィルタ」を Session List のフィルタの実装に合わせて修正、D-07 §4 / §8.1 の統計キャッシュを実装（件数4項目、要求時更新の1分キャッシュ）に合わせて修正。さらに D-07 の設計初期のまま残っていたコード片・型名（§5.6 の Acct-ID 切り詰め、§5.8.4 の fetchAllSessions、§8.2 / §8.3 の SessionListItem・SessionDetailSummary 等、§9 の KB 表記のフォーマット関数）を実装（SessionStore.List、model.Session、SessionListScreen / SessionDetailScreen、internal/format）に差し替え、Session List の Duration の色・Traffic の表記（1.2K 形式）を D-07 / O-01 で実装に合わせた |
| r46 | 2026-10-04 | Vector API の SQN競合制御（CAS）の実装（D-11 r9 までの WATCH/MULTI の設計に代えて、`sub:{IMSI}` の `sqn` が読んだ値と一致するときだけ書き換える Lua スクリプト（`CompareAndSetSQN`）を store に新設し `UpdateSQN` を削除。競合したら 1〜10ms 待って加入者の読み出しからやり直し、最大3回試行して3回とも競合したら 409（`SQN_CONFLICT_ERR`）、やり直すたびに `SQN_CONFLICT_RETRY`（`attempt`）。再同期のやり直しで SQN_HE が SQN_MS 以上なら同期済みとみなして +32。ベクター生成と `SQN_RESYNC` は SQN の書き換えに成功した後。lnav フォーマットに `attempt`）に伴う版数更新: D-02(r17→r18)、D-03(r7→r8)、D-04(r27→r28)、D-06(r14→r15)、D-11(r9→r10)、B-02(r14→r15)、O-03(r7→r8)、O-05(r15→r16)、T-02(r12→r13。テストケース 1,269→1,299件)。Admin TUI の加入者編集が `sqn` を上書きする制約は別途対応予定と各文書に記載 |
| r47 | 2026-10-04 | Admin TUI の加入者編集が SQN を巻き戻す問題の修正（SQN を変えずに保存したときは `sqn` を書き換えない（ki / opc / amf だけを更新）。SQN を変えたときは、編集開始時に読んだ値と一致するときだけ書き換え、編集中に認証で SQN が進んでいたら何も保存せずエラーを表示。存在チェックと更新を1つの Lua スクリプトで行い、途中で削除された加入者の一部フィールドだけの Hash を作らない。SQN の変更判定を大文字小文字を区別しない比較にし、Vector API が小文字で書き戻した SQN で誤って警告が出て古い値で上書きされていた問題も修正）に伴う版数更新: D-02(r18→r19)、D-04(r28→r29)、D-05(r11→r12)、D-11(r10→r11)、O-01(r5→r6)、O-03(r8→r9)、O-05(r16→r17)、T-02(r13→r14。テストケース 1,299→1,314件)。PR #23（r46）で「別PRで対応予定」とした制約を解消済みに改めた |
| r48 | 2026-10-04 | T-03 G3（SQN再同期）の手順の誤りの訂正（サーバー側 SQN を `000000000001`（IND=1）にすると eapaka_test の別の IND スロットと比較されて再同期が起きずに Accept になっていたため、`sqn_initial_hex` `FF9BB4D0B607` と同じ IND=7 の少し古い値 `FF9BB4D0B587` に変更。2026-10-04 に simwifi 実機で、修正前は再同期が起きないこと・修正後は INT-003-01/02 とも再同期が起きて Accept となることを確認）に伴う版数更新: T-03(r13→r14)、S-01(r3→r4) |
| r49 | 2026-10-04 | インターネット公開（VPS 等）に向けた安全面の修正（fluent-bit の 24224 を 127.0.0.1 だけに公開、RADIUS_SECRET（フォールバックの共有シークレット）を任意にして空を推奨（空なら登録のない送信元IPのパケットは破棄）、auth-server / acct-server の起動ログに radius_secret_fallback と設定時の WARN を追加）と、文書の訂正（Docker が公開したポートは UFW を素通りすること、インターネット越しに RADIUS を受ける場合の注意（共有シークレット、送信元IPとクライアント登録、NAT・動的IP、クラウド側ファイアウォール）、デプロイ後・セキュリティのチェックリストにテストベクターモード・フォールバックの無効と公開ポートの確認を追加）に伴う版数更新: B-01(r3→r4)、B-02(r15→r16)、D-01(r15→r16)、D-04(r29→r30)、D-06(r15→r16)、D-08(r18→r19)、D-09(r15→r16)、D-10(r10→r11)、E-01(r7→r8)、O-01(r6→r7)、O-03(r9→r10)、T-02(r14→r15。テストケース 1,314→1,318件) |
| r50 | 2026-10-04 | バックアップ・リストア手順の訂正（2026-10-04 に simwifi 実機で、旧手順が失敗すること・新手順で戻せることを確認。リストアは `docker compose stop valkey` → `docker compose run` で valkey サービスのボリュームの中身をバックアップと入れ替え → `start`。旧手順はボリューム名が実際の `deployments_valkey_data` と異なり、停止しただけのコンテナが参照するボリュームの `volume rm` は失敗していた。バックアップスクリプトは失敗時に 0バイトのファイルを残さず、バックアップを 600 で作る）と、運用手順の誤りの訂正（加入者キー `sub:*`、Vector Gateway/API のヘルスチェックはコンテナ内で curl、logrotate・systemd の名前、D-08 の「RDB併用なし」）に伴う版数更新: B-02(r16→r17)、D-08(r19→r20)、O-03(r10→r11)、O-04(r1→r2) |
| r51 | 2026-10-04 | RADIUS パケットの認証をハンドラーに一本化し、ライブラリのログを JSON にした実装修正（auth-server / acct-server の PacketServer に InsecureSkipVerify: true と ErrorLog（pkg/logging.NewRADIUSLibraryLogger）を設定。シークレットが一致しない Accounting-Request は、ライブラリが素のテキスト「bad secret」を出して捨てていたため RADIUS_AUTH_ERR が出ていなかったが、ハンドラーが送信元IP付きの RADIUS_AUTH_ERR を出して捨てるようになった。未知の Code もハンドラーに届き PKT_UNKNOWN_CODE / RADIUS_UNKNOWN_CODE で記録。形の壊れたパケット等のライブラリのエラーは RADIUS_LIB_ERR（WARN）、シークレットが空の重複ログは DEBUG）に伴う版数更新: D-04(r30→r31)、D-06(r16→r17)、D-09(r16→r17)、D-10(r11→r12)、E-03(r8→r9)、O-03(r11→r12)、O-05(r17→r18)、T-02(r15→r16。テストケース 1,318→1,329件) |
| r52 | 2026-10-04 | VPS 向けのデプロイ手順書 B-03（AWS Lightsail、r1、机上確認）を追加（構築・デプロイドキュメント 2→3件、総数 27→28件）。あわせて B-01 / B-02 の誤りを訂正（Ubuntu 24.04 の SSH はソケット起動のためポート変更は daemon-reload と ssh.socket の再起動で反映し ss で確認、git clone したログディレクトリは 775 で logrotate が処理を飛ばすため 755 に、クローン URL、コンテナ名）、D-01 の Acct Server の検証方式、D-09 の main.go の例の NewServer を訂正: B-01(r4→r5)、B-02(r17→r18)、B-03(r1 新規)、D-01(r16→r17)、D-09(r17→r18) |
| r53 | 2026-10-04 | B-03 §4.4 の admin ユーザーの作成の訂正（Ubuntu には旧来の sudo 用の admin グループが既にあり、`adduser admin` が失敗することを Lightsail の実機で確認。`--ingroup admin` で既存グループを主グループにする）と、.env のオプション項目の扱いの明記（コメントアウトのままなら compose の `${変数:-既定値}` で既定値になり、明示的に空にする必要はない。コメント内の値には設定例のもの（`VECTOR_GATEWAY_AKAONLY_*`、`COMPOSE_FILE`）があり、コメント記号だけを外すとその値が有効になる。`.env.example` の冒頭にも追記）、Valkey のパスワードをコマンドラインに直接書く手順の廃止（接続確認は `.env` を読み込み `VALKEYCLI_AUTH` を値なしの `-e` で渡す、Admin TUI は `.env` を読み込んでから起動、`echo $VALKEY_PASSWORD` を設定の有無の表示に）、B-02 §9.4 の初回の `crontab -e` のエディタ選択の注記、B-02 §11 のデプロイ後チェックリストの作り直し（作業ディレクトリへの移動と `.env` の読み込みを先頭に置き、1行1コマンドで順に実行するコードブロックと、期待値の表に分けた）に伴う版数更新: B-03(r1→r2)、B-02(r18→r19)、D-08(r20→r21) |
| r54 | 2026-10-05 | Status-Server に正常に応答したときのログを DEBUG に下げた実装修正（radsecproxy 等の複数のプロキシが Status-Server を定期的に送る構成で INFO ログが埋まらないようにする。auth-server は Status-Server の PKT_RECV と RADIUS_STATUS_OK、acct-server は Status-Server: 応答送信（あわせて event_id を PKT_RECV から auth-server と同じ RADIUS_STATUS_OK に変更）。Message-Authenticator 検証失敗の WARN（RADIUS_STATUS_AUTH_FAIL / RADIUS_AUTH_ERR）と送信失敗の PKT_SEND_ERR（ERROR）は変更なし）に伴う版数更新: D-04(r31→r32)、D-09(r18→r19)、D-10(r12→r13)、O-05(r18→r19)、T-02(r16→r17。テストケース 1,329→1,334件) |
| r55 | 2026-10-06 | NAS-Identifier を NAS の識別情報としてセッションとログに加えた実装修正（radsecproxy 等のプロキシ経由では送信元IPがプロキシのIPになり NAS を区別できないため。auth-server はポリシー評価に使った NAS-Identifier をセッションに書き、PKT_RECV（Access-Request）・AUTH_SUCCESS・AUTH_POLICY_DENIED・AUTH_POLICY_NOT_FOUND に出す。acct-server は Start / Interim で NAS-Identifier があればセッションを上書きし、src_ip を持つ課金のログに出す。Admin TUI の Session List / Session Search に NAS-ID カラムとフィルタ。lnav フォーマットに nas_identifier）と、外部の radsecproxy と接続する場合の注意（radsecproxy は本PoCに含めず外部に置く方針。2026-10-06 の PoC の結果）に伴う版数更新: D-02(r19→r20)、D-04(r32→r33)、D-07(r10→r11)、D-08(r21→r22)、D-09(r19→r20)、D-10(r13→r14)、E-03(r9→r10)、O-05(r19→r20)、T-02(r17→r18。テストケース 1,334→1,355件) |
| r56 | 2026-10-07 | D-13 Provisioning API詳細設計書（r1、設計中・未実装）と OpenAPI 定義 `docs/openapi/provisioning-api.yaml`（0.1.0-draft）を追加（設計ドキュメント 12→13件、総数 28→29件）。Admin TUI の CRUD を REST API として本PoCの外の BFF に提供する provisioning-api の設計で、aka-only-server の管理API に作法を揃え、将来の統合API（eapaka-node-provisioner）から使う前提。§1.3 の実装ノードの表に Provisioning API（未実装）を追加 |
| r57 | 2026-10-07 | Admin TUI の加入者・RADIUSクライアント・認可ポリシーの store と validation を pkg（`pkg/masterdata`、`pkg/validation`）に移し、Admin TUI の `internal/model` を `pkg/model` に統合した実装修正（Provisioning API（D-13）と共通で使うため。作成・変更を Lua スクリプトで原子的にし、`Patch` / `Put` / `Count` を追加）に伴う版数更新: D-01(r17→r18)、D-02(r20→r21)、D-05(r12→r13)、D-07(r11→r12)、D-11(r11→r12)、D-13(r1→r2)、E-03(r10→r11)、T-02(r18→r19。テストケース 1,355→1,361件) |
| r58 | 2026-10-08 | Provisioning API（`apps/provisioning-api`。D-13）の実装（REST API `/admin/v1`、mTLS のフィンガープリント固定、監査ログ、compose の profile `provisioning`。`pkg/masterdata` に `ListPage`・`ClientStore.Patch` を追加）と simwifi 実機での結合確認に伴う版数更新: D-01(r18→r19)、D-02(r21→r22)、D-04(r33→r34)、D-08(r22→r23)、D-13(r2→r3。設計中→実装済み)、E-03(r11→r12)、T-02(r19→r20。テストケース 1,361→1,461件)、T-03(r14→r15。G11 Provisioning API 結合 23件を追加)、B-02(r19→r20。§15 Provisioning API の有効化)、O-05(r20→r21。§11.6 Provisioning API のログ・監査ログの確認、`src_ip` の注意)。OpenAPI 定義 `docs/openapi/provisioning-api.yaml` を 0.1.0-draft→0.1.0 に。§1.3 の実装ノードの表の Provisioning API を「任意」に |
| r59 | 2026-10-08 | Admin TUI の RADIUS クライアントの登録・編集画面で Save / Cancel ボタンが枠外に出て表示されなかった不具合の修正（フォームの高さを入力欄の数から `ui.FormHeight` で計算）に伴う版数更新: D-05(r13→r14)、T-02(r20→r21。テストケース 1,461→1,464件) |
| r60 | 2026-10-08 | 同じホストの BFF（別の compose。web-gui-for-eapaka-radius）から provisioning-api に接続するための共有ネットワーク（docker-compose.yml の `provisioning-shared`、既定名 `eapaka-prov`。provisioning-api だけが参加）の追加に伴う版数更新: D-01(r19→r20)、D-08(r23→r24)、D-13(r3→r4)、T-03(r15→r16。G11 に INT-PROV-024〜029 を追加し全90件)、B-02(r20→r21。§15.11 を新設、サーバー証明書の SAN に DNS:provisioning-api)、O-05(r21→r22)。simwifi 実機で確認 |
| r61 | 2026-10-08 | RADIUSクライアントにサーバー採番の ID を導入した実装修正（`client:{IP}` に `id`、索引 `idx:client:{ID}`、採番の `seq:client`。Provisioning API 0.2.0 はパスを `/clients/{clientId}` に、IP は PATCH で変更可、`?ip=` で検索。Admin TUI は一覧と編集画面に ID。起動時に既存データへ採番）に伴う版数更新: D-01(r20→r21)、D-02(r22→r23)、D-04(r34→r35)、D-05(r14→r15)、D-13(r4→r5)、E-03(r12→r13)、T-02(r21→r22。テストケース 1,464→1,474件)、T-03(r16→r17。G11 に INT-PROV-030〜036 を追加し全97件)。OpenAPI 定義を 0.1.0→0.2.0 に。simwifi 実機で確認 |
| r62 | 2026-10-08 | T-03(r17→r18): 付録Aの全シナリオ一覧に G9 の INT-VALKEY-005（インデックスクリーンアップ）の行が欠けており、4.1 グループ一覧の合計（98件）と付録A（97件）が一致していなかったのを修正（付録Aを全98件に） |
| r63 | 2026-10-09 | Provisioning API の監査ログとセッションの参照（GET /audit-logs、GET /sessions、/status の sessionCount。API 0.3.0）の追加の反映: D-13(r5→r6)、D-02(r23→r24)、D-04(r35→r36)、D-07(r12→r13)、D-08(r24→r25)、D-01(r21→r22)、E-03(r13→r14)、T-02(r22→r23)、T-03(r18→r19)、B-02(r21→r22)、O-05(r22→r23)。OpenAPI 定義を 0.3.0 に。CSV の一括操作は Admin TUI だけで行う方針を D-13 §10 に記載 |
| r64 | 2026-10-10 | 加入者の停止・再開（認可ポリシーの停止の印 `status`、Auth Server の停止の判定、Provisioning API の `PUT /policies/{imsi}/status`（API 0.4.0）、Admin TUI の停止・再開と CSV の `status` 列）の追加の反映: D-02(r24→r25)、D-03(r8→r9)、D-04(r36→r37)、D-05(r15→r16)、D-06(r17→r18)、D-07(r13→r14)、D-08(r25→r26)、D-09(r20→r21)、D-13(r6→r7)、E-03(r14→r15)、T-02(r23→r24)、T-03(r19→r20)、B-02(r22→r23)、O-01(r7→r8)、O-02(r3→r4)、O-03(r12→r13)、O-05(r23→r24)。OpenAPI 定義を 0.4.0 に |
| r65 | 2026-10-12 | PoC セット（eapaka-pocset）の品質改善のステップ 3（Go 1.25.5 → 1.27.2、govulncheck の指摘による `golang.org/x/net` v0.60.0・`quic-go` v0.59.1 への更新、イメージの版の固定（`golang:1.27.2-bookworm`、`debian:12.15-slim`、`valkey/valkey:9.1.2`、`fluent/fluent-bit:4.2.8`）、CI（gofmt、golangci-lint v2.14.0、イメージと compose の設定、govulncheck））の反映: B-03(r2→r3)、D-01(r22→r23)、D-02(r25→r26)、D-08(r26→r27)、D-09(r21→r22)、D-10(r14→r15)、D-11(r12→r13)、D-12(r9→r10)、E-01(r8→r9)、E-03(r15→r16) |
| r66 | 2026-10-12 | PoC セット（eapaka-pocset）の品質改善のステップ 4（auth-server・acct-server の待ち受けの失敗での終了（`SYS_LISTEN_ERR`、終了コード 1）とパケット処理中のパニックの回復（`SYS_PANIC`）、vector-api・vector-gateway の HTTP サーバーの時間の上限、`VALKEY_PASSWORD` が空なら compose が断ることと Valkey の healthcheck（`VALKEYCLI_AUTH`、PONG の確認）、5 つのアプリへのビルドの版の埋め込み（起動ログの `build`、compose の `POC_VERSION`）、`VECTOR_GATEWAY_INTERNAL_TIMEOUT` と `TEST_VECTOR_IMSI_PREFIX` の検証、使っていない `VectorConnectTimeout` の削除、`stop_grace_period: 20s`、aka-only-server の X-Trace-ID の記録）の反映: D-01(r23→r24)、D-04(r37→r38)、D-06(r18→r19)、D-08(r27→r28)、D-09(r22→r23)、D-10(r15→r16)、D-11(r13→r14)、D-12(r10→r11)、D-13(r7→r8)、E-01(r9→r10)、T-02(r24→r25)、B-02(r23→r24)、O-03(r13→r14)、O-05(r24→r25) |
| r67 | 2026-10-12 | PoC セット（eapaka-pocset）の品質改善のステップ 6（文書の整理）の反映: 他の文書を参照するときに版数を書かない方針に変え（今の版はこの一覧で分かる。冒頭に注記を追加。改訂履歴の記述と、本文の経緯の注記は当時の版のまま）、本文の参照から版数を外した（24 文書・196 か所。大半は各文書の関連ドキュメントの表）。git の管理外で公開しない `docs/subdocs/`・`docs/testresults/` へのリンク・参照（59 か所）を外して「非公開の作業資料」と注記し、公開の文書（T-02 の付録A、T-03 の §3.1・§5・付録A など）を案内。T-01 の CI/CD（GitHub Actions の ci.yml・govulncheck.yml）、PoC セットを通した試験（eapaka-pocset の scripts/e2e.sh・e2e.yml）、結合テスト環境（存在しない .env.test・docker-compose.test.yml・tests/integration/ をやめ T-03 の手順書に）、静的解析（golangci-lint v2）を実態に合わせて書き直し。T-03 §5.10 と T-04 §10.4 の aka-only-server との突き合わせを、認証ベクターAPI 0.2.0-draft（aka-only-server f0756ab）以降は trace_id で行えるに（T-04 は実施記録のため注記）。B-03 §5.7 のサービスごとのビルドに POC_VERSION。D-09 の関連ドキュメントの表の誤り（ID・名前）を訂正。§3.1 の T-02 の行のテストケースの件数を 1,531件に（1,519件のままだった）、§8 の推奨作成順序の版数を今の版に（D-07・T-03・O-01・O-02 が古かった）。版数更新: B-01(r5→r6)、B-02(r24→r25)、B-03(r3→r4)、D-01(r24→r25)、D-06(r19→r20)、D-07(r14→r15)、D-08(r28→r29)、D-09(r23→r24)、D-10(r16→r17)、D-11(r14→r15)、D-13(r8→r9)、E-01(r10→r11)、E-02(r6→r7)、E-03(r16→r17)、O-01(r8→r9)、O-02(r4→r5)、O-03(r14→r15)、O-04(r2→r3)、O-05(r25→r26)、S-01(r4→r5)、T-01(r4→r5)、T-02(r25→r26)、T-03(r20→r21)、T-04(r8→r9)。README のテストの件数・実行方法（make test / make test-race）・文書の件数・Go のバッジ・PoC セットへの案内も更新 |
| r68 | 2026-10-16 | PoC セット（eapaka-pocset）のリリース前の整理（文書と実装の照合）: T-01 の全体を実装に合わせた（§3.2〜§3.6 のパッケージ別テスト内容を実際のパッケージに（存在しない `handler`・`model` を削除し、Auth Server の `engine`・`eap/akaprime`、Acct Server の `acct`、Vector API の `dto`、Admin TUI の `audit`・`csv`・`format` を追加）、§3.7 に Admin TUI のフォームの高さの描画のテストを例外として注記、§4.1 の mockgen を go.mod の版（v0.6.0）に、§5 モック戦略（モック対象のインターフェースの名前と定義場所、go:generate は Vector API の usecase だけで Auth Server・Acct Server は `internal/mocks` に mockgen を直接実行して生成、テストダブルの使用例を実際のテストの抜粋に、miniredis の対象パッケージ）、§6.1 のテスト用IMSIの例を15桁に、§6.2 のテスト用RADIUSクライアントを実際の使い方に）。T-02 の §2・§3（Go 1.27.2、mockgen・miniredis の版を go.mod の版に、使っていない testify を削除、モック生成と置き場所、Fake の対象）を実装に合わせた（テストケースと件数は変えていない）。T-04 の §8.5 の完全合格を「E2E-001〜203 の全11シナリオ」に、§1.5 の T-03 のシナリオ数を 117件に。§3.1 の T-04 の行の概要に接続方式01 の16シナリオ（E2E-301〜316）を追記。版数更新: T-01(r5→r6)、T-02(r26→r27)、T-04(r9→r10)。README の T-04 の行（11 シナリオと接続方式01 の 16 シナリオ）とドキュメント一覧へのリンクも更新 |
| r69 | 2026-10-23 | PoC セット（eapaka-pocset）の pocset-v1.0.1（Go 1.27.2 → 1.27.3。crypto/x509 の CVE-2026-111943（GO-2026-6809。eMudhra の侵害された中間 CA 8 件を `Certificate.Verify` で拒む）の修正。go.work・各 go.mod・Dockerfile のビルドのイメージ `golang:1.27.3-bookworm`）の反映: 版数更新: B-03(r4→r5)、D-09(r24→r25)、D-10(r17→r18)、D-11(r15→r16)、D-12(r11→r12)、E-01(r11→r12)、E-03(r17→r18)、T-01(r6→r7)、T-02(r27→r28)。README の Go の版とドキュメント一覧へのリンクも更新 |
| r70 | 2026-10-29 | MVP セットの段階 2-6 で改版（設計概要 §17.2）: 題を「aka-radius ドキュメント一覧」に改め、ファイル名を `aka-radius_ドキュメント一覧_r70.md` に。§1 に MVP の文書（設計概要、運用ガイド、OpenAPI、README）を載せ、§2.1 の対応表を `apps/` からサブコマンドと `internal/` のパッケージに。§7「廃止した文書」を新設（最後の版・廃止した日・移した先・本PoC の固定のコミットへのリンク）。推奨作成順序と依存関係図を削除。版数更新: D-01(r25→r26。題を「aka-radius 設計仕様書」に)、D-02(r26→r27)、D-04(r38→r39)、D-06(r20→r21)、D-08(r29→r30)、D-09(r25→r26)、D-10(r18→r19)、D-13(r9→r10。題を「管理API 詳細設計書」に)、E-01(r12→r13)、E-02(r7→r8)、T-01(r7→r8)、O-05(r26→r27)、S-01(r5→r6)。廃止: D-03・D-05・D-07・D-11・D-12・E-03・O-01〜O-04・T-02〜T-04・B-01〜B-03・`docs/openapi/provisioning-api.yaml` |
| r71 | 2026-10-30 | 段階 2 で残した小さな直し（設計概要 §17.1 の N2。admin の Valkey の起動の誤りの `event_id`、lnav の書式の項目）の反映に伴う版数更新: D-04(r39→r40)、D-06(r21→r22)、O-05(r27→r28) |
