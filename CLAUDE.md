# aka-radius（EAP-AKA RADIUS PoC プロジェクトの後継）

## 概要
- MVP セット（aka-mvpset）の RADIUS サーバーのノード。eapaka-radius-server-poc の履歴を引き継ぎ、MVP セットの段階 1 で名前（module パス `github.com/oyaguma3/aka-radius`）を変えた。MVP の改修は `docs/design-overview.md`（MVP の設計概要）の §17.1 の手順（2-1〜2-6）で行う。
- 段階 2-1 は済み: 1 つのモジュールとコマンド `aka-radius`（サブコマンド `auth`・`acct`・`admin`）、vector-api と Admin-TUI の廃止、auth と acct の共通部分（`internal/radiussrv`）、環境変数の接頭辞 `RADIUS_`（`internal/envconf`）、IMSI のマスクの既定 `false`。admin の Gin は 2-3 で外す。
- 段階 2-2 は済み: vector-gateway を auth に取り込んだ（`internal/auth/source`。取得先の登録簿 `RADIUS_SOURCES_FILE` を `SIGHUP` で読み直す、取得先ごとの mTLS の接続と遮断器、契約 R1〜R7、再同期は 1 回まで）。取得先は `sub:{imsi}` の `source_id`（2-3 で加入者の口を作るまでは Valkey に直接入れる）。登録簿と証明書は `deployments/sources/` をディレクトリごとマウントする。結合試験は `internal/auth/integration_test.go`（偽の取得先は `internal/auth/source/sourcetest`）。
- 段階 2-3 は済み: 加入者（`internal/subscriber`。状態・取得先・ルールを `sub:` 1 つに、索引 `subs`、取得先ごとの数 `cnt:src:{id}`、Lua で原子的に）、ベクターを取る前の判定（有無 → 状態 → 刻印 → ルール。Accept の前に状態をもう一度）、テナントの刻印（VSA と `client:{ip}` の `trust_stamp`）、IMSI を EAP の Identity から、重複応答のキャッシュ・要求の期限・同時処理の上限（`radiussrv.Guard`。auth と acct）、管理API 2.0.0（`internal/admin/api`、`net/http`、`/admin/v2`。Gin を外した）。決めたことは設計概要 §17.1 の「段階 2-3 で決めたこと」（G1〜G12）。エンジンの単体試験はメモリーの偽物（gomock を使うのは auth/server のハンドラーの試験だけ）。
- 段階 2-4 は済み: Accounting の作り直し（`internal/acct/store` の 1 つの Lua で、重複の判定・前回の累計からの増分・利用の集計・セッションと索引の更新。前回の累計は `acct:seen:{送信元}:{NAS}:{Acct-Session-Id}` に持つ）、利用の集計（`internal/usage`。auth が `auth_ok` を足す）、セッションと索引（`internal/sessions`。auth・acct・admin で共通、acct が 1 時間ごとに掃除）、管理API の `/sessions`・`/usage`。決めたことは設計概要 §17.1 の「段階 2-4 で決めたこと」（J1〜J12）。acct の試験は偽物と miniredis（`internal/acct/mocks` を消した）。
- 段階 2-5 は済み: 管理API の `/clients`・`/sources`（2.0.0 にした）、RADIUS クライアント（`internal/clients`。`client:{ip}` と索引 `clients`）、運用のコマンド（`cmd/aka-radius` の `client`・`sources check`・`admin-cert`・`fingerprint`・`healthcheck`）、管理クライアントのピンのファイル `RADIUS_ADMIN_CLIENTS_FILE` と `SIGHUP`（ピン・サーバー証明書・登録簿）、監査ログ `internal/audit`（Stream と日ごとのファイル）、ループバックの healthcheck の口（`internal/health`、`127.0.0.1:9090`）、遮断器の状態の報告（auth が `stat:sources` に書く）、distroless のイメージ（UID 65532）、admin の profile をなくした、共有ネットワーク `aka-radius-admin`。決めたことは設計概要 §17.1 の「段階 2-5 で決めたこと」（K1〜K15）。
- 手元の compose で試すときは `docker compose -p <名前>` でプロジェクト名を変える（ディレクトリの名前 `deployments` のままだと、手元に残っている既存のボリューム `deployments_valkey_data`（もとの PoC と前の試しのデータ）を使ってしまう。2026-10-28 に分かった）。
- 段階 2-6 は済み: 運用ガイド `docs/operation-guide.md`、README を絞った、小さなコードの直し（`RADIUS_UDP_BIND`、`maxmemory 1gb`、`scripts/` のバックアップと戻し、lnav の書式、`ADMIN_CLIENT_REJECTED`）、RADIUS のライブラリの重複の判定に再送を捨てさせない直し（M11）、検証機の通し（aka-mvpset の `verify/stage2/`、29/29）、D-文書の改版と廃止（残すのは D-01・02・04・06・08・09・10・13、E-01・02、T-01、O-05、S-01 と一覧。廃止した文書は一覧の「廃止した文書」）。決めたことは設計概要 §17.1 の「段階 2-6 で決めたこと」（M1〜M11）。
- 文書の正: 今の作りの細部はコード・OpenAPI（`docs/openapi/admin-api.yaml`）・運用ガイド・改版した D-文書。設計概要は決定事項と段階の記録。D-文書はコードの写し（compose の全文、構造体の定義、試験の一覧など）を持たない。
EAP-AKA/AKA' の RADIUS サーバー。Wi-Fi認証（WPA2/WPA3-Enterprise）向け。RADIUS認証機能・課金機能と管理API を持つ。

