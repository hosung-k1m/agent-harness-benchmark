import unittest
from src.color import with_alpha
class TestColor(unittest.TestCase):
 def test_middle(self): self.assertEqual(with_alpha(.5),.5)
