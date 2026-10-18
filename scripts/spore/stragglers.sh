#!/bin/bash
# Straggler check after a spore.host run: anything still running or left
# behind under project=ambit. Read-only. Exits 1 if anything is found.
set -uo pipefail
found=0
running=$(spawn list --tag project=ambit --state running 2>/dev/null | grep -v -E '^(No |NAME|$)' || true)
if [ -n "$running" ]; then
	echo "running instances tagged project=ambit:"
	echo "$running"
	found=1
fi
spawn orphans --all-regions 2>/dev/null | grep -i ambit && found=1
[ $found -eq 0 ] && echo "no stragglers"
exit $found