## 設計面の概要
- ドキュメント D-01「aka-radius 設計仕様書」と、MVP の設計概要 `docs/design-overview.md` を参照。運用は `docs/operation-guide.md`。

## リポジトリ構成
- `cmd/aka-radius`（サブコマンドの振り分け）と `internal/`（`auth`・`acct`・`admin`・`radiussrv`・`subscriber`・`envconf` と共有パッケージ）。README の「リポジトリ構成」と D-01 のパッケージの地図を参照。

## Go言語の外部パッケージ
- `go.mod` と、ドキュメント D-01「aka-radius 設計仕様書」の外部依存を参照。

## コーディング規約
- ドキュメント E-02「コーディング規約（簡易版）」を参照。

## ドキュメント参照
- ドキュメント一覧は `docs/` 配下の「aka-radius_ドキュメント一覧」です（廃止した文書と、本PoC の固定のコミットへのリンクもある）。
- 設計・開発などの各ドキュメントは `docs/` 配下にあります。

## テスト戦略
- 本プロジェクトのテスト戦略は、ドキュメント T-01「テスト戦略書」を参照。
- 実装時はテストファイルを作成し、テストカバレッジ80%以上となるよう検討する。

## 利用可能なテストツール
- delve
- mockgen
- radclient
- eapaka_test
  - GitHub Repository : https://github.com/oyaguma3/eapaka_test

## セッション管理
- マルチフェーズ計画を実装する際は、次のフェーズに移る前に各フェーズのテストを完了させることを優先する。
- セッション制限に近づいている場合は、新しいフェーズを開始するのではなく、現在のフェーズのテストをパスさせることに集中する。

## Go開発ガイドライン
- go.mod の依存関係を変更する前に、変更をリセットする可能性のある git フックやリンターがないか確認する。
- 変更後は `git diff go.mod` で変更が保持されていることを確認する。
- Goパッケージを再構築する際は、実装コードを書く前に `make build`（`go build ./...`）を実行して循環インポートを早期にチェックする。
- モック（`internal/auth/mocks`。acct は段階 2-4 でなくした）は mockgen v0.6.0 でリポジトリの直下から作る（例: `mockgen -source=internal/auth/eap/types.go -destination=internal/auth/mocks/eap_mock.go -package=mocks`）。

