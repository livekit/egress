#!/usr/bin/env bash
# Sample egress CPU usage to CSV and print the average.
# Measures both the container total (docker stats) and the per-handler process
# (/proc inside the container), since one service process can host multiple
# handler subprocesses.
#
# Usage: ./measure-cpu.sh <output.csv> [duration_seconds] [container]
# Container defaults to $EGRESS_CONTAINER or "egress".
set -euo pipefail

OUT="${1:?output csv required}"
DURATION="${2:-1800}"
CONTAINER="${3:-${EGRESS_CONTAINER:-egress}}"
INTERVAL=5

echo "timestamp,container_cpu_pct,handler_cpu_pct,handler_pids" > "$OUT"

read_handler_jiffies() {
    # sum utime+stime for every run-handler process (and children via their own entries)
    docker exec "$CONTAINER" sh -c '
        total=0; pids=""
        for pid in $(ls /proc | grep -E "^[0-9]+$"); do
            if grep -qa "run-handler" /proc/$pid/cmdline 2>/dev/null; then
                stat=$(cat /proc/$pid/stat 2>/dev/null) || continue
                set -- $stat
                total=$((total + $14 + $15))
                pids="$pids $pid"
            fi
        done
        cpu_total=$(awk "/^cpu /{sum=0; for(i=2;i<=NF;i++) sum+=\$i; print sum}" /proc/stat)
        echo "$total $cpu_total $pids"
    '
}

NCPU=$(docker exec "$CONTAINER" nproc)
END=$(( $(date +%s) + DURATION ))
prev=$(read_handler_jiffies)

echo "sampling $CONTAINER every ${INTERVAL}s for ${DURATION}s -> $OUT"
while (( $(date +%s) < END )); do
    sleep "$INTERVAL"
    cur=$(read_handler_jiffies)

    prev_h=$(echo "$prev" | awk '{print $1}')
    prev_t=$(echo "$prev" | awk '{print $2}')
    cur_h=$(echo "$cur" | awk '{print $1}')
    cur_t=$(echo "$cur" | awk '{print $2}')
    pids=$(echo "$cur" | cut -d' ' -f3- | tr -s ' ' ';')

    handler_pct=$(awk -v h=$((cur_h - prev_h)) -v t=$((cur_t - prev_t)) -v n="$NCPU" \
        'BEGIN { if (t > 0) printf "%.2f", 100 * n * h / t; else print "0" }')
    container_pct=$(docker stats --no-stream --format '{{.CPUPerc}}' "$CONTAINER" | tr -d '%')

    echo "$(date +%s),$container_pct,$handler_pct,$pids" >> "$OUT"
    prev="$cur"
done

echo "--- summary ($OUT) ---"
awk -F, 'NR>1 { c+=$2; h+=$3; n++ } END {
    if (n > 0) printf "samples: %d\navg container cpu: %.2f%%\navg handler cpu: %.2f%%\n", n, c/n, h/n
}' "$OUT"
