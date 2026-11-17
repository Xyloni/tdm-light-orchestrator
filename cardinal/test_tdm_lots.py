import configparser
import datetime as dt
from pathlib import Path
import re
import tempfile
from threading import RLock
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch
import sys

import tdm_lot_preview as plugin


def cardinal_message_entities(message):
    text = "\n".join(line.strip() for line in message.split("\n"))
    while "\n\n" in text:
        text = text.replace("\n\n", "\n[a][/a]\n")
    parts = re.split(r"(\$new|\$sleep=\d+(?:\.\d+)?|\$photo=\d+)", text)
    entities = []
    for index, part in enumerate(parts):
        if index % 2:
            if part.startswith("$sleep="):
                entities.append(float(part.removeprefix("$sleep=")))
            elif part.startswith("$photo="):
                entities.append(int(part.removeprefix("$photo=")))
            continue
        lines = part.strip().split("\n")
        for start in range(0, len(lines), 20):
            chunk = "\n".join(lines[start:start + 20])
            if chunk.strip() and chunk.strip() != "[a][/a]":
                entities.append(chunk)
    return entities


class ManagedLotsTest(unittest.TestCase):
    def test_lot_code_button_sends_exact_code(self):
        class LegacyButton:
            def __init__(self, text):
                self.text = text

        ref = "0123456789abcdef01234567"
        button = plugin.lot_code_button(LegacyButton, ref)
        self.assertEqual(button.to_dict(), {"text": "📋 Скопировать код", "copy_text": {"text": ref}})

    def test_registration_sample_omits_passwords_and_reserved_accounts(self):
        self.path.write_text("sold:password\nreserve:password\nshort:password:with:colon\n"
                             "fixture@example.invalid:full:private-password:oauth-fixture:17:device\nshort:again\n",
                             encoding="utf-8")
        sample = plugin.lot_stock_sample(self.path, {"reserve"}, {plugin.sold_key("sold")})
        self.assertEqual(sample, [{"Login": "short"},
                                  {"Login": "full", "OAuth": "oauth-fixture", "UserID": "17", "DeviceID": "device"}])

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
            account=SimpleNamespace(id=12, get_chat_by_name=Mock(return_value=None), get_chat_history=Mock(return_value=[])),
            parse_message_entities=lambda text: [text], send_message=Mock(side_effect=lambda chat, text, buyer, **kw: [
                SimpleNamespace(id=12, author_id=12, text=text)]),
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
        self.event = SimpleNamespace(lot_id=1, order=SimpleNamespace(id="ORDER", buyer_username="buyer", buyer_id=19,
                                                                    amount=1, chat_id=77, status=SimpleNamespace(name="PAID")))
        self.crd.account.get_order = Mock(return_value=self.event.order)
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

    def test_long_cardinal_template_delivers_stock_and_all_instructions(self):
        instructions = [f"Step {number}. Read this instruction." for number in range(1, 24)]
        self.crd.AD_CFG["Example lot"]["response"] = (
            "Thank you, $username!\n\n$product\n\n$new\n$sleep=1.5\n\nInstructions\n\n"
            + "\n\n".join(instructions) + "\n$new\n$sleep=1.5\n\nOrder link: $order_link"
        )
        self.crd.parse_message_entities = cardinal_message_entities
        self.tools.format_order_text = lambda text, order: text.replace("$username", order.buyer_username).replace(
            "$order_link", "https://example.invalid/orders/ORDER")
        self.auto.check = Mock(return_value="ready")
        with patch.object(plugin, "managed_request", return_value={"Rewards": ["reward"]}):
            self.auto.guarded_delivery(self.event, self.ref, self.auto.data[self.ref])
            self.auto.guarded_delivery(self.event, self.ref, self.auto.data[self.ref])
        self.crd.send_message.assert_called_once()
        text = self.crd.send_message.call_args.args[1]
        self.assertEqual(cardinal_message_entities(text), [text])
        self.assertIn("Thank you, buyer!", text)
        self.assertIn("first:fixture", text.splitlines())
        self.assertIn("https://example.invalid/orders/ORDER", text)
        for instruction in instructions:
            self.assertIn(instruction, text)
        self.assertEqual(self.path.read_text(), "second:fixture")
        self.assertTrue(self.auto.data[self.ref]["orders"]["ORDER"]["sent"])

    def test_delivery_template_attachment_leaves_stock_and_reservation_untouched(self):
        self.crd.AD_CFG["Example lot"]["response"] = "Instructions\n$photo=25\n$product"
        self.crd.parse_message_entities = cardinal_message_entities
        self.auto.check = Mock(return_value="ready")
        before = self.path.read_text()
        with patch.object(plugin, "managed_request", return_value={"Rewards": ["reward"]}):
            with self.assertRaisesRegex(ValueError, "вложения"):
                self.auto.guarded_delivery(self.event, self.ref, self.auto.data[self.ref])
        self.assertEqual(self.path.read_text(), before)
        self.assertEqual(self.auto.data[self.ref]["orders"], {})
        self.crd.send_message.assert_not_called()

    def test_uncertain_send_stays_reserved_across_restart(self):
        self.auto.check = Mock(return_value="ready")
        self.crd.send_message.side_effect = None
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
        self.auto.open_orders = Mock(return_value="Есть открытый заказ")
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

    def test_refunded_or_stale_paid_order_does_not_send_or_remove_stock(self):
        self.auto.check = Mock(return_value="ready")
        refunded = SimpleNamespace(**vars(self.event.order))
        refunded.status = SimpleNamespace(name="REFUNDED")
        self.crd.account.get_order.return_value = refunded
        with patch.object(plugin, "managed_request", return_value={"Rewards": ["reward"]}):
            with self.assertRaisesRegex(ValueError, "оплачен"):
                self.auto.guarded_delivery(self.event, self.ref, self.auto.data[self.ref])
            self.event.order.status = refunded.status
            self.auto.guarded_delivery(self.event, self.ref, self.auto.data[self.ref])
        self.crd.send_message.assert_not_called()
        self.assertEqual(self.path.read_text(), "first:fixture\nsecond:fixture\n")
        self.assertFalse(self.auto.data[self.ref]["orders"])

    def test_chat_reconciliation_requires_real_message_and_does_not_send_twice(self):
        self.auto.check = Mock(return_value="ready")
        self.crd.send_message.side_effect = None
        self.crd.send_message.return_value = [SimpleNamespace(id=0, author_id=12, text="first:fixture")]
        with patch.object(plugin, "managed_request", return_value={"Rewards": ["reward"]}):
            with self.assertRaisesRegex(ValueError, "не подтверждена"):
                self.auto.guarded_delivery(self.event, self.ref, self.auto.data[self.ref])
        lot = self.auto.data[self.ref]
        order = lot["orders"]["ORDER"]
        self.assertNotIn("text", order)
        self.assertFalse(order["sent"])
        self.crd.account.get_chat_history.return_value = [SimpleNamespace(id=23, author_id=75, text="first:fixture")]
        self.auto.recover_reserved(self.ref, lot, "ORDER", order)
        self.assertFalse(self.auto.data[self.ref]["orders"]["ORDER"]["sent"])
        self.crd.account.get_chat_history.return_value = [SimpleNamespace(id=24, author_id=12, text="first:fixture")]
        self.auto.recover_reserved(self.ref, lot, "ORDER", order)
        self.assertEqual(self.auto.data[self.ref]["orders"]["ORDER"]["message_id"], 24)
        self.assertEqual(self.crd.send_message.call_count, 1)

    def test_native_config_denial_is_persistent_and_scoped_to_managed_lot(self):
        self.auto.native_config = self.state.with_name("auto_delivery.cfg")
        self.crd.AD_CFG["Other lot"] = {"response": "$product", "disable": "0"}
        self.auto.protect_native_stock(self.auto.data[self.ref])
        restored = configparser.ConfigParser()
        restored.read(self.auto.native_config, encoding="utf-8")
        self.assertTrue(restored["Example lot"].getboolean("disable"))
        self.assertTrue(restored["Example lot"].getboolean("disableAutoRestore"))
        self.assertFalse(restored["Other lot"].getboolean("disable"))

    def test_restart_resumes_paid_reservation_after_stock_rename(self):
        self.auto.check = Mock(return_value="ready")
        save_order = self.auto.save_order

        def interrupt_before_prepared(ref, order_id, order):
            if order["stage"] == "prepared":
                raise OSError("simulated shutdown")
            save_order(ref, order_id, order)

        self.auto.save_order = interrupt_before_prepared
        with patch.object(plugin, "managed_request", return_value={"Rewards": ["reward"]}):
            with self.assertRaisesRegex(OSError, "shutdown"):
                self.auto.guarded_delivery(self.event, self.ref, self.auto.data[self.ref])
            self.assertEqual(self.path.read_text(), "second:fixture")
            self.crd.send_message.assert_not_called()
            restored = plugin.LotAutomation(self.crd, self.tools, self.state)
            restored.products = self.products
            restored.check = Mock(return_value="ready")
            lot = restored.data[self.ref]
            restored.recover_reserved(self.ref, lot, "ORDER", lot["orders"]["ORDER"])
        self.crd.send_message.assert_called_once()
        self.assertTrue(restored.data[self.ref]["orders"]["ORDER"]["sent"])

    def test_recovery_considers_only_paid_orders_from_its_lot_after_cutover(self):
        now = dt.datetime.now(dt.timezone.utc)
        self.auto.update(self.ref, delivery_since=(now - dt.timedelta(minutes=5)).isoformat())
        orders = [SimpleNamespace(id=kind, date=date, status=SimpleNamespace(name=status)) for kind, date, status in (
            ("OLD", now - dt.timedelta(hours=1), "PAID"), ("OTHER", now, "PAID"),
            ("CANCELLED", now, "REFUNDED"), ("RECENT", now, "PAID"),
        )]
        self.crd.account.get_sells = Mock(return_value=(None, orders))
        self.auto.guarded_delivery = Mock()
        sys.modules["handlers"].setup_event_attributes_handler = lambda crd, event: setattr(
            event, "lot_id", 2 if event.order.id == "OTHER" else 1)
        events = SimpleNamespace(NewOrderEvent=lambda tag, order: SimpleNamespace(order=order))
        with patch.dict(sys.modules, {"FunPayAPI.updater.events": events}):
            self.auto.recover_paid_orders(self.ref, self.auto.data[self.ref])
        self.auto.guarded_delivery.assert_called_once()
        self.assertEqual(self.auto.guarded_delivery.call_args.args[0].order.id, "RECENT")

    def test_retirement_does_not_release_line_removed_outside_plugin(self):
        inactive = (dt.datetime.now(plugin.MSK) - dt.timedelta(minutes=3)).isoformat()
        self.auto.update(self.ref, retired="deadline", inactive_since=inactive)
        self.auto.offer = lambda lot: SimpleNamespace(active=False)
        self.auto.open_orders = Mock(return_value="")

        def check(ref, lot, login, line):
            if login == "first":
                self.path.write_text("second:fixture", encoding="utf-8")
            return "missing"

        self.auto.check = check
        with patch.object(plugin, "managed_request", return_value={"state": "returned"}) as http:
            self.auto.retire_stock(self.ref, self.auto.data[self.ref])
        http.assert_called_once_with(self.ref, "release", "second")
        self.assertEqual(self.auto.data[self.ref]["retirement_review"], ["first"])

    def test_other_category_paid_order_does_not_delay_detachment(self):
        offer = SimpleNamespace(subcategory=SimpleNamespace(id=23))
        other = SimpleNamespace(id="OTHER", subcategory=SimpleNamespace(id=24))
        self.crd.account.get_sells = Mock(return_value=(None, [other]))
        self.assertFalse(self.auto.open_orders(self.auto.data[self.ref], offer))
        self.assertEqual(self.crd.account.get_sells.call_args.kwargs["section"], "lot-23")

    def test_deadline_persists_at_midnight_moscow(self):
        now = dt.datetime(2026, 11, 15, 17, tzinfo=plugin.MSK)
        expected = {"marker": "23.11.", "at": "2026-11-23T00:00:00+03:00"}
        self.assertEqual(plugin.title_deadline("[ДО 23.11] Game", now), expected)
        self.assertEqual(plugin.title_deadline("[ДО 23.11] Game", now.replace(year=2027)),
                         {"marker": "23.11.", "at": "2027-11-23T00:00:00+03:00"})
        self.assertEqual(plugin.title_deadline("Game [ДО 23.11]", now.replace(year=2027), expected), expected)


if __name__ == "__main__":
    unittest.main()
