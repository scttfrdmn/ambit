#!/bin/bash
# Launches with scripts/spore/launch.sh, trying GPU types, regions and AZs
# (spot first, then on-demand) until one has capacity. Same arguments as
# launch.sh. Prints "LAUNCHED <type> <region> <az> spot=<0|1>" on success.
set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 1
for spot in 1 0; do
	for type in g6.xlarge g5.xlarge g6.2xlarge g4dn.xlarge; do
		for region in us-east-1 us-west-2; do
			for az in $(aws ec2 describe-instance-type-offerings --region "$region" --location-type availability-zone \
				--filters "Name=instance-type,Values=$type" --query 'InstanceTypeOfferings[].Location' --output text); do
				out=$(AMBIT_SPOT=$spot AMBIT_REGION=$region AMBIT_AZ=$az AMBIT_INSTANCE=$type scripts/spore/launch.sh "$@" 2>&1)
				if grep -q "staged at" <<<"$out"; then
					echo "LAUNCHED $type $region $az spot=$spot"
					exit 0
				fi
			done
		done
	done
done
echo "NO CAPACITY"
exit 1
