#!/bin/sh
# Herakles "before" hook on k-inf1: dump the databases of all running
# Postgres and MongoDB containers into /srv/backup-dumps, so every snapshot
# of /srv contains consistent dumps next to the raw data directories.
# A failed dump is reported but does not stop the backup; the hook only
# fails if no dump succeeded at all.
set -u
umask 077
OUT=/srv/backup-dumps
mkdir -p "$OUT"
chmod 700 "$OUT"
ok=0
failed=""

envval() { # container variable
	docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$1" | sed -n "s/^$2=//p" | head -n1
}

dump() { # container extension command...
	c=$1 ext=$2
	shift 2
	tmp="$OUT/.$c.$ext.tmp"
	if "$@" >"$tmp" 2>"$OUT/.$c.err" && [ -s "$tmp" ]; then
		mv "$tmp" "$OUT/$c.$ext"
		rm -f "$OUT/.$c.err"
		ok=$((ok + 1))
	else
		rm -f "$tmp"
		echo "dump of $c failed: $(head -c 300 "$OUT/.$c.err")" >&2
		failed="$failed $c"
	fi
}

for c in $(docker ps --format '{{.Names}}'); do
	image=$(docker inspect -f '{{.Config.Image}}' "$c")
	case "$image" in
	*postgres*)
		# Sidecars built from a Postgres image without a running server
		# (e.g. zammad-backup) are skipped.
		docker exec "$c" pg_isready -q 2>/dev/null || continue
		user=$(envval "$c" POSTGRES_USER)
		dump "$c" sql docker exec "$c" pg_dumpall -U "${user:-postgres}" --clean --if-exists
		;;
	mongo:*)
		dump "$c" mongo.archive docker exec "$c" mongodump --archive --quiet
		;;
	esac
done

# Remove dumps of containers that no longer exist.
find "$OUT" -maxdepth 1 -type f -mtime +14 \( -name '*.sql' -o -name '*.mongo.archive' \) -delete

echo "database dumps: $ok ok${failed:+, failed:$failed}"
[ "$ok" -gt 0 ]
