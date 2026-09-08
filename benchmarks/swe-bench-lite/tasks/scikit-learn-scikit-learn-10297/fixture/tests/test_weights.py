import unittest
from src.weights import validate_weights
class TestWeights(unittest.TestCase):
 def test_values(self): self.assertEqual(validate_weights([1,2],2),[1.0,2.0])
