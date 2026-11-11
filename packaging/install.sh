#!/bin/sh
# Install Herakles from https://herakles.kloeck-it.de and keep it updated
# with the system's package manager:
#
#   curl -fsSL https://herakles.kloeck-it.de/install.sh | sudo sh
#
# Debian/Ubuntu (apt), Fedora/RHEL/Rocky/Alma (dnf), openSUSE (zypper) and
# Arch Linux (pacman). Adds the package repository and its signing key,
# then installs the package "herakles".
set -eu

BASE=https://herakles.kloeck-it.de
KEY_FPR=1035012D5247A80FD7988DFDE64EFE5644EE8226

say() { printf '\033[1m==> %s\033[0m\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" = 0 ] || die "run as root, e.g.: curl -fsSL $BASE/install.sh | sudo sh"
fetch() {
  if command -v curl >/dev/null 2>&1; then curl -fsSL "$1"
  elif command -v wget >/dev/null 2>&1; then wget -qO- "$1"
  else die "neither curl nor wget found"; fi
}

if command -v apt-get >/dev/null 2>&1; then
  say "Adding the Herakles apt repository"
  install -d -m 0755 /etc/apt/keyrings
  fetch "$BASE/herakles.gpg" > /etc/apt/keyrings/herakles.gpg
  chmod 0644 /etc/apt/keyrings/herakles.gpg
  echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/herakles.gpg] $BASE/apt stable main" \
    > /etc/apt/sources.list.d/herakles.list
  apt-get update -o Dir::Etc::sourcelist=sources.list.d/herakles.list -o Dir::Etc::sourceparts=- -o APT::Get::List-Cleanup=0
  say "Installing herakles"
  DEBIAN_FRONTEND=noninteractive apt-get install -y herakles
elif command -v dnf >/dev/null 2>&1 || command -v yum >/dev/null 2>&1; then
  say "Adding the Herakles rpm repository"
  fetch "$BASE/rpm/herakles.repo" > /etc/yum.repos.d/herakles.repo
  rpm --import "$BASE/herakles.asc"
  say "Installing herakles"
  if command -v dnf >/dev/null 2>&1; then dnf install -y herakles; else yum install -y herakles; fi
elif command -v zypper >/dev/null 2>&1; then
  say "Adding the Herakles rpm repository"
  rpm --import "$BASE/herakles.asc"
  zypper --non-interactive removerepo herakles >/dev/null 2>&1 || true
  zypper --non-interactive addrepo --refresh "$BASE/rpm/herakles.repo"
  say "Installing herakles"
  zypper --non-interactive --gpg-auto-import-keys install herakles
elif command -v pacman >/dev/null 2>&1; then
  say "Adding the Herakles pacman repository"
  fetch "$BASE/herakles.asc" | pacman-key --add -
  pacman-key --lsign-key "$KEY_FPR"
  if ! grep -q '^\[herakles\]' /etc/pacman.conf; then
    printf '\n[herakles]\nServer = %s/arch/$arch\n' "$BASE" >> /etc/pacman.conf
  fi
  say "Installing herakles"
  pacman -Sy --noconfirm herakles
else
  die "no supported package manager found (apt, dnf, yum, zypper, pacman); download the binary from $BASE"
fi

say "Done: $(herakles version 2>/dev/null || echo herakles)"
echo "Desktop: start \"Herakles\" from the menu or run 'herakles tray --open'."
echo "Server:  'sudo herakles tui' to set up /etc/herakles/herakles.yaml, then 'sudo systemctl enable --now herakles'."
