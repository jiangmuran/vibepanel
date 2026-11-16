#!/usr/bin/env python3
"""A docstring."""
import sys


def main(argv: list[str]) -> int:
    for a in argv:
        print(f"arg: {a!r}")  # a comment
    return 0


if __name__ == '__main__':
    sys.exit(main(sys.argv[1:]))
