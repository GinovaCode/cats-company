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

    def gated_manifest(self, **protocol_overrides):
        protocol = {
            "id": "multi-device",
            "minimumClientCommit": "abc",
            "requireStableRelease": True,
            "enabledByEnv": "CATSCO_TEST_PROTOCOL_ENABLED",
            **protocol_overrides,
        }
        return {
            "schemaVersion": 1,
            "clientRepository": "buildsense-ai/XiaoBa-CLI",
            "protocols": [protocol],
        }

    def test_protocol_that_is_switched_off_does_not_gate_production(self):
        # A protocol that is declared but not enabled must not freeze unrelated
        # deployments while its client release is still pending.
        calls = []
        original = MODULE.compare_contains
        try:
            MODULE.compare_contains = lambda repository, required, target, token, api_url=None: calls.append(target) or True
            messages = MODULE.check_manifest(
                self.gated_manifest(assumeEnabledWhenUnset=False),
                "stable-release",
                None,
                environ={"CATSCO_TEST_PROTOCOL_ENABLED": "0"},
            )
        finally:
            MODULE.compare_contains = original
        self.assertEqual(calls, ["main"])
        self.assertIn("not enabled yet", messages[0])
        self.assertNotIn("does not contain", messages[0])

    def test_enabled_protocol_still_requires_a_released_client(self):
        calls = []
        original = MODULE.compare_contains
        try:
            MODULE.compare_contains = lambda repository, required, target, token, api_url=None: calls.append(target) or True
            messages = MODULE.check_manifest(
                self.gated_manifest(assumeEnabledWhenUnset=False),
                "stable-release",
                None,
                latest_release={"tag_name": "v1.7.0"},
                environ={"CATSCO_TEST_PROTOCOL_ENABLED": "1"},
            )
        finally:
            MODULE.compare_contains = original
        self.assertEqual(calls, ["main", "v1.7.0"])
        self.assertIn("passed", messages[0])

    def test_enabled_protocol_fails_when_the_release_lacks_the_commit(self):
        original = MODULE.compare_contains
        try:
            MODULE.compare_contains = lambda repository, required, target, token, api_url=None: target == "main"
            messages = MODULE.check_manifest(
                self.gated_manifest(assumeEnabledWhenUnset=False),
                "stable-release",
                None,
                latest_release={"tag_name": "v1.6.2"},
                environ={"CATSCO_TEST_PROTOCOL_ENABLED": "true"},
            )
        finally:
            MODULE.compare_contains = original
        self.assertIn("stable release v1.6.2 does not contain", messages[0])

    def test_flags_stay_fail_closed_when_unset_or_unknown(self):
        # Unset/empty flags fall back to assumeEnabledWhenUnset (default true),
        # and an unrecognised value such as a typo is treated as enabled, so a
        # missing declaration can never quietly remove the release requirement.
        default = self.gated_manifest()["protocols"][0]
        opted_out = self.gated_manifest(assumeEnabledWhenUnset=False)["protocols"][0]
        self.assertTrue(MODULE.protocol_enabled(default, {}))
        self.assertTrue(MODULE.protocol_enabled(default, {"CATSCO_TEST_PROTOCOL_ENABLED": ""}))
        self.assertFalse(MODULE.protocol_enabled(opted_out, {}))
        self.assertFalse(MODULE.protocol_enabled(opted_out, {"CATSCO_TEST_PROTOCOL_ENABLED": ""}))
        for disabled in ("0", "false", "NO", " off "):
            with self.subTest(disabled=disabled):
                self.assertFalse(MODULE.protocol_enabled(default, {"CATSCO_TEST_PROTOCOL_ENABLED": disabled}))
        for enabled in ("1", "true", "yes", "on", "ture"):
            with self.subTest(enabled=enabled):
                self.assertTrue(MODULE.protocol_enabled(opted_out, {"CATSCO_TEST_PROTOCOL_ENABLED": enabled}))

    def test_manifest_rejects_invalid_gate_declarations(self):
        import json
        import tempfile

        base = {
            "schemaVersion": 1,
            "clientRepository": "buildsense-ai/XiaoBa-CLI",
        }
        for protocol, message in (
            ({"id": "p", "minimumClientCommit": "abc", "enabledByEnv": "lower-case"}, "upper-case environment variable"),
            ({"id": "p", "minimumClientCommit": "abc", "enabledByEnv": "OK", "assumeEnabledWhenUnset": "no"}, "must be a boolean"),
        ):
            with self.subTest(protocol=protocol):
                with tempfile.TemporaryDirectory() as directory:
                    path = Path(directory) / "manifest.json"
                    path.write_text(json.dumps({**base, "protocols": [protocol]}), encoding="utf-8")
                    with self.assertRaisesRegex(RuntimeError, message):
                        MODULE.load_manifest(path)


if __name__ == "__main__":
    unittest.main()
