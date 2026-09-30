import json,os,pathlib,subprocess,sys,tempfile,unittest
class Provenance(unittest.TestCase):
 def test_composition_identity_and_default(self):
  with tempfile.TemporaryDirectory() as name:
   folder=pathlib.Path(name)
   (folder/'metadata-admin-api.json').write_text(json.dumps({'containerimage.digest':'sha256:'+'a'*64}))
   env=dict(os.environ,GITHUB_REPOSITORY='example/composition',GITHUB_RUN_ID='1',GITHUB_RUN_ATTEMPT='2',GITHUB_REF='refs/heads/main',GITHUB_SHA='b'*40,BUILD_STARTED='2026-10-01T00:00:00Z')
   base=[sys.executable,str(pathlib.Path(__file__).with_name('build-manifest.py')),'--metadata-dir',str(folder),'--component','admin-api','--platform','linux/arm64','--out',str(folder/'manifest.json')]
   for flags,expected in [([], 'build.yml'),(['--workflow','composition.yml'],'composition.yml')]:
    subprocess.run(base+flags,env=env,check=True,capture_output=True)
    manifest=json.loads((folder/'manifest.json').read_text())
    self.assertEqual(manifest['build']['workflow'],expected)
    self.assertTrue(manifest['images'][0]['reference'].startswith('ghcr.io/example/composition/admin-api@sha256:'))
   self.assertNotEqual(subprocess.run(base+['--workflow','../other.yml'],env=env,capture_output=True).returncode,0)
if __name__=='__main__':unittest.main()
