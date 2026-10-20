import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parent.parent


class SessionForkConfigTest(unittest.TestCase):
    def test_dedicated_mysql_persists_trigger_prerequisite(self):
        compose = (ROOT / "deploy/test/docker-compose.yml").read_text(encoding="utf-8")
        mysql = compose.split("  mysql:\n", 1)[1].split("\n  server:", 1)[0]
        self.assertIn("--log-bin-trust-function-creators=1", mysql)
        self.assertNotIn("--skip-log-bin", mysql)
        server = compose.split("\n  server:", 1)[1].split("\n  web:", 1)[0]
        self.assertNotIn("MYSQL_ROOT_PASSWORD", server)

    def test_ci_runs_fork_contracts_without_root_application_dsn(self):
        workflow = (ROOT / ".github/workflows/ci.yml").read_text(encoding="utf-8")
        denied = workflow.index("- name: Verify restricted MySQL schema failure")
        configure = workflow.index("- name: Configure dedicated CI MySQL trigger prerequisite")
        contract = workflow.index("- name: Test MySQL session fork contract")
        self.assertLess(denied, configure)
        self.assertLess(configure, contract)
        self.assertIn("^TestSessionForkMySQLRestrictedSchema$", workflow[denied:configure])
        self.assertIn("SET GLOBAL log_bin_trust_function_creators = 1", workflow[configure:contract])
        self.assertIn("CATS_SESSION_FORK_MYSQL_TEST_DSN: catsco:", workflow)
        self.assertNotIn("CATS_SESSION_FORK_MYSQL_TEST_DSN: root:", workflow)
        self.assertIn("go test ./server/db/mysql -run '^TestSessionForkMySQL'", workflow)
        self.assertIn("go test ./server/db/postgres -run '^TestSessionForkPostgres'", workflow)


if __name__ == "__main__":
    unittest.main()
