import unittest
from src.signs import sign
class TestSigns(unittest.TestCase):
 def test_zero(self): self.assertEqual(sign(0),0)
