#!/usr/bin/env python3
"""Build a self-contained HTML version of this runbook from README.md.

    python3 make_html.py [--ref master] [--out guide.html]

The README is the single source of truth -- this script adds no content.
What it does:
  * inlines every "Snippet: [...](code_snippets/...)" reference as a
    collapsible block with the script's full source, so the HTML is useful
    without a repo checkout;
  * rewrites repo-relative links (../../../public/architectures/...) to
    GitHub URLs at --ref, so they still work from a standalone file;
  * embeds the markdown renderer (marked) and diagram renderer (mermaid)
    INSIDE the output, so the file works offline as a single artifact.

The two JS libraries are downloaded once into vendor/ (kept out of git) and
reused on later runs. Only the first run needs internet access.
"""

import argparse
import html
import json
import os
import pathlib
import re
import subprocess
import sys
import urllib.request

HERE = pathlib.Path(__file__).resolve().parent
REPO_BLOB = "https://github.com/mongodb/mongodb-kubernetes/blob/{ref}/"
REPO_TREE = "https://github.com/mongodb/mongodb-kubernetes/tree/{ref}/"

VENDOR = {
    "marked.min.js": "https://cdn.jsdelivr.net/npm/marked@12.0.2/marked.min.js",
    "mermaid.min.js": "https://cdn.jsdelivr.net/npm/mermaid@10.9.1/dist/mermaid.min.js",
    "highlight.min.js": "https://cdn.jsdelivr.net/gh/highlightjs/cdn-release@11.9.0/build/highlight.min.js",
    "highlight-theme.css": "https://cdn.jsdelivr.net/gh/highlightjs/cdn-release@11.9.0/build/styles/github-dark.min.css",
}

CSS = """
:root { --green:#00684A; --dark:#001E2B; --mist:#E3FCF7; --line:#E8EDEB; }
* { box-sizing:border-box; }
body { margin:0; font:16px/1.6 -apple-system,BlinkMacSystemFont,"Segoe UI",Helvetica,Arial,sans-serif;
       color:#1C2D38; }
main { max-width:900px; margin:0 auto; padding:2rem 1.5rem 6rem; }
h1,h2,h3,h4 { line-height:1.25; color:var(--dark); }
h1 { font-size:1.9rem; border-bottom:3px solid var(--green); padding-bottom:.4rem; }
h2 { font-size:1.5rem; margin-top:2.5em; border-bottom:1px solid var(--line); padding-bottom:.3rem; }
h3 { font-size:1.2rem; margin-top:2em; }
h4 { font-size:1.05rem; margin-top:1.6em; }
a { color:var(--green); }
code { background:#F4F6F5; border-radius:4px; padding:.1em .35em; font-size:.9em; }
pre { background:var(--dark); color:#E6EDF3; border-radius:8px; padding:1rem; overflow-x:auto; }
pre code, pre code.hljs { background:none; color:inherit; padding:0; font-size:.85rem; }
blockquote { border-left:4px solid var(--green); background:#F7FBFA; margin:1em 0;
             padding:.6em 1em; color:#33454F; }
blockquote p { margin:.4em 0; }
table { border-collapse:collapse; width:100%; margin:1em 0; font-size:.92rem; }
th,td { border:1px solid var(--line); padding:.5em .7em; text-align:left; vertical-align:top; }
th { background:var(--mist); color:var(--dark); }
details.snippet { margin:.8em 0 1.4em; border:1px solid var(--line); border-radius:8px; }
details.snippet summary { cursor:pointer; padding:.55em .9em; font-family:ui-monospace,Menlo,monospace;
                          font-size:.85rem; color:var(--green); font-weight:600; }
details.snippet[open] summary { border-bottom:1px solid var(--line); }
details.snippet pre { margin:0; border-radius:0 0 8px 8px; }
.mermaid { background:#fff; text-align:center; margin:1.5em 0; }
"""

PAGE = """<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{title}</title>
<style>{highlight_css}</style>
<style>{css}</style>
</head>
<body>
<div style="background:var(--mist);border-bottom:1px solid var(--line);padding:.6em 1.5rem;font-size:.85rem;">
This is a generated offline copy. Canonical source, always current:
<a href="{repo_dir}">docs/search/12-search-percluster-operator-rs</a> in the mongodb-kubernetes repository.
</div>
<main id="content">JavaScript is required to render this guide.</main>
<script>{marked_js}</script>
<script>{mermaid_js}</script>
<script>{highlight_js}</script>
<script>
const SOURCE = {source_json};
const SNIPPETS = {snippets_json};
document.getElementById("content").innerHTML = marked.parse(SOURCE);
// GitHub-style heading ids so the README's internal #anchor links keep working
// (marked v5+ no longer generates them itself).
const seen = {{}};
for (const h of document.querySelectorAll("#content h1,#content h2,#content h3,#content h4")) {{
  let slug = h.textContent.trim().toLowerCase()
    .replace(/[^\\w\\s-]/g, "").replace(/\\s+/g, "-");
  if (slug in seen) {{ slug = `${{slug}}-${{++seen[slug]}}`; }} else {{ seen[slug] = 0; }}
  h.id = slug;
}}
// marked renders ```mermaid fences as <pre><code class="language-mermaid">; hand them to mermaid.
for (const code of document.querySelectorAll("code.language-mermaid")) {{
  const div = document.createElement("div");
  div.className = "mermaid";
  div.textContent = code.textContent;
  code.closest("pre").replaceWith(div);
}}
mermaid.initialize({{ startOnLoad: false, theme: "base" }});
mermaid.run();
// Inject snippet sources as text (never through the markdown parser).
for (const p of document.querySelectorAll("#content p")) {{
  const m = p.textContent.trim().match(/^@@SNIPPET\|([^|]+)\|(.+)@@$/);
  if (!m) continue;
  const details = document.createElement("details");
  details.className = "snippet";
  const summary = document.createElement("summary");
  summary.textContent = m[2];
  const pre = document.createElement("pre");
  const code = document.createElement("code");
  code.textContent = SNIPPETS[m[1]];
  code.className = "language-bash";
  pre.appendChild(code);
  details.append(summary, pre);
  p.replaceWith(details);
}}
// Syntax coloring for bash/yaml blocks (mermaid blocks were already replaced above).
for (const code of document.querySelectorAll("pre code")) {{ hljs.highlightElement(code); }}
</script>
</body>
</html>
"""


