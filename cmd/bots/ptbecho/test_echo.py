import unittest
from types import SimpleNamespace

import echo


class EchoTest(unittest.TestCase):
    def test_invalid_ephemeral_id_is_not_accepted(self):
        self.assertIsNone(echo.ephemeral_message_id(SimpleNamespace(api_kwargs={"ephemeral_message_id": 0})))
        self.assertIsNone(echo.ephemeral_message_id(SimpleNamespace(api_kwargs={"ephemeral_message_id": True})))


if __name__ == "__main__":
    unittest.main()
