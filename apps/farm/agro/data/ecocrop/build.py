#!/usr/bin/env python3
"""Builds ../wide.json.gz, the experimental crop tier, from FAO ECOCROP.

    python3 build.py EcoCrop_DB.csv

The table is FAO's ECOCROP database (https://gaez.fao.org/pages/ecocrop);
a public copy of the whole table is EcoCrop_DB.csv in
https://github.com/OpenCLIM/ecocrop. It is not committed here. A plant is
kept when it is a food, fodder, medicinal or material plant, has every
range the recommender scores (temperature, rain, pH, soil texture, days),
and its species is not already in ../crops.json (by "species").
"""
import csv, gzip, json, os, re, sys

HERE = os.path.dirname(os.path.abspath(__file__))
KINDS = [  # ECOCROP category -> our kind, first match wins
    ("cereals", "cereal"), ("pulses", "pulse"), ("roots/tubers", "root"), ("vegetables", "vegetable"),
    ("fruits & nuts", "fruit"), ("forage/pasture", "fodder"), ("medicinals & aromatic", "medicinal"),
    ("materials", "material"),
]
TEXTURES = {"heavy": ["clay", "clay_loam"], "medium": ["loam", "silt_loam", "sandy_loam"], "light": ["sand", "loamy_sand", "sandy_loam"]}
SAME = {"citrus aurantifolia", "pennisetum americanum", "lycopersicon esculentum", "phyllanthus emblica", "emblica officinalis", "hylocereus undatus"}  # other spellings of species ../crops.json has
ORDER = ["sand", "loamy_sand", "sandy_loam", "loam", "silt_loam", "clay_loam", "clay"]


def num(v):
    try:
        return float(v)
    except ValueError:
        return None


def whole(v):
    return int(v) if v == int(v) else v


def build(path):
    have = {" ".join(c["species"].lower().split()[:2]) for c in json.load(open(os.path.join(HERE, "..", "crops.json"), encoding="utf8"))["crops"]}
    out, seen = [], set()
    for r in csv.DictReader(open(path, encoding="latin-1")):
        cat = r["CAT"].lower()
        if "weed" in cat:
            continue
        kind = next((k for c, k in KINDS if c in cat), None)
        v = {k: num(r[k]) for k in ("TOPMN", "TOPMX", "TMIN", "TMAX", "ROPMN", "ROPMX", "RMIN", "PHOPMN", "PHOPMX", "GMIN", "GMAX", "KTMP")}
        if kind is None or any(v[k] is None for k in v if k != "KTMP"):
            continue
        if not (v["TMIN"] <= v["TOPMN"] <= v["TOPMX"] <= v["TMAX"] and v["ROPMN"] <= v["ROPMX"] and v["PHOPMN"] <= v["PHOPMX"] and 0 < v["GMIN"] <= v["GMAX"]):
            continue
        tex = {t for word in re.split(r",\s*", (r["TEXT"] + ", " + r["TEXTR"]).lower()) for t in TEXTURES.get(word, [])}
        if "wide" in (r["TEXT"] + r["TEXTR"]).lower():
            tex = set(ORDER)
        if not tex:
            continue
        species = " ".join(r["ScientificName"].split())
        key = " ".join(species.lower().split()[:2])
        cid = re.sub(r"[^a-z0-9]+", "_", species.lower()).strip("_")
        if key in have or key in SAME or cid in seen or not cid:
            continue
        seen.add(cid)
        common = []
        for n in re.split(r",\s*", r["COMNAME"]):
            n = n.strip()
            if n and n.isascii() and len(n) <= 30 and n.lower() not in (c.lower() for c in common):
                common.append(n)
        life = r["LISPA"].lower()
        tol = []
        if "poorly" in (r["DRA"] + r["DRAR"]).lower():
            tol.append("waterlogging")
        if v["KTMP"] is not None and v["KTMP"] < 0:
            tol.append("frost")
        if v["RMIN"] <= 350:
            tol.append("drought")
        if re.search("medium|high", (r["SAL"] + r["SALR"]).lower()):
            tol.append("salinity")
        light = (r["LIOPMN"] + " " + r["LIOPMX"]).lower()
        photo = [p for p in ("short", "neutral", "long") if p + " day" in r["PHOTO"].lower()]
        c = {
            "id": cid, "name": species, "species": species, "common": common[:8],
            "family": r["FAMNAME"].split(":")[-1], "kind": kind,
            "perennial": "perennial" in life and "annual" not in life,
            "days": [whole(v["GMIN"]), whole(v["GMAX"])],
            "temp_opt": [whole(v["TOPMN"]), whole(v["TOPMX"])], "temp_abs": [whole(v["TMIN"]), whole(v["TMAX"])],
            "water_mm": [whole(v["ROPMN"]), whole(v["ROPMX"])], "ph": [v["PHOPMN"], v["PHOPMX"]],
            "textures": [t for t in ORDER if t in tex], "tolerates": tol,
            "light": "shade" if "heavy shade" in light else "part" if "light shade" in light and "clear" not in light and "bright" not in light else "full",
            "day_length": photo[0] if len(photo) == 1 else "neutral",
            "zones": re.findall(r"\(([A-Z][a-z]?)\)", r["CLIZ"]),
            "grown": "large" if "large scale" in r["PLAT"] else "small" if "small scale" in r["PLAT"] else "wild" if "wild" in r["PLAT"] else "",
            "experimental": True,
        }
        out.append(c)
    out.sort(key=lambda c: c["id"])
    doc = {
        "source": "FAO ECOCROP (https://gaez.fao.org/pages/ecocrop): temperature (monthly mean, °C), rainfall (mm, as the water need), pH, soil texture and days to harvest as the database gives them. Experimental: these plants are scored on climate and soil alone, with no Indian season, spacing, dose or local trial behind them. Credit: FAO.",
        "crops": out,
    }
    raw = json.dumps(doc, ensure_ascii=False, separators=(",", ":")).encode("utf8")
    with open(os.path.join(HERE, "..", "wide.json.gz"), "wb") as f:
        with gzip.GzipFile(fileobj=f, mode="wb", mtime=0, filename="") as g:
            g.write(raw)
    return out, len(raw)


if __name__ == "__main__":
    crops, size = build(sys.argv[1])
    kinds = {}
    for c in crops:
        kinds[c["kind"]] = kinds.get(c["kind"], 0) + 1
    print(len(crops), "plants,", size, "bytes of JSON,", os.path.getsize(os.path.join(HERE, "..", "wide.json.gz")), "gzipped;", kinds)
