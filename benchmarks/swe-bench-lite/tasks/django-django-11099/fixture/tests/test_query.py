import unittest
from src.query import first_value
class TestQuery(unittest.TestCase):
 def test_first(self): self.assertEqual(first_value({'q':['a','b']}, 'q'), 'a')
