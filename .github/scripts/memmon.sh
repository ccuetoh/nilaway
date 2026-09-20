#!/usr/bin/env bash
#
# Runs a command while sampling system and cgroup memory usage, then prints a summary. Samples are
# streamed to the job log as they are taken (not only at the end), because GitHub-hosted runners can
# be shut down mid-job (exit 143) and all subsequent steps are skipped -- the streamed samples are
# then the only evidence. See ci-flakiness-plan.md.
#
# Usage: memmon.sh <command> [args...]

set -uo pipefail

if [ "$#" -eq 0 ]; then
  echo "usage: $0 <command> [args...]" >&2
  exit 2
fi

log="${RUNNER_TEMP:-/tmp}/memmon.log"
: >"$log"

# Resolve the cgroup directory of this process so that the reported usage/peak/counters describe
# the job rather than the whole VM (on cgroup v2 the line in /proc/self/cgroup looks like
# "0::/some/path").
cgroup_dir=/sys/fs/cgroup
if [ -r /proc/self/cgroup ]; then
  rel=$(sed -n 's/^0:://p' /proc/self/cgroup | head -n1)
  if [ -n "$rel" ] && [ -d "/sys/fs/cgroup$rel" ]; then
    cgroup_dir="/sys/fs/cgroup$rel"
  fi
fi

# sample_once prints a single line of memory data. All values are in MB. In addition to memory
# totals, it reports the cgroup usage/peak (to detect a job approaching its limit), the
# oom/oom_kill counters from memory.events (to distinguish a kernel OOM kill from a platform
# shutdown), and the top RSS process (to attribute memory to a process, e.g. nilaway or the Go
# compiler).
sample_once() {
  printf '%s ' "$(date -u +%H:%M:%S)"
  awk '/MemTotal|MemAvailable|SwapTotal|SwapFree/ {k = $1; sub(/:$/, "", k); printf "%s=%d ", k, $2/1024}' /proc/meminfo 2>/dev/null
  for f in current peak max; do
    if [ -r "$cgroup_dir/memory.$f" ]; then
      awk -v n="cg_$f" '{printf "%s=%d ", n, $1/1048576}' "$cgroup_dir/memory.$f"
    fi
  done
  if [ -r "$cgroup_dir/memory.events" ]; then
    awk '/^oom /{printf "oom=%d ", $2} /^oom_kill /{printf "oom_kill=%d ", $2}' "$cgroup_dir/memory.events"
  fi
  top=$(ps -eo rss=,comm= --sort=-rss 2>/dev/null | head -n1 | awk '{printf "%s:%d", $2, $1/1024}')
  [ -n "$top" ] && printf 'top=%s' "$top"
  echo
}

sample_loop() {
  while true; do
    line=$(sample_once)
    printf '%s\n' "$line" >>"$log"      # on-disk copy for the summary / report step
    printf '[memmon] %s\n' "$line" >&2  # streamed to the job log so it survives a shutdown
    sleep 5
  done
}

echo "[memmon] monitoring memory (cgroup: $cgroup_dir) while running: $*"
sample_loop &
monitor=$!
trap 'kill "$monitor" 2>/dev/null || true' EXIT

"$@"
status=$?

kill "$monitor" 2>/dev/null || true
wait "$monitor" 2>/dev/null || true

awk '
{
  for (i = 1; i <= NF; i++) {
    n = split($i, kv, "=")
    if (n != 2) continue
    if (kv[1] == "cg_peak" && kv[2] + 0 > peak) peak = kv[2] + 0
    if (kv[1] == "MemAvailable" && (min_avail == 0 || kv[2] + 0 < min_avail)) min_avail = kv[2] + 0
    if (kv[1] == "SwapFree" && (min_swap == 0 || kv[2] + 0 < min_swap)) min_swap = kv[2] + 0
    if (kv[1] == "oom_kill" && kv[2] + 0 > oom_kill) oom_kill = kv[2] + 0
  }
}
END {
  peak_str = (peak > 0) ? sprintf("%dMB", peak) : "n/a"
  printf "[memmon] summary: peak_cgroup=%s min_MemAvailable=%dMB min_SwapFree=%dMB oom_kill=%d\n", peak_str, min_avail, min_swap, oom_kill
}' "$log"

exit "$status"
