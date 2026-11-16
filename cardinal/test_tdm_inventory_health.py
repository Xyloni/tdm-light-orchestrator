import datetime as dt
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace

import tdm_lot_preview as plugin


class InventoryHealthTest(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name) / "state.json"
        self.cardinal = SimpleNamespace(telegram=None)
        self.ref = "b" * 24
        self.auto = plugin.LotAutomation(self.cardinal, None, self.root)
        self.auto.update(self.ref, id=1, chat=123, retired=None)
        self.start = dt.datetime(2026, 11, 16, 15, tzinfo=plugin.MSK)

    def test_outage_and_recovery_persist_across_restart_without_repeat_alerts(self):
        self.auto.record_inventory_health(self.ref, True, self.start)
        failed = self.start + dt.timedelta(seconds=30)
        self.auto.record_inventory_health(self.ref, False, failed)
        almost_due = failed + dt.timedelta(seconds=plugin.SCAN_INTERVAL - 1)
        self.auto.record_inventory_health(self.ref, False, almost_due)
        self.assertFalse(self.auto.data[self.ref].get("notices"))
        self.assertEqual(self.auto.data[self.ref]["inventory_checked_at"], self.start.isoformat())

        self.auto = plugin.LotAutomation(self.cardinal, None, self.root)
        due = failed + dt.timedelta(seconds=plugin.SCAN_INTERVAL)
        self.auto.record_inventory_health(self.ref, False, due)
        self.auto.record_inventory_health(self.ref, False, due + dt.timedelta(minutes=1))
        self.assertEqual(list(self.auto.data[self.ref]["notices"]), [f"inventory-outage:{failed.isoformat()}"])
        self.assertEqual(self.auto.data[self.ref]["inventory_outage_since"], failed.isoformat())

        recovered = due + dt.timedelta(minutes=2)
        self.auto.record_inventory_health(self.ref, True, recovered)
        self.auto.record_inventory_health(self.ref, True, recovered + dt.timedelta(minutes=1))
        reloaded = plugin.LotAutomation(self.cardinal, None, self.root).data[self.ref]
        self.assertEqual(list(reloaded["notices"]), [f"inventory-outage:{failed.isoformat()}",
                                                    f"inventory-recovered:{failed.isoformat()}"])
        self.assertIsNone(reloaded["inventory_outage_since"])
        self.assertEqual(reloaded["inventory_checked_at"], (recovered + dt.timedelta(minutes=1)).isoformat())

    def test_short_failure_leaves_no_alert_or_recovery_message(self):
        self.auto.record_inventory_health(self.ref, False, self.start)
        recovered = self.start + dt.timedelta(seconds=30)
        self.auto.record_inventory_health(self.ref, True, recovered)
        lot = self.auto.data[self.ref]
        self.assertFalse(lot.get("notices"))
        self.assertIsNone(lot["inventory_failed_since"])
        self.assertEqual(lot["inventory_checked_at"], recovered.isoformat())
