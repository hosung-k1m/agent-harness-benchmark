import unittest
from src.query import first_value
class TestHidden(unittest.TestCase):
 def test_default_and_empty(self):
  self.assertEqual(first_value({}, 'q', 'x'), 'x')
  self.assertIsNone(first_value({'q':[]}, 'q'))
