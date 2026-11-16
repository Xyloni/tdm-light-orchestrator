import json
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import Mock, patch

import tdm_lot_preview as plugin


class TelegramNoticeTest(unittest.TestCase):
    def setUp(self):
        storage = tempfile.TemporaryDirectory()
        self.addCleanup(storage.cleanup)
        self.path = Path(storage.name) / "state.json"
        self.send = Mock(return_value=SimpleNamespace(message_id=1))
        self.cardinal = SimpleNamespace(telegram=SimpleNamespace(
            authorized_users={123}, bot=SimpleNamespace(send_message=self.send)))
        self.ref = "a" * 24
        self.automation = plugin.LotAutomation(self.cardinal, None, self.path)
        self.automation.update(self.ref, id=1, chat=123, deadline=None,
                               retired="search finished", inactive_since=None)

    def test_retirement_persists_notice_and_retries_after_restart(self):
        self.automation.offer = Mock(return_value=SimpleNamespace(active=False, title_ru="Example"))
        self.automation.refresh_offer(self.ref)
        persisted = json.loads(self.path.read_text(encoding="utf-8"))[self.ref]
        self.assertTrue(persisted["inactive_since"])
        self.assertIn("retired", persisted["notices"])

        self.send.side_effect = OSError("offline")
        with self.assertLogs(plugin.LOG, level="WARNING"), patch.object(plugin.time, "time", return_value=100):
            self.automation.flush_notices(self.ref)
        restarted = plugin.LotAutomation(self.cardinal, None, self.path)
        self.send.side_effect = None
        with patch.object(plugin.time, "time", return_value=104):
            restarted.flush_notices(self.ref)
        self.assertEqual(self.send.call_count, 1)
        with patch.object(plugin.time, "time", return_value=105):
            restarted.flush_notices(self.ref)
            restarted.flush_notices(self.ref)
        self.assertEqual(self.send.call_count, 2)
        self.assertFalse(plugin.LotAutomation(self.cardinal, None, self.path).data[self.ref]["notices"])

    def test_confirmed_send_is_not_repeated_after_disk_error_in_current_process(self):
        automation = self.automation
        automation.update_with_notice(self.ref, "returned:1", "Return confirmed", returned=1)
        with patch.object(plugin, "atomic_text", side_effect=OSError("disk unavailable")):
            with self.assertRaises(OSError):
                automation.flush_notices(self.ref)
        self.send.assert_called_once()
        automation.flush_notices(self.ref)
        self.send.assert_called_once()
        self.assertFalse(automation.data[self.ref]["notices"])

    def test_retry_delay_is_capped_and_does_not_drop_notice(self):
        automation = self.automation
        automation.update_with_notice(self.ref, "returned:1", "Return confirmed", returned=1)
        self.cardinal.telegram = None
        now = 100
        for _ in range(10):
            with patch.object(plugin.time, "time", return_value=now):
                automation.flush_notices(self.ref)
            pending = automation.data[self.ref]["notices"]["returned:1"]
            self.assertGreater(pending["next_attempt"], now)
            self.assertLessEqual(pending["next_attempt"] - now, 300)
            now = pending["next_attempt"]
        self.send.assert_not_called()


if __name__ == "__main__":
    unittest.main()
