#!/usr/bin/env python3
import smtplib
import ssl
import sys

host = "mail.infomaniak.com"

# Test port 465 (TLS direct)
print(f"Testing {host}:465 (TLS)...")
try:
    ctx = ssl.create_default_context()
    with smtplib.SMTP_SSL(host, 465, context=ctx) as s:
        s.set_debuglevel(1)
        s.login(sys.argv[1], sys.argv[2])
        print("SUCCESS on 465!")
except Exception as e:
    print(f"FAILED 465: {e}")

# Test port 587 (STARTTLS)
print(f"\nTesting {host}:587 (STARTTLS)...")
try:
    with smtplib.SMTP(host, 587) as s:
        s.set_debuglevel(1)
        s.starttls()
        s.login(sys.argv[1], sys.argv[2])
        print("SUCCESS on 587!")
except Exception as e:
    print(f"FAILED 587: {e}")
