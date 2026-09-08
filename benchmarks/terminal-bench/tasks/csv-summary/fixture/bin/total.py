import csv, sys
print(sum(int(row[0]) for row in csv.reader(sys.stdin)))
