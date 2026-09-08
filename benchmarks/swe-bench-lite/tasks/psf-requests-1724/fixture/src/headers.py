def charset(value): return value.split('charset=')[-1] if 'charset=' in value else None
