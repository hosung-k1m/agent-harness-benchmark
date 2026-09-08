def is_ignored(name, ignored):
    folded = name.casefold()
    return any(candidate.casefold() == folded for candidate in ignored)
