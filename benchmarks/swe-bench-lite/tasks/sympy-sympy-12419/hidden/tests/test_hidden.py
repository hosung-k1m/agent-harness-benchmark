import unittest
from src.signs import sign
class TestHidden(unittest.TestCase):
 def test_negative(self): self.assertEqual(sign(-.5),-1)
