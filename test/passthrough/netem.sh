#!/usr/bin/env bash
# Apply/remove packet loss on a UDP port (or range) on the loopback interface.
# With host networking every leg (publisher->ingress, SFU->egress) crosses lo,
# so loss is scoped by port to avoid disturbing redis/API traffic.
#
# Usage:
#   sudo ./netem.sh apply 7885 5%             # single port (ingress RTC)
#   sudo ./netem.sh apply 50000-60000 5%      # port range (SFU RTC)
#   sudo ./netem.sh clear
#   ./netem.sh status
set -euo pipefail

DEV="${DEV:-lo}"

case "${1:-}" in
apply)
    PORTS="${2:?port or port range required}"
    LOSS="${3:-5%}"

    # root: prio qdisc, band 3 gets netem loss; filters select the target ports
    tc qdisc replace dev "$DEV" root handle 1: prio bands 4
    tc qdisc replace dev "$DEV" parent 1:4 handle 40: netem loss "$LOSS"

    add_filters() {
        local port="$1"
        # match both directions
        tc filter add dev "$DEV" protocol ip parent 1:0 prio 1 u32 \
            match ip protocol 17 0xff match ip dport "$port" 0xffff flowid 1:4
        tc filter add dev "$DEV" protocol ip parent 1:0 prio 1 u32 \
            match ip protocol 17 0xff match ip sport "$port" 0xffff flowid 1:4
    }

    if [[ "$PORTS" == *-* ]]; then
        START="${PORTS%-*}"
        END="${PORTS#*-}"
        # u32 has no native range match; use a mask-aligned walk
        port=$START
        while (( port <= END )); do
            # find the largest power-of-two block starting at $port within range
            span=1
            while (( (port % (span * 2)) == 0 && port + span * 2 - 1 <= END )); do
                span=$((span * 2))
            done
            mask=$(printf '0x%04x' $(( 0xffff & ~(span - 1) )))
            tc filter add dev "$DEV" protocol ip parent 1:0 prio 1 u32 \
                match ip protocol 17 0xff match ip dport "$port" "$mask" flowid 1:4
            tc filter add dev "$DEV" protocol ip parent 1:0 prio 1 u32 \
                match ip protocol 17 0xff match ip sport "$port" "$mask" flowid 1:4
            port=$((port + span))
        done
    else
        add_filters "$PORTS"
    fi
    echo "netem loss $LOSS applied on $DEV udp $PORTS at $(date +%s.%N)"
    ;;

clear)
    tc qdisc del dev "$DEV" root 2>/dev/null || true
    echo "netem cleared on $DEV at $(date +%s.%N)"
    ;;

status)
    tc -s qdisc show dev "$DEV"
    tc filter show dev "$DEV" 2>/dev/null | head -20
    ;;

*)
    echo "usage: $0 apply <port|start-end> [loss%] | clear | status" >&2
    exit 1
    ;;
esac
