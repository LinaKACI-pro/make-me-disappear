#!/usr/bin/env python3
"""
Build brokers.yaml from YourDigitalRights.org API + their prod-domains.csv.

The API returns structured data: name, DPO email, headquarters, industry, etc.
for 4000+ companies. We filter for data brokers and ad-tech, and prioritize
EU-based companies.

Usage:
    python3 scripts/scrape_brokers.py
    # -> writes brokers_scraped.yaml (~5-10 min)

Review the output manually before copying to brokers.yaml.
"""

import json
import re
import ssl
import sys
import time
import random
from urllib.request import Request, urlopen

# --- Config ---

DOMAINS_CSV_URL = "https://raw.githubusercontent.com/your-digital-rights/yourdigitalrights.org/master/prod-domains.csv"
API_BASE = "https://api.yourdigitalrights.org/domains/"

USER_AGENT = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36"

# Companies in these industries are likely data brokers / ad-tech.
# We keep all of them. Others are kept only if they have an email.
PRIORITY_INDUSTRIES = {
    "advertising", "marketing", "data", "analytics", "information",
    "technology", "internet", "software", "financial", "insurance",
    "credit", "telecom", "media",
}

# EU country names / cities for region detection.
EU_MARKERS = {
    "france", "paris", "germany", "berlin", "munich", "netherlands",
    "amsterdam", "belgium", "brussels", "spain", "madrid", "barcelona",
    "italy", "rome", "milan", "ireland", "dublin", "sweden", "stockholm",
    "denmark", "copenhagen", "finland", "helsinki", "austria", "vienna",
    "portugal", "lisbon", "poland", "warsaw", "czech", "prague",
    "luxembourg", "europe", "eu", "uk", "london", "united kingdom",
    "norway", "oslo", "switzerland", "zurich", "geneva",
}

US_MARKERS = {
    "united states", "usa", "new york", "san francisco", "los angeles",
    "chicago", "boston", "seattle", "austin", "denver", "atlanta",
    "california", "texas", "florida", "virginia", "washington",
}

ssl_ctx = ssl.create_default_context()
ssl_ctx.check_hostname = False
ssl_ctx.verify_mode = ssl.CERT_NONE


# --- Helpers ---

def fetch(url, timeout=15):
    try:
        req = Request(url, headers={"User-Agent": USER_AGENT})
        with urlopen(req, timeout=timeout, context=ssl_ctx) as resp:
            data = resp.read(500_000)
            charset = resp.headers.get_content_charset() or "utf-8"
            return data.decode(charset, errors="replace")
    except Exception:
        return None


def fetch_json(url, timeout=15):
    body = fetch(url, timeout)
    if body is None:
        return None
    try:
        return json.loads(body)
    except json.JSONDecodeError:
        return None


def slugify(name):
    s = re.sub(r"[^a-z0-9]+", "-", name.lower().strip()).strip("-")
    return s or "unknown"


def guess_region(hq, industries):
    """Guess region from headquarters and industry text."""
    text = (hq + " " + industries).lower()
    for marker in EU_MARKERS:
        if marker in text:
            return "EU"
    for marker in US_MARKERS:
        if marker in text:
            return "US"
    return "GLOBAL"


def escape_yaml(s):
    if not s:
        return '""'
    s = s.replace("\\", "\\\\").replace('"', '\\"')
    return '"' + s + '"'


def is_interesting(domain_data, email):
    """Filter: keep data brokers, ad-tech, and any company with an email."""
    if email:
        industries = (domain_data.get("industries") or "").lower()
        specialties = (domain_data.get("specialties") or "").lower()
        text = industries + " " + specialties

        for kw in PRIORITY_INDUSTRIES:
            if kw in text:
                return True

        # Also keep any company with a DPO/privacy email.
        if email and any(p in email for p in ["dpo@", "privacy@", "gdpr@", "dataprotection@", "rgpd@"]):
            return True

    return False


# --- Main ---

