import configparser
import json
from pathlib import Path
import sys
import tempfile
from threading import RLock
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

import tdm_lot_preview as plugin


class CardinalDetachTest(unittest.TestCase):
    CONFIG_FLAGS = ("disable", "disableAutoRestore", "disableAutoDisable")

    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        root = Path(directory.name)
        self.stock = root / "stock.txt"
        self.stock.write_text("first:fixture\nsecond:fixture\n", encoding="utf-8")
        self.original_stock = self.stock.read_bytes()
        self.config_path = root / "auto_delivery.cfg"
        self.state = root / "lots.json"
        self.ref = "a" * 24
        self.section = "Example lot"
        config = configparser.ConfigParser()
        config[self.section] = {"productsFileName": self.stock.name, "response": "$product", "disable": "0",
                                "disableAutoRestore": "0", "disableAutoDisable": "1"}
        config["Other lot"] = {"productsFileName": "other.txt", "disable": "1", "disableAutoRestore": "0"}
        self.cardinal = SimpleNamespace(AD_CFG=config, autodelivery_enabled=True,
                                        plugins={plugin.UUID: SimpleNamespace(enabled=True)},
                                        account=SimpleNamespace(save_lot=Mock()), send_message=Mock())
        lock = RLock()
        self.tools = SimpleNamespace(get_products_file_lock=lambda path: lock)
        self.auto = plugin.LotAutomation(self.cardinal, self.tools, self.state, self.config_path)
        self.auto.products = root
        self.auto.supported = True
        self.auto.start_worker = Mock()
        self.auto.offer = Mock(return_value=SimpleNamespace(title_ru=self.section, active=False))
        self.auto.open_orders = Mock(return_value="")

    def enroll(self):
        with patch.object(plugin, "managed_request", return_value={"Rewards": ["fixture"]}):
            self.auto.enable(self.ref, self.section, self.stock.name, 17, "login-password", 42)

    def stored_config(self):
        config = configparser.ConfigParser()
        config.read(self.config_path, encoding="utf-8")
        return config

    def flags(self, config):
        return tuple(config[self.section].getboolean(key) for key in self.CONFIG_FLAGS)

    def test_detach_restores_only_this_sections_saved_settings_and_keeps_stock(self):
        self.enroll()
        self.assertEqual(self.flags(self.stored_config()), (True, True, True))
        with patch.object(plugin, "managed_request") as request:
            lot = self.auto.data[self.ref]
            self.auto.detach(self.ref, expected_generation=(lot["id"], lot["delivery_since"]))
        request.assert_not_called()
        reopened = plugin.LotAutomation(self.cardinal, self.tools, self.state, self.config_path)
        self.assertNotIn(self.ref, reopened.data)
        self.assertEqual(self.flags(self.cardinal.AD_CFG), (False, False, True))
        stored = self.stored_config()
        self.assertEqual(self.flags(stored), (False, False, True))
        self.assertEqual(dict(stored["Other lot"]), dict(self.cardinal.AD_CFG["Other lot"]))
        self.assertEqual(self.stock.read_bytes(), self.original_stock)
        self.cardinal.send_message.assert_not_called()
        self.cardinal.account.save_lot.assert_not_called()

    def test_cardinal_order_handler_resumes_after_detach(self):
        self.enroll()
        native_delivery = Mock()
        handlers = SimpleNamespace(deliver_product_handler=native_delivery, update_lot_state=Mock())
        self.cardinal.new_order_handlers = [native_delivery]
        event = SimpleNamespace(lot_id=17, config_section_obj=self.cardinal.AD_CFG[self.section],
                                order=SimpleNamespace(id="ORDER", description=self.section))
        self.auto.guarded_delivery = Mock()
        with patch.dict(sys.modules, {"handlers": handlers}):
            self.auto.install()
        deliver = self.cardinal.new_order_handlers[0]
        deliver(self.cardinal, event)
        native_delivery.assert_not_called()
        self.auto.guarded_delivery.assert_called_once()

        self.auto.detach(self.ref)
        deliver(self.cardinal, event)
        native_delivery.assert_called_once_with(self.cardinal, event)
        self.auto.guarded_delivery.assert_called_once()

    def test_stale_confirmation_cannot_detach_a_new_connection(self):
        self.enroll()
        with self.assertRaisesRegex(ValueError, "Подключение лота изменилось"):
            self.auto.detach(self.ref, expected_generation=(17, "old-connection"))
        self.auto.offer.reset_mock()
        self.assertEqual(self.flags(self.stored_config()), (True, True, True))
        self.assertIn(self.ref, self.auto.data)

    def test_detach_invalidates_the_running_monitor(self):
        self.enroll()
        with patch.object(plugin, "Thread") as thread:
            plugin.LotAutomation.start_worker(self.auto, self.ref)
        worker = thread.call_args.kwargs
        worker_ref, generation = worker["args"]
        self.assertEqual(worker_ref, self.ref)
        self.assertIs(self.auto.workers[self.ref], generation)
        self.auto.detach(self.ref)
        self.cardinal.plugins = {plugin.UUID: SimpleNamespace(enabled=True)}
        self.auto.offer.reset_mock()
        worker["target"](*worker["args"])
        self.auto.offer.assert_not_called()

    def test_legacy_detach_enables_native_delivery_and_preserves_other_flags(self):
        self.auto.update(self.ref, section=self.section, file=self.stock.name, id=17, format="login-password",
                         retired=None, inactive_since=None, orders={}, pending={}, sold=[])
        self.auto.protect_native_stock(self.auto.data[self.ref])
        self.auto.detach(self.ref)
        self.assertEqual(self.flags(self.stored_config()), (False, True, True))

    def test_detach_stops_on_open_order(self):
        self.enroll()
        self.auto.open_orders.return_value = "У лота открыт оплаченный заказ #TEST."
        with self.assertRaises(ValueError):
            self.auto.detach(self.ref)
        self.auto.open_orders.assert_called_once()
        self.assertIn(self.ref, self.auto.data)
        self.assertEqual(self.flags(self.stored_config()), (True, True, True))
        self.assertEqual(self.stock.read_bytes(), self.original_stock)

    def test_detach_stops_while_offer_can_receive_orders(self):
        self.enroll()
        self.auto.offer.return_value.active = True
        with self.assertRaises(ValueError):
            self.auto.detach(self.ref)
        self.assertIn(self.ref, self.auto.data)
        self.assertEqual(self.flags(self.stored_config()), (True, True, True))
        self.assertEqual(self.stock.read_bytes(), self.original_stock)

    def test_detach_stops_on_unfinished_stock_and_delivery_transactions(self):
        self.enroll()
        blockers = (
            {"orders": {"ORDER": {"sent": False, "logins": ["reserved"], "stage": "attempted"}}},
            {"orders": {"ORDER": {"sent": True, "logins": ["sold"], "stage": "sent"}}},
            {"pending": {"pending": True}},
            {"retirement_review": ["review"]},
            {"retirement_detachment": {"logins": ["stage"]}},
            {"sold": [plugin.sold_key("first")]},
        )
        for blocker in blockers:
            with self.subTest(blocker=blocker):
                self.auto.update(self.ref, **blocker)
                with self.assertRaises(ValueError):
                    self.auto.detach(self.ref)
                self.auto.update(self.ref, orders={}, pending={}, retirement_review=[], retirement_detachment=None,
                                 sold=[])
                self.assertEqual(self.flags(self.stored_config()), (True, True, True))
                self.assertEqual(self.stock.read_bytes(), self.original_stock)

    def reload(self):
        self.cardinal.AD_CFG = self.stored_config()
        restarted = plugin.LotAutomation(self.cardinal, self.tools, self.state, self.config_path)
        restarted.products = self.stock.parent
        restarted.offer = self.auto.offer
        restarted.open_orders = self.auto.open_orders
        return restarted

    def test_detach_resumes_after_native_config_write_failure(self):
        self.enroll()
        write_file = plugin.atomic_text

        def fail_native_config(path, *args, **kwargs):
            if path == self.config_path:
                raise OSError("injected config write failure")
            return write_file(path, *args, **kwargs)

        with patch.object(plugin, "atomic_text", side_effect=fail_native_config):
            with self.assertRaises((OSError, ValueError)):
                self.auto.detach(self.ref)
        self.assertEqual(self.flags(self.stored_config()), (True, True, True))
        restarted = self.reload()
        with patch.object(plugin, "Thread") as thread:
            restarted.start_worker(self.ref)
        worker = thread.call_args.kwargs
        worker["target"](*worker["args"])
        self.assertNotIn(self.ref, restarted.data)
        self.assertEqual(self.flags(self.stored_config()), (False, False, True))
        self.assertEqual(self.stock.read_bytes(), self.original_stock)

    def test_detach_resumes_if_completed_handoff_cannot_be_removed(self):
        self.enroll()
        write_file = plugin.atomic_text

        def fail_state_removal(path, text, **kwargs):
            if path == self.state and self.ref not in json.loads(text):
                raise OSError("injected state write failure")
            return write_file(path, text, **kwargs)

        with patch.object(plugin, "atomic_text", side_effect=fail_state_removal):
            with self.assertRaises((OSError, ValueError)):
                self.auto.detach(self.ref)
        restarted = self.reload()
        self.assertIn(self.ref, restarted.data)
        restarted.detach(self.ref)
        self.assertNotIn(self.ref, restarted.data)
        self.assertEqual(self.flags(self.stored_config()), (False, False, True))
        self.assertEqual(self.stock.read_bytes(), self.original_stock)


if __name__ == "__main__":
    unittest.main()
