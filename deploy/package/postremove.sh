#!/bin/sh
# After removal. Settings, accounts and cache in /var/lib/schuhkarton and the
# user "schuhkarton" stay; remove them by hand when no longer needed.
set -e

if [ -d /run/systemd/system ]; then
	systemctl daemon-reload >/dev/null 2>&1 || true
fi
exit 0
