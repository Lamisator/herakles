#!/usr/bin/env bash
# Build the Herakles website with release binaries of the current commit and
# deploy it to https://herakles.kloeck-it.de (k-inf1, /srv/docker/herakles).
#
#   SSH_KEY=/workspace/id_ssh website/deploy/deploy.sh
#
# Commit first: the version shown on the site is `git describe --tags`.
# BUILD_ONLY=1 only builds website/build for a local preview.
# Binaries are only uploaded when they changed; older versions stay
# available under /downloads/<version>/.
set -euo pipefail

HOST=${HOST:-root@k-inf1-server.kloeck-it.de}
DIR=${DIR:-/srv/docker/herakles}
URL=${URL:-https://herakles.kloeck-it.de}
ssh_opts=(-o BatchMode=yes)
[ -n "${SSH_KEY:-}" ] && ssh_opts+=(-i "$SSH_KEY")
remote() { ssh "${ssh_opts[@]}" "$HOST" "$@"; }

cd "$(dirname "$0")/../.."
VERSION=$(git describe --tags --always --dirty)
if [[ $VERSION == *-dirty && ${ALLOW_DIRTY:-} != 1 ]]; then
  echo "uncommitted changes; commit first (or set ALLOW_DIRTY=1)" >&2
  exit 1
fi

echo "== building binaries ($VERSION)"
rm -rf dist
make dist VERSION="$VERSION" >/dev/null

build=website/build
rm -rf "$build"
mkdir -p "$build/release" "$build/downloads/$VERSION"
cp -r website/site/. "$build/release/"

# dist suffix -> published name
declare -A names=(
  [linux-amd64]=herakles-linux-amd64
  [linux-arm64]=herakles-linux-arm64
  [linux-arm]=herakles-linux-armv7
  [windows-amd64.exe]=herakles-windows-amd64.exe
  [windows-arm64.exe]=herakles-windows-arm64.exe
  [darwin-arm64]=herakles-macos-arm64
)
subst=(-e "s/@VERSION@/$VERSION/g")
for suffix in "${!names[@]}"; do
  src="dist/herakles-$VERSION-$suffix"
  name=${names[$suffix]}
  cp "$src" "$build/downloads/$VERSION/"
  cp "$src" "$build/downloads/$name"
  size=$(stat -c %s "$src" | awk '{printf "%.1f MiB", $1/1048576}')
  sum=$(sha256sum "$src" | cut -d' ' -f1)
  subst+=(-e "s/@SIZE_${name}@/$size/g" -e "s/@SHA_${name}@/$sum/g")
done
(cd "$build/downloads" && sha256sum herakles-* > SHA256SUMS)
(cd "$build/downloads/$VERSION" && sha256sum herakles-* > SHA256SUMS)
sed -i "${subst[@]}" "$build"/release/*.html
if grep -n '@[A-Z_a-z0-9.-]*@' "$build"/release/*.html; then
  echo "unreplaced placeholders" >&2
  exit 1
fi

if [[ ${BUILD_ONLY:-} == 1 ]]; then
  echo "built $build (not deployed)"
  exit 0
fi

echo "== uploading to $HOST:$DIR"
remote "mkdir -p $DIR/www/releases $DIR/www/downloads"
tar -C website/deploy -cf - docker-compose.yml nginx.conf | remote "tar -xf - -C $DIR"
if [[ "$(remote "cat $DIR/www/downloads/$VERSION/SHA256SUMS 2>/dev/null" || true)" == "$(cat "$build/downloads/$VERSION/SHA256SUMS")" ]]; then
  echo "   binaries unchanged"
else
  tar -C "$build/downloads" -cf - . | remote "tar -xf - -C $DIR/www/downloads"
fi
release=$(date -u +%Y%m%d%H%M%S)
tar -C "$build/release" -cf - . | remote "set -e
  mkdir $DIR/www/releases/$release
  tar -xf - -C $DIR/www/releases/$release
  ln -sfn releases/$release $DIR/www/current.new
  mv -T $DIR/www/current.new $DIR/www/current
  cd $DIR/www/releases && ls -1d */ | sort | head -n -3 | xargs -r rm -rf
  cd $DIR && docker compose up -d --quiet-pull"

echo "== checking $URL"
for _ in $(seq 30); do
  if curl -fsS "$URL/" | grep -q "Herakles $VERSION"; then
    curl -fsS "$URL/downloads/SHA256SUMS" | diff - "$build/downloads/SHA256SUMS" && echo "deployed $VERSION to $URL"
    exit 0
  fi
  sleep 2
done
echo "site did not come up with $VERSION" >&2
exit 1
