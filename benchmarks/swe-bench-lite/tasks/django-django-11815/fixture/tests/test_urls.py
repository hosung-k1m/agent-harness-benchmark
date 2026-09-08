import unittest
from src.urls import join_path
class TestURLs(unittest.TestCase):
 def test_simple(self): self.assertEqual(join_path('api','v1'), '/api/v1')
