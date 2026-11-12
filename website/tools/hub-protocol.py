#!/usr/bin/env python3
"""Draws the Herakles hub enrollment and connection protocol as SVG.

Usage: python3 hub-protocol.py out.svg, then render it in a browser to
PNG/WebP for website/site/img/hub-protocol.webp (1500 px wide).
"""
import html
import sys

W = 1500
X = {"admin": 250, "hub": 760, "server": 1230}
NAMES = {
    "admin": ("Administrator", "browser on the hub's web UI"),
    "hub": ("Hub", "herakles daemon + web interface"),
    "server": ("Server", "herakles daemon (agent)"),
}
C = {
    "ink": "#1d2330", "muted": "#5d6578", "line": "#c9ced8", "accent": "#c8941a",
    "blue": "#2f6fde", "green": "#1f9d55", "violet": "#7c4dcc", "bg": "#ffffff",
    "band": ["#fbf6ea", "#eef4fd", "#eefaf3", "#f4effc"],
    "bandEdge": ["#ecd9a8", "#c5d8f6", "#bfe5cf", "#d7c8f0"],
}

out = []
y = 150


def esc(s):
    return html.escape(s, quote=True)


def text(x, y, s, size=15, weight=400, color=None, anchor="middle", mono=False, italic=False):
    fam = "JetBrains Mono, DejaVu Sans Mono, monospace" if mono else "Inter, DejaVu Sans, Helvetica, Arial, sans-serif"
    st = ' font-style="italic"' if italic else ""
    out.append(f'<text x="{x}" y="{y}" font-family="{fam}" font-size="{size}" font-weight="{weight}" '
               f'fill="{color or C["ink"]}" text-anchor="{anchor}"{st}>{esc(s)}</text>')


def phase(n, title, sub, color_i):
    global y
    y += 18
    out.append(("BAND", y, color_i, n, title, sub))
    y += 92


def end_phase():
    global y
    y += 14
    out.append(("ENDBAND", y))


def msg(a, b, lines, color="blue", dashed=False, mono_from=1):
    """Arrow from a to b with label lines above it (lines[0] bold)."""
    global y
    x1, x2 = X[a], X[b]
    lh = 19
    top = y
    mid = (x1 + x2) / 2
    for i, ln in enumerate(lines):
        mono = i >= mono_from
        text(mid, top + i * lh, ln, size=15 if i == 0 else 13, weight=600 if i == 0 else 400,
             color=C["ink"] if i == 0 else C["muted"], mono=mono)
    ay = top + len(lines) * lh - 6
    d = ' stroke-dasharray="7 5"' if dashed else ""
    col = C[color]
    out.append(f'<line x1="{x1}" y1="{ay}" x2="{x2 - (12 if x2 > x1 else -12)}" y2="{ay}" stroke="{col}" stroke-width="2.4"{d}/>')
    s = 1 if x2 > x1 else -1
    out.append(f'<path d="M{x2},{ay} l{-14 * s},-7 l0,14 z" fill="{col}"/>')
    y = ay + 30


def note(at, lines, color="accent", width=470):
    """A box on a lifeline: a step one party does on its own."""
    global y
    lh = 19
    h = 18 + len(lines) * lh
    x = min(max(X[at] - width / 2, 44), W - 44 - width)
    col = C[color]
    out.append(f'<rect x="{x}" y="{y}" width="{width}" height="{h}" rx="9" fill="#fff" stroke="{col}" stroke-width="1.6"/>')
    out.append(f'<rect x="{x}" y="{y}" width="6" height="{h}" rx="3" fill="{col}"/>')
    for i, ln in enumerate(lines):
        mono = ln.startswith("`")
        ln = ln.strip("`")
        text(x + 18, y + 22 + i * lh, ln, size=13.5 if i else 14.5, weight=600 if i == 0 else 400,
             color=C["ink"] if i == 0 else C["muted"], anchor="start", mono=mono)
    y += h + 22


# ---------------------------------------------------------------- content
phase("1", "Enrollment code", "the administrator asks the hub for a code", 0)
msg("admin", "hub", ["“Enroll a server” (name: web-1)", "POST /api/hub/enroll  — logged-in administrator"])
note("hub", ["Hub creates the one-time secret",
             "s = 160 random bits; stores only SHA-256(s), valid 24 h",
             "identity key K_hub (Ed25519, made once, 0600)",
             "TLS pin = SHA-256 of its certificate's key (own TLS only)"])
msg("hub", "admin", ["Enrollment code", "HKE1-base64url( {URL, s, K_hub.pub, pin, name, expiry} + CRC-32 )"], color="accent")
msg("admin", "server", ["Out of band: SSH, console, deployment tool", "sudo herakles hub join HKE1-…"], color="muted", dashed=True)
end_phase()

phase("2", "Enrollment", "the server proves the secret; the hub proves its identity", 1)
note("server", ["Server checks the code",
                "CRC-32 and expiry; makes its own key pair K_srv (Ed25519)",
                "and a random nonce n (256 bit)"], color="blue")
msg("server", "hub", ["POST /hub/join   (HTTPS: system CA, ca_cert or the pin)", "{ s, K_srv.pub, n, hello: host, OS, version }"])
note("hub", ["Hub accepts once",
             "SHA-256(s) known, unused, not expired → delete it",
             "register server {id, name, K_srv.pub} — no password stored"])
msg("hub", "server", ["{ id, name, signature }", "sig = Sign_K_hub(\"herakles-join-v1\" ‖ n ‖ K_srv.pub ‖ id ‖ name)"], color="green")
note("server", ["Server verifies with K_hub from the code",
                "wrong key → refuses: \"someone may be in between\"",
                "`hub-agent.json (0600): id, K_srv private, K_hub, pin"], color="blue")
