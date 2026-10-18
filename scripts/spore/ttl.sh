# ttl_minutes prints a spawn duration (2d12h, 8h, 1h30m, 90m) or a bare
# number of minutes as whole minutes, or fails. Sourced by launch.sh and
# deadman.sh so both read a TTL the same way.
ttl_minutes() {
	local s="$1" d=0 h=0 m=0
	if [[ "$s" =~ ^[0-9]+$ ]]; then echo $((10#$s)); return 0; fi
	[[ "$s" =~ ^([0-9]+d)?([0-9]+h)?([0-9]+m)?$ ]] || { echo "cannot parse TTL '$s'" >&2; return 1; }
	[[ "$s" =~ ([0-9]+)d ]] && d=$((10#${BASH_REMATCH[1]}))
	[[ "$s" =~ ([0-9]+)h ]] && h=$((10#${BASH_REMATCH[1]}))
	[[ "$s" =~ ([0-9]+)m ]] && m=$((10#${BASH_REMATCH[1]}))
	if [ $((d + h + m)) -eq 0 ]; then echo "cannot parse TTL '$s'" >&2; return 1; fi
	echo $((d * 1440 + h * 60 + m))
}
