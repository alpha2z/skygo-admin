import base64
import importlib.util
from pathlib import Path
import subprocess
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('keys', Path(__file__).resolve().parents[1] / 'deploy/admin-api-templates/generate-keys.py')
keys = importlib.util.module_from_spec(spec)
spec.loader.exec_module(keys)


class TemplateKeysTest(unittest.TestCase):
    def test_key_pair_permissions_and_no_overwrite(self):
        with tempfile.TemporaryDirectory() as tmp:
            output = Path(tmp) / 'private'
            keys.generate(output)
            values = {p.name: p.read_bytes() for p in output.iterdir()}
            private = base64.b64decode(values['signing'])
            public = base64.b64decode(values['signing.pub'])
            self.assertEqual(len(private), 64)
            self.assertEqual(len(public), 32)
            self.assertEqual(private[32:], public)
            derived = subprocess.check_output(['openssl', 'pkey', '-inform', 'DER', '-pubout', '-outform', 'DER'],
                                             input=bytes.fromhex('302e020100300506032b657004220420') + private[:32], stderr=subprocess.DEVNULL)
            self.assertEqual(derived[-32:], public)
            self.assertNotEqual(values['jwt'], values['bootstrap'])
            self.assertGreaterEqual(len(values['jwt'].strip()), 32)
            self.assertEqual(output.stat().st_mode & 0o777, 0o700)
            for p in output.iterdir():
                self.assertEqual(p.stat().st_mode & 0o777, 0o600)
            with self.assertRaises(FileExistsError):
                keys.generate(output)
            self.assertEqual(values, {p.name: p.read_bytes() for p in output.iterdir()})
