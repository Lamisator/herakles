#!/usr/bin/env bash
# Build the Herakles website with release binaries of the current commit and
# deploy it to https://herakles.kloeck-it.de (k-inf1, /srv/docker/herakles).
#
#   SSH_KEY=/workspace/id_ssh website/deploy/deploy.sh
#
# Commit first: the version shown on the site is `git describe --tags`.
# BUILD_ONLY=1 only builds website/build for a local preview.
# The Windows setup needs makensis (apt install nsis). Release tags also
# publish downloads/latest.json for the updater, signed with
# .herakles-signing/update-ed25519.pem (never commit it).
# Binaries are only uploaded when they changed; older versions stay
# available under /downloads/<version>/. On release tags the signed Linux
# packages and apt/rpm/pacman repositories are rebuilt (packaging/build.sh)
# and published under /apt, /rpm, /arch (REPACKAGE=1 forces it).
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
echo "== building the Windows setup"
packaging/windows/build.sh "$VERSION" dist "dist/herakles-$VERSION-windows-setup.exe"

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
  [windows-setup.exe]=herakles-setup.exe
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

# Release announcement for the updater (internal/update), signed with the
# Ed25519 release key: only for release tags.
if [[ $VERSION =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  key=${UPDATE_KEY:-.herakles-signing/update-ed25519.pem}
  [[ -r $key ]] || { echo "update signing key $key missing" >&2; exit 1; }
  python3 - "$VERSION" "$URL" "$build/downloads/$VERSION" "$(git log -1 --format=%s)" > "$build/downloads/latest.json" <<'PY'
import hashlib, json, os, sys, datetime
version, url, d, notes = sys.argv[1:5]
files = {}
prefix = "herakles-" + version + "-"
for name in sorted(os.listdir(d)):
    if not name.startswith(prefix):
        continue
    data = open(os.path.join(d, name), "rb").read()
    files[name[len(prefix):]] = {"url": f"{url}/downloads/{version}/{name}", "sha256": hashlib.sha256(data).hexdigest(), "size": len(data)}
print(json.dumps({"version": version, "released": datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"), "notes": notes, "files": files}, indent=1))
PY
  openssl pkeyutl -sign -inkey "$key" -rawin -in "$build/downloads/latest.json" | base64 -w0 > "$build/downloads/latest.json.sig"
  echo >> "$build/downloads/latest.json.sig"
fi
(cd "$build/downloads/$VERSION" && sha256sum herakles-* > SHA256SUMS)
sed -i "${subst[@]}" "$build"/release/*.html
if grep -n '@[A-Z_a-z0-9.-]*@' "$build"/release/*.html; then
  echo "unreplaced placeholders" >&2
  exit 1
fi

# Linux packages and repositories (apt, rpm, pacman): only for release tags.
packages=
if [[ $VERSION =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "== building packages"
  packaging/build.sh "$VERSION" dist "$build/packages" >/dev/null
  echo "$VERSION" > "$build/packages/VERSION"
  packages=1
else
  echo "   $VERSION is no release tag: packages stay at the last release"
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
  tar -C "$build/downloads" --exclude='./latest.json*' -cf - . | remote "tar -xf - -C $DIR/www/downloads"
fi
if [[ -n $packages ]]; then
  if [[ "$(remote "cat $DIR/www/packages/VERSION 2>/dev/null" || true)" == "$VERSION" && ${REPACKAGE:-} != 1 ]]; then
    echo "   packages for $VERSION are already published"
  else
    tar -C "$build/packages" -cf - . | remote "set -e
      rm -rf $DIR/www/packages.new $DIR/www/packages.old
      mkdir $DIR/www/packages.new
      tar -xf - -C $DIR/www/packages.new
      [ -d $DIR/www/packages ] && mv $DIR/www/packages $DIR/www/packages.old
      mv $DIR/www/packages.new $DIR/www/packages
      rm -rf $DIR/www/packages.old"
  fi
fi
release=$(date -u +%Y%m%d%H%M%S)
tar -C "$build/release" -cf - . | remote "set -e
  mkdir $DIR/www/releases/$release
  tar -xf - -C $DIR/www/releases/$release
  ln -sfn releases/$release $DIR/www/current.new
  mv -T $DIR/www/current.new $DIR/www/current
  cd $DIR/www/releases && ls -1d */ | sort | head -n -3 | xargs -r rm -rf
  cd $DIR && docker compose up -d --quiet-pull
  # nginx.conf is replaced (new inode), which a running container does not see
  if ! docker exec herakles-web cat /etc/nginx/conf.d/default.conf 2>/dev/null | cmp -s - nginx.conf; then
    docker restart herakles-web >/dev/null
  fi"

# Announce the release only now that files, packages and site are online.
if [[ -e $build/downloads/latest.json ]]; then
  tar -C "$build/downloads" -cf - latest.json latest.json.sig | remote "tar -xf - -C $DIR/www/downloads"
fi

echo "== checking $URL"
for _ in $(seq 30); do
  if curl -fs "$URL/" 2>/dev/null | grep -q "Herakles $VERSION"; then
    curl -fsS "$URL/downloads/SHA256SUMS" | diff - "$build/downloads/SHA256SUMS" && echo "deployed $VERSION to $URL"
    exit 0
  fi
  sleep 2
done
echo "site did not come up with $VERSION" >&2
exit 1
