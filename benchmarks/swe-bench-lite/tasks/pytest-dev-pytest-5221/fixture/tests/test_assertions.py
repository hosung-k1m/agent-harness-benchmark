import unittest
from src.assertions import format_label
class TestAssertions(unittest.TestCase):
 def test_name(self): self.assertEqual(format_label('x','bad'),'x: bad')
