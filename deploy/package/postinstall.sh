#!/bin/sh
# After install and upgrade (deb, rpm, apk, archlinux): create the service
# user, reload systemd and restart running services (system and user) with the
# new binary.
set -e

if ! getent passwd schuhkarton >/dev/null 2>&1; then
	if command -v systemd-sysusers >/dev/null 2>&1 && [ -f /usr/lib/sysusers.d/schuhkarton.conf ]; then
		systemd-sysusers /usr/lib/sysusers.d/schuhkarton.conf
	elif command -v useradd >/dev/null 2>&1; then
		useradd --system --user-group --home-dir /var/lib/schuhkarton --no-create-home \
			--shell /usr/sbin/nologin --comment "Schuhkarton photo gallery" schuhkarton
	else # busybox (Alpine)
		addgroup -S schuhkarton 2>/dev/null || true
		adduser -S -D -H -h /var/lib/schuhkarton -s /sbin/nologin -G schuhkarton \
			-g "Schuhkarton photo gallery" schuhkarton
	fi
fi

# Running user services (systemctl --user) of logged-in or lingering users.
user_services() {
	command -v loginctl >/dev/null 2>&1 || return 0
	for u in $(loginctl list-users --no-legend 2>/dev/null | awk '{print $2}'); do
		systemctl --user -M "$u@" daemon-reload >/dev/null 2>&1 || continue
		systemctl --user -M "$u@" try-restart schuhkarton.service >/dev/null 2>&1 || true
	done
}

if [ -d /run/systemd/system ]; then
	systemctl daemon-reload >/dev/null 2>&1 || true
	user_services
	if systemctl is-active --quiet schuhkarton.service; then
		systemctl restart schuhkarton.service || true
	else
		cat <<'MSG'

Schuhkarton is installed. Start it as a system service:
  sudo systemctl enable --now schuhkarton
  journalctl -u schuhkarton        # the setup link
Settings: /etc/default/schuhkarton, data: /var/lib/schuhkarton
Or with your own account: systemctl --user enable --now schuhkarton

MSG
	fi
elif [ -x /sbin/openrc-run ] && [ -x /etc/init.d/schuhkarton ]; then
	if rc-service schuhkarton status >/dev/null 2>&1; then
		rc-service schuhkarton restart || true
	else
		cat <<'MSG'

Schuhkarton is installed. Start it:
  rc-update add schuhkarton && rc-service schuhkarton start
  cat /var/log/schuhkarton.log     # the setup link

MSG
	fi
fi
exit 0
