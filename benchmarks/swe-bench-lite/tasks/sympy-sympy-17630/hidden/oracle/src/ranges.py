def inclusive_range(start, end):
    step = 1 if end >= start else -1
    return list(range(start, end + step, step))
