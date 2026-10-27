# aka-radius — ビルド・試験・静的解析の Makefile
#
# 1 つのモジュール（github.com/oyaguma3/aka-radius）なので、./... で全体を扱う。

# govulncheck の版（CI と同じ）
GOVULNCHECK := golang.org/x/vuln/cmd/govulncheck@v1.9.0

.PHONY: build test test-cover test-race fmt vet lint vulncheck clean

## build: 全パッケージをビルド
build:
	go build ./...

## test: 全パッケージのテスト実行
test:
	go test ./...

## test-cover: カバレッジ付きテスト実行（cover.out を生成）
test-cover:
	go test -coverprofile=cover.out ./...
	go tool cover -func=cover.out | tail -1

## test-race: データ競合検出付きテスト実行
test-race:
	go test -race ./...

## fmt: 全ソースコードのフォーマット
fmt:
	gofmt -w .

## vet: 全パッケージの静的解析（go vet）
vet:
	go vet ./...

## lint: golangci-lint による静的解析
lint:
	golangci-lint run ./...

## vulncheck: 依存と標準ライブラリの既知の脆弱性の検査（govulncheck）
vulncheck:
	go run $(GOVULNCHECK) ./...

## clean: ビルド成果物・カバレッジファイルの削除
clean:
	rm -f cover.out coverage.out
	go clean ./...
