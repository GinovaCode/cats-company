#!/usr/bin/env python3
"""Fail a CatsCompany check when its paired XiaoBa client is not ready.

The compatibility manifest is intentionally data-only.  It lets a server
change declare the minimum client commit it needs without making release
workflows guess which other repository or PR is part of the change.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
from pathlib import Path
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen


ROOT = Path(__file__).resolve().parents[1]
MANIFEST = ROOT / "docs" / "xiaoba-cli-compatibility.json"


def load_manifest(path: Path = MANIFEST) -> dict:
    if not path.exists():
        return {}
    try:
        manifest = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise RuntimeError(f"Cannot read compatibility manifest {path}: {exc}") from exc
    if not isinstance(manifest, dict) or manifest.get("schemaVersion") != 1:
        raise RuntimeError("Compatibility manifest must be an object with schemaVersion=1")
    repository = manifest.get("clientRepository")
    protocols = manifest.get("protocols")
    if not isinstance(repository, str) or not repository.strip():
        raise RuntimeError("Compatibility manifest clientRepository is required")
    if not isinstance(protocols, list):
        raise RuntimeError("Compatibility manifest protocols must be an array")
    for protocol in protocols:
        if not isinstance(protocol, dict):
            raise RuntimeError("Each compatibility protocol must be an object")
        if not protocol.get("id") or not protocol.get("minimumClientCommit"):
            raise RuntimeError("Each compatibility protocol needs id and minimumClientCommit")
    return manifest


def github_get(path: str, token: str | None, api_url: str | None = None) -> dict:
    base = (api_url or os.environ.get("GITHUB_API_URL") or "https://api.github.com").rstrip("/")
    headers = {"Accept": "application/vnd.github+json", "User-Agent": "catscompany-release-compat"}
    if token:
        headers["Authorization"] = f"Bearer {token}"
    request = Request(f"{base}{path}", headers=headers)
    try:
        with urlopen(request, timeout=30) as response:
            return json.loads(response.read().decode("utf-8"))
    except (HTTPError, URLError, TimeoutError, json.JSONDecodeError) as exc:
        raise RuntimeError(f"GitHub compatibility lookup failed for {path}: {exc}") from exc


def compare_contains(repository: str, required_commit: str, target_ref: str, token: str | None, api_url: str | None = None) -> bool:
    result = github_get(f"/repos/{repository}/compare/{required_commit}...{target_ref}", token, api_url)
    return result.get("status") in {"ahead", "identical"}


def check_manifest(manifest: dict, mode: str, token: str | None, api_url: str | None = None, main_ref: str = "main", latest_release: dict | None = None) -> list[str]:
    if not manifest:
        return ["XiaoBa client compatibility manifest is not present; check skipped."]
    repository = manifest["clientRepository"]
    failures: list[str] = []
    for protocol in manifest["protocols"]:
        required = protocol["minimumClientCommit"]
        protocol_id = protocol["id"]
        if not compare_contains(repository, required, main_ref, token, api_url):
            failures.append(f"{protocol_id}: {required} is not contained in {repository}@{main_ref}")
            continue
        if mode == "stable-release" and protocol.get("requireStableRelease", True):
            release = latest_release or github_get(f"/repos/{repository}/releases/latest", token, api_url)
            tag = release.get("tag_name")
            if not tag or not compare_contains(repository, required, tag, token, api_url):
                failures.append(f"{protocol_id}: stable release {tag or '(missing)'} does not contain {required}")
    if failures:
        return failures
    if mode == "stable-release":
        return [f"XiaoBa compatibility passed: {len(manifest['protocols'])} protocol(s) have a released client."]
    return [f"XiaoBa compatibility passed: {len(manifest['protocols'])} protocol(s) are present on client main."]


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=("main", "stable-release"), default="main")
    parser.add_argument("--manifest", type=Path, default=MANIFEST)
    args = parser.parse_args()
    try:
        manifest = load_manifest(args.manifest)
        messages = check_manifest(manifest, args.mode, os.environ.get("GH_TOKEN"))
    except RuntimeError as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 1
    for message in messages:
        print(message, file=sys.stderr if "does not" in message or "not contained" in message else sys.stdout)
    return 1 if any("does not" in message or "not contained" in message for message in messages) else 0


if __name__ == "__main__":
    raise SystemExit(main())
