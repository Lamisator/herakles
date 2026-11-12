#!/bin/sh
# Runs after installing or upgrading (deb: "configure", rpm: 1/2,
# pacman: post_install/post_upgrade).
mkdir -p /etc/herakles
chmod 700 /etc/herakles
if [ -d /run/systemd/system ]; then
  systemctl daemon-reload >/dev/null 2>&1 || true
  # Upgrade: a running daemon picks up the new version.
  systemctl try-restart herakles.service >/dev/null 2>&1 || true
fi
# install.sh asks and sets up itself.
if [ ! -e /etc/herakles/herakles.yaml ] && [ -z "${HERAKLES_INSTALLER:-}" ]; then
  echo "Herakles is installed. Next steps:"
  echo "  sudo herakles daemon desktop on   desktop: backups run as a root service (whole disks,"
  echo "                                    all files); open them from the \"Herakles\" menu entry"
  echo "  herakles tray --open              desktop, only your own files: runs as your user"
  echo "  sudo herakles tui                 server: set up /etc/herakles/herakles.yaml, then"
  echo "  sudo systemctl enable --now herakles"
fi
exit 0
