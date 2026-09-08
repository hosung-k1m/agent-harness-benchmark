import sys


needle = sys.argv[1].casefold()
print(sum(needle in line.casefold() for line in sys.stdin))
