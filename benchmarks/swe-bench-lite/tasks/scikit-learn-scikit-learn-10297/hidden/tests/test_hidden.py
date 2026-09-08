import unittest
from src.weights import validate_weights
class TestHidden(unittest.TestCase):
 def test_length(self):
  with self.assertRaises(ValueError): validate_weights([1],2)
