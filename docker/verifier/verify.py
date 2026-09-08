#!/usr/bin/env python3
"""Trusted offline verifier: safe extraction, protected digest gate, then tests."""
import hashlib, json, pathlib, shutil, subprocess, sys, tarfile, tempfile

archive, hidden, output = map(pathlib.Path, sys.argv[1:4])
MAX_ENTRIES, MAX_FILE_BYTES, MAX_BYTES = 1000, 16 * 1024 * 1024, 128 * 1024 * 1024
logs = []
def fail(reason):
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps({'status':'infrastructure_invalid','verifier_passed':False,'reason':reason,'logs':logs[-20:]})+'\n')
    raise SystemExit(0)
def digest(p):
    return hashlib.sha256(p.read_bytes()).hexdigest()
protected = json.loads((hidden/'protected-hashes.json').read_text())

def load_command_config():
    """Load an optional hidden-owned, argv-only verifier configuration.

    Commands are deliberately lists, not shell snippets: task data can choose
    a language runner without gaining shell interpolation or access to the
    agent workspace before the protected-file gate has passed.
    """
    default = {
        'visible_command': ['python3', '-m', 'unittest', 'discover', '-s', 'tests'],
        'hidden_command': ['python3', '-m', 'unittest', 'discover', '-s', '{hidden_tests}'],
        'timeout_seconds': 30,
    }
    config_path = hidden / 'verifier.json'
    if not config_path.exists(): return default
    try:
        config = json.loads(config_path.read_text())
        if not isinstance(config, dict): raise ValueError('must be an object')
        for key in ('visible_command', 'hidden_command'):
            value = config.get(key, default[key])
            if not isinstance(value, list) or not value or not all(isinstance(x, str) and x and '\x00' not in x for x in value):
                raise ValueError(key + ' must be a non-empty argv list')
            if value[0] not in {'python3', 'node', 'go'}:
                raise ValueError(key + ' uses a disallowed executable')
            config[key] = value
        timeout = config.get('timeout_seconds', default['timeout_seconds'])
        if not isinstance(timeout, int) or not 1 <= timeout <= 60: raise ValueError('timeout_seconds must be 1..60')
        config['timeout_seconds'] = timeout
        return config
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        fail('invalid hidden verifier config: ' + str(exc))

config = load_command_config()
with tarfile.open(archive, 'r:*') as tf:
    members = tf.getmembers()
    if len(members) > MAX_ENTRIES: fail('archive has too many entries')
    total = 0
    seen = set()
    for m in members:
        name = pathlib.PurePosixPath(m.name)
        # Normalize harmless `./` and repeated-slash spelling before duplicate
        # detection; extraction would otherwise let two spellings overwrite
        # the same destination.
        canonical = name.as_posix().rstrip('/')
        if (not canonical or canonical == '.' or '\\' in m.name or name.is_absolute() or
            '..' in name.parts or m.isdev() or m.isfifo() or m.issym() or m.islnk()): fail('unsafe archive entry')
        if canonical in seen: fail('duplicate archive entry')
        seen.add(canonical)
        if not (m.isfile() or m.isdir()): fail('unsupported archive entry')
        if m.size < 0 or (m.isfile() and m.size > MAX_FILE_BYTES): fail('archive file too large')
        total += m.size
        if total > MAX_BYTES: fail('archive too large')
    with tempfile.TemporaryDirectory(prefix='verify-') as td:
        ws = pathlib.Path(td)/'workspace'; ws.mkdir()
        tf.extractall(ws, members=members, filter='data')
        for rel, expected in protected.items():
            p = ws/rel
            if not p.is_file() or digest(p) != expected: fail('protected path changed: '+rel)
        # The hash manifest lists every protected regular file. Treat every
        # directory that contains one as protected too, so an agent cannot add
        # or delete a hidden helper (including an empty directory) under tests/.
        expected_files = set(protected)
        expected_dirs = set()
        for rel in expected_files:
            parent = pathlib.PurePosixPath(rel).parent
            while str(parent) != '.':
                expected_dirs.add(str(parent))
                parent = parent.parent
        actual_files, actual_dirs = set(), set()
        for top in {pathlib.PurePosixPath(rel).parts[0] for rel in expected_dirs}:
            root = ws/top
            if root.is_dir():
                actual_dirs.add(top)
                for p in root.rglob('*'):
                    rel = p.relative_to(ws).as_posix()
                    if p.is_symlink() or not (p.is_dir() or p.is_file()): fail('unsafe protected entry: '+rel)
                    if p.is_dir(): actual_dirs.add(rel)
                    else: actual_files.add(rel)
        expected_directory_files = {p for p in expected_files if any(p.startswith(d + '/') for d in expected_dirs)}
        if actual_files != expected_directory_files or actual_dirs != expected_dirs: fail('protected directory changed')
        try:
            tests = subprocess.run(config['visible_command'], cwd=ws, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=config['timeout_seconds'])
        except subprocess.TimeoutExpired as exc:
            logs.append((exc.stdout or '')[-8192:] if isinstance(exc.stdout, str) else '')
            fail('visible tests timed out')
        logs.append(tests.stdout[-8192:])
        # hidden tests stay solely inside this verifier image.
        htests = pathlib.Path(td)/'hidden'; shutil.copytree(hidden/'tests', htests)
        try:
            hidden_command = [part.replace('{hidden_tests}', str(htests)) for part in config['hidden_command']]
            hidden_run = subprocess.run(hidden_command, cwd=ws, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=config['timeout_seconds'])
        except subprocess.TimeoutExpired as exc:
            logs.append((exc.stdout or '')[-8192:] if isinstance(exc.stdout, str) else '')
            fail('hidden tests timed out')
        logs.append(hidden_run.stdout[-8192:])
        passed = tests.returncode == 0 and hidden_run.returncode == 0
        output.parent.mkdir(parents=True, exist_ok=True)
        output.write_text(json.dumps({'status':'completed','verifier_passed':passed,'logs':logs})+'\n')
