"""Generate the admin-panel API doc's route and permission tables from source.

The reason this script exists: those tables drifted ~40% out of date without
anyone noticing, because nothing checked them. They are generated from
web/src/nav.ts, web/src/permissions.tsx and cmd/telesrv-admin/server.go
instead, and the doc carries the generated marker so a hand edit is visible in
review as a diff against the next regeneration.

The hand-written "## API: ..." tables are held to server.go as well: --check
fails on a route without a row and on a row for a route that is gone, and the
Permission column of the action tables is generated (--write fills it in).

Usage:
    python3 scripts/gen_admin_panel_doc_tables.py            # print
    python3 scripts/gen_admin_panel_doc_tables.py --check     # exit 1 if stale
"""

from __future__ import annotations

import argparse
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
WEB = ROOT / "cmd/telesrv-admin/web/src"
WEB_PAGES = WEB / "pages"
SERVER_GO = ROOT / "cmd/telesrv-admin/server.go"

# ---------------------------------------------------------------- nav sections

# The section header is matched on its own and the tabs array is then read out by
# bracket matching. It used to be captured by one non-greedy (?P<tabs>.*?)
# terminated with `\]\s*\}`, which stops at the first `]` that happens to be
# followed by a `}` -- i.e. the closing bracket of an entry's own
# `aliases: [...]` -- so a section whose tabs carry aliases lost its tabs and
# dropped out of the table with no error at all. /messages/private is that
# entry, and the docs silently lost the Messages section because of it.
SECTION_RE = re.compile(
    r'\{\s*id:\s*"(?P<id>[a-z]+)",\s*labelKey:\s*"(?P<label>[a-zA-Z.]+)",'
    r'\s*icon:\s*\w+,\s*tabs:\s*\['
)
TAB_RE = re.compile(
    r'\{\s*path:\s*"(?P<path>[^"]+)",\s*labelKey:\s*"(?P<label>[a-zA-Z.]+)",'
    r'\s*permission:\s*(?P<perm>\w+)(?P<rest>[^}]*)\}'
)


def read_bracket_array(src: str, open_bracket: int) -> str:
    """Return the text between the `[` at open_bracket and its matching `]`."""
    depth = 0
    for i in range(open_bracket, len(src)):
        if src[i] == "[":
            depth += 1
        elif src[i] == "]":
            depth -= 1
            if depth == 0:
                return src[open_bracket + 1 : i]
    raise ValueError(f"unbalanced [ at offset {open_bracket} of nav.ts")


def parse_nav() -> list[tuple[str, str, list[tuple[str, str, str, bool]]]]:
    """Return [(section id, label key, [(path, label key, permission const, has alias)])]."""
    src = (WEB / "nav.ts").read_text(encoding="utf-8")
    body = src[src.index("export const navSections"):]
    body = body[: body.index("\n];")]

    out = []
    pos = 0
    while (m := SECTION_RE.search(body, pos)) is not None:
        tabs_src = read_bracket_array(body, m.end() - 1)
        pos = m.end() + len(tabs_src) + 1
        tabs = []
        for t in TAB_RE.finditer(tabs_src):
            tabs.append(
                (
                    t.group("path"),
                    t.group("label"),
                    t.group("perm"),
                    "aliases" in t.group("rest"),
                )
            )
        # A section that parses to zero tabs is a broken parse, not an empty
        # section -- nav.ts has no such thing, and quietly dropping one is
        # exactly how these tables rotted in the first place.
        if not tabs:
            raise ValueError(f"nav section {m.group('id')!r} parsed to zero tabs")
        out.append((m.group("id"), m.group("label"), tabs))
    return out


# ----------------------------------------------------------------- permissions

PERM_CONST_RE = re.compile(r'export const (\w+) = "([a-zA-Z.*]+)";')
LABEL_RE = re.compile(r'"([a-zA-Z.*]+)":\s*\{\s*title:\s*"((?:[^"\\]|\\.)*)",\s*hint:\s*"((?:[^"\\]|\\.)*)"')
# The group array is one object per line-per-field, so match each object as a
# unit rather than relying on the fields being adjacent.
GROUP_TITLE_RE = re.compile(
    r'title:\s*"([^"]+)",\s*\n\s*hint:\s*"([^"]+)",\s*\n\s*permissions:\s*\[([^\]]*)\]',
)
GROUP_ITEM_RE = re.compile(r'"([a-zA-Z.*]+)"')


