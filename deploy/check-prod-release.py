"""Fail closed unless CI and the test deployment approve the same main revision."""

import json
import os
from pathlib import Path
import re
from urllib.parse import urlencode
from urllib.request import Request, urlopen


REQUIRED_WORKFLOWS = ("ci.yml", "deploy-test.yml")
ALLOWED_EVENTS = {"push", "workflow_dispatch"}


def trusted_run(run, repository, revision):
    return (
        run.get("head_sha") == revision
        and run.get("head_branch") == "main"
        and (run.get("head_repository") or {}).get("full_name") == repository
        and run.get("event") in ALLOWED_EVENTS
    )


def release_decision(trigger, repository, main_revision, workflow_runs):
    revision = trigger.get("head_sha", "")
    if not re.fullmatch(r"[0-9a-f]{40}", revision) or not trusted_run(trigger, repository, revision):
        return False, "The triggering run is not a trusted main run."
    if revision != main_revision:
        return False, "A newer main revision superseded this release; historical reruns cannot roll production back."
    if trigger.get("conclusion") != "success" or trigger.get("status") != "completed":
        return False, "The triggering workflow did not succeed."

    latest = []
    for workflow in REQUIRED_WORKFLOWS:
        candidates = [run for run in workflow_runs.get(workflow, []) if trusted_run(run, repository, revision)]
        if not candidates:
            return False, f"Waiting for {workflow} on {revision}."
        # A prior success must not hide a newer failed, queued, or rerunning run.
        run = max(candidates, key=lambda item: int(item["id"]))
        if run.get("status") != "completed" or run.get("conclusion") != "success":
            return False, f"Latest {workflow} has not succeeded."
        latest.append(run)

    if not any(
        (int(trigger["id"]), int(trigger.get("run_attempt", 1)))
        == (int(run["id"]), int(run.get("run_attempt", 1)))
        for run in latest
    ):
        return False, "A newer upstream run or attempt superseded this event."
    return True, f"CI and test deployment both succeeded for {revision}."


def github_get(path):
    request = Request(
        f"{os.environ.get('GITHUB_API_URL', 'https://api.github.com')}/repos/{os.environ['GITHUB_REPOSITORY']}/{path}",
        headers={
            "Authorization": f"Bearer {os.environ['GH_TOKEN']}",
            "Accept": "application/vnd.github+json",
            "X-GitHub-Api-Version": "2022-11-28",
        },
    )
    with urlopen(request, timeout=30) as response:
        return json.load(response)


def workflow_runs(workflow, revision):
    runs = []
    # A SHA normally has one run; paginate so PR/manual runs cannot conceal a
    # newer eligible run. GitHub caps filtered searches at 1,000; fail closed.
    for page in range(1, 11):
        query = urlencode({"branch": "main", "head_sha": revision, "per_page": 100, "page": page})
        result = github_get(f"actions/workflows/{workflow}/runs?{query}")
        if result.get("total_count", 0) >= 1000:
            raise RuntimeError("Too many workflow runs to establish a safe release decision")
        batch = result["workflow_runs"]
        runs.extend(batch)
        if len(batch) < 100:
            return runs
    raise RuntimeError("Workflow run pagination did not finish")


def deployed_job(jobs):
    return any(
        job.get("name") == "deploy-prod"
        and job.get("status") == "completed"
        and job.get("conclusion") == "success"
        and any(step.get("name") == "Deploy prod stack" and step.get("conclusion") == "success"
                for step in job.get("steps", []))
        for job in jobs
    )


def already_deployed(revision):
    for run in workflow_runs("deploy-prod.yml", revision):
        if str(run["id"]) == os.environ["GITHUB_RUN_ID"]:
            continue
        # workflow_run's head SHA is the default branch revision, which can
        # differ from the upstream revision. Match our explicit release title.
        if run.get("display_title") != f"Deploy production {revision}" or run.get("event") != "workflow_run":
            continue
        # Inspect the actual deploy step: a skipped release gate also makes the
        # outer workflow successful and must never count as a deployment.
        for page in range(1, 11):
            result = github_get(f"actions/runs/{run['id']}/jobs?filter=latest&per_page=100&page={page}")
            if deployed_job(result["jobs"]):
                return True
            if len(result["jobs"]) < 100:
                break
        else:
            raise RuntimeError("Production job pagination did not finish")
    return False


def main():
    event = json.loads(Path(os.environ["GITHUB_EVENT_PATH"]).read_text(encoding="utf-8"))
    trigger = event["workflow_run"]
    repository = os.environ["GITHUB_REPOSITORY"]
    revision = trigger["head_sha"]
    if not re.fullmatch(r"[0-9a-f]{40}", revision) or not trusted_run(trigger, repository, revision):
        raise RuntimeError("Untrusted release trigger")
    runs = {workflow: workflow_runs(workflow, revision) for workflow in REQUIRED_WORKFLOWS}
    # Read main last, including when checking again after the deployment queue.
    main_revision = github_get("git/ref/heads/main")["object"]["sha"]
    ready, reason = release_decision(trigger, repository, main_revision, runs)
    # The deployment job serializes production and runs this check again. This
    # handles both completion orders and same-second completions without using
    # lossy timestamp comparisons or cancelling a rollout already in progress.
    if ready and already_deployed(revision):
        ready, reason = False, "This revision already completed a successful production deployment."
    print(reason)
    with open(os.environ["GITHUB_OUTPUT"], "a", encoding="utf-8") as output:
        output.write(f"ready={str(ready).lower()}\nrevision={revision}\n")


if __name__ == "__main__":
    main()
