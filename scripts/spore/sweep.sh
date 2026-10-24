#!/bin/bash
# Terminates every live instance of one run, in every region it could be in:
#
#   scripts/spore/sweep.sh <name> [region ...]
#
# An instance is the run's if it is tagged project=ambit and run=<name>.
# The regions default to AMBIT_REGIONS, else us-east-1 and us-west-2 (the
# ones try-launch.sh tries). Used after a launch attempt that reported failure
# (spawn can create the instance and still fail, and nothing else then
# watches it), and by the dead-man watcher. Prints one line per instance
# terminated; exits 0 whether or not it found any.
set -uo pipefail
NAME="$1"
shift
REGIONS=("$@")
[ ${#REGIONS[@]} -gt 0 ] || read -r -a REGIONS <<<"${AMBIT_REGIONS:-us-east-1 us-west-2}"
seen=" "
for region in "${REGIONS[@]}"; do
	case "$seen" in *" $region "*) continue ;; esac
	seen="$seen$region "
	ids=$(aws ec2 describe-instances --region "$region" \
		--filters "Name=tag:project,Values=ambit" "Name=tag:run,Values=$NAME" \
		"Name=instance-state-name,Values=pending,running,stopping,stopped" \
		--query 'Reservations[].Instances[].InstanceId' --output text) || {
		echo "$(date -u +%FT%TZ) $NAME: cannot list instances in $region" >&2
		continue
	}
	for id in $ids; do
		aws ec2 terminate-instances --region "$region" --instance-ids "$id" --output text >/dev/null &&
			echo "$(date -u +%FT%TZ) $NAME ($id, $region) terminated"
	done
done
