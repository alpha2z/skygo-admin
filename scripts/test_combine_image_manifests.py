import copy
import importlib.util
import pathlib
import unittest

spec = importlib.util.spec_from_file_location('combine', pathlib.Path(__file__).with_name('combine-image-manifests.py'))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class ManifestCombinationTest(unittest.TestCase):
    def setUp(self):
        self.manifests = [dict(version=2, id='ci-1-1', build={'source_commit': 'a' * 40}, images=[
            dict(service=s, platform=p, reference='ghcr.io/example/admin/' + s + '@sha256:' + 'a' * 64)
            for s in ('admin-api', 'admin-web', 'ops-agent')]) for p in ('linux/amd64', 'linux/arm64')]

    def test_complete(self):
        self.assertEqual(len(module.combine(self.manifests)['images']), 6)

    def test_reject_incomplete_duplicate_and_different_build(self):
        for case in ('incomplete', 'duplicate', 'different-build'):
            manifests = copy.deepcopy(self.manifests)
            if case == 'incomplete':
                manifests[1]['images'].pop()
            elif case == 'duplicate':
                manifests[1]['images'][0] = manifests[0]['images'][0]
            else:
                manifests[1]['build']['source_commit'] = 'b' * 40
            with self.subTest(case=case), self.assertRaises(ValueError):
                module.combine(manifests)
