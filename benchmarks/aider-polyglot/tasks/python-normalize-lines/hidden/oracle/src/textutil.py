def normalize_lines(text: str) -> str:
    normalized = text.replace("\r\n", "\n").replace("\r", "\n")
    final_newline = normalized.endswith("\n")
    lines = normalized.split("\n")
    if final_newline:
        lines.pop()
    result = "\n".join(line.rstrip() for line in lines)
    return result + ("\n" if final_newline else "")
