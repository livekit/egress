#!/usr/bin/env bash
# render-avsync-stats.sh — render the avsync stats markdown summary.
#
# Reads per-job stats JSON files (one positional argument per file, or a
# shell glob like stats/*/avsync-stats.json), merges them into a single
# array, and prints the markdown summary to stdout. The summary has:
#   - An aggregate table by requestType (median / worst per metric)
#   - A collapsible per-output detail table sorted by score ascending
#
# Durations in the input JSON are float seconds (see DumpContentStats in
# test/content_stats.go); no parsing needed in jq.
#
# CI uses this by piping stdout into $GITHUB_STEP_SUMMARY:
#   .github/scripts/render-avsync-stats.sh stats/*/avsync-stats.json >> "$GITHUB_STEP_SUMMARY"
#
# Locally you can run it the same way and pipe to less or a file to
# preview the rendered markdown without round-tripping through CI.

set -euo pipefail

if [ "$#" -eq 0 ]; then
  echo "usage: $0 <avsync-stats.json> [...]" >&2
  exit 2
fi

# Merge all per-job arrays into one. Fall back to [] if any input is
# empty/missing so the no-stats branch below still has something to read.
merged=$(mktemp)
trap 'rm -f "$merged"' EXIT
jq -s '[.[][]]' "$@" > "$merged" 2>/dev/null || echo "[]" > "$merged"

rowCount=$(jq 'length' "$merged")
if [ "$rowCount" = "0" ]; then
  echo "## avsync stats summary"
  echo
  echo "_no stats collected_"
  exit 0
fi

# ---- aggregate by requestType (median / worst per metric) -------------
aggregate=$(jq -r '
  def fmtDur(seconds):
    if seconds == null then "-"
    else "\(seconds * 1000 | round)ms" end;

  def fmtSigned(seconds):
    if seconds == null then "-"
    elif seconds >= 0 then "+\(seconds * 1000 | round)ms"
    else "\(seconds * 1000 | round)ms" end;

  def medianAbs(field):
    [.[] | .[field] | select(. != null) | fabs] | sort as $v |
    if ($v | length) == 0 then null
    else $v[($v | length / 2 | floor)] end;

  def maxAbs(field):
    [.[] | .[field] | select(. != null) | fabs] | sort as $v |
    if ($v | length) == 0 then null
    else $v[-1] end;

  def medianScore: [.[] | .score] | sort | .[(length / 2 | floor)];
  def worstScore:  [.[] | .score] | min;

  # Signed-median: pick the original-sign value whose abs equals the median abs.
  def medianSigned(field):
    [.[] | .[field] | select(. != null)] as $vs |
    if ($vs | length) == 0 then null
    else
      ($vs | map(fabs) | sort | .[(length / 2 | floor)]) as $m |
      ($vs | map(select((. | fabs) == $m)))[0]
    end;

  # Worst-signed: max-abs value with its sign preserved.
  def worstSigned(field):
    [.[] | .[field] | select(. != null)] as $vs |
    if ($vs | length) == 0 then null
    else
      ($vs | map(fabs) | max) as $m |
      ($vs | map(select((. | fabs) == $m)))[0]
    end;

  def tracksRange:
    [.[] | .tracks] | unique as $u |
    if ($u | length) == 1 then ($u[0] | tostring)
    else "\($u | min)-\($u | max)" end;

  group_by(.requestType) | map({
    requestType: .[0].requestType,
    source:      .[0].source,
    outputs:     length,
    tracks:      tracksRange,
    medianScore: medianScore,
    worstScore:  worstScore,
    medianTimeToStable:      medianAbs("timeToStable"),
    worstTimeToStable:       maxAbs("timeToStable"),
    medianAudioJitter:       medianAbs("audioJitter"),
    worstAudioJitter:        maxAbs("audioJitter"),
    medianVideoJitter:       medianAbs("videoJitter"),
    worstVideoJitter:        maxAbs("videoJitter"),
    medianAVSync:            medianSigned("avSync"),
    worstAVSync:             worstSigned("avSync"),
    medianAVSyncStdDev:      medianAbs("avSyncStdDev"),
    worstAVSyncStdDev:       maxAbs("avSyncStdDev"),
    medianMaxAVSync:         medianAbs("maxAVSync"),
    worstMaxAVSync:          maxAbs("maxAVSync"),
  }) | sort_by(.worstScore) |
  (
    "| requestType | source | outputs | tracks | score | timeToStable | audioJitter | videoJitter | avSync | avSyncStdDev | maxAVSync |",
    "|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|",
    (.[] |
      "| \(.requestType) | \(.source) | \(.outputs) | \(.tracks) " +
      "| \(.medianScore) / \(.worstScore) " +
      "| \(fmtDur(.medianTimeToStable)) / \(fmtDur(.worstTimeToStable)) " +
      "| \(fmtDur(.medianAudioJitter)) / \(fmtDur(.worstAudioJitter)) " +
      "| \(fmtDur(.medianVideoJitter)) / \(fmtDur(.worstVideoJitter)) " +
      "| \(fmtSigned(.medianAVSync)) / \(fmtSigned(.worstAVSync)) " +
      "| \(fmtDur(.medianAVSyncStdDev)) / \(fmtDur(.worstAVSyncStdDev)) " +
      "| \(fmtDur(.medianMaxAVSync)) / \(fmtDur(.worstMaxAVSync)) |"
    )
  )
' "$merged")

# ---- per-output detail table (sorted by score asc) --------------------
detail=$(jq -r '
  def fmtDur:
    if . == null then "-"
    else "\(. * 1000 | round)ms" end;

  def fmtSigned:
    if . == null then "-"
    elif . >= 0 then "+\(. * 1000 | round)ms"
    else "\(. * 1000 | round)ms" end;

  def fmtOutput:
    if .format == "" or .format == null then (.output | ascii_upcase)
    else .format | ascii_upcase
    end;

  sort_by(.score) |
  (
    "| test | requestType | source | output | tracks | score | timeToStable | audioJitter | videoJitter | avSync | avSyncStdDev | maxAVSync |",
    "|---|---|---|---|---:|---:|---:|---:|---:|---:|---:|---:|",
    (.[] |
      "| \(.test) | \(.requestType) | \(.source) | \(fmtOutput) | \(.tracks) | \(.score) " +
      "| \(.timeToStable | fmtDur) " +
      "| \(.audioJitter | fmtDur) " +
      "| \(.videoJitter | fmtDur) " +
      "| \(.avSync | fmtSigned) " +
      "| \(.avSyncStdDev | fmtDur) " +
      "| \(.maxAVSync | fmtDur) |"
    )
  )
' "$merged")

echo "## avsync stats summary"
echo
echo "Aggregate by \`requestType\`. Each metric cell is \`median / worst\`; lower is better for all metrics except \`score\` (where worst = minimum). \`tracks\` is the input track count: participants × (audio + video). Sorted by worst score ascending."
echo
echo "$aggregate"
echo
echo "<details>"
echo "<summary>Per-output detail (${rowCount} rows)</summary>"
echo
echo "$detail"
echo
echo "</details>"
