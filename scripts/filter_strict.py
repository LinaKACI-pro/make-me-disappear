#!/usr/bin/env python3
"""
Strict filter: keep ONLY actual data brokers, ad-tech, and tracking companies.
Not SaaS, not banks, not software shops.

Usage: python3 scripts/filter_strict.py
"""

import re

# ONLY these industries are real data brokers / ad-tech.
KEEP_INDUSTRIES = {
    "advertising services",
    "marketing services",
    "market research",
    "data infrastructure and analytics",
    "information services",
    "online media",
    "internet publishing",
}

# Known data brokers / ad-tech by name fragment — always keep regardless of industry.
ALWAYS_KEEP = [
    # Ad-tech / tracking (EU)
    "criteo",
    "teads",
    "smart adserver",
    "smartadserver",
    "displayce",
    "sirdata",
    "didomi",
    "commanders act",
    "commandersact",
    "at internet",
    "piano",
    "contentpass",
    "eyeo",
    "adform",
    "sizmek",
    # Ad-tech / tracking (global)
    "taboola",
    "outbrain",
    "quantcast",
    "lotame",
    "adsquare",
    "the trade desk",
    "tradedesk",
    "appnexus",
    "xandr",
    "doubleclick",
    "google ads",
    "google marketing",
    "dv360",
    "facebook",
    "meta platforms",
    "meta ",
    "instagram",
    "tiktok",
    "bytedance",
    "snap inc",
    "snapchat",
    "pinterest",
    "twitter",
    "linkedin",
    "microsoft advertising",
    "amazon ads",
    "amazon advertising",
    "oracle data",
    "oracle advertising",
    "bluekai",
    "adobe advertising",
    "adobe audience",
    "appsflyer",
    "adjust",
    "branch",
    "kochava",
    "singular",
    "mopub",
    "ironsource",
    "unity ads",
    "applovin",
    "vungle",
    "liveramp",
    "the trade desk",
    "index exchange",
    "pubmatic",
    "magnite",
    "openx",
    "triplelift",
    "sovrn",
    "sharethrough",
    # Data brokers (US/global)
    "acxiom",
    "epsilon",
    "experian",
    "equifax",
    "transunion",
    "dun & bradstreet",
    "dun and bradstreet",
    "bisnode",
    "lexisnexis",
    "thomson reuters",
    "verisk",
    "corelogic",
    "intelius",
    "spokeo",
    "whitepages",
    "beenverified",
    "peoplefinder",
    "mylife",
    "radaris",
    "instantcheckmate",
    "truthfinder",
    "ussearch",
    "pipl",
    "zoominfo",
    "clearbit",
    "fullcontact",
    "peopledatalabs",
    "tower data",
    "towerdata",
    "exactis",
    "datalogix",
    # Data brokers (FR/EU)
    "pagesjaunes",
    "pages jaunes",
    "solocal",
    "infobel",
    "118218",
    "118712",
    "copainsdavant",
    "copains d'avant",
    "societe.com",
    "manageo",
    "infogreffe",
    "kompass",
    "europages",
    "world-check",
    "creditreform",
    "schufa",
    "crif",
    "altares",
    "ellisphere",
    "coface",
    # CRM / email marketing (they have your data)
    "hubspot",
    "salesforce",
    "mailchimp",
    "intuit mailchimp",
    "sendgrid",
    "twilio",
    "brevo",
    "sendinblue",
    "klaviyo",
    "constant contact",
    "mailjet",
    "campaign monitor",
    # Analytics / tracking
    "google analytics",
    "amplitude",
    "mixpanel",
    "segment",
    "datadog",
    "heap",
    "hotjar",
    "fullstory",
    "mouseflow",
    "crazy egg",
    "optimizely",
    "ab tasty",
    "abtasty",
    "contentsquare",
    "matomo",
    # Clearview / surveillance
    "clearview",
    "palantir",
    # DMP / CDP
    "treasure data",
    "tealium",
    "mparticle",
    "blueconic",
    "zeotap",
    "permutive",
    "1plusx",
    "mediarithmics",
]


def parse_entries(path):
    with open(path) as f:
        lines = f.readlines()

    entries = []
    current = []
    in_brokers = False

    for line in lines:
        if line.strip() == "brokers:":
            in_brokers = True
            continue
        if not in_brokers:
            continue
        if line.startswith("  - id:"):
            if current:
                entries.append(current)
            current = [line]
        elif current:
            current.append(line)

    if current:
        entries.append(current)
    return entries


def extract_field(entry_lines, field):
    for line in entry_lines:
        m = re.match(rf'^\s+{field}:\s*"?(.*?)"?\s*$', line)
        if m:
            return m.group(1)
    return ""


def get_industry(notes):
    """Extract industry from notes (last part after ';')."""
    notes = notes.lower().strip().rstrip('"')
    if ";" in notes:
        return notes.split(";")[-1].strip()
    return notes


def should_keep(entry_lines):
    name = extract_field(entry_lines, "name").lower()
    slug = extract_field(entry_lines, "id").lower()
    contact = extract_field(entry_lines, "contact").lower()
    notes = extract_field(entry_lines, "notes").lower()

    # Always keep known data brokers / ad-tech.
    for kw in ALWAYS_KEEP:
        if kw in name or kw in slug:
            return True

    # Keep if industry is a core data broker / ad-tech industry.
    industry = get_industry(notes)
    if industry in KEEP_INDUSTRIES:
        return True

    return False


def main():
    src = "brokers_scraped.yaml"
    dst = "brokers_final.yaml"

    entries = parse_entries(src)
    kept = [e for e in entries if should_keep(e)]

    eu = sum(1 for e in kept if 'region: "EU"' in "".join(e))
    us = sum(1 for e in kept if 'region: "US"' in "".join(e))
    gl = sum(1 for e in kept if 'region: "GLOBAL"' in "".join(e))

    with open(dst, "w") as f:
        f.write(f"# Data brokers & ad-tech — filtered for FR/EU\n")
        f.write(f"# Total: {len(kept)} (EU: {eu} | US: {us} | GLOBAL: {gl})\n")
        f.write(f"# Reduced from {len(entries)} -> {len(kept)}\n")
        f.write(f"#\n")
        f.write(f"# cp brokers_final.yaml brokers.yaml && ./bot db-seed\n\n")
        f.write("brokers:\n")
        for entry in kept:
            for line in entry:
                f.write(line)

    print(f"Strict filter: {len(entries)} -> {len(kept)} brokers")
    print(f"  EU: {eu} | US: {us} | GLOBAL: {gl}")
    print(f"Written to {dst}")


if __name__ == "__main__":
    main()
