import json, unittest
class TestHidden(unittest.TestCase):
 def test_call(self): self.assertEqual(json.load(open('answer.json')),{'name':'get_weather','arguments':{'city':'Oslo','units':'celsius'}})
