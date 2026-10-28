#!/bin/sh
# Valkey のスナップショットを、止めずに取って手元に写す（運用ガイドの「バックアップと戻し」。aka-vecgen と同じ作り）。
# deployments/（docker-compose.yml のあるディレクトリ）で実行する。写しには RADIUS クライアントの共有シークレットが入るので、
# ノードの運用者だけが読める場所に置く。
#
#   ../scripts/backup-valkey.sh backups/valkey-$(date +%Y%m%d).rdb
set -eu

out=${1:?usage: scripts/backup-valkey.sh <出力のファイル>}

# 動いている valkey がなければ何もしない（compose のプロジェクトを取り違えて、別のボリュームを触らないため）。
# 別のプロジェクト名や重ね合わせで起動しているときは、COMPOSE_PROJECT_NAME と COMPOSE_FILE を付けて動かす
if [ -z "$(docker compose ps -q valkey 2> /dev/null)" ]; then
	echo "backup-valkey: この compose のプロジェクトで動いている valkey が見つからない（COMPOSE_PROJECT_NAME・COMPOSE_FILE を確かめる）" >&2
	exit 1
fi

# valkey-cli は環境変数 VALKEYCLI_AUTH（compose で VALKEY_PASSWORD を渡している）でパスワードを受け取る
cli() {
	docker compose exec -T valkey valkey-cli "$@"
}
# 起動してからスナップショットを書き終えた回数（BGSAVE の終わりを、この数が増えたことで知る）
saves() {
	cli INFO persistence | sed -n 's/^rdb_saves:\([0-9]*\).*/\1/p'
}

before=$(saves)
# 自動のスナップショットや AOF の書き直しの途中なら、終わってから取る
cli BGSAVE SCHEDULE > /dev/null
i=0
until [ "$(saves)" != "$before" ]; do
	i=$((i + 1))
	if [ "$i" -gt 600 ]; then
		echo "backup-valkey: スナップショットが 10 分たっても終わらない" >&2
		exit 1
	fi
	sleep 1
done
if ! cli INFO persistence | grep -q '^rdb_last_bgsave_status:ok'; then
	echo "backup-valkey: スナップショットに失敗した（docker compose logs valkey を見る）" >&2
	exit 1
fi

docker compose cp valkey:/data/dump.rdb "$out"
echo "backup-valkey: $out"
