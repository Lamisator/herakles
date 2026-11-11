#!/bin/sh
# Stop the service only when the package is removed, not on upgrades:
# deb passes "remove"/"purge", rpm 0, pacman the old version (pre_remove).
case "$1" in
  remove|purge|0|[0-9]*.*)
    if [ -d /run/systemd/system ]; then
      systemctl disable --now herakles.service >/dev/null 2>&1 || true
    fi ;;
esac
exit 0
