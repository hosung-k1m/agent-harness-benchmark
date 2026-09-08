import unittest
from src.textutil import normalize_lines

class TestHidden(unittest.TestCase):
    def test_cr_and_spaces(self): self.assertEqual(normalize_lines(' a  \r b\t\r'), ' a\n b\n')
    def test_no_final_newline(self): self.assertEqual(normalize_lines('x  \ny'), 'x\ny')
