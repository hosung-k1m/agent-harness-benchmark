def first_value(values, key, default=None):
    return values.get(key, [default])[-1]
