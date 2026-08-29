import io, re, sys, glob

AI_WORDS = r"\b(moreover|furthermore|crucial|pivotal|robust|seamless|leverage|delve|underscore|underscores|landscape|testament|showcase|showcases|vibrant|comprehensive|empower|foster|fostering|intricate|myriad|realm|tapestry|holistic|paradigm|synergy|elevate|unlock|harness|meticulous|nuanced|multifaceted)\b"
NEG_PAR  = r"\b(not just|not only|isn't just|is not just|isn't merely|rather than merely)\b"
HEDGE    = r"\b(it is worth noting|it should be noted|it is important to note|in order to|due to the fact that|at this point in time)\b"
ING_TAIL = r", (?:ensuring|highlighting|showcasing|reflecting|emphasising|emphasizing|underscoring|demonstrating|contributing|fostering|solidifying|cementing) "
CHAT     = r"\b(hope this helps|certainly!|absolutely!|great question|let me know if)\b"

def strip_code(s):
    s = re.sub(r'```.*?```', '', s, flags=re.S)
    return re.sub(r'`[^`]*`', '', s)

rows = []
for p in sorted(glob.glob('docs/*.md')) + ['AGENTS.md', 'README.md', 'README.zh-CN.md']:
    raw = io.open(p, encoding='utf-8').read()
    s = strip_code(raw)
    n = raw.count('\n') + 1
    hits = {
        # The Chinese dash is two of the same character and is ordinary
        # punctuation there, not the imported "punchy" dash the skill warns
        # about. Counting bare `—` scored a Chinese file at twice its real
        # figure and put the roadmap top of the table when it is not close.
        'dash':  len(re.findall(r'—', s)) - 2 * len(re.findall(r'——', s)),
        'zhdash': len(re.findall(r'——', s)),
        'ai':    len(re.findall(AI_WORDS, s, re.I)),
        'neg':   len(re.findall(NEG_PAR, s, re.I)),
        'hedge': len(re.findall(HEDGE, s, re.I)),
        'ing':   len(re.findall(ING_TAIL, s)),
        'chat':  len(re.findall(CHAT, s, re.I)),
        'bold':  len(re.findall(r'^\s*[-*] \*\*[^*]+\*\*', s, re.M)),
        'emoji': len(re.findall(r'[\U0001F300-\U0001FAFF✀-➿]', s)),
    }
    per_k = {k: round(v / max(n, 1) * 1000, 1) for k, v in hits.items()}
    rows.append((p, n, hits, per_k))

print(f"{'file':28} {'lines':>6}  " + "  ".join(f"{k:>5}" for k in rows[0][2]))
for p, n, h, _ in rows:
    print(f"{p:28} {n:6}  " + "  ".join(f"{h[k]:5}" for k in h))
print()
print("每千行密度（破折号 / AI词 / 否定排比）:")
for p, n, h, k in sorted(rows, key=lambda r: -r[3]['dash']):
    print(f"  {p:28} {k['dash']:6}  {k['ai']:5}  {k['neg']:5}")
