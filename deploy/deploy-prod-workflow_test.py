import re
import importlib.util
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parent.parent
SPEC = importlib.util.spec_from_file_location("release_gate", ROOT / "deploy/check-prod-release.py")
GATE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(GATE)


class DeployProdWorkflowTest(unittest.TestCase):
    def test_prod_requires_both_upstream_workflows_before_using_deployment_secrets(self):
        workflow = (ROOT / ".github/workflows/deploy-prod.yml").read_text(encoding="utf-8")
        self.assertIn('workflows: ["CI", "Deploy Docker Test"]', workflow)
        self.assertIn('branches: ["main"]', workflow)
        self.assertIn('actions: read', workflow)
        self.assertIn('github.event.workflow_run.head_repository.full_name == github.repository', workflow)
        self.assertIn("needs: release-gate", workflow)
        self.assertIn("if: ${{ needs.release-gate.outputs.ready == 'true' }}", workflow)
        self.assertEqual(workflow.count("run: python3 deploy/check-prod-release.py"), 2)
        self.assertIn("IMAGE_TAG: ${{ needs.release-gate.outputs.revision }}", workflow)
        self.assertIn("ref: ${{ needs.release-gate.outputs.revision }}", workflow)
        # Only ready deployment jobs serialize; unrelated failed/PR completions
        # must not cancel production or displace its pending job.
        self.assertNotIn("\nconcurrency:", workflow)
        self.assertIn("cancel-in-progress: false", workflow)
        self.assertNotIn("cancel-in-progress: true", workflow)
        self.assertIn("run-name: Deploy production ${{ github.event.workflow_run.head_sha }}", workflow)
        self.assertIn("if: ${{ failure() && steps.deploy.outcome == 'failure' }}", workflow)
        for name in ("Checkout tested revision", "Prepare SSH key", "Upload prod stack files", "Deploy prod stack"):
            step = workflow.split(f"- name: {name}\n", 1)[1].split("\n      - name:", 1)[0]
            self.assertIn("if: steps.gate.outputs.ready == 'true'", step)

    def test_prod_deploy_uses_noninteractive_sudo_for_root_owned_state(self):
        workflow = (ROOT / ".github/workflows/deploy-prod.yml").read_text(
            encoding="utf-8"
        )

        for script in ("sync-vapid-env.py", "sync-stt-env.py", "sync-worker-project-env.py", "sync-shimo-env.py", "sync-shimo-worker-env.py"):
            self.assertIn(
                f'"sudo -n python3 ${{PROD_STACK_ROOT}}/compose/{script}',
                workflow,
            )

        self.assertIn(
            'sudo -n -E "$root/compose/remote-build-source.sh" "$root" "$revision" "$owner"',
            workflow,
        )
        self.assertIn(
            'sudo -n -E "$root/compose/remote-deploy.sh" "$root" "$revision"',
            workflow,
        )
        self.assertIn(
            'sudo -n -E bash ${PROD_STACK_ROOT}/compose/remote-rollback.sh',
            workflow,
        )
        self.assertIn(
            'sudo -n bash ${PROD_STACK_ROOT}/compose/remote-status.sh',
            workflow,
        )
        self.assertIn("SHIMO_WORKER_ENABLED", workflow)
        self.assertIn("Shimo Worker enablement requires actor, Worker, and session secrets", workflow)

    def test_prod_deploy_installs_and_starts_worker_release_prune_timer(self):
        workflow = (ROOT / ".github/workflows/deploy-prod.yml").read_text(
            encoding="utf-8"
        )
        self.assertIn(
            'deploy/prod/systemd/catsco-worker-release-prune.service', workflow
        )
        self.assertIn(
            'deploy/prod/systemd/catsco-worker-release-prune.timer', workflow
        )
        self.assertIn(
            'systemctl enable --now catsco-worker-release-prune.timer', workflow
        )

    def test_prod_deploy_repairs_cloud_worker_timeout_route_on_both_domains(self):
        workflow = (ROOT / ".github/workflows/deploy-prod.yml").read_text(
            encoding="utf-8"
        )
        self.assertIn("ensure-cloud-worker-nginx.sh", workflow)
        self.assertIn("ensure-shimo-login-nginx.sh", workflow)
        self.assertIn("update-nginx-shimo-login.py", workflow)
        self.assertIn(
            'sudo -n "$root/compose/ensure-shimo-login-nginx.sh" /etc/nginx/sites-available/catscompany-app "$root/env/prod.env"',
            workflow,
        )
        for route in (
            "/etc/nginx/sites-available/catscompany-app:app.catsco.cc",
            "/etc/nginx/sites-available/catscompany-api:api.catsco.cc",
            "/etc/nginx/sites-available/catscompany-app-cn:app.catsco.cn",
            "/etc/nginx/sites-available/catscompany-api-cn:api.catsco.cn",
        ):
            self.assertIn(route, workflow)

    def test_prod_deploy_script_survives_stdin_reading_children(self):
        # The deploy body reaches the host through `ssh ... bash -s`, so any
        # child that inherits and reads stdin swallows the rest of the script.
        # `docker compose exec` does exactly that: before this guard, every
        # command after it - including the host Nginx reconciliation that adds
        # the Shimo login WebSocket route - silently stopped running.
        workflow = (ROOT / ".github/workflows/deploy-prod.yml").read_text(
            encoding="utf-8"
        )
        self.assertIn("""bash -s" <<'EOF'""", workflow)
        script = (ROOT / "deploy/prod/remote-deploy.sh").read_text(encoding="utf-8")
        exec_lines = [
            line
            for line in script.splitlines()
            if not line.lstrip().startswith("#")
            and "compose" in line
            and re.search(r"\bexec\b", line)
        ]
        self.assertTrue(exec_lines, "expected the deploy to exec in a container")
        for line in exec_lines:
            self.assertRegex(line, r"<\s*/dev/null\s*$")

    def test_prod_deploy_does_not_recreate_web_after_api_recreation(self):
        # The web nginx config resolves the API container per request through
        # the Docker DNS resolver, so a second web recreate would only add
        # another multi-second 502 window to every deploy.
        script = (ROOT / "deploy/prod/remote-deploy.sh").read_text(encoding="utf-8")
        self.assertNotIn("--force-recreate", script)
        self.assertIn('wait_for_health "api" "$health_api"', script)
        self.assertIn('wait_for_health "web" "$health_web"', script)

    def test_prod_deploy_waits_for_the_dreamina_worker(self):
        # A one-shot `compose ps` snapshot raced the worker's boot once the
        # rest of the rollout got fast enough (the 2026-09-18 deploy failed
        # and rolled back). The worker now gets a bounded wait.
        script = (ROOT / "deploy/prod/remote-deploy.sh").read_text(encoding="utf-8")
        self.assertIn("wait_for_worker_health()", script)
        self.assertIn("wait_for_worker_health dreamina-worker", script)
        self.assertNotIn('echo "Dreamina worker is not healthy"', script)

    def test_deploy_hands_the_shimo_session_mount_to_the_worker_user(self):
        # The Shimo worker image runs as uid/gid 1000.  When the deploy only
        # ran `mkdir`, the container could read the session mount but never
        # write it, so every finished Shimo login failed to persist with a
        # silently swallowed EACCES.
        script = (ROOT / "deploy/prod/remote-deploy.sh").read_text(encoding="utf-8")
        self.assertIn('shimo_state_dir="$root/data/shimo-sessions"', script)
        self.assertIn("chown -R \"$shimo_state_uid:$shimo_state_gid\" \"$shimo_state_dir\"", script)
        self.assertIn("chmod 700 \"$shimo_state_dir\"", script)
        self.assertIn("shimo_state_uid=1000", script)


