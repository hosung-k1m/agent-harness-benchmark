import json, unittest
class TestHidden(unittest.TestCase):
 def test_call(self): self.assertEqual(json.load(open('answer.json')),{'name':'convert_currency','arguments':{'amount':3,'source':'USD','target':'EUR'}})
