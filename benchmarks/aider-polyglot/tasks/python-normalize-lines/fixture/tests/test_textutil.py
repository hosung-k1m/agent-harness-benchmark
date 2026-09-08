import unittest
from src.textutil import normalize_lines

class TestNormalizeLines(unittest.TestCase):
    def test_crlf(self): self.assertEqual(normalize_lines('a\r\nb\r\n'), 'a\nb\n')
