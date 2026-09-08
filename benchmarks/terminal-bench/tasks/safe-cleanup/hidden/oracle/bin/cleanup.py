import sys


for raw_name in sys.stdin.read().splitlines():
    name = raw_name.strip()
    if not name or '/' in name or '..' in name or name == '.tmp':
        continue
    if name.endswith('.tmp'):
        print(name)
