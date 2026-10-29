#!/usr/bin/env python3
"""Enforce measured package coverage floors without hiding generated coverage."""
import argparse
from pathlib import Path
import sys


def read_profile(path):
    lines = path.read_text().splitlines()
    if not lines or lines[0] not in ("mode: set", "mode: count", "mode: atomic"):
        raise ValueError("missing or invalid coverage mode")
    packages = {}
    seen = set()
    for line in lines[1:]:
        block, statements, count = line.rsplit(" ", 2)
        if block in seen:
            raise ValueError(f"duplicate coverage block: {block}")
        seen.add(block)
        filename, _ = block.rsplit(":", 1)
        package = filename.rsplit("/", 1)[0]
        statements, count = int(statements), int(count)
        if statements < 0 or count < 0:
            raise ValueError("negative statement or execution count")
        # covered, total, non-generated covered, non-generated total
        values = packages.setdefault(package, [0, 0, 0, 0])
        values[0] += statements if count else 0
        values[1] += statements
        if not filename.endswith("/wbc_generated.go"):
            values[2] += statements if count else 0
            values[3] += statements
    if not packages:
        raise ValueError("empty coverage profile")
    return packages


def compare(current, baseline):
    errors = []
    checks = [(package, current.get(package), expected) for package, expected in baseline.items()]
    # New packages must not dilute aggregate coverage while every older package
    # still meets its own floor. Keep generated and handwritten scopes separate.
    checks.append(("all packages", [sum(row[i] for row in current.values()) for i in range(4)],
                   [sum(row[i] for row in baseline.values()) for i in range(4)]))
    for package, actual, expected in checks:
        if actual is None:
            errors.append(f"missing package: {package}")
            continue
        for start, scope in ((0, "all"), (2, "non-generated")):
            covered, total = actual[start:start+2]
            old_covered, old_total = expected[start:start+2]
            if covered < old_covered or covered * old_total < old_covered * total:
                errors.append(f"{package} ({scope}): {covered}/{total}, baseline {old_covered}/{old_total}")
    return errors


def read_baseline(path):
    result = {}
    for line in path.read_text().splitlines():
        if not line or line.startswith("#"):
            continue
        package, *counts = line.split()
        if len(counts) != 4 or package in result:
            raise ValueError("invalid or duplicate baseline package")
        values = list(map(int, counts))
        if any(value < 0 for value in values) or values[0] > values[1] or values[2] > values[3]:
            raise ValueError("invalid baseline counts")
        result[package] = values
    if not result:
        raise ValueError("empty coverage baseline")
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("profile", type=Path)
    parser.add_argument("--baseline", type=Path, default=Path(__file__).resolve().parents[1]/".github/coverage-baseline.tsv")
    args = parser.parse_args()
    try:
        actual = read_profile(args.profile)
        errors = compare(actual, read_baseline(args.baseline))
    except (OSError, ValueError) as exc:
        parser.error(str(exc))
    for package, values in sorted(actual.items()):
        percent = 100*values[0]/values[1] if values[1] else 0
        nongenerated = 100*values[2]/values[3] if values[3] else 0
        print(f"{package}: {percent:.3f}% all; {nongenerated:.3f}% non-generated")
    if errors:
        print("Coverage regressions:\n" + "\n".join(errors), file=sys.stderr)
        return 1
    print("All package coverage floors passed")
    return 0


if __name__ == "__main__":
    sys.exit(main())
