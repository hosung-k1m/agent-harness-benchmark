import subprocess, sys, unittest
class TestCLI(unittest.TestCase):
 def test_total(self): self.assertEqual(subprocess.check_output([sys.executable,'bin/total.py'],input=b'name,amount\na,2\nb,3\n').decode().strip(),'5')
