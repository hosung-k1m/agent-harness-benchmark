def join_path(*parts):
    segments = [part.strip('/') for part in parts if part.strip('/')]
    return '/' + '/'.join(segments)
