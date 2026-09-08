import sys
for name in sys.stdin.read().splitlines():
 if name.endswith('.tmp'): print(name)
