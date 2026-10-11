"""Render ../SPEC.md into the marked section of index.html. Run: python3 docs/gen_spec.py"""

import html
import re
from pathlib import Path

HERE = Path(__file__).resolve().parent
START, END = "<!--SPEC-->", "<!--/SPEC-->"


def inline(s: str) -> str:
    parts = re.split(r"(`[^`]+`)", s)
    out = []
    for p in parts:
        if p.startswith("`") and p.endswith("`") and len(p) > 1:
            out.append(f"<code>{html.escape(p[1:-1])}</code>")
        else:
            p = html.escape(p)
            p = re.sub(r"\*\*(.+?)\*\*", r"<b>\1</b>", p)
            p = re.sub(r"\[([^\]]+)\]\(([^)]+)\)", r'<a href="\2">\1</a>', p)
            out.append(p)
    return "".join(out)


def cells(line: str) -> list[str]:
    line = line.strip().strip("|")
    return [c.strip() for c in re.split(r"(?<!\\)\|(?![^`]*`)", line)]


def render(md: str) -> str:
    lines = md.splitlines()
    out: list[str] = []
    i = 0
    while i < len(lines):
        ln = lines[i]
        if ln.startswith("```"):
            j = i + 1
            while not lines[j].startswith("```"):
                j += 1
            out.append("<pre>" + html.escape("\n".join(lines[i + 1 : j])) + "</pre>")
            i = j + 1
        elif ln.startswith("#"):
            n = len(ln) - len(ln.lstrip("#"))
            out.append(f"<h{n + 1}>{inline(ln[n:].strip())}</h{n + 1}>")
            i += 1
        elif ln.startswith("|"):
            rows = []
            while i < len(lines) and lines[i].startswith("|"):
                rows.append(lines[i])
                i += 1
            head, body = cells(rows[0]), [cells(r) for r in rows[2:]]
            t = '<div class="scroll"><table class="spec"><thead><tr>'
            t += "".join(f"<th>{inline(c)}</th>" for c in head) + "</tr></thead><tbody>"
            for r in body:
                t += "<tr>" + "".join(f"<td>{inline(c)}</td>" for c in r) + "</tr>"
            out.append(t + "</tbody></table></div>")
        elif ln.startswith("- "):
            items = []
            while i < len(lines) and lines[i].startswith("- "):
                items.append(lines[i][2:])
                i += 1
            out.append(
                "<ul>" + "".join(f"<li>{inline(x)}</li>" for x in items) + "</ul>"
            )
        elif ln.strip():
            para = [ln]
            i += 1
            while (
                i < len(lines)
                and lines[i].strip()
                and not re.match(r"(```|#|\||- )", lines[i])
            ):
                para.append(lines[i])
                i += 1
            out.append(f"<p>{inline(' '.join(para))}</p>")
        else:
            i += 1
    return "\n".join(out)


def main() -> None:
    spec = (HERE.parent / "SPEC.md").read_text()
    spec = spec.split("\n", 1)[1]  # the page supplies the title
    page = (HERE / "index.html").read_text()
    a, b = page.index(START) + len(START), page.index(END)
    (HERE / "index.html").write_text(page[:a] + "\n" + render(spec) + "\n" + page[b:])


if __name__ == "__main__":
    main()
