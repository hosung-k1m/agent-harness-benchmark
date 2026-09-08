import unittest
from src.color import with_alpha
class TestHidden(unittest.TestCase):
 def test_bounds(self): self.assertEqual(with_alpha(0),0); self.assertEqual(with_alpha(1),1)
 def test_invalid(self):
  with self.assertRaises(ValueError): with_alpha(2)
