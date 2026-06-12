#!/usr/bin/env bash
# runner.sh — run `atest grafana` for ONE (expr, datasource) over a window,
# handling transient failures and cardinality/step aborts WITHOUT giving up the
# whole batch. Always exits 0; the caller reads the final RESULT line.
#
# The bearer token is taken from the environment (ATEST_GRAFANA_BEARER_TOKEN);
# never pass it as an argument.
#
# Usage:
#   runner.sh \
#     --atest ./atest \
#     --grafana-url https://...grafana.azure.com \
#     --datasource hcps-uksouth \
#     --expr '<promql, threshold inline>' \
#     --for 5m,10m,15m,30m \
#     --from 2026-06-01T00:00:00Z --to 2026-06-08T00:00:00Z \
#     --log run/<batch>/<NN>-<Alert>-<region>.log \
#     [--incident-group-by cluster,namespace] \
#     [--label '<Alert> @ uksouth'] \
#     [--delay-resolution-by 5m] \
#     [--chunk-size 1h] \
#     [--max-transient-retries 3] \
#     [-- <extra atest args...>]
#
# Final line on stdout (machine-parseable):
#   RESULT <OK|NODATA|FAILED> label=<…> log=<path> [reason=<…>] [escalations=<…>]
set -u

atest="" url="" ds="" expr="" fors="" from="" to="" log="" gb=""
label="" delay="5m" chunk="1h" max_transient=3
extra=()

while [ $# -gt 0 ]; do
  case "$1" in
    --atest) atest="$2"; shift 2;;
    --grafana-url) url="$2"; shift 2;;
    --datasource) ds="$2"; shift 2;;
    --expr) expr="$2"; shift 2;;
    --for) fors="$2"; shift 2;;
    --from) from="$2"; shift 2;;
    --to) to="$2"; shift 2;;
    --log) log="$2"; shift 2;;
    --incident-group-by) gb="$2"; shift 2;;
    --label) label="$2"; shift 2;;
    --delay-resolution-by) delay="$2"; shift 2;;
    --chunk-size) chunk="$2"; shift 2;;
    --max-transient-retries) max_transient="$2"; shift 2;;
    --) shift; extra=("$@"); break;;
    *) echo "runner.sh: unknown arg: $1" >&2; shift;;
  esac
done

[ -z "$label" ] && label="$ds"

fail_hard() { # config error in how runner was called — not an alert failure
  echo "RESULT FAILED label=${label} log=${log:-none} reason=$1"
  exit 0
}
notice() {
  echo "runner.sh: $*" >&2
}
last_error() {
  grep -m1 -E '^error:' "$log" | sed 's/^error: //' | cut -c1-160
}
[ -n "$atest" ] || fail_hard "missing --atest"
[ -n "$url" ]   || fail_hard "missing --grafana-url"
[ -n "$ds" ]    || fail_hard "missing --datasource"
[ -n "$expr" ]  || fail_hard "missing --expr"
[ -n "$from" ]  || fail_hard "missing --from"
[ -n "$to" ]    || fail_hard "missing --to"
[ -n "$log" ]   || fail_hard "missing --log"
[ -n "$fors" ]  || fors="5m,10m,15m,30m"
[ -n "${ATEST_GRAFANA_BEARER_TOKEN:-}" ] || fail_hard "missing ATEST_GRAFANA_BEARER_TOKEN"

mkdir -p "$(dirname "$log")"

# Halve a prometheus-style duration. Floor at 10m.
halve_chunk() {
  case "$1" in
    2h)     echo "1h";;
    1h|60m) echo "30m";;
    30m)    echo "15m";;
    15m)    echo "10m";;
    *)      echo "";;   # already at 10m floor
  esac
}

allow_card=0
escalations=""

