import configparser
from pathlib import Path
import tempfile
from threading import RLock
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

import tdm_lot_preview as plugin


class QuantitySyncTest(unittest.TestCase):
    def setUp(self):
        folder = tempfile.TemporaryDirectory()
        self.addCleanup(folder.cleanup)
        directory = Path(folder.name)
        self.stock = directory / "stock.txt"
        self.rows = {"one": "one:fixture", "two": "two:fixture", "three": "three:fixture", "four": "four:fixture"}
        self.stock.write_text(plugin.stock_text(self.rows), encoding="utf-8")
        config = configparser.ConfigParser()
        config["listing"] = {"productsFileName": "stock.txt"}
        self.account = SimpleNamespace(get_sells=Mock(return_value=(None, [])), save_lot=Mock())
        cardinal = SimpleNamespace(account=self.account, AD_CFG=config)
        stock_lock = RLock()
        self.auto = plugin.LotAutomation(cardinal, SimpleNamespace(get_products_file_lock=lambda path: stock_lock),
                                         directory / "state.json")
        self.auto.products = directory
        self.ref = "a" * 24
        self.auto.update(self.ref, id=11, section="listing", file="stock.txt", format="login-password",
                         retired=None, orders={}, pending={}, sold=[])
        self.offer = SimpleNamespace(active=True, amount=8, subcategory=SimpleNamespace(id=4))
        self.auto.offer = Mock(return_value=self.offer)
        self.bundle = {"Rewards": ["fixture"]}

    def run_sync(self, results):
        cache = {login: (plugin.sold_key(line), result) for (login, line), result in zip(self.rows.items(), results)}
        with patch.object(plugin, "managed_request", return_value=self.bundle):
            self.auto.sync_verified_stock(self.ref, self.rows, cache, self.bundle)

    def test_sets_confirmed_quantity_less_paid_accounts_not_yet_reserved(self):
        paid = lambda order_id, category, offer: SimpleNamespace(id=order_id, lot_id=offer, amount=1,
                                                                 status=SimpleNamespace(name="PAID"),
                                                                 subcategory=SimpleNamespace(id=category))
        self.auto.update(self.ref, orders={"reserved": {"sent": False, "logins": ["someone_else"]}})
        self.account.get_sells.return_value = (None, [paid("open", 4, 11), paid("reserved", 4, 11), paid("other", 5, 12)])
        self.run_sync(["ready", "ready", "ready", "missing"])
        self.assertEqual(self.offer.amount, 2)
        self.assertEqual(self.account.get_sells.call_args.kwargs["section"], "lot-4")
        self.account.save_lot.assert_called_once_with(self.offer)

    def test_incomplete_twitch_check_does_not_raise_quantity_or_reopen_offer(self):
        self.offer.amount = 1
        self.run_sync(["ready", "ready", "ready", "inventory_unknown"])
        self.assertEqual(self.offer.amount, 1)
        self.assertIn("Увеличение ждёт", self.auto.data[self.ref]["quantity_status"])
        self.offer.active = False
        self.run_sync(["ready"] * len(self.rows))
        self.assertFalse(self.offer.active)
        self.account.save_lot.assert_not_called()

    def test_modified_stock_invalidates_earlier_checks(self):
        self.stock.write_text("one:different", encoding="utf-8")
        self.run_sync(["ready"] * len(self.rows))
        self.account.save_lot.assert_not_called()
        self.assertIn("TXT изменился", self.auto.data[self.ref]["quantity_status"])

    def test_no_verified_stock_never_serializes_zero_as_empty_amount(self):
        self.run_sync(["inventory_unknown"] * len(self.rows))
        self.account.save_lot.assert_not_called()
        self.assertIn("Нет подтверждённого", self.auto.data[self.ref]["quantity_status"])


if __name__ == "__main__":
    unittest.main()
