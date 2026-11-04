#!/bin/sh
# A check: up while the host name in the check's target (dns-resolve:example.com) resolves.
# A check prints {"up": true|false, "detail": "…"}; it may instead exit 0 when up and non-zero
# when down, with its last line of output as the detail.
set -u

name=${NIGHTJAR_ARGUMENT:-}
case $name in
'') echo "give the check a host name, as dns-resolve:example.com"; exit 1 ;;
*[!A-Za-z0-9.-]*) echo "$name is not a host name"; exit 1 ;;
esac

addrs=$(getent ahosts "$name" 2>/dev/null | awk '{print $1}' | sort -u | tr '\n' ' ')
if [ -z "$addrs" ]; then
  printf '{"up":false,"detail":"%s does not resolve"}\n' "$name"
  exit 1
fi
first=${addrs%% *}
expect=${NIGHTJAR_SETTING_EXPECT:-}
if [ -n "$expect" ]; then
  case " $addrs" in
  *" $expect "*) ;;
  *) printf '{"up":false,"detail":"%s is %s, not %s"}\n' "$name" "$first" "$expect"; exit 1 ;;
  esac
  first=$expect
fi
printf '{"up":true,"detail":"%s is %s"}\n' "$name" "$first"