def parse_i18n(lang: str = "en") -> dict[str, str]:
    """English UI strings, so the tables show what an operator reads rather than
    an i18n key. Falls back to the key itself when a string is missing, which
    is itself a useful failure signal in review."""
    src = (WEB / "i18n.tsx").read_text(encoding="utf-8")
    body = src[src.index(f"  {lang}: {{") + len(lang) + 6:]
    nxt = [m.start() for m in re.finditer(r"^  (?:en|zh|ru): \{$", body, re.M)]
    body = body[: nxt[0]] if nxt else body
    out = {}
    for m in re.finditer(r'^\s*"([a-zA-Z0-9_.]+)":\s*"((?:[^"\\]|\\.)*)"', body, re.M):
        out[m.group(1)] = m.group(2)
    return out


def parse_permissions() -> tuple[dict[str, str], dict[str, str], list[tuple[str, str, list[str]]], list[str]]:
    """Return (const name -> name, name -> (title, hint), groups, assignable order)."""
    src = (WEB / "permissions.tsx").read_text(encoding="utf-8")
    const_to_name = dict(PERM_CONST_RE.findall(src))

    labels: dict[str, str] = {}
    label_block = src[src.index("const permissionLabels"): src.index("export function permissionTitle")]
    for m in LABEL_RE.finditer(label_block):
        labels[m.group(1)] = f"{m.group(2)} — {m.group(3)}"

    groups: list[tuple[str, str, list[str]]] = []
    group_src = src[src.index("export const permissionGroups"):]
    group_src = group_src[: group_src.index("\n];")]
    for m in GROUP_TITLE_RE.finditer(group_src):
        groups.append((m.group(1), m.group(2), GROUP_ITEM_RE.findall(m.group(3))))

    return const_to_name, labels, groups, sorted(labels)


SECURITY_GO = ROOT / "cmd/telesrv-admin/security.go"


def parse_assignable() -> list[str]:
    """Permission names in the order assignablePermissions() lists them.

    This is the list the operator-accounts screen offers, so it -- not
    permissionLabels -- is the set of rights that can actually be granted, and
    it is therefore the set the documentation is about.
    """
    src = SECURITY_GO.read_text(encoding="utf-8")
    block = src[src.index("func assignablePermissions() []string {"):]
    block = block[: block.index("\n}\n")]
    const_to_name = dict(PERM_CONST_RE.findall((WEB / "permissions.tsx").read_text(encoding="utf-8")))
    names = []
    for line in block.splitlines():
        m = re.match(r"\s*(permission\w+|permissionAll),", line)
        if m:
            names.append(const_to_name.get(m.group(1), m.group(1)))
    return names


# ------------------------------------------------------------- server routes

ROUTE_RE = re.compile(
    r'mux\.Handle(?:Func)?\(\s*"(?P<method>GET|POST|PUT|DELETE)\s+(?P<path>[^"]+)"\s*,\s*'
    r'(?P<gate>s\.\w+)\((?P<handler>[\w.]+)'
)


def parse_routes() -> list[tuple[str, str, str, str]]:
    """Return [(method, path, gate name, handler)] for the panel's own routes."""
    src = SERVER_GO.read_text(encoding="utf-8")
    body = src[src.index("func (s *server) routes() http.Handler {"):]
    body = body[: body.index("\n}\n")]
    out = []
    for m in ROUTE_RE.finditer(body):
        gate = m.group("gate")
        if gate == "mux.HandleFunc" or gate.endswith("HandleFunc"):
            continue
        gate = gate.rsplit(".", 1)[-1]
        out.append((m.group("method"), m.group("path"), gate, m.group("handler")))
    return out