end_phase()

phase("3", "Connection", "on every start and reconnect; the server dials out, so no open port is needed", 2)
msg("server", "hub", ["GET /hub/connect   (WebSocket upgrade)",
                      "X-Herakles-Auth: id . t . n′ . Sign_K_srv(\"herakles-connect-v1\" ‖ id ‖ t ‖ n′ ‖ K_hub.pub)"])
note("hub", ["Hub checks the login",
             "signature with the stored K_srv.pub",
             "clock |t − now| ≤ 5 min; nonce n′ not seen before (replay)",
             "server removed? → 401, the server retries every 10 min"])
msg("hub", "server", ["101 Switching Protocols",
                      "X-Herakles-Hub-Sig: Sign_K_hub(\"herakles-hub-v1\" ‖ id ‖ n′)"], color="green")
note("server", ["Server verifies the hub's signature",
                "then multiplexes streams over the WebSocket (yamux)",
                "keep-alive every 20 s; reconnects with back-off"], color="blue")
end_phase()

phase("4", "Operation", "the hub opens streams; each carries one ordinary HTTP request", 3)
msg("hub", "server", ["GET /hub/hello", "→ host name, OS, architecture, version, container"], color="violet")
msg("hub", "server", ["PUT /hub/repositories  (on connect and after every config change)",
                      "shared repositories with resolved passwords and key files"], color="violet")
note("server", ["Server stores them",
                "secrets in state/hub/repositories/*.pw (0600)",
                "config marks them hub: true; maintenance stays on the hub"], color="violet")
msg("admin", "hub", ["Wizard, disks, jobs, snapshots of web-1",
                     "/api/hub/servers/{id}/api/…   (hub login and role)"], color="blue")
msg("hub", "server", ["Forwarded GUI API request", "/api/…  + role/user headers, no cookies"], color="violet")
msg("server", "admin", ["Answer, back through the hub", "sandboxed (CSP sandbox, no HTML) — a server cannot script the hub"],
    color="violet", dashed=True)
end_phase()

H = y + 40

# ---------------------------------------------------------------- render
svg = [f'<svg xmlns="http://www.w3.org/2000/svg" width="{W}" height="{H}" viewBox="0 0 {W} {H}">',
       f'<rect width="{W}" height="{H}" fill="{C["bg"]}"/>']
svg.append(f'<text x="40" y="52" font-family="Inter, DejaVu Sans, sans-serif" font-size="28" font-weight="700" fill="{C["ink"]}">'
           'Herakles hub — enrolling and connecting a server</text>')
svg.append(f'<text x="40" y="82" font-family="Inter, DejaVu Sans, sans-serif" font-size="15" fill="{C["muted"]}">'
           'One code carries everything; no reusable password crosses the network; each side proves its identity with an Ed25519 key.</text>')

# bands first (behind everything)
bands = []
start = None
for item in out:
    if isinstance(item, tuple) and item[0] == "BAND":
        start = item
    elif isinstance(item, tuple) and item[0] == "ENDBAND":
        bands.append((start, item[1]))
for (b, yend) in bands:
    _, by, ci, n, title, sub = b
    svg.append(f'<rect x="24" y="{by}" width="{W - 48}" height="{yend - by}" rx="14" fill="{C["band"][ci]}" stroke="{C["bandEdge"][ci]}"/>')
titles = []
for (b, yend) in bands:
    _, by, ci, n, title, sub = b
    titles.append(f'<rect x="36" y="{by + 8}" width="{max(len(title) * 11, len(sub) * 7.2) + 70}" height="58" rx="10" fill="{C["band"][ci]}"/>')
    titles.append(f'<circle cx="58" cy="{by + 30}" r="16" fill="{C["bandEdge"][ci]}"/>')
    titles.append(f'<text x="58" y="{by + 36}" font-family="Inter, DejaVu Sans, sans-serif" font-size="16" font-weight="700" fill="{C["ink"]}" text-anchor="middle">{n}</text>')
    titles.append(f'<text x="86" y="{by + 36}" font-family="Inter, DejaVu Sans, sans-serif" font-size="18" font-weight="700" fill="{C["ink"]}">{esc(title)}</text>')
    titles.append(f'<text x="86" y="{by + 56}" font-family="Inter, DejaVu Sans, sans-serif" font-size="13.5" fill="{C["muted"]}">{esc(sub)}</text>')

# lifelines and heads
for k, x in X.items():
    svg.append(f'<line x1="{x}" y1="140" x2="{x}" y2="{H - 30}" stroke="{C["line"]}" stroke-width="2" stroke-dasharray="3 6"/>')
for k, x in X.items():
    name, sub = NAMES[k]
    svg.append(f'<rect x="{x - 150}" y="100" width="300" height="58" rx="12" fill="{C["ink"]}"/>')
    svg.append(f'<text x="{x}" y="125" font-family="Inter, DejaVu Sans, sans-serif" font-size="17" font-weight="700" fill="#fff" text-anchor="middle">{name}</text>')
    svg.append(f'<text x="{x}" y="146" font-family="Inter, DejaVu Sans, sans-serif" font-size="12.5" fill="#c6cbd6" text-anchor="middle">{sub}</text>')

svg.extend(titles)
for item in out:
    if isinstance(item, str):
        svg.append(item)
svg.append("</svg>")
open(sys.argv[1], "w").write("\n".join(svg))
print(W, H)
