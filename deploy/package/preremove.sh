#!/bin/sh
# Before removal: stop and disable the service. Not on an upgrade
# (deb: "upgrade", rpm: 1 package left).
set -e

case "$1" in
upgrade | failed-upgrade | deconfigure | 1) exit 0 ;;
esac

if [ -d /run/systemd/system ]; then
	systemctl disable --now schuhkarton.service >/dev/null 2>&1 || true
	# user services (systemctl --user) of logged-in or lingering users; their
	# enable links in ~/.config stay and are ignored without the unit file
	if command -v loginctl >/dev/null 2>&1; then
		for u in $(loginctl list-users --no-legend 2>/dev/null | awk '{print $2}'); do
			systemctl --user -M "$u@" stop schuhkarton.service >/dev/null 2>&1 || true
		done
	fi
elif [ -x /sbin/openrc-run ] && [ -x /etc/init.d/schuhkarton ]; then
	rc-service schuhkarton stop >/dev/null 2>&1 || true
	rc-update del schuhkarton >/dev/null 2>&1 || true
fi
exit 0