# --------------------------------------------------- route and action coverage
#
# The doc's per-route tables ("## API: ..." -- Method | Path | ...) and action
# tables (Path | Specific body fields | Permission) are prose, so they are not
# generated wholesale. They rotted anyway: when the check below was written, 18
# GET routes and 23 actions had no row in either language and two rows named
# routes that had been registered differently. So the *set* of rows is checked
# against server.go, and the one column that is pure fact -- which right an
# action needs -- is generated.

STATEMENT_RE = re.compile(r'mux\.Handle(?P<func>Func)?\(\s*"(?P<method>GET|POST|PUT|DELETE)\s+(?P<path>[^"]+)"\s*,')
GO_PERM_CONST_RE = re.compile(r'^\s*(permission\w+)\s*=\s*"([^"]*)"', re.M)
SESSION_ONLY = "(session)"
PUBLIC = "(public)"


def go_permission_consts() -> dict[str, str]:
    return dict(GO_PERM_CONST_RE.findall(SECURITY_GO.read_text(encoding="utf-8")))


def wrapper_gates(consts: dict[str, str]) -> dict[str, list[str]]:
    """Map the small route wrappers (serverManage, premiumManage, ...) to the
    right each one demands, read from the wrapper's own body so a wrapper that
    changes its right changes the doc with it."""
    out: dict[str, list[str]] = {}
    for path in sorted(SERVER_GO.parent.glob("*.go")):
        src = path.read_text(encoding="utf-8")
        for m in re.finditer(r"func \(s \*server\) (\w+)\([^)]*\) http\.Handler \{\n(?P<body>(?:.*\n){1,4}?)\}", src):
            consts_used = re.findall(r"\b(permission\w+)\b", m.group("body"))
            if consts_used:
                out[m.group(1)] = [consts.get(c, c) for c in consts_used[:1]]
    return out


def parse_route_rights() -> list[tuple[str, str, tuple[str, ...]]]:
    """Return [(method, path, rights)] for every route the panel registers.

    rights is the tuple of grantable rights the route checks (all of them, for
    scopedRouteAll), or (PUBLIC,) / (SESSION_ONLY,) for the routes that need no
    right. A route whose gate this cannot read is an error rather than a guess:
    an unreadable gate documented as "—" is how a wrong right ends up in print.
    """
    src = SERVER_GO.read_text(encoding="utf-8")
    body = src[src.index("func (s *server) routes() http.Handler {"):]
    body = body[: body.index("\n}\n")]
    consts = go_permission_consts()
    wrappers = wrapper_gates(consts)
    out = []
    matches = list(STATEMENT_RE.finditer(body))
    for i, m in enumerate(matches):
        rest = body[m.end() : matches[i + 1].start() if i + 1 < len(matches) else len(body)]
        method, path = m.group("method"), m.group("path")
        if m.group("func"):
            rights: tuple[str, ...] = (PUBLIC,)
        elif (g := re.match(r"\s*s\.scopedRoute\(\s*(permission\w+)", rest)) is not None:
            value = consts[g.group(1)]
            rights = (value or SESSION_ONLY,)
        elif (g := re.match(r"\s*s\.scopedRouteAll\(\s*\[\]string\{([^}]*)\}", rest)) is not None:
            rights = tuple(consts[c.strip()] for c in g.group(1).split(",") if c.strip())
        elif (g := re.match(r"\s*s\.(\w+)\(", rest)) is not None and g.group(1) in wrappers:
            rights = tuple(wrappers[g.group(1)])
        else:
            raise ValueError(f"cannot read the gate of {method} {path} in server.go")
        out.append((method, path, rights))
    return out


ACTION_PREFIX = "/api/actions/"
ROUTE_ROW_RE = re.compile(r"^\|\s*(GET|POST|PUT|DELETE)\s*\|\s*`([^`]+)`", re.M)
# The action tables are the ones whose first two header cells are these, in
# either language; a third "Permission" cell is the generated column.
ACTION_HEADERS = {("Path", "Specific body fields"), ("Путь", "Специфичные поля тела")}


