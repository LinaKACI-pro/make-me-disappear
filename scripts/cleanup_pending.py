#!/usr/bin/env python3
"""
Delete pending requests for brokers no longer in brokers.yaml.
Also removes those brokers from the brokers table.

Usage: python3 scripts/cleanup_pending.py [brokers.yaml] [bot.db]
"""

import re
import sqlite3
import sys

def load_broker_ids(path):
    ids = []
    with open(path) as f:
        for line in f:
            m = re.match(r'^\s+- id:\s*"?(.*?)"?\s*$', line)
            if m:
                ids.append(m.group(1).strip())
    return ids


def main():
    yaml_path = sys.argv[1] if len(sys.argv) > 1 else "brokers.yaml"
    db_path   = sys.argv[2] if len(sys.argv) > 2 else "bot.db"

    broker_ids = load_broker_ids(yaml_path)
    if not broker_ids:
        print("No broker IDs found in yaml, aborting.")
        return

    placeholders = ",".join("?" * len(broker_ids))

    con = sqlite3.connect(db_path)
    cur = con.cursor()

    cur.execute(f"""
        DELETE FROM requests
        WHERE status = 'pending'
        AND broker_id NOT IN ({placeholders})
    """, broker_ids)
    deleted_requests = cur.rowcount

    cur.execute(f"""
        DELETE FROM brokers
        WHERE id NOT IN ({placeholders})
    """, broker_ids)
    deleted_brokers = cur.rowcount

    con.commit()
    con.close()

    print(f"Deleted {deleted_requests} pending request(s)")
    print(f"Deleted {deleted_brokers} broker(s) from DB")


if __name__ == "__main__":
    main()
