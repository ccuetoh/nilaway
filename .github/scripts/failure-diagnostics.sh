#!/usr/bin/env bash
#
# Prints diagnostics that help distinguish a kernel OOM kill (exit 137) from a platform-initiated
# runner shutdown (exit 143) on GitHub-hosted runners. Run this on failure.

echo "::group::dmesg (last 50 lines)"
sudo dmesg -T 2>/dev/null | tail -n 50 || true
echo "::endgroup::"

echo "::group::memory"
free -m || true
cgroup_dir=/sys/fs/cgroup
if [ -r /proc/self/cgroup ]; then
  rel=$(sed -n 's/^0:://p' /proc/self/cgroup | head -n1)
  [ -n "$rel" ] && [ -d "/sys/fs/cgroup$rel" ] && cgroup_dir="/sys/fs/cgroup$rel"
fi
echo "cgroup: $cgroup_dir"
for f in memory.current memory.peak memory.max memory.events memory.stat; do
  if [ -r "$cgroup_dir/$f" ]; then
    printf '== %s ==\n' "$cgroup_dir/$f"
    cat "$cgroup_dir/$f"
  fi
done
echo "::endgroup::"

echo "::group::disk"
df -h || true
echo "::endgroup::"

echo "::group::top processes by RSS"
ps -eo pid,ppid,rss,comm --sort=-rss 2>/dev/null | head -n 20 || true
echo "::endgroup::"
