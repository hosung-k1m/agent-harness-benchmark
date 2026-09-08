import json, unittest
class TestHidden(unittest.TestCase):
 def test_no_call(self): self.assertEqual(json.load(open('answer.json')),{'name':None,'arguments':{}})
