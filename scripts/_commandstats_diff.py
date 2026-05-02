#!/usr/bin/env python3
"""Diff two Redis INFO commandstats snapshots and print top commands by delta usec.

Used by scripts/profile_saturation.sh — keeps the diff logic out of the
shell heredoc where quoting + variable expansion gets tangled.

Input: two file paths to `INFO commandstats` output. Lines look like:
    cmdstat_set:calls=12345,usec=678,usec_per_call=0.054,rejected_calls=0,failed_calls=0

Output: top 20 commands by total usec delta (during - before), sorted
descending. Useful for answering "where did Redis spend its time during
the saturation window?"
"""

import pathlib
import re
import sys


def parse(path: str) -> dict[str, tuple[int, int]]:
    out: dict[str, tuple[int, int]] = {}
    for line in pathlib.Path(path).read_text().splitlines():
        m = re.match(r"cmdstat_(\S+):calls=(\d+),usec=(\d+)", line)
        if m:
            out[m.group(1)] = (int(m.group(2)), int(m.group(3)))
    return out


def main() -> int:
    if len(sys.argv) != 3:
        print("usage: _commandstats_diff.py <before.txt> <during.txt>", file=sys.stderr)
        return 2

    before = parse(sys.argv[1])
    during = parse(sys.argv[2])

    rows = []
    for cmd in sorted(set(before) | set(during)):
        bc, bu = before.get(cmd, (0, 0))
        dc, du = during.get(cmd, (0, 0))
        delta_calls = dc - bc
        delta_usec = du - bu
        if delta_calls > 0:
            per_call = delta_usec / delta_calls
            rows.append((delta_usec, delta_calls, per_call, cmd))

    rows.sort(reverse=True)
    print(f"{'cmd':<24} {'delta_calls':>12} {'delta_usec':>14} {'usec/call':>11}")
    print("-" * 64)
    for delta_usec, delta_calls, per_call, cmd in rows[:20]:
        print(f"{cmd:<24} {delta_calls:>12} {delta_usec:>14} {per_call:>11.2f}")

    return 0


if __name__ == "__main__":
    sys.exit(main())
