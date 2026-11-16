#!/usr/bin/env python3
"""List every dated source behind the farm app's data, oldest first.

Read-only and offline: it reads the data files in the repository and
prints what a person (or an agent) should check against the source. It
fetches nothing and changes nothing.

    sources.py [--kind schemes|windows|varieties|wide|inputs|banned|msp|all] [--older-than DAYS] [--json]
"""
import argparse
import datetime
import glob
import json
import os
import re
import sys

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "..", "..", ".."))
FARM = os.path.join(ROOT, "apps", "farm")


def rel(path):
    return os.path.relpath(path, ROOT)


def region_rows():
    files = [os.path.join(FARM, "region", "data", "base.json")]
    files += sorted(glob.glob(os.path.join(FARM, "region", "data", "overlays", "*.json")))
    for path in files:
        with open(path, encoding="utf-8") as f:
            d = json.load(f)
        uses = {}
        schemes = d.get("schemes") or {}
        if isinstance(schemes, list):
            schemes = {s.get("id", "?"): s for s in schemes}
        for sid, s in schemes.items():
            cut = ", ".join("%s %s by %s" % (c.get("season"), c.get("what"), c.get("by")) for c in s.get("cutoffs") or [])
            what = "scheme %s" % sid + (" (%s)" % cut if cut else "")
            uses.setdefault((s.get("ref") or {}).get("source"), []).append(("schemes", what))
        for cid, c in (d.get("crops") or {}).items():
            if c.get("windows"):
                uses.setdefault((c.get("ref") or {}).get("source"), []).append(("windows", "sowing windows of %s" % cid))
            for src in sorted({(v.get("ref") or {}).get("source") for v in c.get("varieties") or []}):
                uses.setdefault(src, []).append(("varieties", "varieties of %s" % cid))
        for key in ("inputs", "rules", "storage", "msp"):
            if d.get(key):
                uses.setdefault(None, []).append((key, key))
        for s in d.get("sources") or []:
            used = uses.get(s.get("id"), [])
            kinds = sorted({k for k, _ in used}) or ["other"]
            yield {
                "kind": "+".join(kinds),
                "file": rel(path),
                "id": s.get("id"),
                "name": s.get("name"),
                "url": s.get("url"),
                "read": s.get("read"),
                "used_for": "; ".join(w for _, w in used) or "listed, not referenced",
            }


def crops_rows():
    path = os.path.join(FARM, "agro", "data", "crops.json")
    with open(path, encoding="utf-8") as f:
        d = json.load(f)
    uses = {}
    for c in d.get("crops") or []:
        for src in {(v.get("ref") or {}).get("source") for v in c.get("varieties") or []}:
            uses.setdefault(src, []).append(c.get("id"))
    for s in d.get("sources") or []:
        crops = sorted(uses.get(s.get("id"), []))
        yield {"kind": "varieties" if crops else "wide", "file": rel(path), "id": s.get("id"), "name": s.get("name"), "url": s.get("url"),
               "read": s.get("read"), "used_for": ("varieties of " + ", ".join(crops)) if crops else (s.get("note") or "the experimental tier")}


def inputs_rows():
    path = os.path.join(FARM, "inputs", "data", "inputs.json")
    with open(path, encoding="utf-8") as f:
        d = json.load(f)
    for s in d.get("sources") or []:
        yield {"kind": "inputs", "file": rel(path), "id": s.get("id"), "name": s.get("name"), "url": s.get("url"),
               "read": s.get("read"), "used_for": "doses, products and schedules that cite it"}
    path = os.path.join(FARM, "inputs", "data", "banned.json")
    with open(path, encoding="utf-8") as f:
        b = json.load(f)
    yield {"kind": "banned", "file": rel(path), "id": "banned", "name": "banned and restricted pesticides, as of %s" % b.get("as_of"),
           "url": None, "read": b.get("checked"), "used_for": "%d active ingredients the app never lists; see the file's note for the source" % len(b.get("actives") or [])}


def msp_rows():
    path = os.path.join(FARM, "prices", "msp.go")
    with open(path, encoding="utf-8") as f:
        text = f.read()
    for name, src in re.findall(r'(msp\w+Source)\s*=\s*"([^"]+)"', text):
        m = re.search(r"(\d{1,2} \w+ \d{4})", src)
        read = None
        if m:
            try:
                read = datetime.datetime.strptime(m.group(1), "%d %B %Y").date().isoformat()
            except ValueError:
                pass
        yield {"kind": "msp", "file": rel(path), "id": name, "name": src, "url": "https://pib.gov.in (search the Cabinet's MSP decision)",
               "read": read, "used_for": "minimum support prices; a new decision comes each May or June (kharif) and September or October (rabi)"}


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--kind", default="all")
    ap.add_argument("--older-than", type=int, default=0, metavar="DAYS")
    ap.add_argument("--json", action="store_true")
    ap.add_argument("--today", default=datetime.date.today().isoformat(), help=argparse.SUPPRESS)
    a = ap.parse_args()
    today = datetime.date.fromisoformat(a.today)
    rows = list(region_rows()) + list(crops_rows()) + list(inputs_rows()) + list(msp_rows())
    for r in rows:
        try:
            r["age_days"] = (today - datetime.date.fromisoformat(r["read"])).days
        except (TypeError, ValueError):
            r["age_days"] = None
    if a.kind != "all":
        rows = [r for r in rows if a.kind in r["kind"].split("+")]
    rows = [r for r in rows if r["age_days"] is None or r["age_days"] >= a.older_than]
    rows.sort(key=lambda r: (r["age_days"] is not None, -(r["age_days"] or 0), r["file"], r["id"] or ""))
    if a.json:
        json.dump(rows, sys.stdout, indent=1, ensure_ascii=False)
        print()
        return
    for r in rows:
        age = "never dated" if r["age_days"] is None else "read %s, %d days ago" % (r["read"], r["age_days"])
        print("[%s] %s (%s)" % (r["kind"], r["name"], age))
        print("    id %s in %s" % (r["id"], r["file"]))
        if r["url"]:
            print("    %s" % r["url"])
        print("    used for: %s" % r["used_for"])
    print("%d sources" % len(rows))


if __name__ == "__main__":
    main()
