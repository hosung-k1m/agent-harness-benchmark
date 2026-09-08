import subprocess, sys, unittest
class TestHidden(unittest.TestCase):
 def test_order(self): self.assertEqual(subprocess.check_output([sys.executable,'bin/total.py'],input=b'amount,name\n7,x\n').decode().strip(),'7')