## ビルド・静的解析
- 1 つのモジュールなので、Makefile は `./...` で全体を扱う（段階 2-1 で Go Workspace をやめた）。
- 主要Makefileターゲット:
  - `make build` — 全パッケージのビルド
  - `make test` — 全パッケージのテスト実行
  - `make test-cover` — カバレッジ付きテスト
  - `make test-race` — レースコンディション検出テスト（`CGO_ENABLED=1` が必要。WSL環境ではデフォルト無効、GitHub Actions環境では有効）
  - `make fmt` — コードフォーマット
  - `make vet` — go vet 実行
  - `make lint` — golangci-lint 実行
  - `make vulncheck` — govulncheck（依存と標準ライブラリの既知の脆弱性）
  - `make clean` — ビルド成果物の削除
- golangci-lint は **v2** 形式（`version: "2"` 必須）
  - v2での注意: `gosimple` は staticcheck に統合済み、`typecheck` は linter ではない
  - テストファイル除外は `linters.exclusions.rules` で `path: _test\.go`
- 設定ファイル: `.golangci.yml`, `Makefile`

## CI/CD（GitHub Actions）
- 設定ファイル: `.github/workflows/ci.yml`
- トリガー: `main` / `develop` への push および PR
- パイプライン: Checkout → Setup Go → Format(gofmt) → Build → Vet → Lint → Test(race)。別のジョブでイメージ `aka-radius` のビルド（1 つ。サブコマンドなしで終了コード 2 になることも）と compose の設定を確かめる
- `golangci/golangci-lint-action` は v9.3.0 をコミットの SHA で固定し、golangci-lint は v2.14.0（Go 1.27 に対応した版）。第三者の Action は SHA で固定する（PoC セット eapaka-pocset の決まり）
- `.github/workflows/govulncheck.yml` が push・PR と毎週月曜に `make vulncheck` を実行する
- イメージ（Dockerfile、compose）の版はパッチ版まで書く（PoC セットの決まり）
- Go バージョンは `go.mod` の `go` ディレクティブから自動取得

## ブランチ戦略・PRワークフロー
- `main`（安定版） ← `develop`（統合） ← `feature/*`（個別改善）
- feature → develop: `gh pr merge <N> --squash --delete-branch`
- develop → main: Create a merge commit（履歴保持）
- コミットメッセージ prefix: `feat:`, `fix:`, `refactor:`, `test:`, `docs:`, `chore:`

## 共有パッケージ（internal/。もとの pkg/）
- `internal/logging.MaskIMSI` — IMSIマスキング（D-04仕様: 先頭6桁+末尾1桁）
- `internal/subscriber` — 加入者（`sub:`・`subs`・`cnt:src:{id}`）の Valkey 読み書き（Lua で原子的）とルールの評価。auth と admin で共通
- `internal/sessions` — セッション（`sess:`）と索引（`idx:sess:imsi:`・`idx:sess:tenant:`）。auth・acct・admin で共通
- `internal/usage` — 利用の集計（`usage:{日}:{テナント}:{IMSI}`・`usage:idx:{日}`）。日本時間の日の割り当て
- `internal/radiussrv` — auth と acct の RADIUS の受け口（待ち受けとパニックの回復、共有シークレットの解決、刻印の信頼の印、Message-Authenticator、Proxy-State、Status-Server、停止のシグナルの処理、`Guard`（重複応答のキャッシュ・要求の期限・同時処理の上限））
- `internal/envconf` — 環境変数の読み込み（`RADIUS_` の共通の設定。Valkey とログ）
- 各サブコマンドで重複実装せず、共有パッケージをimportして利用すること
- 共有パッケージの相互依存は禁止（例外: `internal/radiussrv` が `envconf`・`logging`・`clients` を読む。admin が取得先の情報のために `internal/auth/source` を読む。E-02 §3）
