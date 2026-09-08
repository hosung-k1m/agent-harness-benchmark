import unittest
from src.ranges import inclusive_range
class TestRanges(unittest.TestCase):
 def test_up(self): self.assertEqual(inclusive_range(1,3),[1,2,3])
