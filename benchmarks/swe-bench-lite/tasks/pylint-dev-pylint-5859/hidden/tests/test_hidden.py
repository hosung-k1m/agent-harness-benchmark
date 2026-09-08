import unittest
from src.names import is_ignored
class TestHidden(unittest.TestCase):
 def test_case(self): self.assertTrue(is_ignored('TMP',['tmp']))
