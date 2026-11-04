#!/bin/sh
# An alert channel: Nightjar sends each alert as JSON on standard input, as {"mode": "notify",
# "settings": {…}, "alert": {"title", "description", "severity", "fields", "host", "category",
# "dashboard_url", "time"}}. Exiting 0 means it was delivered; anything else is a failed delivery,
# with the last line of output as the reason.
set -eu

rank() {
  case $1 in
  critical) echo 3 ;;
  warning) echo 2 ;;
  *) echo 1 ;;
  esac
}
severity=${NIGHTJAR_ALERT_SEVERITY:-info}
if [ "$(rank "$severity")" -lt "$(rank "${NIGHTJAR_SETTING_MIN_SEVERITY:-info}")" ]; then
  cat >/dev/null
  exit 0
fi

file=${NIGHTJAR_SETTING_FILE:-alerts.jsonl}
# One alert a line: the input, without its line breaks.
tr -d '\n' >>"$file"
echo >>"$file"