def split_row(line: str) -> list[str]:
    """Cells of one markdown table row. Pipes inside backticks are not cell
    separators (a few body-field descriptions quote `a|b` values)."""
    cells, cur, tick = [], [], False
    for ch in line.strip().strip("|"):
        if ch == "`":
            tick = not tick
        if ch == "|" and not tick:
            cells.append("".join(cur).strip())
            cur = []
        else:
            cur.append(ch)
    cells.append("".join(cur).strip())
    return cells


def action_table_rows(doc: str) -> list[tuple[int, str, list[str], bool]]:
    """Return [(line index, action name, cells, has permission column)]."""
    lines = doc.split("\n")
    out = []
    in_table, has_perm = False, False
    for i, line in enumerate(lines):
        if not line.startswith("|"):
            in_table = False
            continue
        cells = split_row(line)
        if len(cells) >= 2 and (cells[0], cells[1]) in ACTION_HEADERS:
            in_table, has_perm = True, len(cells) >= 3
            continue
        if not in_table or set(line) <= set("|- "):
            continue
        m = re.fullmatch(r"`([a-z0-9-]+)`", cells[0])
        if m:
            out.append((i, m.group(1), cells, has_perm))
    return out


def rights_cell(rights: tuple[str, ...]) -> str:
    return " + ".join(f"`{r}`" for r in rights)


def coverage_problems(doc_path: pathlib.Path, routes: list[tuple[str, str, tuple[str, ...]]]) -> list[str]:
    doc = doc_path.read_text(encoding="utf-8")
    documented = set(ROUTE_ROW_RE.findall(doc))
    actions = {name: (cells, has_perm) for _i, name, cells, has_perm in action_table_rows(doc)}
    known = {(m, p) for m, p, _r in routes}
    known_actions = {p[len(ACTION_PREFIX):]: r for m, p, r in routes if p.startswith(ACTION_PREFIX)}
    problems = []
    for method, path, rights in routes:
        if path.startswith(ACTION_PREFIX):
            # An action is documented either as a row of an action table or,
            # where a section lists its routes by method, as a POST row.
            if path[len(ACTION_PREFIX):] not in actions and (method, path) not in documented:
                problems.append(f"no row for {method} {path}")
        elif (method, path) not in documented:
            problems.append(f"no row for {method} {path}")
    for method, path in sorted(documented - known):
        if path.startswith("/api/"):
            problems.append(f"row for {method} {path}, which server.go does not register")
    for name, (cells, has_perm) in sorted(actions.items()):
        if name not in known_actions:
            problems.append(f"action row `{name}`, which server.go does not register")
        elif has_perm and cells[-1] != rights_cell(known_actions[name]):
            problems.append(f"action `{name}`: permission {cells[-1]!r}, server.go enforces {rights_cell(known_actions[name])}")
    return problems


def rewrite_permission_cells(doc_path: pathlib.Path, routes: list[tuple[str, str, tuple[str, ...]]]) -> bool:
    """Set every action row's Permission cell to the right server.go enforces.
    The other cells are prose and are left alone. Returns True if it changed."""
    known_actions = {p[len(ACTION_PREFIX):]: r for _m, p, r in routes if p.startswith(ACTION_PREFIX)}
    doc = doc_path.read_text(encoding="utf-8")
    lines = doc.split("\n")
    for i, name, cells, has_perm in action_table_rows(doc):
        if has_perm and name in known_actions:
            cells[-1] = rights_cell(known_actions[name])
            lines[i] = "| " + " | ".join(cells) + " |"
    updated = "\n".join(lines)
    if updated == doc:
        return False
    doc_path.write_text(updated, encoding="utf-8")
    return True


# ------------------------------------------------------- section tab placement


def _actions_region(text: str, offset: int) -> tuple[int, int] | None:
    """Return the (start, end) of the nearest preceding `actions={...}` region.

    Braces are matched rather than lines counted because a prop value is a JSX
    expression, not a fixed number of lines.
    """
    start = text.rfind("actions={", 0, offset)
    if start < 0:
        return None
    depth = 0
    for i in range(start + len("actions="), len(text)):
        if text[i] == "{":
            depth += 1
        elif text[i] == "}":
            depth -= 1
            if depth == 0:
                return (start, i)
    return None


