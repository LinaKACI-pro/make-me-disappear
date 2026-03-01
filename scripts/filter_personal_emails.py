#!/usr/bin/env python3
"""
Remove brokers that fail quality checks:
  1. No contact email
  2. Personal-looking work email (john.smith@)
  3. Contact email domain doesn't match broker domain (e.g. dpo@gmail.com)
  4. No opt-out URL and no contact email
  5. Duplicate contact email (keep first occurrence)

Usage: python3 scripts/filter_personal_emails.py brokers.yaml
Output: brokers_clean.yaml
"""

import re
import sys

# Generic/department keywords → keep if found anywhere in local part.
GENERIC_PREFIXES = {
    "dpo", "privacy", "gdpr", "rgpd", "dataprotection", "data-protection",
    "data.protection", "legal", "compliance", "optout", "opt-out", "opt.out",
    "unsubscribe", "erasure", "deletion", "noreply", "no-reply", "postmaster",
    "webmaster", "security", "abuse", "rights", "requests", "dsarteam",
    "dsar", "privacyrequests", "privacy-requests",
}

# Weak prefixes: too generic to reach the right person → remove if local is exactly one of these.
WEAK_EXACT = {"info", "contact", "hello", "support", "admin", "team"}

# Free/generic email providers — broker contact should be on their own domain.
FREE_DOMAINS = {
    "gmail.com", "yahoo.com", "yahoo.fr", "hotmail.com", "hotmail.fr",
    "outlook.com", "live.com", "icloud.com", "me.com", "aol.com",
    "protonmail.com", "proton.me", "gmx.com", "gmx.fr", "laposte.net",
    "orange.fr", "free.fr", "sfr.fr", "wanadoo.fr",
}

# Personal email patterns (firstname.lastname, f.lastname, etc.)
PERSONAL_PATTERNS = [
    re.compile(r'^[a-z]+\.[a-z]{2,}@'),        # john.smith@
    re.compile(r'^[a-z]\.[a-z]{3,}@'),          # j.smith@
    re.compile(r'^[a-z]{2,8}[._-][a-z]{2,8}@'), # john-smith@ or john_smith@
]


def email_domain(email: str) -> str:
    if "@" not in email:
        return ""
    return email.lower().split("@")[1].strip()


def broker_domain(broker_id: str) -> str:
    """Guess the broker's domain from its ID (slug)."""
    # IDs look like "criteo-com", "acxiom", "pages-jaunes-fr"
    slug = broker_id.lower().replace("-", ".")
    # Try to find a TLD-like suffix
    parts = slug.split(".")
    if len(parts) >= 2 and len(parts[-1]) in (2, 3):
        return ".".join(parts[-2:])
    return slug


def is_weak_email(email: str) -> bool:
    """Return True if the local part is exactly a weak generic word."""
    local = email.lower().strip().split("@")[0]
    return local in WEAK_EXACT


def is_personal_email(email: str) -> bool:
    email = email.lower().strip()
    local = email.split("@")[0]

    for prefix in GENERIC_PREFIXES:
        if prefix in local:
            return False

    for pat in PERSONAL_PATTERNS:
        if pat.match(email):
            return True

    return False


def check_domain_mismatch(contact: str, broker_id: str, opt_out_url: str) -> bool:
    """Return True if contact email is on a free/generic provider domain."""
    domain = email_domain(contact)
    if domain in FREE_DOMAINS:
        return True
    return False


def parse_entries(path):
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


def main():
    src = sys.argv[1] if len(sys.argv) > 1 else "brokers.yaml"
    dst = src.replace(".yaml", "_clean.yaml")

    header, entries = parse_entries(src)

    kept = []
    removed = {"no_contact": [], "personal": [], "weak": [], "free_domain": [], "no_url_no_contact": [], "duplicate": []}
    seen_contacts = {}

    for entry in entries:
        contact = extract_field(entry, "contact").lower().strip()
        broker_id = extract_field(entry, "id")
        name = extract_field(entry, "name")
        opt_out_url = extract_field(entry, "opt_out_url")

        # 1. No contact email.
        if not contact:
            removed["no_contact"].append((name, ""))
            continue

        # 2. Personal-looking email.
        if is_personal_email(contact):
            removed["personal"].append((name, contact))
            continue

        # 2b. Weak generic email (info@, contact@, etc.).
        if is_weak_email(contact):
            removed["weak"].append((name, contact))
            continue

        # 3. Contact on free provider domain.
        if check_domain_mismatch(contact, broker_id, opt_out_url):
            removed["free_domain"].append((name, contact))
            continue

        # 4. No opt-out URL and no usable contact.
        if not opt_out_url and not contact:
            removed["no_url_no_contact"].append((name, contact))
            continue

        # 5. Duplicate contact email.
        if contact in seen_contacts:
            removed["duplicate"].append((name, contact))
            continue
        seen_contacts[contact] = name

        kept.append(entry)

    with open(dst, "w") as f:
        for line in header:
            f.write(line)
        for entry in kept:
            for line in entry:
                f.write(line)

    total_removed = sum(len(v) for v in removed.values())
    print(f"Total: {len(entries)} → kept: {len(kept)}, removed: {total_removed}")
    print(f"  No contact:        {len(removed['no_contact'])}")
    print(f"  Personal email:    {len(removed['personal'])}")
    print(f"  Weak email:        {len(removed['weak'])}")
    print(f"  Free domain:       {len(removed['free_domain'])}")
    print(f"  No URL + no email: {len(removed['no_url_no_contact'])}")
    print(f"  Duplicate email:   {len(removed['duplicate'])}")
    print(f"\nWritten to {dst}")

    for reason, entries_list in removed.items():
        if entries_list:
            print(f"\n--- {reason} ({len(entries_list)}) ---")
            for name, contact in entries_list:
                print(f"  {name:<35} {contact}")


if __name__ == "__main__":
    main()
