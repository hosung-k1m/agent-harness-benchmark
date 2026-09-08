def with_alpha(alpha):
    if 0 <= alpha <= 1:
        return alpha
    raise ValueError('alpha')
