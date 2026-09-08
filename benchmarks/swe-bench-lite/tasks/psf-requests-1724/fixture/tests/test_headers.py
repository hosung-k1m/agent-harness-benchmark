import unittest
from src.headers import charset
class TestHeaders(unittest.TestCase):
 def test_plain(self): self.assertEqual(charset('text/plain; charset=utf-8'),'utf-8')
