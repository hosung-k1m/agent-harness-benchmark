def validate_weights(weights, count):
    result = [float(weight) for weight in weights]
    if len(result) != count:
        raise ValueError('weight count')
    return result
