import unittest
from types import SimpleNamespace

import echo


class EchoTest(unittest.TestCase):
    def test_extra_field_fallback_and_invalid_values(self):
        message = SimpleNamespace(ephemeral_message_id=None, model_extra={})
        message.model_extra = {"ephemeral_message_id": 66}
        self.assertEqual(echo.ephemeral_message_id(message), 66)
        message.model_extra = {"ephemeral_message_id": True}
        self.assertIsNone(echo.ephemeral_message_id(message))


if __name__ == "__main__":
    unittest.main()
