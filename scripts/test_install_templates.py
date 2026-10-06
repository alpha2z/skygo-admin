import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


class InstallTemplatesTest(unittest.TestCase):
    def test_install_failures_and_existing_files(self):
        with tempfile.TemporaryDirectory() as tmp:
            base = Path(tmp)
            archive = base / 'templates.tar.gz'
            with tarfile.open(archive, 'w:gz') as tar:
                tar.add(ROOT / 'deploy/admin-api-templates', arcname='skygo-admin-main/deploy/admin-api-templates')
            binary = base / 'bin'
            binary.mkdir()
            curl = binary / 'curl'
            curl.write_text('''#!/bin/sh
[ "${FAIL_DOWNLOAD:-0}" = 0 ] || exit 22
while [ "$#" -gt 0 ]; do
  if [ "$1" = --output ]; then cp "$FIXTURE" "$2"; exit; fi
  shift
done
exit 1
''')
            curl.chmod(0o755)
            env = {**os.environ, 'PATH': str(binary) + ':' + os.environ['PATH'], 'FIXTURE': str(archive)}

            def run(dest, extra=(), overrides=None):
                return subprocess.run(['sh', str(ROOT / 'install-templates.sh'), '--output', str(dest), *extra],
                                      env={**env, **(overrides or {})}, capture_output=True)

            target = base / 'with spaces'
            self.assertEqual(run(target).returncode, 0)
            self.assertTrue((target / 'private/management-dsn.example').is_file())
            self.assertFalse((target / 'private/management-dsn').exists())
            self.assertFalse((target / '.env').exists())
            marker = target / 'keep'
            marker.write_text('preserve')
            self.assertNotEqual(run(target).returncode, 0)
            self.assertEqual(marker.read_text(), 'preserve')
            link = base / 'symlink'
            link.symlink_to(target, target_is_directory=True)
            self.assertNotEqual(run(link).returncode, 0)
            failed = base / 'failed'
            self.assertNotEqual(run(failed, overrides={'FAIL_DOWNLOAD': '1'}).returncode, 0)
            self.assertFalse(failed.exists())
            with tarfile.open(archive, 'w:gz'):
                pass
            self.assertNotEqual(run(failed).returncode, 0)
            self.assertFalse(failed.exists())
            self.assertEqual(run(failed, ('--ref', '../bad')).returncode, 2)
