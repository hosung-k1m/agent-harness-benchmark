def first_value(values, key, default=None):
    if key not in values:
        return default
    return values[key][0] if values[key] else None
