#!/bin/sh
# Install Herakles from https://herakles.kloeck-it.de and keep it updated
# with the system's package manager:
#
#   curl -fsSL https://herakles.kloeck-it.de/install.sh | sudo sh
#
# Debian/Ubuntu (apt), Fedora/RHEL/Rocky/Alma (dnf), openSUSE (zypper) and
# Arch Linux (pacman). Adds the package repository and its signing key,
# installs the package "herakles", then asks how Herakles will be used and
# sets it up:
#
#   desktop   the window and tray icon on your desktop open the system
#             service, which runs the backups as root (whole disks, all files)
#   user      desktop, only your own files: Herakles runs as your user
#   server    no desktop: backup wizard in the terminal, then the service
#   none      install only
#
# Without a terminal (or to skip the questions) set HERAKLES_MODE to one of
# these, and HERAKLES_USER to the desktop user if sudo did not set SUDO_USER:
#
#   curl -fsSL https://herakles.kloeck-it.de/install.sh | sudo HERAKLES_MODE=server sh
set -eu

BASE=https://herakles.kloeck-it.de
KEY_FPR=1035012D5247A80FD7988DFDE64EFE5644EE8226

say() { printf '\033[1m==> %s\033[0m\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

export HERAKLES_INSTALLER=1   # the package's own hints are replaced by the questions below
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

say "Installed: $(herakles version 2>/dev/null || echo herakles)"

# ---------- setup ----------

# The script usually arrives through a pipe, so questions go to the terminal.
TTY=""
if [ -r /dev/tty ] && [ -w /dev/tty ] && (exec </dev/tty) 2>/dev/null; then TTY=/dev/tty; fi
ask() { # ask "question" default -> REPLY
  REPLY=""
  if [ -n "$TTY" ]; then
    printf '%s ' "$1" > "$TTY"
    read -r REPLY < "$TTY" || REPLY=""
  fi
  [ -n "$REPLY" ] || REPLY=$2
}
is_yes() { case "$1" in [Yy]*|[Jj]*) return 0 ;; *) return 1 ;; esac; }
has_systemd() { [ -d /run/systemd/system ]; }
as_user() { # as_user NAME command...: run as the desktop user with their HOME
  u=$1; shift
  home=$(getent passwd "$u" | cut -d: -f6)
  if command -v runuser >/dev/null 2>&1; then
    runuser -u "$u" -- env HOME="$home" USER="$u" LOGNAME="$u" "$@"
  else
    su -s /bin/sh "$u" -c 'HOME="$1"; export HOME; shift; exec "$@"' sh "$home" "$@"
  fi
}

# A desktop is likely if a graphical session type or display manager exists.
default_mode=server
if [ -n "${SUDO_USER:-}" ] && [ "${SUDO_USER}" != root ] && { ls /usr/share/xsessions/*.desktop >/dev/null 2>&1 || ls /usr/share/wayland-sessions/*.desktop >/dev/null 2>&1; }; then
  default_mode=desktop
fi

MODE=${HERAKLES_MODE:-}
if [ -z "$MODE" ]; then
  if [ -z "$TTY" ]; then
    MODE=none
  else
    cat > "$TTY" <<EOT

How will you use Herakles on this computer?
  1) Desktop: the Herakles window and tray icon. Backups run as root in the
     system service, so they can include whole disks and every file, also
     when nobody is logged in.                                  (recommended)
  2) Desktop, only my own files: Herakles runs as your user while you are
     logged in.
  3) Server without a desktop: set up a backup in the terminal, then run it
     as a system service.
  4) Nothing now: just install.
EOT
    d=1; [ "$default_mode" = server ] && d=3
    ask "Choose 1-4 [$d]:" "$d"
    case "$REPLY" in 1) MODE=desktop ;; 2) MODE=user ;; 3) MODE=server ;; *) MODE=none ;; esac
  fi
fi

desktop_user() {
  DUSER=${HERAKLES_USER:-${SUDO_USER:-}}
  [ "$DUSER" = root ] && DUSER=""
  if [ -z "$DUSER" ]; then
    ask "Which user logs in to the desktop?" ""
    DUSER=$REPLY
  fi
  [ -n "$DUSER" ] && getent passwd "$DUSER" >/dev/null || die "unknown desktop user '${DUSER}'; set HERAKLES_USER"
}

case "$MODE" in
desktop)
  has_systemd || die "the system service needs systemd; choose 'user' (HERAKLES_MODE=user) instead"
  desktop_user
  say "Sharing the system service with $DUSER"
  herakles daemon desktop on --user "$DUSER" >/dev/null
  ask "Show the Herakles icon in the tray after every login of $DUSER? [Y/n]" y
  if is_yes "$REPLY"; then as_user "$DUSER" herakles tray autostart on >/dev/null && echo "Tray icon: on at login."; fi
  cat <<EOT

Herakles is ready. As $DUSER, open "Herakles" from the applications menu
(or run: herakles gui). The backup wizard guides you through the first backup;
it can also back up whole disks. Backups run in the service herakles.service.
Others can be allowed to use it with: sudo herakles daemon desktop on --user NAME
EOT
  ;;
user)
  desktop_user
  ask "Start Herakles in the tray after every login of $DUSER? [Y/n]" y
  if is_yes "$REPLY"; then as_user "$DUSER" herakles tray autostart on >/dev/null && echo "Tray icon: on at login."; fi
  cat <<EOT

Herakles is ready. As $DUSER, open "Herakles" from the applications menu
(or run: herakles tray --open). The backup wizard guides you through the first
backup. Scheduled backups run while $DUSER is logged in.
For whole disks or system files later: sudo herakles daemon desktop on
EOT
  ;;
server)
  has_systemd || die "the system service needs systemd; run 'herakles daemon run' under your init system"
  if [ -n "$TTY" ]; then
    ask "Set up a backup now with the wizard in this terminal? [Y/n]" y
    if is_yes "$REPLY"; then
      echo "Starting the wizard. When it is done, press q to leave the terminal interface."
      sleep 2
      herakles --config /etc/herakles/herakles.yaml tui --wizard < "$TTY" > "$TTY" 2>&1 || true
    fi
  fi
  if [ -e /etc/herakles/herakles.yaml ] && herakles --config /etc/herakles/herakles.yaml config validate >/dev/null 2>&1; then
    say "Enabling and starting the service"
    systemctl enable --now herakles.service
    sleep 2
    herakles --config /etc/herakles/herakles.yaml daemon status 2>/dev/null | head -n 8 || true
    cat <<EOT

Backups run on schedule in herakles.service (configuration /etc/herakles/herakles.yaml).
  sudo herakles tui                    dashboard, jobs, browse and restore
  sudo herakles daemon status          schedule and last results
  Web interface with logins: see https://herakles.kloeck-it.de/guide.html#web
EOT
  else
    cat <<EOT

No configuration yet. Set up a backup with 'sudo herakles tui --wizard',
then start the service: sudo systemctl enable --now herakles
EOT
  fi
  ;;
*)
  cat <<EOT

Next steps:
  Desktop:  sudo herakles daemon desktop on, then open "Herakles" from the menu
            (backups run as root in the system service: whole disks, all files)
  Desktop, only your own files: open "Herakles" from the menu or 'herakles tray --open'
  Server:   sudo herakles tui --wizard, then sudo systemctl enable --now herakles
Or run this script again in a terminal to be asked.
EOT
  ;;
esac
