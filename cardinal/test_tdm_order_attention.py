import datetime as dt
from pathlib import Path
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

import tdm_lot_preview as plugin


class OrderAttentionTest(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.state = Path(directory.name) / "lots.json"
        self.crd = SimpleNamespace(account=SimpleNamespace(id=7, get_order=Mock(), get_chat_history=Mock()),
                                   send_message=Mock())
        self.auto = plugin.LotAutomation(self.crd, SimpleNamespace(), self.state)
        self.ref = "a" * 24
        self.auto.update(self.ref, id=17, orders={}, sold=[])

    def set_payment(self, status, order_id="ORDER"):
        self.crd.account.get_order.return_value = SimpleNamespace(
            id=order_id, buyer_id=33, status=SimpleNamespace(name=status))

    def test_held_order_persists_a_reason_code_without_private_details(self):
        self.auto.remember_order_attention(self.ref, "ORDER", ValueError("Нет достаточно аккаунтов: secret-fixture"))
        restored = plugin.LotAutomation(self.crd, SimpleNamespace(), self.state)
        self.assertIn("Не хватило проверенных", restored.attention_orders()[0][3])
        self.assertNotIn("secret-fixture", self.state.read_text(encoding="utf-8"))
        self.set_payment("REFUNDED")
        self.assertIn("Резерва по этому заказу нет", restored.reconcile_attention_order(self.ref, "ORDER"))
        self.assertEqual(restored.attention_orders(), [])
        self.crd.send_message.assert_not_called()

    def test_manual_reconciliation_preserves_prepared_order_without_sending(self):
        order = {"sent": False, "logins": ["redacted"], "stage": "prepared", "buyer_id": 33}
        self.auto.save_order(self.ref, "ORDER", order)
        self.set_payment("PAID")
        result = self.auto.reconcile_attention_order(self.ref, "ORDER")
        self.assertIn("заказ оплачен", result)
        self.assertIn("Аккаунтов в резерве · 1", result)
        self.assertNotIn("redacted", result)
        self.assertEqual(self.auto.data[self.ref]["orders"]["ORDER"], order)
        self.crd.account.get_chat_history.assert_not_called()
        self.crd.send_message.assert_not_called()

    def test_manual_reconciliation_confirms_only_matching_seller_message(self):
        order = {"sent": False, "logins": ["redacted"], "stage": "attempted", "buyer_id": 33,
                 "buyer": "fixture", "chat": 42, "last_message": 100,
                 "text_hash": plugin.delivery_text_hash("test delivery")}
        self.auto.save_order(self.ref, "ORDER", order)
        self.set_payment("PAID")
        self.crd.account.get_chat_history.return_value = [
            SimpleNamespace(id=101, author_id=33, text="test delivery"),
            SimpleNamespace(id=100, author_id=7, text="test delivery"),
        ]
        self.assertIn("Ждём подтверждение", self.auto.reconcile_attention_order(self.ref, "ORDER"))
        self.assertFalse(self.auto.data[self.ref]["orders"]["ORDER"]["sent"])
        self.crd.account.get_chat_history.return_value.append(SimpleNamespace(id=103, author_id=7, text="test delivery"))
        self.assertIn("Отправка подтверждена", self.auto.reconcile_attention_order(self.ref, "ORDER"))
        self.assertEqual(self.auto.data[self.ref]["orders"]["ORDER"]["message_id"], 103)
        self.assertEqual(self.auto.attention_orders(), [])
        self.crd.send_message.assert_not_called()

    def test_recovery_continues_after_held_order(self):
        now = dt.datetime.now(dt.timezone.utc)
        self.auto.update(self.ref, delivery_since=(now - dt.timedelta(minutes=1)).isoformat())
        paid = SimpleNamespace(name="PAID")
        first = SimpleNamespace(id="HELD", date=now, status=paid)
        second = SimpleNamespace(id="LATER", date=now, status=paid)
        self.crd.account.get_sells = Mock(return_value=(None, [first, second]))
        self.auto.guarded_delivery = Mock(side_effect=[ValueError("Нет достаточно аккаунтов"), None])
        events = SimpleNamespace(NewOrderEvent=lambda _, order: SimpleNamespace(lot_id=17, order=order))
        handlers = SimpleNamespace(setup_event_attributes_handler=Mock())
        with patch.dict(sys.modules, {"FunPayAPI.updater.events": events, "handlers": handlers}):
            with self.assertLogs(plugin.LOG, "WARNING"):
                self.auto.recover_paid_orders(self.ref, self.auto.data[self.ref])
        self.assertEqual([call.args[0].order.id for call in self.auto.guarded_delivery.call_args_list], ["HELD", "LATER"])
        self.assertEqual([entry[2] for entry in self.auto.attention_orders()], ["HELD"])
