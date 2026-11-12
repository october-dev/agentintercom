#!/usr/bin/env python3
"""Prepare a PostgreSQL restore list for a private, quarantined Bus recovery."""
import argparse
import re
import sys

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--quarantine', action='store_true', required=True,
                    help='keep recovered service private and fence credentials before reconnecting')
parser.parse_args()
source = sys.stdin.read(16 * 1024 * 1024 + 1)
if len(source) > 16 * 1024 * 1024:
    raise SystemExit('Restore list exceeds 16 MiB')
schema = re.compile(r'^\d+;\s+\d+\s+\d+\s+SCHEMA\s+-\s+october_bus\s+')
identity = re.compile(r'^\d+;\s+\d+\s+\d+\s+TRIGGER\s+october_bus\s+hosted_members\s+bus_member_account\s+')
lines = source.splitlines()
if sum(bool(schema.match(line)) for line in lines) != 1:
    raise SystemExit('Expected exactly one october_bus schema creation entry')
if sum(bool(identity.match(line)) for line in lines) > 1:
    raise SystemExit('Ambiguous shared identity hook')
print('; Quarantined recovery: reapply shared identity guards before admitting users.')
for line in lines:
    if not schema.match(line) and not identity.match(line):
        print(line)
