import datetime as dt
import unittest
from types import SimpleNamespace
from unittest.mock import Mock, patch

import tdm_lot_preview as plugin


class RetirementJournalTest(unittest.TestCase):
    def setUp(self):
        from test_tdm_lots import ManagedLotsTest

        self.fixture = ManagedLotsTest()
        self.addCleanup(self.fixture.doCleanups)
        self.fixture.setUp()
        self.ref = self.fixture.ref
        self.stock = self.fixture.path
        self.auto = self.fixture.auto
        self.auto.offer = Mock(return_value=SimpleNamespace(active=False))
        self.auto.open_orders = Mock(return_value="")
        inactive = (dt.datetime.now(plugin.MSK) - dt.timedelta(minutes=3)).isoformat()
        self.auto.update(self.ref, retired="deadline", inactive_since=inactive)

    def reload(self):
        restarted = plugin.LotAutomation(self.fixture.crd, self.fixture.tools, self.fixture.state)
        restarted.products = self.fixture.products
        restarted.offer = self.auto.offer
        restarted.open_orders = self.auto.open_orders
        return restarted

    def test_one_removed_row_during_check_requires_manual_review(self):
        def check(ref, lot, login, line):
            if login == "first":
                self.stock.write_text("second:fixture\n", encoding="utf-8")
            return "missing"

        self.auto.check = check
        with patch.object(plugin, "managed_request", return_value={"state": "returned"}) as request:
            self.auto.retire_stock(self.ref, self.auto.data[self.ref])
        request.assert_called_once_with(self.ref, "release", "second")
        self.assertEqual(self.auto.data[self.ref]["retirement_review"], ["first"])
        self.assertEqual(self.auto.data[self.ref]["returned"], 1)

    def test_restart_replays_our_detachment_but_skips_identical_external_change(self):
        original_recover = self.auto.recover_retirement_detachment
        self.auto.check = Mock(return_value="missing")

        def crash_before_replace(ref, path, stage):
            if self.auto.data[ref].get("retirement_detachment"):
                raise InterruptedError("crash before stock rename")
            return original_recover(ref, path, stage)

        self.auto.recover_retirement_detachment = crash_before_replace
        with self.assertRaises(InterruptedError):
            self.auto.retire_stock(self.ref, self.auto.data[self.ref])
        self.assertIn("first:fixture", self.stock.read_text(encoding="utf-8"))
        self.assertIsNotNone(self.auto.data[self.ref]["retirement_detachment"])

        self.stock.write_text("", encoding="utf-8")
        restarted = self.reload()
        with patch.object(plugin, "managed_request") as request:
            restarted.retire_stock(self.ref, restarted.data[self.ref])
        request.assert_not_called()
        self.assertEqual(restarted.data[self.ref]["retirement_review"], ["first", "second"])

    def test_restart_confirms_rename_that_finished_before_journal_commit(self):
        original_update = self.auto.update
        self.auto.check = Mock(return_value="missing")

        def crash_after_replace(ref, **changes):
            if "retirement_detachment" in changes and changes["retirement_detachment"] is None:
                raise InterruptedError("crash after stock rename")
            return original_update(ref, **changes)

        self.auto.update = crash_after_replace
        with self.assertRaises(InterruptedError):
            self.auto.retire_stock(self.ref, self.auto.data[self.ref])
        self.assertEqual(self.stock.read_text(encoding="utf-8"), "")
        restarted = self.reload()
        with patch.object(plugin, "managed_request", return_value={"state": "returned"}) as request:
            restarted.retire_stock(self.ref, restarted.data[self.ref])
            restarted.retire_stock(self.ref, restarted.data[self.ref])
        self.assertEqual([call.args[2] for call in request.call_args_list], ["first", "second"])
        self.assertFalse(restarted.data[self.ref]["pending"])
        self.assertEqual(restarted.data[self.ref]["returned"], 2)

    def test_paid_orders_filter_by_category_and_known_listing_identity(self):
        offer = SimpleNamespace(subcategory=SimpleNamespace(id=101))
        other_category = SimpleNamespace(id="ONE", subcategory=SimpleNamespace(id=202))
        other_lot = SimpleNamespace(id="TWO", lot_id=5, subcategory=offer.subcategory)
        ambiguous = SimpleNamespace(id="THREE", subcategory=offer.subcategory)
        account = self.fixture.crd.account
        account.get_sells = Mock(return_value=(None, [other_category, other_lot, ambiguous]))
        lot = self.auto.data[self.ref]
        self.assertIn("#THREE", plugin.LotAutomation.open_orders(self.auto, lot, offer))
        self.assertEqual(account.get_sells.call_args.kwargs["section"], "lot-101")
        account.get_sells.return_value = (None, [other_category, other_lot])
        self.assertFalse(plugin.LotAutomation.open_orders(self.auto, lot, offer))
