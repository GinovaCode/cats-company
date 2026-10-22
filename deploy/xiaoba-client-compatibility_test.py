import importlib.util
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("xiaoba_compat", ROOT / "deploy" / "check-xiaoba-client-compat.py")
MODULE = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(MODULE)


class CompatibilityCheckTest(unittest.TestCase):
    manifest = {
        "schemaVersion": 1,
        "clientRepository": "buildsense-ai/XiaoBa-CLI",
        "protocols": [{"id": "multi-device", "minimumClientCommit": "abc", "requireStableRelease": True}],
    }

    def test_main_mode_passes_when_client_contains_commit(self):
        original = MODULE.compare_contains
        try:
            MODULE.compare_contains = lambda *args, **kwargs: True
            messages = MODULE.check_manifest(self.manifest, "main", None, main_ref="main")
        finally:
            MODULE.compare_contains = original
        self.assertIn("passed", messages[0])

    def test_main_mode_reports_missing_commit(self):
        original = MODULE.compare_contains
        try:
            MODULE.compare_contains = lambda *args, **kwargs: False
            messages = MODULE.check_manifest(self.manifest, "main", None)
        finally:
            MODULE.compare_contains = original
        self.assertIn("not contained", messages[0])

    def test_stable_release_checks_release_tag(self):
        calls = []
        original_compare = MODULE.compare_contains
        try:
            MODULE.compare_contains = lambda *args, **kwargs: calls.append(args[2]) or True
            messages = MODULE.check_manifest(self.manifest, "stable-release", None, latest_release={"tag_name": "v1.7.0"})
        finally:
            MODULE.compare_contains = original_compare
        self.assertIn("passed", messages[0])
        self.assertEqual(calls, ["main", "v1.7.0"])

    def test_stable_release_fails_when_tag_lacks_commit(self):
        original = MODULE.compare_contains
        try:
            MODULE.compare_contains = lambda repository, required, target, token, api_url=None: target == "main"
            messages = MODULE.check_manifest(self.manifest, "stable-release", None, latest_release={"tag_name": "v1.6.2"})
        finally:
            MODULE.compare_contains = original
        self.assertIn("does not contain", messages[0])


if __name__ == "__main__":
    unittest.main()