def main():
    print("=== Step 1: Fetching domain list ===")
    csv_data = fetch(DOMAINS_CSV_URL, timeout=30)
    if csv_data is None:
        print("ERROR: could not fetch prod-domains.csv")
        sys.exit(1)

    # Parse CSV: skip header, strip quotes.
    lines = csv_data.strip().split("\n")
    domains = []
    for line in lines[1:]:  # skip header
        d = line.strip().strip('"').strip()
        if d and "." in d:
            domains.append(d)

    print(f"Found {len(domains)} domains")

    print(f"\n=== Step 2: Querying API for each domain ===")
    print(f"(this will take ~10-15 min, be patient)\n")

    brokers = []
    skipped = 0
    errors = 0

    for i, domain in enumerate(domains):
        progress = f"[{i+1}/{len(domains)}]"

        data = fetch_json(API_BASE + domain)
        if data is None or data.get("statusCode", 0) >= 400:
            errors += 1
            if (i + 1) % 100 == 0:
                print(f"  {progress} {domain}: API error (skipped)")
            continue

        info = data.get("Domain", {})
        name = info.get("name") or domain
        email = info.get("email") or ""
        hq = info.get("headquarters") or ""
        industries = info.get("industries") or ""
        privacy_url = info.get("privacyPolicy") or ""

        # Filter: only keep interesting companies.
        if not is_interesting(info, email):
            skipped += 1
            continue

        region = guess_region(hq, industries)

        brokers.append({
            "slug": slugify(name),
            "name": name,
            "domain": domain,
            "email": email,
            "region": region,
            "hq": hq,
            "industries": industries,
            "privacy_url": privacy_url,
        })

        print(f"  {progress} {name}: {email or 'NO EMAIL'} [{region}] ({hq})")

        # Polite delays.
        time.sleep(0.3 + random.random() * 0.3)
        if (i + 1) % 200 == 0:
            print(f"  ... {i+1}/{len(domains)} done, {len(brokers)} kept so far ...")
            time.sleep(2)

    print(f"\n=== Step 3: Writing YAML ===")

    # Sort: EU first, then GLOBAL, then US. Within each group, alphabetical.
    region_order = {"EU": 0, "GLOBAL": 1, "US": 2}
    brokers.sort(key=lambda b: (region_order.get(b["region"], 9), b["name"].lower()))

    found = sum(1 for b in brokers if b["email"])
    missing = len(brokers) - found
    eu_count = sum(1 for b in brokers if b["region"] == "EU")

    lines = [
        f"# Auto-generated from YourDigitalRights.org API",
        f"# Total brokers: {len(brokers)} (EU: {eu_count})",
        f"# With email: {found} | Without: {missing}",
        f"# Skipped (not data broker/ad-tech): {skipped}",
        f"# API errors: {errors}",
        f"#",
        f"# Review this file:",
        f"#   - Verify emails for brokers you care about",
        f"#   - Adjust regions if the guess is wrong",
        f"#   - Remove brokers you don't want to contact",
        f"#   - For brokers without email: check their privacy page",
        f"#",
        f"# Then: cp brokers_scraped.yaml brokers.yaml && ./bot db-seed",
        f"",
        f"brokers:",
    ]

    seen_slugs = set()
    for b in brokers:
        slug = b["slug"]
        # Deduplicate slugs.
        if slug in seen_slugs:
            slug = slug + "-" + b["domain"].split(".")[0]
        seen_slugs.add(slug)

        method = "email" if b["email"] else "manual"
        lines.append(f"  - id: {escape_yaml(slug)}")
        lines.append(f"    name: {escape_yaml(b['name'])}")
        lines.append(f"    region: \"{b['region']}\"")
        lines.append(f"    method: \"{method}\"")
        if b["email"]:
            lines.append(f"    contact: {escape_yaml(b['email'])}")
        if b["privacy_url"]:
            lines.append(f"    opt_out_url: {escape_yaml(b['privacy_url'])}")
        lines.append(f"    tier: 2")
        notes = []
        if b["hq"]:
            notes.append(b["hq"])
        if b["industries"]:
            notes.append(b["industries"])
        if not b["email"]:
            notes.append("no email found")
        if notes:
            lines.append(f"    notes: {escape_yaml('; '.join(notes))}")
        lines.append("")

    out_path = "brokers_scraped.yaml"
    with open(out_path, "w") as f:
        f.write("\n".join(lines))

    print(f"\nWritten to {out_path}")
    print(f"  {len(brokers)} brokers total ({eu_count} EU)")
    print(f"  {found} with email, {missing} without")
    print(f"\nNext steps:")
    print(f"  1. Open {out_path}")
    print(f"  2. Focus on EU brokers first (they're at the top)")
    print(f"  3. Remove any you don't want to contact")
    print(f"  4. cp brokers_scraped.yaml brokers.yaml")
    print(f"  5. ./bot db-seed && ./bot campaign-init && ./bot run")


if __name__ == "__main__":
    main()
