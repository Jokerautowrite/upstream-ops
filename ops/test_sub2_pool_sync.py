import importlib.util
import json
import os
import unittest
from pathlib import Path


MODULE_PATH = Path(__file__).with_name("sub2-pool-sync.py")
SPEC = importlib.util.spec_from_file_location("sub2_pool_sync", MODULE_PATH)
sync = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(sync)


class Sub2PoolSyncTests(unittest.TestCase):
    def setUp(self):
        self.original_sh = sync.sh
        self.original_env = {
            sync.SYNC_USERNAME_ENV: os.environ.get(sync.SYNC_USERNAME_ENV),
            sync.SYNC_PASSWORD_ENV: os.environ.get(sync.SYNC_PASSWORD_ENV),
        }

    def tearDown(self):
        sync.sh = self.original_sh
        for key, value in self.original_env.items():
            if value is None:
                os.environ.pop(key, None)
            else:
                os.environ[key] = value

    def test_candidates_use_pool_mode_and_report_image_only_sites(self):
        rows = [
            [
                "1",
                "生图 Image2",
                "openai",
                "relay",
                "t",
                json.dumps({"base_url": "https://image.example/v1", "pool_mode": True}),
                "",
            ],
            [
                "2",
                "PLUS text",
                "openai",
                "relay",
                "t",
                json.dumps({"base_url": "https://text.example/v1", "pool_mode": True}),
                "",
            ],
            [
                "3",
                "Image worker",
                "openai",
                "relay",
                "t",
                json.dumps({"base_url": "https://mixed.example/v1", "pool_mode": True}),
                "",
            ],
            [
                "4",
                "PLUS mixed",
                "openai",
                "relay",
                "t",
                json.dumps({"base_url": "https://mixed.example/v1", "pool_mode": True}),
                "",
            ],
            [
                "5",
                "not in pool",
                "openai",
                "relay",
                "t",
                json.dumps({"base_url": "https://outside.example/v1", "pool_mode": False}),
                "",
            ],
            [
                "6",
                "uo-kiro-4-generated",
                "openai",
                "relay",
                "t",
                json.dumps({"base_url": "https://generated.example/v1", "pool_mode": True}),
                "",
            ],
        ]
        sync.sh = lambda _cmd, _input: "\n".join("\t".join(row) for row in rows)

        candidates, image_only_candidates = sync.fetch_sub2_candidates()

        self.assertEqual(image_only_candidates, 1)
        self.assertEqual(
            {item["site"] for item in candidates},
            {"https://image.example", "https://text.example", "https://mixed.example"},
        )
        mixed = next(item for item in candidates if item["site"] == "https://mixed.example")
        self.assertEqual(mixed["source_name"], "Image worker / PLUS mixed")

    def test_notification_events_match_priority_workflow(self):
        self.assertNotIn("sub2_pool_changed", sync.DEFAULT_EVENTS)
        self.assertIn("login_failed", sync.DEFAULT_EVENTS)
        self.assertIn("sub2_pool_priority_applied", sync.DEFAULT_EVENTS)
        self.assertIn("sub2_pool_priority_failed", sync.DEFAULT_EVENTS)

    def test_new_channel_name_is_based_only_on_url(self):
        item = {
            "site": "https://api.example.com/custom/path",
            "source_id": 99,
            "source_name": "Kiro group account",
        }
        self.assertEqual(sync.unique_name(item), "URL-api.example.com-custom-path")

    def test_sync_credentials_require_root_only_environment_file(self):
        os.environ.pop(sync.SYNC_USERNAME_ENV, None)
        os.environ.pop(sync.SYNC_PASSWORD_ENV, None)
        with self.assertRaises(RuntimeError):
            sync.sync_credentials()

        os.environ[sync.SYNC_USERNAME_ENV] = "monitor@example.invalid"
        os.environ[sync.SYNC_PASSWORD_ENV] = "test-password"
        self.assertEqual(sync.sync_credentials(), ("monitor@example.invalid", "test-password"))


if __name__ == "__main__":
    unittest.main()
