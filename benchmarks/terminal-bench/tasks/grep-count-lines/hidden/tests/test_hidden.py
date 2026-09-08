import subprocess, sys, unittest
class TestHidden(unittest.TestCase):
 def test_case(self): self.assertEqual(subprocess.check_output([sys.executable,'bin/count_matches.py','go'],input=b'GO\nno\nGo\n').decode().strip(),'2')
