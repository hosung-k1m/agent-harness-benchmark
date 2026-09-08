import unittest
from src.anchors import unique_anchors
class TestHidden(unittest.TestCase):
 def test_order(self): self.assertEqual(unique_anchors(['b','a','b','c']),['b','a','c'])
