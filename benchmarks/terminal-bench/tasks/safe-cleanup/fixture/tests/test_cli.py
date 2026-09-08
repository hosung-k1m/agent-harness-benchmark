import subprocess, sys, unittest
class TestCLI(unittest.TestCase):
 def test_tmp(self): self.assertEqual(subprocess.check_output([sys.executable,'bin/cleanup.py'],input=b'a.tmp\na.txt\n').decode(),'a.tmp\n')
