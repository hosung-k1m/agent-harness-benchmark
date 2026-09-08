import json, unittest
class TestAnswer(unittest.TestCase):
 def test_is_json_object(self):
  with open('answer.json') as f: self.assertIsInstance(json.load(f), dict)
