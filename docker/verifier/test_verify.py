#!/usr/bin/env python3
"""Focused host tests for hidden-owned verifier configuration validation."""
import hashlib, json, pathlib, subprocess, sys, tarfile, tempfile, unittest

VERIFY = pathlib.Path(__file__).with_name('verify.py')

class VerifierConfigTest(unittest.TestCase):
    def run_verifier(self, config):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory); fixture = root / 'fixture'; fixture.mkdir()
            (fixture / 'app.py').write_text('value = 1\n')
            (fixture / 'tests').mkdir(); (fixture / 'tests' / 'test_ok.py').write_text('import unittest\nclass T(unittest.TestCase):\n def test_ok(self): self.assertTrue(True)\n')
            hidden = root / 'hidden'; (hidden / 'tests').mkdir(parents=True)
            (hidden / 'tests' / 'test_secret.py').write_text('import unittest\nclass T(unittest.TestCase):\n def test_ok(self): self.assertTrue(True)\n')
            digest = hashlib.sha256((fixture / 'tests' / 'test_ok.py').read_bytes()).hexdigest()
            (hidden / 'protected-hashes.json').write_text(json.dumps({'tests/test_ok.py': digest}))
            (hidden / 'verifier.json').write_text(json.dumps(config))
            archive = root / 'workspace.tar'
            with tarfile.open(archive, 'w') as bundle:
                for item in fixture.rglob('*'):
                    bundle.add(item, arcname=item.relative_to(fixture), recursive=False)
            output = root / 'result.json'
            subprocess.run([sys.executable, str(VERIFY), str(archive), str(hidden), str(output)], check=True)
            return json.loads(output.read_text())

    def test_rejects_shell(self):
        result = self.run_verifier({'visible_command':['sh','-c','true'], 'hidden_command':['python3','-c','pass'], 'timeout_seconds':30})
        self.assertEqual(result['status'], 'infrastructure_invalid')
        self.assertIn('disallowed executable', result['reason'])

    def test_rejects_task_owned_make(self):
        result = self.run_verifier({'visible_command':['make'], 'hidden_command':['python3','-c','pass'], 'timeout_seconds':30})
        self.assertEqual(result['status'], 'infrastructure_invalid')
        self.assertIn('disallowed executable', result['reason'])

    def test_accepts_argv_python_commands(self):
        result = self.run_verifier({'visible_command':['python3','-m','unittest','discover','-s','tests'], 'hidden_command':['python3','{hidden_tests}/test_secret.py'], 'timeout_seconds':30})
        self.assertTrue(result['verifier_passed'], result)

if __name__ == '__main__': unittest.main()
