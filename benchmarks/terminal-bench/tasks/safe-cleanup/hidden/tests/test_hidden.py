import subprocess, sys, unittest
class TestHidden(unittest.TestCase):
 def test_unsafe(self): self.assertEqual(subprocess.check_output([sys.executable,'bin/cleanup.py'],input=b'../x.tmp\ndir/x.tmp\n .tmp\nok.tmp\n').decode(),'ok.tmp\n')
