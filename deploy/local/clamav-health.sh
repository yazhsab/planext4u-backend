#!/bin/sh
set -eu

config=/etc/clamav/planext4u.conf
clamdscan --config-file="$config" --stream --no-summary "$config" >/dev/null
version=$(clamdscan --config-file="$config" --version)
case "$version" in
  'ClamAV '*/*/*) ;;
  *) exit 1 ;;
esac
loaded_at=${version##*/}
loaded_epoch=$(date -u -D '%a %b %e %H:%M:%S %Y' -d "$loaded_at" +%s)
now_epoch=$(date -u +%s)
age_seconds=$((now_epoch - loaded_epoch))
[ "$age_seconds" -ge -300 ] && [ "$age_seconds" -le 172800 ]
