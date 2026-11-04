#!/bin/sh
# A card on the overview: the load averages, how many processes there are and who is signed in.
# A card prints {"title", "text", "items": [{"label", "value", "tone", "hint"}], "link"}.
set -eu

read -r one five fifteen procs _ </proc/loadavg
running=${procs%/*}
total=${procs#*/}
cores=$(nproc 2>/dev/null || echo 1)
users=$(who 2>/dev/null | awk '{print $1}' | sort -u | wc -l | tr -d ' ')

# The 1-minute load warns above one per core, and is critical above twice that.
tone=$(awk -v l="$one" -v c="$cores" 'BEGIN { if (l > 2 * c) print "crit"; else if (l > c) print "warn"; else print "" }')

printf '{"title":"System load","items":[' 
printf '{"label":"1 min","value":"%s","tone":"%s","hint":"%s cores"},' "$one" "$tone" "$cores"
printf '{"label":"5 min","value":"%s"},' "$five"
printf '{"label":"15 min","value":"%s"},' "$fifteen"
printf '{"label":"Processes","value":"%s","hint":"%s running now"},' "$total" "$running"
printf '{"label":"Signed in","value":"%s"}' "$users"
printf ']}\n'
