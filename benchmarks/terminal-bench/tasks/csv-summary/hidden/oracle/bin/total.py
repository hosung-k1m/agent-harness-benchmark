import csv
import sys


rows = csv.DictReader(sys.stdin)
print(sum(int(row['amount']) for row in rows))
