# Recorta o documento do MSRC num exemplo pequeno para os testes: quatro CVEs
# escolhidas por propriedade e só os produtos que elas citam.
import json, sys, re
src, dst = sys.argv[1], sys.argv[2]
d = json.load(open(src))
vulns = d["Vulnerability"]

def produtos(v):
    ids = set()
    for s in v.get("ProductStatuses", []):
        ids.update(s.get("ProductID", []))
    for k in ("Threats", "Remediations", "CVSSScoreSets"):
        for x in v.get(k, []):
            ids.update(x.get("ProductID", []))
    return ids

def primeira(cond):
    return next(v for v in vulns if cond(v))

escolhidas = [
    primeira(lambda v: v["CVE"] == "CVE-2026-50349"),
    primeira(lambda v: any("Exploited:Yes" in t["Description"]["Value"] for t in v.get("Threats", []) if t.get("Type") == 1)),
    primeira(lambda v: any(r.get("Type") == 2 and "20438" in r.get("ProductID", []) and r.get("FixedBuild", "").startswith("10.0.26200.")
                           for r in v.get("Remediations", []))),
    primeira(lambda v: any(r.get("Type") == 2 and not re.fullmatch(r"[0-9]{6,8}", r.get("Description", {}).get("Value", ""))
                           for r in v.get("Remediations", []))),
]
unicas = list({v["CVE"]: v for v in escolhidas}.values())
ids = set().union(*(produtos(v) for v in unicas))
saida = {
    "DocumentTitle": d["DocumentTitle"],
    "DocumentTracking": d["DocumentTracking"],
    "ProductTree": {"FullProductName": [p for p in d["ProductTree"]["FullProductName"] if p["ProductID"] in ids]},
    "Vulnerability": unicas,
}
with open(dst, "w") as f:
    json.dump(saida, f, ensure_ascii=False, indent=1)
print("CVEs:", [v["CVE"] for v in unicas], "| produtos:", len(saida["ProductTree"]["FullProductName"]))
