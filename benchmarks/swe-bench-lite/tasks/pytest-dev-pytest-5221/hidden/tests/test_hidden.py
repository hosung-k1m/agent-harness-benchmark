import unittest
from src.assertions import format_label
class TestHidden(unittest.TestCase):
 def test_no_name(self): self.assertEqual(format_label(None,'bad'),'bad')
