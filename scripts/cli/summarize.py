"""Summarise a captured CLI payload (scripts/cli/capture.ps1) without leaking secrets.

Usage: python -I scripts/cli/summarize.py .captures/<name>.txt [max_items] [keys...]
Prints the request lines (bearer token masked) and each item's chosen keys (or all keys).
Any secret-looking key, such as accessKey, secretKey, password or token, is replaced by <redacted>.
"""
import json
import re
import sys

SECRET = re.compile(r"(secret|password|passwd|token|privatekey|private_key|accesskey|access_key|apikey|api_key|credential|cert|keypair)", re.I)


def scrub(v, key=""):
    if key and SECRET.search(key):
        return "<redacted>"
    if isinstance(v, dict):
        return {k: scrub(x, k) for k, x in v.items()}
    if isinstance(v, list):
        return [scrub(x) for x in v]
    return v


def main():
    path, rest = sys.argv[1], sys.argv[2:]
    limit = int(rest[0]) if rest else 3
    keys = rest[1:]
    raw = open(path, encoding="utf-8-sig", errors="replace").read()
    for line in raw.splitlines():
        if line.startswith(("GET ", "POST ", "PUT ", "DELETE ", "# =>")):
            print(re.sub(r'"Authorization"=>"[^"]*"', '"Authorization"=>"<masked>"', line)[:220])
    start = raw.find("{")
    if start < 0:
        print("NO JSON:", raw[:400])
        return
    data, _ = json.JSONDecoder().raw_decode(raw[start:])
    data = scrub(data)
    for top, val in data.items():
        if isinstance(val, list):
            print(f"[{top}] {len(val)} items")
            for item in val[:limit]:
                shown = {k: item.get(k) for k in keys} if keys and isinstance(item, dict) else item
                print(json.dumps(shown, ensure_ascii=False)[:1500])
        elif top == "meta":
            print("meta", val)
        else:
            shown = {k: val.get(k) for k in keys} if keys and isinstance(val, dict) else val
            print(top, json.dumps(shown, ensure_ascii=False)[:1500])


main()
