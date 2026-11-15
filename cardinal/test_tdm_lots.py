import configparser
import datetime as dt
from pathlib import Path
import tempfile
from threading import RLock
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch
import sys

import tdm_lot_preview as plugin


class ManagedLotsTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        root = Path(self.directory.name)
        self.products = root / "stock"
        self.products.mkdir()
        self.path = self.products / "sample.txt"
        self.path.write_text("first:fixture\nsecond:fixture\n", encoding="utf-8")
        config = configparser.ConfigParser()
        config["Example lot"] = {"productsFileName": "sample.txt", "response": "$product", "disable": "false"}
        self.crd = SimpleNamespace(
            AD_CFG=config, autodelivery_enabled=True, bl_delivery_enabled=False,
            multidelivery_enabled=False, telegram=None,
            account=SimpleNamespace(get_chat_by_name=Mock(return_value=None)),
            parse_message_entities=lambda text: [text], send_message=Mock(return_value=[object()]),
        )
        lock = RLock()
        self.tools = SimpleNamespace(get_products_file_lock=lambda path: lock, format_order_text=lambda text, order: text)
        self.state = root / "state.json"
        self.auto = plugin.LotAutomation(self.crd, self.tools, self.state)
        self.auto.products = self.products
        self.ref = "a" * 24
        self.auto.update(self.ref, section="Example lot", file="sample.txt", id=1, format="login-password",
                         deadline=None, retired=None, inactive_since=None, orders={}, pending={}, sold=[], chat=123)
        self.auto.refresh_offer = lambda ref: self.auto.data[ref]
        self.event = SimpleNamespace(lot_id=1, order=SimpleNamespace(id="ORDER", buyer_username="buyer", amount=1, chat_id=77))
        original = SimpleNamespace(deliver_product_handler=Mock())
        self.addCleanup(patch.stopall)
        patch.dict(sys.modules, {"handlers": original}).start()

    def test_unknown_is_skipped_and_confirmed_delivery_is_never_repeated(self):
        self.auto.check = Mock(side_effect=["inventory_unknown", "ready"])
        with patch.object(plugin, "managed_request", return_value={"Rewards": ["reward"]}):
            self.auto.guarded_delivery(self.event, self.ref, self.auto.data[self.ref])
            self.auto.guarded_delivery(self.event, self.ref, self.auto.data[self.ref])
        self.assertEqual(self.path.read_text(), "first:fixture")
        self.crd.send_message.assert_called_once_with(77, "second:fixture", "buyer", attempts=1, watermark=False)
        self.assertTrue(self.auto.data[self.ref]["orders"]["ORDER"]["sent"])

    def test_uncertain_send_stays_reserved_across_restart(self):
        self.auto.check = Mock(return_value="ready")
        self.crd.send_message.return_value = []
        with patch.object(plugin, "managed_request", return_value={"Rewards": ["reward"]}):
            with self.assertRaises(ValueError):
                self.auto.guarded_delivery(self.event, self.ref, self.auto.data[self.ref])
        restored = plugin.LotAutomation(self.crd, self.tools, self.state)
        self.assertEqual(restored.blocked_logins(restored.data[self.ref]), {"first"})
        self.assertFalse(restored.data[self.ref]["orders"]["ORDER"]["sent"])
        self.assertEqual(self.path.read_text(), "second:fixture")

    def test_retirement_waits_for_orders_and_retries_inventory(self):
        inactive = (dt.datetime.now(plugin.MSK) - dt.timedelta(minutes=3)).isoformat()
        self.auto.update(self.ref, retired="deadline", inactive_since=inactive)
        self.auto.offer = lambda lot: SimpleNamespace(active=False)
        self.auto.open_orders = Mock(return_value=True)
        self.auto.check = Mock(return_value="missing")
        with patch.object(plugin, "managed_request") as http:
            self.auto.retire_stock(self.ref, self.auto.data[self.ref])
            http.assert_not_called()
        self.assertIn("first:fixture", self.path.read_text())
        self.auto.open_orders.return_value = False
        with patch.object(plugin, "managed_request", side_effect=[{"state": "returned"}, {"state": "inventory_not_empty"}]):
            self.auto.retire_stock(self.ref, self.auto.data[self.ref])
        self.assertEqual(self.path.read_text(), "")
        self.assertEqual(self.auto.data[self.ref]["pending"], {"second": True})
        with patch.object(plugin, "managed_request", return_value={"state": "returned"}):
            self.auto.retire_stock(self.ref, self.auto.data[self.ref])
        self.assertEqual(self.auto.data[self.ref]["returned"], 2)

    def test_deadline_persists_at_midnight_moscow(self):
        now = dt.datetime(2026, 11, 15, 17, tzinfo=plugin.MSK)
        expected = {"marker": "23.11.", "at": "2026-11-23T00:00:00+03:00"}
        self.assertEqual(plugin.title_deadline("[ДО 23.11] Game", now), expected)
        self.assertEqual(plugin.title_deadline("[ДО 23.11] Game", now.replace(year=2027)),
                         {"marker": "23.11.", "at": "2027-11-23T00:00:00+03:00"})
        self.assertEqual(plugin.title_deadline("Game [ДО 23.11]", now.replace(year=2027), expected), expected)


if __name__ == "__main__":
    unittest.main()
