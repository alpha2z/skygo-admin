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
                for component in ('admin-api', 'admin-web', 'ops-agent'):
                    tar.add(ROOT / ('deploy/' + component + '-templates'), arcname='skygo-admin-main/deploy/' + component + '-templates')
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
            for component in ('admin-web', 'ops-agent'):
                dest = base / component
                self.assertEqual(run(dest, ('--component', component)).returncode, 0)
                for filename in ('compose.yaml', '.env.example', 'start.sh', 'update.sh', 'README.md'):
                    self.assertTrue((dest / filename).is_file())
                self.assertFalse((dest / '.env').exists())
                self.assertFalse((dest / 'private/management-dsn.example').exists())
                self.assertFalse((dest / 'private/agent-token').exists())
                self.assertNotEqual(run(dest, ('--component', component)).returncode, 0)
                self.assertNotEqual(subprocess.run(['sh', str(dest / 'start.sh')], capture_output=True).returncode, 0)
            self.assertEqual(run(base / 'invalid', ('--component', 'unrecognized')).returncode, 2)
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
