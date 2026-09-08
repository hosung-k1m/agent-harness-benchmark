import unittest
from src.anchors import unique_anchors
class TestAnchors(unittest.TestCase):
 def test_duplicates(self): self.assertEqual(unique_anchors(['a','a']),['a'])
