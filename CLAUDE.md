# aka-radius（EAP-AKA RADIUS PoC プロジェクトの後継）

## 概要
- MVP セット（aka-mvpset）の RADIUS サーバーのノード。eapaka-radius-server-poc の履歴を引き継ぎ、MVP セットの段階 1 で名前（module パス `github.com/oyaguma3/aka-radius`）を変えた。MVP の改修は `docs/design-overview.md`（MVP の設計概要）の §17.1 の手順（2-1〜2-6）で行う。
- 段階 2-1 は済み: 1 つのモジュールとコマンド `aka-radius`（サブコマンド `auth`・`acct`・`admin`・`gateway`）、vector-api と Admin-TUI の廃止、auth と acct の共通部分（`internal/radiussrv`）、環境変数の接頭辞 `RADIUS_`（`internal/envconf`）、IMSI のマスクの既定 `false`。`gateway` は 2-2 で auth に取り込んでなくす（それまでは Gin と envconfig と PoC の環境変数の名前のまま）。admin の Gin は 2-3 で外す。
- D-文書は 2-6 でまとめて改版する。それまでは設計概要を正とし、D-文書に残る PoC の名前と作り（`apps/`、`pkg/`、`go.work`、Admin-TUI、vector-api など）は読み替える。
EAP-AKA/AKA' の RADIUS サーバー。Wi-Fi認証（WPA2/WPA3-Enterprise）向け。RADIUS認証機能・課金機能と管理API を持つ。

## 設計面の概要
- ドキュメント「D-01_ミニPC版_EAP-AKA_RADIUS_PoC環境_設計仕様書」を参照。

## リポジトリ構成
- `cmd/aka-radius`（サブコマンドの振り分け）と `internal/`（`auth`・`acct`・`admin`・`gateway`・`radiussrv`・`envconf` と共有パッケージ）。README の「リポジトリ構成」を参照（D-01 の `6. 開発リポジトリ構成` は PoC の形）。

## Go言語の外部パッケージ
- ドキュメント 「D-01_ミニPC版_EAP-AKA_RADIUS_PoC環境_設計仕様書」を参照。
- Go言語で開発するノードの各設計ドキュメントに記載された外部パッケージ依存も参照すること。

## コーディング規約
- ドキュメント E-02「コーディング規約（簡易版）」を参照。

## ドキュメント参照
- ドキュメント一覧は `docs/` 配下の「EAP-AKA_RADIUS_PoC環境_ドキュメント一覧」です。
- 設計・開発などの各ドキュメントは `docs/` 配下にあります。

## テスト戦略
- 本プロジェクトのテスト戦略は、ドキュメント「T-01_テスト戦略書」を参照。
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
- モック（`internal/{auth,acct}/mocks`）は mockgen v0.6.0 でリポジトリの直下から作る（例: `mockgen -source=internal/auth/policy/interfaces.go -destination=internal/auth/mocks/policy_mock.go -package=mocks`）。

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
- `internal/httputil.ProblemDetail` — RFC 7807準拠エラーレスポンス
- `internal/masterdata` — 加入者・RADIUSクライアント・認可ポリシー（`sub:` / `client:` / `policy:`）の Valkey 読み書き（作成・変更は Lua で原子的。E-03 §9）
- `internal/validation` — 上記マスタデータの入力検証・正規化（E-03 §8）
- `internal/radiussrv` — auth と acct の RADIUS の受け口（待ち受けとパニックの回復、共有シークレットの解決、Message-Authenticator、Proxy-State、Status-Server、停止のシグナルの処理）
- `internal/envconf` — 環境変数の読み込み（`RADIUS_` の共通の設定。Valkey とログ）
- 各サブコマンドで重複実装せず、共有パッケージをimportして利用すること
- 共有パッケージの相互依存は禁止。例外として、依存を持たない `internal/model` への依存だけ許可する（E-03 §10.2）
