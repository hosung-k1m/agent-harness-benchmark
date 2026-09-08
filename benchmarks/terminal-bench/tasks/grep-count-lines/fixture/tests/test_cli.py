import subprocess, sys, unittest
class TestCLI(unittest.TestCase):
 def test_count(self): self.assertEqual(subprocess.check_output([sys.executable,'bin/count_matches.py','x'],input=b'x\ny\nx\n').decode().strip(),'2')
