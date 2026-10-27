#!/usr/bin/env python3
"""Preview/migrate Cloudflare A or AAAA records by exact old address.

No external packages. Credentials: CLOUDFLARE_DNS_TOKEN and CLOUDFLARE_ZONE_ID.
Only content is patched; TTL, proxy state and record metadata are preserved.
"""
import argparse
import ipaddress
import json
import os
import sys
import urllib.error
import urllib.parse
import urllib.request


class Cloudflare:
    def __init__(self, token, zone):
        self.token, self.zone = token, zone

    def request(self, method, path, body=None):
        request = urllib.request.Request(
            "https://api.cloudflare.com/client/v4/zones/" + self.zone + path,
            data=json.dumps(body).encode() if body is not None else None,
            headers={"Authorization": "Bearer " + self.token, "Content-Type": "application/json"},
            method=method,
        )
        try:
            with urllib.request.urlopen(request, timeout=30) as response:
                data = json.load(response)
        except urllib.error.HTTPError as error:
            raise RuntimeError(f"Cloudflare {method} failed: HTTP {error.code}") from None
        if not data.get("success"):
            raise RuntimeError("Cloudflare request failed: " + json.dumps(data.get("errors")))
        return data

    def records(self):
        result, page = [], 1
        while True:
            data = self.request("GET", f"/dns_records?per_page=100&page={page}")
            result.extend(data["result"])
            if page >= data.get("result_info", {}).get("total_pages", 1):
                return result
            page += 1


def plan_records(records, old_ip, names=()):
    old = ipaddress.ip_address(old_ip)
    kind = "A" if old.version == 4 else "AAAA"
    return sorted((r for r in records if r["type"] == kind and r["content"] == str(old)
                   and (not names or r["name"] in names)), key=lambda r: r["name"])


def migrate_record(api, record, new_ip):
    path = "/dns_records/" + record["id"]
    before = api.request("GET", path)["result"]
    preserved = ("type", "name", "ttl", "proxied")
    for key in (*preserved, "content"):
        if before.get(key) != record.get(key):
            raise RuntimeError(f"{record['name']}: record changed since preview; rerun")
    api.request("PATCH", path, {"content": str(ipaddress.ip_address(new_ip))})
    after = api.request("GET", path)["result"]
    if after.get("content") != str(ipaddress.ip_address(new_ip)) or any(
        after.get(key) != before.get(key) for key in preserved
    ):
        raise RuntimeError(f"{record['name']}: DNS read-back disagrees with requested change")
    return after


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--zone-id", default=os.environ.get("CLOUDFLARE_ZONE_ID"))
    parser.add_argument("--old-ip", required=True)
    parser.add_argument("--new-ip", required=True)
    parser.add_argument("--name", action="append", default=[], help="restrict to these exact names")
    parser.add_argument("--apply", action="store_true", help="write and read back; default is preview")
    args = parser.parse_args()
    old, new = ipaddress.ip_address(args.old_ip), ipaddress.ip_address(args.new_ip)
    if old.version != new.version or old == new:
        parser.error("old and new must be different addresses of the same IP family")
    token = os.environ.get("CLOUDFLARE_DNS_TOKEN")
    if not token or not args.zone_id:
        parser.error("set CLOUDFLARE_DNS_TOKEN and CLOUDFLARE_ZONE_ID (or --zone-id)")
    api = Cloudflare(token, args.zone_id)
    planned = plan_records(api.records(), str(old), args.name)
    print(json.dumps({"apply": args.apply, "old_ip": str(old), "new_ip": str(new),
                      "records": [{k: r.get(k) for k in ("name", "type", "ttl", "proxied")}
                                  for r in planned]}, ensure_ascii=False), flush=True)
    if args.apply:
        for record in planned:
            result = migrate_record(api, record, str(new))
            print(json.dumps({"verified": result["name"], "content": result["content"]}), flush=True)


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, OSError, ValueError) as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)
