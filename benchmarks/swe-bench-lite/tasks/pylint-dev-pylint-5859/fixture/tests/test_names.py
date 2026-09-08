import unittest
from src.names import is_ignored
class TestNames(unittest.TestCase):
 def test_exact(self): self.assertTrue(is_ignored('tmp',['tmp']))
