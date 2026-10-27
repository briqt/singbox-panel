#!/usr/bin/env python3
"""Read-only configuration audit. Report filenames/counts, never file contents.

Example: python3 scripts/audit_ip.py --ip 203.0.113.1 --host tokyo --host lax03
Uses existing SSH authentication and host-key checks. No keys are accepted/reset.
"""
import argparse
import ipaddress
import json
import shlex
import subprocess


REMOTE = r'''
import json, os, pathlib, sys
needle = sys.argv[1].encode()
matches, errors, scanned = [], 0, 0
for root in sys.argv[2:]:
    base = pathlib.Path(os.path.expanduser(root))
    if not base.exists():
        continue
    for directory, dirs, files in os.walk(base):
        dirs[:] = [d for d in dirs if d not in ("node_modules", ".git", "logs", "log", "__pycache__")]
        for name in files:
            path = pathlib.Path(directory) / name
            if name.endswith((".log", ".db", ".sqlite", ".db-wal", ".sqlite-wal", ".gz", ".zip")):
                continue
            try:
                if path.is_symlink() or not path.is_file() or path.stat().st_size > 2_000_000:
                    continue
                data = path.read_bytes()
                if b"\0" in data:
                    continue
                scanned += 1
                count = data.count(needle)
                if count:
                    matches.append({"path": str(path), "occurrences": count})
            except OSError:
                errors += 1
print(json.dumps({"matches": matches, "files_scanned": scanned, "unreadable": errors}))
'''


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--ip", required=True)
    parser.add_argument("--host", action="append", required=True)
    parser.add_argument("--root", action="append", default=[])
    parser.add_argument("--jump", help="existing SSH jump host, if needed")
    args = parser.parse_args()
    ip = str(ipaddress.ip_address(args.ip))
    roots = args.root or ["/etc", "/opt", "~/.ssh"]
    failed = False
    for host in args.host:
        if host.startswith("-") or any(c.isspace() for c in host):
            parser.error("invalid SSH host")
        command = ["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "StrictHostKeyChecking=yes"]
        if args.jump:
            command += ["-J", args.jump]
        command += [host, "python3 - " + " ".join(shlex.quote(v) for v in [ip, *roots])]
        try:
            result = subprocess.run(command, input=REMOTE, text=True, capture_output=True, timeout=90)
            if result.returncode:
                raise RuntimeError(result.stderr.strip())
            print(json.dumps({"host": host, "status": "checked", **json.loads(result.stdout)}, ensure_ascii=False), flush=True)
        except (OSError, RuntimeError, ValueError, subprocess.TimeoutExpired) as error:
            failed = True
            print(json.dumps({"host": host, "status": "unreachable", "error": str(error)}, ensure_ascii=False), flush=True)
    return int(failed)


if __name__ == "__main__":
    raise SystemExit(main())
