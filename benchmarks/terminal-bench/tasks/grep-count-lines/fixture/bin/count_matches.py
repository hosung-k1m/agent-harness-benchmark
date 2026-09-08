import sys
needle = sys.argv[1]
print(sum(needle in line for line in sys.stdin))