def misplaced_section_tabs() -> list[str]:
    """Pages whose <SectionTabs> sits inside PageFrame's `actions` prop.

    PageFrame renders `actions` into the header's right-aligned action row and
    everything else below the title, so the section sub-menu only belongs among
    the children. Seven pages got it wrong once -- three of them nested the tab
    bar inside a <button>, which is invalid HTML -- and nothing caught it,
    because the component compiles and renders either way. It is checked here
    for the same reason the tables are: a placement that is wrong but renders is
    exactly the kind of drift that survives review.
    """
    bad: list[str] = []
    for page in sorted(WEB_PAGES.glob("*.tsx")):
        text = page.read_text(encoding="utf-8")
        offset = 0
        for lineno, line in enumerate(text.splitlines(), 1):
            if "<SectionTabs" in line:
                region = _actions_region(text, offset)
                if region and region[0] < offset < region[1]:
                    bad.append(f"{page.name}:{lineno}")
            offset += len(line) + 1
    return bad


# ------------------------------------------------------------------- rendering

GATE_TO_PERMISSION = {
    "scopedRoute": None,  # resolved per-route below
    "serverManage": "server.manage",
    "premiumManage": "premium.manage",
    "requireAdminsManage": "admins.manage",
}


def gate_permission(gate: str) -> str:
    if gate in GATE_TO_PERMISSION and GATE_TO_PERMISSION[gate]:
        return GATE_TO_PERMISSION[gate]
    if gate == "scopedRoute":
        return "per-route"
    return gate


def render_tables() -> dict[str, str]:
    const_to_name, labels, _groups, all_names = parse_permissions()
    i18n = parse_i18n()
    assignable = parse_assignable()
    sections = parse_nav()
    routes = parse_routes()

    def label(key: str) -> str:
        # A missing string is a bug worth seeing in the diff, not hiding.
        return i18n.get(key, f"`{key}` (missing)")

    # -- sections / tabs -----------------------------------------------------
    # One row per tab. The section repeats rather than being left blank: a
    # generated table with merged cells renders unpredictably once it is
    # copy-pasted into anything else.
    sections_md = [
        "| Sidebar section | Tab | Path | Required right |",
        "| --- | --- | --- | --- |",
    ]
    tab_count = 0
    for _sid, section_key, tabs in sections:
        for path, tab_key, perm, has_alias in tabs:
            name = const_to_name.get(perm, perm)
            suffix = " (also served at the bare alias)" if has_alias else ""
            sections_md.append(
                f"| {label(section_key)} | {label(tab_key)} | `{path}` | `{name}`{suffix} |"
            )
            tab_count += 1

    # -- permissions ---------------------------------------------------------
    # Grouped the way the operator-accounts screen groups them, so the doc and
    # the picker an operator actually clicks through read the same way.
    perms_md = ["| Permission | Grants |", "| --- | --- |"]
    seen: set[str] = set()
    _c2n, _lbl, groups, _all = parse_permissions()
    for title, _hint, members in groups:
        rows = []
        for name in members:
            if name not in assignable or name in seen:
                continue
            seen.add(name)
            rows.append(f"| `{name}` | {labels.get(name, '—')} |")
        if rows:
            perms_md.append(f"| **{title}** | |")
            perms_md.extend(rows)
    leftovers = [n for n in assignable if n not in seen]
    if leftovers:
        perms_md.append("| **Ungrouped** | |")
        perms_md.extend(f"| `{n}` | {labels.get(n, '—')} |" for n in leftovers)
    perms_md.append("| `*` | Every right, including ones added later (wildcard). |")

    return {
        "sections": "\n".join(sections_md),
        "permissions": "\n".join(perms_md),
        "assignable_count": str(len(assignable)),
        "permission_count": str(len(all_names)),
        "section_count": str(len(sections)),
        "tab_count": str(tab_count),
        "route_count": str(len(routes)),
        "unlabelled": ", ".join(n for n in assignable if n not in labels) or "none",
    }


BEGIN = "<!-- BEGIN GENERATED:{block} -->"
END = "<!-- END GENERATED:{block} -->"
NOTE = (
    "<!-- GENERATED by scripts/gen_admin_panel_doc_tables.py -- run it and paste"
    " the output, or use --check in CI. Edit nav.ts / permissions.tsx instead. -->"
)


