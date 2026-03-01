#!/usr/bin/env python3
"""
Filter brokers_scraped.yaml to keep only relevant data brokers/ad-tech.
Reads line-by-line (no YAML lib needed).

Usage: python3 scripts/filter_brokers.py
"""

import re

# Industries to KEEP — these are the ones that collect/sell personal data.
KEEP_INDUSTRIES = {
    "advertising services",
    "marketing services",
    "market research",
    "data infrastructure and analytics",
    "data security software products",
    "business intelligence platforms",
    "information services",
    "technology, information and internet",
    "technology, information and media",
    "telecommunications",
    "internet publishing",
    "internet marketplace platforms",
    "online media",
    "financial services",
    "insurance",
    "software development",
    "it services and it consulting",
    "computer and network security",
    "staffing and recruiting",
    "human resources services",
    "online audio and video media",
    "wireless services",
}

# Industries to ALWAYS REJECT — clearly not data brokers.
REJECT_INDUSTRIES = {
    "hospitals and health care",
    "higher education",
    "government administration",
    "construction",
    "motor vehicle manufacturing",
    "truck transportation",
    "food and beverage manufacturing",
    "food and beverage services",
    "farming",
    "mining",
    "oil and gas",
    "architecture and planning",
    "civil engineering",
    "mechanical or industrial engineering",
    "religious institutions",
    "museums, historical sites, and zoos",
    "libraries",
    "performing arts",
    "music",
    "musicians",
    "primary and secondary education",
    "veterinary services",
    "law enforcement",
    "armed forces",
    "public safety",
    "furniture",
    "maritime transportation",
    "shipbuilding",
    "semiconductor manufacturing",
    "chemical manufacturing",
    "plastics manufacturing",
    "paper and forest product manufacturing",
    "printing services",
    "textile manufacturing",
    "pharmaceutical manufacturing",
    "medical equipment manufacturing",
    "medical device",
    "medical practices",
    "mental health care",
    "biotechnology research",
    "biotechnology",
    "nanotechnology research",
    "defense and space manufacturing",
    "airlines and aviation",
    "accommodation and food services",
    "restaurants",
    "industrial machinery manufacturing",
    "machinery manufacturing",
    "sporting goods manufacturing",
    "furniture and home furnishings manufacturing",
    "appliances, electrical, and electronics manufacturing",
    "packaging and containers manufacturing",
    "personal care product manufacturing",
    "beverage manufacturing",
    "gambling facilities and casinos",
    "spectator sports",
    "animation and post-production",
    "photography",
    "graphic design",
    "retail groceries",
    "retail pharmacies",
    "retail art supplies",
    "renewable energy semiconductor manufacturing",
    "vehicle repair and maintenance",
    "cosmetics",
    "individual and family services",
    "wholesale building materials",
    "recreation facilities",
    "political organizations",
    "environmental services",
    "education administration programs",
    "e-learning providers",
}

# Known data brokers / ad-tech by domain or name — always keep.
ALWAYS_KEEP = {
    "criteo", "taboola", "outbrain", "quantcast", "lotame", "teads",
    "acxiom", "liveramp", "epsilon", "experian", "equifax", "oracle",
    "salesforce", "adobe", "google", "facebook", "meta", "amazon",
    "twitter", "linkedin", "microsoft", "apple", "tiktok", "bytedance",
    "snap", "snapchat", "pinterest", "reddit", "spotify", "netflix",
    "pagesjaunes", "solocal", "infobel", "118218", "copainsdavant",
    "societe.com", "manageo", "infogreffe", "sirene",
    "clearview", "palantir", "zoominfo", "spokeo", "whitepages",
    "beenverified", "intelius", "peoplefinder", "mylife",
    "cloudflare", "akamai", "fastly",
    "hubspot", "mailchimp", "sendgrid", "twilio",
    "datadog", "segment", "amplitude", "mixpanel",
    "stripe", "paypal", "klarna", "adyen",
}


def parse_entries(path):
    """Parse YAML entries from the file, yielding (lines, metadata) per broker."""
    with open(path) as f:
        lines = f.readlines()

    header = []
    entries = []
    current = []
    in_brokers = False

    for line in lines:
        if line.strip() == "brokers:":
            in_brokers = True
            header.append(line)
            continue

        if not in_brokers:
            header.append(line)
            continue

        if line.startswith("  - id:"):
            if current:
                entries.append(current)
            current = [line]
        elif current:
            current.append(line)

    if current:
        entries.append(current)

    return header, entries


def extract_field(entry_lines, field):
    for line in entry_lines:
        m = re.match(rf'^\s+{field}:\s*"?(.*?)"?\s*$', line)
        if m:
            return m.group(1)
    return ""


def should_keep(entry_lines):
    name = extract_field(entry_lines, "name").lower()
    contact = extract_field(entry_lines, "contact").lower()
    notes = extract_field(entry_lines, "notes").lower()
    slug = extract_field(entry_lines, "id").lower()

    # Always keep known data brokers.
    for kw in ALWAYS_KEEP:
        if kw in name or kw in slug or kw in contact:
            return True

    # Check industry from notes (last part after ";").
    industry = ""
    if ";" in notes:
        industry = notes.split(";")[-1].strip().rstrip('"')
    else:
        industry = notes.strip().rstrip('"')

    # Reject known irrelevant industries.
    if industry in REJECT_INDUSTRIES:
        return False

    # Keep known relevant industries.
    if industry in KEEP_INDUSTRIES:
        return True

    # Keep if email looks like a privacy/dpo address.
    if any(p in contact for p in ["dpo@", "privacy@", "gdpr@", "rgpd@", "dataprotection@"]):
        return True

    # Default: reject (too noisy otherwise).
    return False


def main():
    src = "brokers_scraped.yaml"
    dst = "brokers_filtered.yaml"

    header, entries = parse_entries(src)

    kept = []
    for entry in entries:
        if should_keep(entry):
            kept.append(entry)

    # Count stats.
    eu = sum(1 for e in kept if 'region: "EU"' in "".join(e))
    us = sum(1 for e in kept if 'region: "US"' in "".join(e))
    gl = sum(1 for e in kept if 'region: "GLOBAL"' in "".join(e))

    with open(dst, "w") as f:
        f.write(f"# Filtered from {len(entries)} -> {len(kept)} brokers\n")
        f.write(f"# EU: {eu} | US: {us} | GLOBAL: {gl}\n")
        f.write(f"#\n")
        f.write(f"# cp brokers_filtered.yaml brokers.yaml && ./bot db-seed\n\n")
        f.write("brokers:\n")
        for entry in kept:
            for line in entry:
                f.write(line)

    print(f"Filtered: {len(entries)} -> {len(kept)} brokers")
    print(f"  EU: {eu} | US: {us} | GLOBAL: {gl}")
    print(f"Written to {dst}")


if __name__ == "__main__":
    main()