run_once() {
  local args=(grafana
    --grafana-url "$url"
    --datasource "$ds"
    -q "$expr"
    --for "$fors"
    --from "$from" --to "$to"
    --delay-resolution-by "$delay"
    --chunk-size "$chunk")
  [ -n "$gb" ] && args+=(--incident-group-by "$gb")
  [ "$allow_card" = 1 ] && args+=(--allow-high-cardinality)
  [ ${#extra[@]} -gt 0 ] && args+=("${extra[@]}")
  "$atest" "${args[@]}" >"$log" 2>&1
  return $?
}

classify_success() { # atest exited 0; echo one of: ok | nodata
  if grep -q 'no data returned' "$log"; then echo nodata; return; fi
  if grep -qE '^analysis:' "$log"; then echo ok; return; fi
  echo ok
}

classify_failure() { # inspect a failed atest log; echo one of: cardinality | widen | transient | hard
  if grep -qiE 'safety limit|allow-high-cardinality' "$log"; then echo cardinality; return; fi
  if grep -qiE 'silently widened step' "$log"; then echo widen; return; fi
  if grep -qiE '429|Too Many Requests|500|502|503|504|context deadline exceeded|timeout|i/o timeout|connection reset|EOF|TLS handshake|no such host|temporarily' "$log"; then echo transient; return; fi
  if grep -qE '^error:' "$log"; then echo hard; return; fi
  echo hard
}

transient_tries=0
attempt=0
while :; do
  attempt=$((attempt+1))
  if run_once; then
    verdict=$(classify_success)
  else
    verdict=$(classify_failure)
  fi
  case "$verdict" in
    ok)
      echo "RESULT OK label=${label} log=${log}${escalations:+ escalations=${escalations}}"
      exit 0;;
    nodata)
      echo "RESULT NODATA label=${label} log=${log}${escalations:+ escalations=${escalations}}"
      exit 0;;
    cardinality)
      if [ "$allow_card" = 0 ]; then
        allow_card=1
        escalations="${escalations:+${escalations},}allow-high-cardinality"
        notice "${label}: high cardinality detected; retrying with --allow-high-cardinality (log: ${log})"
        continue
      fi
      notice "${label}: high cardinality persisted after --allow-high-cardinality; inspect ${log}"
      echo "RESULT FAILED label=${label} log=${log} reason=cardinality-after-override escalations=${escalations}"
      exit 0;;
    widen)
      newchunk=$(halve_chunk "$chunk")
      if [ -n "$newchunk" ]; then
        escalations="${escalations:+${escalations},}chunk:${chunk}->${newchunk}"
        notice "${label}: Grafana widened step at chunk-size ${chunk}; retrying with --chunk-size ${newchunk} (log: ${log})"
        chunk="$newchunk"
        continue
      fi
      notice "${label}: Grafana still widened step at 10m floor; try increasing --step or narrowing the window (log: ${log})"
      echo "RESULT FAILED label=${label} log=${log} reason=step-widen-at-floor escalations=${escalations}"
      exit 0;;
    transient)
      transient_tries=$((transient_tries+1))
      if [ "$transient_tries" -le "$max_transient" ]; then
        # cache keeps already-fetched chunks; rerun only refetches the failed ones
        # backoff before retry: 5s, 15s, 30s, 60s, … capped
        case $transient_tries in
          1) backoff=5 ;;
          2) backoff=15 ;;
          3) backoff=30 ;;
          *) backoff=60 ;;
        esac
        notice "${label}: transient Grafana/query error; retry ${transient_tries}/${max_transient} after ${backoff}s (log: ${log})"
        sleep "$backoff"
        continue
      fi
      reason=$(last_error)
      notice "${label}: transient retries exhausted; inspect ${log}"
      echo "RESULT FAILED label=${label} log=${log} reason=transient-retries-exhausted:${reason:-unknown} escalations=${escalations}"
      exit 0;;
    hard)
      reason=$(last_error)
      notice "${label}: hard failure: ${reason:-unknown-error} (log: ${log})"
      echo "RESULT FAILED label=${label} log=${log} reason=${reason:-unknown-error} escalations=${escalations}"
      exit 0;;
  esac
done
