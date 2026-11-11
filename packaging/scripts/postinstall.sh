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
if [ ! -e /etc/herakles/herakles.yaml ]; then
  echo "Herakles is installed. Next steps:"
  echo "  herakles tray --open        desktop: tray icon + interface (per user)"
  echo "  sudo herakles tui           server: set up /etc/herakles/herakles.yaml"
  echo "  sudo systemctl enable --now herakles   then run scheduled backups as a service"
fi
exit 0
