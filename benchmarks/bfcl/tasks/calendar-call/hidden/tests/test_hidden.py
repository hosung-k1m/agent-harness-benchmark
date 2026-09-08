import json, unittest
class TestHidden(unittest.TestCase):
 def test_call(self): self.assertEqual(json.load(open('answer.json')),{'name':'create_event','arguments':{'title':'Ship','date':'2026-01-02'}})
