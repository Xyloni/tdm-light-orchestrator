import os
import tempfile
import unittest
from pathlib import Path
from unittest.mock import call, patch

import tdm_lot_preview as plugin


class ConnectionDiagnosticTest(unittest.TestCase):
    def setUp(self):
        self.token = "private-diagnostic-fixture" * 2
        env = patch.dict(os.environ, {"ORCHESTRATOR_LOT_PREVIEW_TOKEN": self.token}, clear=True)
        env.start()
        self.addCleanup(env.stop)
        probe = patch.object(plugin, "diagnostic_request")
        self.probe = probe.start()
        self.addCleanup(probe.stop)
        self.link_ref = "0" * 24
        self.lot_ref = "1" * 24

    def test_missing_socket_and_short_token_fail_before_the_request(self):
        with tempfile.TemporaryDirectory() as folder:
            os.environ["ORCHESTRATOR_CARDINAL_SOCKET"] = str(Path(folder) / "missing.sock")
            self.assertIn("не найден", plugin.orchestrator_diagnostics())
        del os.environ["ORCHESTRATOR_CARDINAL_SOCKET"]
        os.environ["ORCHESTRATOR_LOT_PREVIEW_TOKEN"] = "short"
        self.assertIn("короче 32", plugin.orchestrator_diagnostics())
        self.probe.assert_not_called()

    def test_rejected_credentials_and_old_orchestrator_are_distinct(self):
        self.probe.return_value = (404, None)
        self.assertIn("запрос отклонён", plugin.orchestrator_diagnostics(self.lot_ref, [{"Login": "sample"}]))
        self.probe.assert_called_once_with(self.token, False, self.link_ref)
        self.probe.reset_mock()
        self.probe.return_value = (400, None)
        self.assertIn("Обновите оркестратор", plugin.orchestrator_diagnostics())

    def test_lot_diagnostic_reports_twitch_without_disclosing_identity(self):
        sample = [{"Login": "fixture-login", "OAuth": "fixture-oauth", "UserID": "100", "DeviceID": "fixture-device"}]
        self.probe.side_effect = ((200, "orchestrator_ok"), (200, "twitch_ok"))
        output = plugin.orchestrator_diagnostics(self.lot_ref, sample)
        self.assertIn("Токен · принят", output)
        self.assertIn("инвентарь тестового аккаунта доступен", output)
        self.assertNotIn(self.token, output)
        self.assertNotIn("fixture", output)
        self.assertEqual(self.probe.call_args_list, [
            call(self.token, False, self.link_ref), call(self.token, False, self.lot_ref, sample),
        ])

    def test_successful_connection_distinguishes_twitch_failure_from_missing_sample(self):
        self.probe.side_effect = ((200, "orchestrator_ok"), (200, "twitch_unavailable"), (200, "orchestrator_ok"))
        output = plugin.orchestrator_diagnostics(self.lot_ref, [{"Login": "sample"}])
        self.assertIn("не получил инвентарь", output)
        output = plugin.orchestrator_diagnostics(self.lot_ref, sample_unreadable=True)
        self.assertIn("TXT не удалось прочитать", output)
        self.assertEqual(self.probe.call_count, 3)


if __name__ == "__main__":
    unittest.main()
