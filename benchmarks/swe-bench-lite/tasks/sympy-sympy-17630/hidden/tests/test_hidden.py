import unittest
from src.ranges import inclusive_range
class TestHidden(unittest.TestCase):
 def test_down_and_equal(self): self.assertEqual(inclusive_range(2,0),[2,1,0]); self.assertEqual(inclusive_range(4,4),[4])
