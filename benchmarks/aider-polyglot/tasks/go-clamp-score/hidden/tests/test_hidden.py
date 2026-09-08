import pathlib, subprocess, tempfile

root = pathlib.Path.cwd()
with tempfile.TemporaryDirectory() as directory:
    probe = pathlib.Path(directory) / 'probe_test.go'
    probe.write_text('package score\nimport "testing"\nfunc TestHidden(t *testing.T) { if ClampScore(-2, 0, 5)!=0 || ClampScore(3,5,0)!=3 { t.Fatal("clamp") } }\n')
    target = root / 'probe_test.go'
    target.write_text(probe.read_text())
    try: assert subprocess.run(['go','test','./...'], cwd=root).returncode == 0
    finally: target.unlink(missing_ok=True)