def block(block_name: str, body: str) -> str:
    return f"{BEGIN.format(block=block_name)}\n{body}\n{END.format(block=block_name)}"


def splice(doc_path: pathlib.Path, block_name: str, body: str) -> bool:
    """Replace one generated block in a doc file. Returns True if it changed."""
    doc = doc_path.read_text(encoding="utf-8")
    start = doc.index(BEGIN.format(block=block_name))
    end = doc.index(END.format(block=block_name))
    updated = doc[:start] + block(block_name, body) + doc[end + len(END.format(block=block_name)) :]
    if updated == doc:
        return False
    doc_path.write_text(updated, encoding="utf-8")
    return True


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--check",
        action="store_true",
        help="exit 1 if any generated block in the docs is stale",
    )
    parser.add_argument(
        "--write",
        action="store_true",
        help="rewrite the generated blocks in the docs in place",
    )
    args = parser.parse_args()

    tables = render_tables()
    blocks = {
        "nav": tables["sections"],
        "permissions": tables["permissions"],
    }
    docs = [
        ROOT / "docs/admin-panel-api.en.md",
        ROOT / "docs/admin-panel-api.ru.md",
    ]

    if args.check:
        stale = []
        for doc_path in docs:
            if not doc_path.exists():
                continue
            doc = doc_path.read_text(encoding="utf-8")
            for name, body in blocks.items():
                begin = BEGIN.format(block=name)
                end = END.format(block=name)
                if begin not in doc or end not in doc:
                    continue
                # Compare the body *between* the markers: including the BEGIN
                # line on one side and the END line on the other would report
                # every file as stale.
                current = doc[doc.index(begin) + len(begin) : doc.index(end)]
                if current.strip() != body.strip():
                    stale.append(f"{doc_path.name}: {name}")
        if stale:
            print("stale generated tables:\n  " + "\n  ".join(stale), file=sys.stderr)
            print("\nrun: python3 scripts/gen_admin_panel_doc_tables.py --write", file=sys.stderr)
            return 1
        routes = parse_route_rights()
        gaps = [f"{d.name}: {p}" for d in docs if d.exists() for p in coverage_problems(d, routes)]
        if gaps:
            print("API tables out of step with server.go:\n  " + "\n  ".join(gaps), file=sys.stderr)
            print(
                "\nadd or remove the row (the description is prose, write it by hand), then run"
                " --write to fill in the Permission column",
                file=sys.stderr,
            )
            return 1
        misplaced = misplaced_section_tabs()
        if misplaced:
            print(
                "<SectionTabs> inside a PageFrame actions prop:\n  " + "\n  ".join(misplaced),
                file=sys.stderr,
            )
            print(
                "\nSectionTabs renders into the header action row there. Move it to be"
                " the first child of PageFrame instead.",
                file=sys.stderr,
            )
            return 1
        print("generated admin-panel doc tables are current")
        print("every server.go route has a row, with the right it enforces")
        print("section tab bars are placed as PageFrame children")
        return 0

    if args.write:
        changed = []
        for doc_path in docs:
            if not doc_path.exists():
                continue
            doc = doc_path.read_text(encoding="utf-8")
            if NOTE not in doc:
                continue
            for name, body in blocks.items():
                if BEGIN.format(block=name) in doc and splice(doc_path, name, body):
                    changed.append(f"{doc_path.name}: {name}")
            if rewrite_permission_cells(doc_path, parse_route_rights()):
                changed.append(f"{doc_path.name}: action permissions")
        print("rewrote:\n  " + "\n  ".join(changed) if changed else "nothing to rewrite")
        return 0

    for key in ("sections", "permissions"):
        print(f"### {key}\n")
        print(tables[key])
        print()
    print(
        f"// {tables['section_count']} sections, {tables['tab_count']} tabs, "
        f"{tables['route_count']} routes, {tables['assignable_count']} assignable rights, "
        f"{tables['permission_count']} labelled; unlabelled: {tables['unlabelled']}",
        file=sys.stderr,
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())