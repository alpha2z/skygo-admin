#!/usr/bin/env python3
"""Generate new API credentials without overwriting existing deployment files."""
import argparse
import base64
import os
from pathlib import Path
import secrets
import subprocess
import tempfile


def generate(output):
    output = Path(output)
    output.mkdir(mode=0o700, parents=False, exist_ok=False)
    # OpenSSL emits PKCS#8 private and SubjectPublicKeyInfo public Ed25519 DER.
    with tempfile.TemporaryDirectory() as tmp:
        key = Path(tmp) / 'key.pem'
        subprocess.run(['openssl', 'genpkey', '-algorithm', 'ED25519', '-out', str(key)],
                       check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        private = subprocess.check_output(['openssl', 'pkey', '-in', str(key), '-outform', 'DER'], stderr=subprocess.DEVNULL)
        public = subprocess.check_output(['openssl', 'pkey', '-in', str(key), '-pubout', '-outform', 'DER'], stderr=subprocess.DEVNULL)
    if len(private) != 48 or private[:16].hex() != '302e020100300506032b657004220420':
        raise ValueError('Unsupported Ed25519 private key encoding')
    if len(public) != 44 or public[:12].hex() != '302a300506032b6570032100':
        raise ValueError('Unsupported Ed25519 public key encoding')
    values = {'jwt': secrets.token_urlsafe(32), 'bootstrap': secrets.token_urlsafe(32),
              'signing': base64.b64encode(private[16:] + public[12:]).decode(),
              'signing.pub': base64.b64encode(public[12:]).decode()}
    for name, value in values.items():
        fd = os.open(output / name, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, 'w') as handle:
            handle.write(value + '\n')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--out', default='private-new', help='New directory; must not exist')
    args = parser.parse_args()
    try:
        generate(args.out)
    except (OSError, ValueError, subprocess.SubprocessError):
        raise SystemExit('Key generation failed. No existing files overwritten; inspect the new output directory before retrying.')
    print('Generated jwt, bootstrap, signing and signing.pub in private files. No secret values printed.')