def vendor_js(name: str) -> str:
    path = HERE / "vendor" / name
    if not path.exists():
        path.parent.mkdir(exist_ok=True)
        url = VENDOR[name]
        print(f"downloading {url} -> vendor/{name} (first run only)")
        with urllib.request.urlopen(url) as resp:
            path.write_bytes(resp.read())
    return path.read_text()


def inline_snippets(md: str) -> tuple[str, dict]:
    """Replace 'Snippet: [name](code_snippets/x.sh)' lines with placeholders.

    The sources are injected client-side with textContent AFTER markdown
    parsing: raw HTML blocks in markdown end at the first blank line, so
    embedding the code directly would hand half of it to the markdown parser
    (YAML lists became bullet points).
    """
    sources: dict[str, str] = {}

    def repl(m: re.Match) -> str:
        rel = m.group(2)
        target = HERE / rel
        if not target.exists():
            sys.exit(f"error: README references missing snippet {rel}")
        sources[rel] = target.read_text()
        # Plain-text marker, swapped for a <details> block after parsing. A raw
        # HTML placeholder here makes marked treat the following line as part
        # of the HTML block, which ate the next heading.
        return f"@@SNIPPET|{rel}|{m.group(1)}@@"

    md = re.sub(r"^Snippet: \[([^\]]+)\]\((code_snippets/[^)]+)\)\s*$",
                repl, md, flags=re.MULTILINE)
    return md, sources


def rewrite_repo_links(md: str, ref: str) -> str:
    blob, tree = REPO_BLOB.format(ref=ref), REPO_TREE.format(ref=ref)
    # directory links like ../../../public/architectures/... -> GitHub tree URLs
    md = re.sub(r"\]\(\.\./\.\./\.\./([^)#]+?)/?\)", rf"]({tree}\1)", md)
    # sibling-file links (env.sh, env_variables.sh, test.sh) -> GitHub blob URLs
    md = re.sub(r"\]\((env\.sh|env_variables\.sh|test\.sh)\)",
                rf"]({blob}docs/search/12-search-percluster-operator-rs/\1)", md)
    return md


def upload(out: pathlib.Path) -> None:
    """Publish/update the page on the internal static-page service.

    Set PAGES_BASE_URL to the service's API base URL (internal; not committed
    here because this repository is public). Auth comes from kanopy-oidc.
    The returned slug is cached next to this script so later uploads update
    the same page (stable URL) instead of creating a new one.
    """
    base = os.environ.get("PAGES_BASE_URL")
    if not base:
        sys.exit("error: set PAGES_BASE_URL to the internal page service's API base URL")
    token = subprocess.run(["kanopy-oidc", "login"], capture_output=True, text=True, check=True).stdout.strip()
    slug_file = HERE / ".page-slug"
    if slug_file.exists():
        url = f"{base}/api/pages/{slug_file.read_text().strip()}/versions"
    else:
        url = f"{base}/api/upload"
    result = subprocess.run(
        ["curl", "-sf", "-X", "POST", url,
         "-H", f"Authorization: Bearer {token}",
         "-F", f"file=@{out}"],
        capture_output=True, text=True, check=True)
    info = json.loads(result.stdout)
    slug_file.write_text(info["slug"])
    print(f"published: {info['url']} (version {info.get('version', '?')})")


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--ref", default="master", help="git ref for rewritten GitHub links")
    ap.add_argument("--out", default="guide.html", help="output file (relative to this dir)")
    ap.add_argument("--upload", action="store_true",
                    help="after building, publish to the internal page service (see upload())")
    args = ap.parse_args()

    md = (HERE / "README.md").read_text()
    title_match = re.match(r"# (.+)", md)
    title = title_match.group(1) if title_match else "MongoDB Search Runbook"

    md, snippets = inline_snippets(md)
    md = rewrite_repo_links(md, args.ref)

    out = HERE / args.out
    out.write_text(PAGE.format(
        repo_dir=REPO_TREE.format(ref=args.ref) + "docs/search/12-search-percluster-operator-rs",
        title=html.escape(title),
        css=CSS,
        marked_js=vendor_js("marked.min.js"),
        mermaid_js=vendor_js("mermaid.min.js"),
        highlight_js=vendor_js("highlight.min.js"),
        highlight_css=vendor_js("highlight-theme.css").replace("</", "<\\/"),
        source_json=json.dumps(md).replace("</", "<\\/"),  # keep </script> inert
        snippets_json=json.dumps(snippets).replace("</", "<\\/"),
    ))
    print(f"wrote {out} ({out.stat().st_size // 1024} KB)")

    if args.upload:
        upload(out)


if __name__ == "__main__":
    main()
