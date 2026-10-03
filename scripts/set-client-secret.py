#!/usr/bin/env python3
"""Enter a Microsoft app secret locally without putting it in chat or history."""
import getpass
import os
from pathlib import Path
import tempfile

os.umask(0o077)
root = Path(__file__).resolve().parent.parent / '.secrets'
print('Enter the client secret VALUE for the Microsoft app registration.')
secret = getpass.getpass('Client secret (hidden): ').strip()
if not secret:
    raise SystemExit('Nothing saved.')
root.mkdir(mode=0o700, exist_ok=True)
os.chmod(root, 0o700)
fd, temporary = tempfile.mkstemp(dir=root)
try:
    with os.fdopen(fd, 'w') as stream:
        stream.write(secret)
    os.replace(temporary, root / 'client-secret')
finally:
    if os.path.exists(temporary):
        os.unlink(temporary)
print('Saved locally. In Beeper, retry Work account — browser sign-in.')
input('Press Enter to close. ')
