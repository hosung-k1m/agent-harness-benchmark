import unittest
from src.headers import charset
class TestHidden(unittest.TestCase):
 def test_case_quote(self): self.assertEqual(charset('TEXT; Charset="latin-1"'),'latin-1')
