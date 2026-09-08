import re


def charset(value):
    match = re.search(r'(?:^|;)\s*charset\s*=\s*(?:"([^"]*)"|([^;\s]*))', value, re.IGNORECASE)
    if not match:
        return None
    return match.group(1) if match.group(1) is not None else match.group(2)