class ProdReleaseGateTest(unittest.TestCase):
    revision = "a" * 40
    repository = "buildsense-ai/cats-company"

    def run_record(self, run_id, **changes):
        return {
            "id": run_id, "run_attempt": 1, "head_sha": self.revision,
            "head_branch": "main", "head_repository": {"full_name": self.repository},
            "event": "push", "status": "completed", "conclusion": "success",
            "updated_at": "2026-10-20T01:00:00Z", **changes,
        }

    def decision(self, trigger, runs, main_revision=None):
        return GATE.release_decision(trigger, self.repository, main_revision or self.revision, runs)[0]

    def test_either_completion_order_releases_only_after_both_pass(self):
        for first, last in (("ci.yml", "deploy-test.yml"), ("deploy-test.yml", "ci.yml")):
            with self.subTest(first=first):
                a, b = self.run_record(10), self.run_record(20, status="in_progress", conclusion=None)
                runs = {first: [a], last: [b]}
                self.assertFalse(self.decision(a, runs))
                b.update(status="completed", conclusion="success")
                self.assertTrue(self.decision(b, runs))

    def test_same_second_completion_does_not_lose_the_release(self):
        ci, test = self.run_record(10), self.run_record(20)
        runs = {"ci.yml": [ci], "deploy-test.yml": [test]}
        # Both can queue; the serialized second gate uses the actual deployment
        # step to suppress the duplicate, rather than guessing timestamp order.
        self.assertTrue(self.decision(ci, runs))
        self.assertTrue(self.decision(test, runs))

    def test_any_non_success_or_missing_workflow_blocks_release(self):
        trigger = self.run_record(20)
        for state in ("failure", "cancelled", "skipped", "timed_out", None):
            with self.subTest(state=state):
                runs = {"ci.yml": [self.run_record(10, conclusion=state)], "deploy-test.yml": [trigger]}
                self.assertFalse(self.decision(trigger, runs))
        self.assertFalse(self.decision(trigger, {"deploy-test.yml": [trigger]}))

    def test_latest_run_failure_cannot_be_hidden_by_old_success(self):
        trigger = self.run_record(20)
        runs = {"ci.yml": [self.run_record(10), self.run_record(30, conclusion="failure")], "deploy-test.yml": [trigger]}
        self.assertFalse(self.decision(trigger, runs))

    def test_unrelated_sha_pr_branch_or_fork_cannot_approve_release(self):
        trigger = self.run_record(20)
        for changes in ({"head_sha": "b" * 40}, {"event": "pull_request"},
                        {"head_branch": "feature"}, {"head_repository": {"full_name": "fork/cats-company"}},
                        {"head_repository": None}):
            with self.subTest(changes=changes):
                runs = {"ci.yml": [self.run_record(10, **changes)], "deploy-test.yml": [trigger]}
                self.assertFalse(self.decision(trigger, runs))
                self.assertFalse(self.decision(self.run_record(20, **changes), runs))

    def test_new_main_revision_prevents_historical_manual_run_rollback(self):
        trigger = self.run_record(20, event="workflow_dispatch")
        runs = {"ci.yml": [self.run_record(10)], "deploy-test.yml": [trigger]}
        self.assertTrue(self.decision(trigger, runs))
        self.assertFalse(self.decision(trigger, runs, "b" * 40))

    def test_stale_completion_after_rerun_or_new_run_does_not_release(self):
        old = self.run_record(20)
        for replacement in (self.run_record(20, run_attempt=2), self.run_record(30)):
            runs = {"ci.yml": [self.run_record(10)], "deploy-test.yml": [replacement]}
            self.assertFalse(self.decision(old, runs))
            self.assertTrue(self.decision(replacement, runs))

    def test_successful_skipped_gate_is_not_a_successful_deployment(self):
        job = {"name": "deploy-prod", "status": "completed", "conclusion": "success", "steps": []}
        self.assertFalse(GATE.deployed_job([job]))
        job["steps"] = [{"name": "Deploy prod stack", "conclusion": "skipped"}]
        self.assertFalse(GATE.deployed_job([job]))
        job["steps"][0]["conclusion"] = "success"
        self.assertTrue(GATE.deployed_job([job]))
        job["conclusion"] = "failure"
        self.assertFalse(GATE.deployed_job([job]))

    def test_serialized_gate_suppresses_only_an_actual_prior_release(self):
        prior = self.run_record(90, event="workflow_run", display_title=f"Deploy production {self.revision}")
        current = {**prior, "id": 100}
        with patch.dict(os.environ, {"GITHUB_RUN_ID": "100"}), \
             patch.object(GATE, "workflow_runs", return_value=[current, prior]), \
             patch.object(GATE, "github_get") as get:
            get.return_value = {"jobs": [{"name": "deploy-prod", "status": "completed", "conclusion": "success",
                                         "steps": [{"name": "Deploy prod stack", "conclusion": "success"}]}]}
            self.assertTrue(GATE.already_deployed(self.revision))
            self.assertIn("/90/jobs?", get.call_args.args[0])
            get.return_value = {"jobs": [{"name": "release-gate", "status": "completed", "conclusion": "success"}]}
            self.assertFalse(GATE.already_deployed(self.revision))
            prior["display_title"] = f"Deploy production {'b' * 40}"
            get.reset_mock()
            self.assertFalse(GATE.already_deployed(self.revision))
            get.assert_not_called()

    def test_api_failure_emits_no_release_permission(self):
        import json
        with tempfile.TemporaryDirectory() as directory:
            event, output = Path(directory) / "event.json", Path(directory) / "output"
            event.write_text(json.dumps({"workflow_run": self.run_record(20)}), encoding="utf-8")
            with patch.dict(os.environ, {"GITHUB_EVENT_PATH": str(event), "GITHUB_OUTPUT": str(output),
                                         "GITHUB_REPOSITORY": self.repository}), \
                 patch.object(GATE, "workflow_runs", side_effect=RuntimeError("API unavailable")):
                with self.assertRaisesRegex(RuntimeError, "API unavailable"):
                    GATE.main()
            self.assertFalse(output.exists())


if __name__ == "__main__":
    unittest.main()
