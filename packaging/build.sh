#!/usr/bin/env bash
# Build signed Linux packages (deb, rpm, pacman) and the repositories that
# serve them from https://herakles.kloeck-it.de:
#
#   packaging/build.sh <version tag> <dist dir> <output dir>
#
# <dist dir> holds herakles-<tag>-linux-{amd64,arm64,arm} from `make dist`.
# The output gets apt/, rpm/, arch/, herakles.asc, herakles.gpg and
# install.sh. Signing key: $SIGNING_DIR (default .herakles-signing, never
# committed) with gnupg/ and private.asc.
set -euo pipefail
cd "$(dirname "$0")/.."

TAG=$1 DIST=$2 OUT=$3
if [[ ! $TAG =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "packages are only built for release tags (vX.Y.Z), not $TAG" >&2
  exit 1
fi
VER=${TAG#v}
SIGNING_DIR=$(realpath "${SIGNING_DIR:-.herakles-signing}")
export GNUPGHOME=$SIGNING_DIR/gnupg
KEY=$(gpg --list-secret-keys --with-colons | awk -F: '/^fpr/{print $10; exit}')
[ -n "$KEY" ] || { echo "no signing key in $GNUPGHOME" >&2; exit 1; }
gpgs() { gpg --batch --yes --quiet --local-user "$KEY" "$@"; }

NFPM=(go run github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.48.0)
CACHE=${XDG_CACHE_HOME:-$HOME/.cache}/herakles-packaging
if [ ! -x "$CACHE/venv/bin/createrepo_c" ]; then
  python3 -m venv "$CACHE/venv"
  "$CACHE/venv/bin/pip" install -q createrepo_c==1.2.6
fi

rm -rf "$OUT"
mkdir -p "$OUT"/{apt/pool/main/h/herakles,rpm,arch/x86_64,arch/aarch64,arch/armv7h}
gen=$(mktemp -d)
trap 'rm -rf "$gen"' EXIT
host=$DIST/herakles-$TAG-linux-amd64
for sh in bash zsh fish; do "$host" completion $sh > "$gen/herakles.$sh"; done

declare -A archdir=([amd64]=x86_64 [arm64]=aarch64 [arm7]=armv7h)
for a in amd64 arm64 arm7; do
  bin=$DIST/herakles-$TAG-linux-${a%7}
  for p in deb rpm archlinux; do
    sed -e "s|\${PKG_ARCH}|$a|; s|\${PKG_VERSION}|$VER|; s|\${PKG_BINARY}|$(realpath "$bin")|; s|\${PKG_GEN}|$gen|g; s|\${PKG_SIGN_KEY}|$SIGNING_DIR/private.asc|" \
      packaging/nfpm.yaml > "$gen/nfpm.yaml"
    "${NFPM[@]}" package -f "$gen/nfpm.yaml" -p $p -t "$gen/" >/dev/null
  done
  mv "$gen"/*.deb "$OUT/apt/pool/main/h/herakles/"
  mv "$gen"/*.rpm "$OUT/rpm/"
  mv "$gen"/*.pkg.tar.zst "$OUT/arch/${archdir[$a]}/"
done

# apt: Packages + Release, signed (InRelease and Release.gpg)
go run ./packaging/repotool apt "$OUT/apt"
gpgs --clearsign -o "$OUT/apt/dists/stable/InRelease" "$OUT/apt/dists/stable/Release"
gpgs --detach-sign --armor -o "$OUT/apt/dists/stable/Release.gpg" "$OUT/apt/dists/stable/Release"

# rpm: packages are signed by nfpm; repodata is signed for repo_gpgcheck
"$CACHE/venv/bin/createrepo_c" -q "$OUT/rpm" 2> >(grep -v rpmrc >&2)
gpgs --detach-sign --armor -o "$OUT/rpm/repodata/repomd.xml.asc" "$OUT/rpm/repodata/repomd.xml"
cat > "$OUT/rpm/herakles.repo" <<REPO
[herakles]
name=Herakles
baseurl=https://herakles.kloeck-it.de/rpm
enabled=1
gpgcheck=1
repo_gpgcheck=1
gpgkey=https://herakles.kloeck-it.de/herakles.asc
REPO

# pacman: detached package signatures, then the database (signed too)
for d in "$OUT"/arch/*/; do
  for p in "$d"*.pkg.tar.zst; do gpgs --detach-sign -o "$p.sig" "$p"; done
  go run ./packaging/repotool pacman "$d"
  for db in herakles.db herakles.db.tar.gz; do gpgs --detach-sign -o "$d$db.sig" "$d$db"; done
done

gpg --armor --export "$KEY" > "$OUT/herakles.asc"
gpg --export "$KEY" > "$OUT/herakles.gpg"
cp packaging/install.sh "$OUT/install.sh"
echo "packages for $TAG:"
(cd "$OUT" && find . -name '*.deb' -o -name '*.rpm' -o -name '*.pkg.tar.zst' | sort)
