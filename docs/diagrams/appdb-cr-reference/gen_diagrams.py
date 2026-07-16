#!/usr/bin/env python3
"""Generate the three procedure diagrams for the AppDB CR-reference design doc."""
import json

SLATE = "#001E2B"
FOREST = "#00684A"
SPRING = "#00ED64"
MIST = "#E3FCF7"
LAVENDER = "#F9EBFF"
WHITE = "#FFFFFF"

_id = 0


def nid(prefix):
    global _id
    _id += 1
    return f"{prefix}{_id}"


def box(x, y, w, h, text, fill=MIST, stroke=FOREST, stroke_width=1, font_size=16):
    return {
        "type": "rectangle",
        "id": nid("r"),
        "x": x,
        "y": y,
        "width": w,
        "height": h,
        "roundness": {"type": 3},
        "backgroundColor": fill,
        "fillStyle": "solid",
        "strokeColor": stroke,
        "strokeWidth": stroke_width,
        "roughness": 0,
        "label": {"text": text, "fontSize": font_size, "fontFamily": 2, "textAlign": "center"},
    }


def diamond(x, y, w, h, text, fill=WHITE, stroke=SLATE, font_size=15):
    return {
        "type": "diamond",
        "id": nid("d"),
        "x": x,
        "y": y,
        "width": w,
        "height": h,
        "backgroundColor": fill,
        "fillStyle": "solid",
        "strokeColor": stroke,
        "strokeWidth": 1,
        "roughness": 0,
        "label": {"text": text, "fontSize": font_size, "fontFamily": 2, "textAlign": "center"},
    }


def arrow(x1, y1, x2, y2, label=None, color=SLATE, dash=None, font_size=14):
    el = {
        "type": "arrow",
        "id": nid("a"),
        "x": x1,
        "y": y1,
        "width": x2 - x1,
        "height": y2 - y1,
        "points": [[0, 0], [x2 - x1, y2 - y1]],
        "endArrowhead": "triangle",
        "strokeColor": color,
        "strokeWidth": 1,
        "roughness": 0,
    }
    if dash:
        el["strokeStyle"] = dash
    if label:
        el["label"] = {"text": label, "fontSize": font_size, "fontFamily": 2}
    return el


def text(x, y, s, size=20, color=SLATE, family=2):
    return {
        "type": "text",
        "id": nid("t"),
        "x": x,
        "y": y,
        "text": s,
        "fontSize": size,
        "fontFamily": family,
        "strokeColor": color,
    }


def cam(w, h, x=0, y=0):
    return {"type": "cameraUpdate", "width": w, "height": h, "x": x, "y": y}


def pad_anchor(x, y):
    """Invisible 1x1 rect that extends the fitted bounding box for padding,
    since screenshot fit=1.0 fits tightly to content bounds, not cameraUpdate."""
    return {
        "type": "rectangle",
        "id": nid("pad"),
        "x": x,
        "y": y,
        "width": 1,
        "height": 1,
        "strokeColor": "transparent",
        "backgroundColor": "transparent",
        "roughness": 0,
    }


# ---------------------------------------------------------------------------
# Diagram 1 — Procedure 1: Fresh start
# ---------------------------------------------------------------------------
d1 = [cam(1200, 900, -80, -80)]
d1.append(pad_anchor(-70, -90))
d1.append(text(60, -10, "Procedure 1 — Fresh Start", 24, FOREST))

X, W = 80, 420
ys = [60, 170, 280, 390, 500]
d1 += [
    box(X, ys[0], W, 80, "User creates MongoDB/MongoDBMulti CR\nspec.role: AppDB"),
    box(X, ys[1], W, 90, "Webhook validation:\nSCRAM + ignoreUnknownUsers +\nmembers >= 3 + version >= 4.0.0"),
    box(X, ys[2], W, 90, "StatefulSet ownership check:\nno StatefulSet exists -> no adoption gate"),
    box(
        X,
        ys[3],
        W,
        90,
        "Ensure mongodb-ops-manager user:\ngenerate password, create secret,\ninject user into automation config",
    ),
    box(
        X,
        ys[4],
        W,
        90,
        "Create StatefulSet (own OwnerReference).\nNo connection-string secret of its own\nis ever created by this CR.",
        fill=SPRING,
        stroke_width=1.5,
    ),
]
for i in range(len(ys) - 1):
    d1.append(arrow(X + W / 2, ys[i] + (80 if i == 0 else 90), X + W / 2, ys[i + 1]))

# Side branch — OM controller, order independent
SX, SW = 600, 420
d1.append(
    box(
        SX,
        ys[3],
        SW,
        200,
        "OM controller (order-independent):\nset externalApplicationDatabaseRef\n-> validate name == <om-name>-db\n-> validate role: AppDB + version >= 4.0.0\n-> fetch target CR, call BuildConnectionString()\n-> write result into Primary OM's own fixed secret\n-> establish watch on the CR itself",
        fill=LAVENDER,
    )
)
d1.append(arrow(X + W, ys[4] + 45, SX, ys[3] + 100, label="fetches", color=FOREST))
d1.append(pad_anchor(SX + SW + 40, ys[3] + 220))

with open("d1_fresh_start.json", "w") as f:
    json.dump(d1, f)

# ---------------------------------------------------------------------------
# Diagram 2 — Procedure 2: Forward migration (internal -> external)
# ---------------------------------------------------------------------------
_id = 0
d2 = [cam(1600, 1200, -80, -80)]
d2.append(pad_anchor(-70, -100))
d2.append(text(60, -20, "Procedure 2 — Forward Migration (internal AppDB -> external)", 22, FOREST))

d2.append(
    box(
        60,
        20,
        1400,
        60,
        "Trigger: user creates MongoDB CR named <om-name>-db, spec.role: AppDB\n+ sets spec.externalApplicationDatabaseRef on the OM CR (companion actions)",
        fill=LAVENDER,
    )
)

# Left lane: OM controller detach (host deregistration and password-copy
# steps removed from this design — see Naming convention / dropped-dereg notes)
OMX, OMW = 60, 620
oy = [130, 210, 290, 380, 470]
d2 += [
    box(OMX, oy[0], OMW, 60, "OM controller: skip ReconcileAppDB()"),
    box(OMX, oy[1], OMW, 60, "Skip SetupCommonWatchers for AppDB objects"),
    box(
        OMX,
        oy[2],
        OMW,
        80,
        "Validate reference: name == <om-name>-db,\nrole == AppDB, version >= 4.0.0\n(else fail fast)",
    ),
    box(OMX, oy[3], OMW, 70, "Strip OwnerReferences\n(STS, password secret, ConfigMaps)"),
    box(OMX, oy[4], OMW, 60, 'Annotate StatefulSet:\nappdb-migration-ready: "true"', fill=SPRING, stroke_width=1.5),
]
for i in range(len(oy) - 1):
    d2.append(arrow(OMX + OMW / 2, oy[i] + (80 if i == 2 else (70 if i == 3 else 60)), OMX + OMW / 2, oy[i + 1]))

# Shared StatefulSet node (middle column, aligned with column C's first box)
STX, STY, STW, STH = 760, 320, 220, 90
d2.append(box(STX, STY, STW, STH, "AppDB\nStatefulSet\n(shared)", fill=WHITE, stroke=SLATE, stroke_width=1.5))
d2.append(arrow(OMX + OMW, oy[3] + 35, STX, STY + 25, label="detach", color=FOREST))
d2.append(arrow(STX + 40, STY + STH, OMX + OMW + 40, oy[4] + 30, label="annotate", color=FOREST))

# Right lane: MongoDB controller — single vertical happy-path column;
# the blocked/retry loop lives entirely in a separate far-right margin lane
# so it never crosses the StatefulSet/adopt arrow in the middle column.
MX, MW = 1040, 460
my = [320, 440, 590, 690, 780]
d2.append(box(MX, my[0], MW, 70, "MongoDB controller reconciles:\nownership check -> foreign StatefulSet"))
d2.append(diamond(MX + MW / 2 - 170, my[1], 340, 110, "ready annotation set AND\nOM OwnerRef gone?"))
d2 += [
    box(MX, my[2], MW, 80, "Adopt: patch StatefulSet, set own\nOwnerReference, clear annotation"),
    box(
        MX,
        my[3],
        MW,
        100,
        "Ensure mongodb-ops-manager user:\npassword secret already exists -> reuse.\nNo connection-string secret of its own\nis ever created by this CR.",
        fill=SPRING,
        stroke_width=1.5,
    ),
]
d2.append(arrow(STX + STW, STY + STH / 2, MX, my[0] + 35, label="adopt?", color=FOREST))
d2.append(arrow(MX + MW / 2, my[0] + 70, MX + MW / 2, my[1]))
d2.append(arrow(MX + MW / 2, my[1] + 110, MX + MW / 2, my[2], label="yes", color=FOREST))
d2.append(arrow(MX + MW / 2, my[2] + 80, MX + MW / 2, my[3]))

# Far-right margin lane: blocked/retry loop, fully isolated from the middle column
BX = MX + MW + 100
d2.append(arrow(MX + MW, my[1] + 55, BX, my[1] + 55, label="no", color=SLATE, dash="dashed"))
d2.append(box(BX, my[1] + 20, 260, 70, "Blocked: report waiting\nstatus, requeue", fill=WHITE, stroke=SLATE))
d2.append(arrow(BX + 130, my[1] + 20, BX + 130, my[0] + 35, color=SLATE, dash="dashed"))
d2.append(arrow(BX + 130, my[0] + 35, MX + MW, my[0] + 35, label="retry", color=SLATE, dash="dashed"))

# Bottom: OM fetches the CR and computes the connection string directly
d2.append(
    box(
        400,
        890,
        640,
        100,
        "OM controller fetches the MongoDB CR, calls BuildConnectionString()\ndirectly, writes result into Primary OM's own FIXED secret.\nRestart depends on the value changing (connectionStringHash).\nEstablishes a watch on the CR itself for future changes.",
        fill=LAVENDER,
    )
)
d2.append(arrow(MX + 100, my[3] + 100, 720, 890, color=FOREST))
d2.append(pad_anchor(BX + 320, 1020))

with open("d2_forward_migration.json", "w") as f:
    json.dump(d2, f)

# ---------------------------------------------------------------------------
# Diagram 3 — Procedure 3: Reverse migration (external -> internal)
# ---------------------------------------------------------------------------
_id = 0
d3 = [cam(1600, 1200, -80, -80)]
d3.append(pad_anchor(-70, -100))
d3.append(text(60, -20, "Procedure 3 — Reverse Migration (external AppDB -> internal)", 22, FOREST))

d3.append(
    box(
        60,
        20,
        1400,
        70,
        "Trigger: user deletes the MongoDB CR + removes spec.externalApplicationDatabaseRef\nfrom the OM CR (companion actions). CR has mongodb.com/appdb-detach finalizer,\nso deletionTimestamp is set but the object stays in etcd until finalizer cleanup completes",
        fill=LAVENDER,
    )
)

# Left lane: MongoDB controller finalizer cleanup (mirrors OM's role in Procedure 2).
# Password copy-back step removed — see Naming convention (single shared secret, nothing to copy).
MX2, MW2 = 60, 620
my2 = [140, 230, 320]
d3 += [
    box(MX2, my2[0], MW2, 60, "Finalizer blocks deletion:\nfinalizer cleanup begins"),
    box(MX2, my2[1], MW2, 70, "Strip own OwnerReference\nfrom the StatefulSet"),
    box(MX2, my2[2], MW2, 60, 'Annotate StatefulSet:\nappdb-migration-ready: "true"', fill=SPRING, stroke_width=1.5),
]
for i in range(len(my2) - 1):
    d3.append(arrow(MX2 + MW2 / 2, my2[i] + (60 if i == 0 else 70), MX2 + MW2 / 2, my2[i + 1]))

d3.append(
    box(MX2, my2[2] + 80, MW2, 60, "Remove finalizer -> MongoDB CR\ndeletion completes", fill=SPRING, stroke_width=1.5)
)
d3.append(arrow(MX2 + MW2 / 2, my2[2] + 60, MX2 + MW2 / 2, my2[2] + 80))
d3.append(
    box(
        MX2,
        my2[2] + 160,
        MW2,
        60,
        "No connection-string secret to clean up (this CR\nnever had one); password was never a separate copy",
        fill=WHITE,
        stroke=SLATE,
    )
)
d3.append(arrow(MX2 + MW2 / 2, my2[2] + 140, MX2 + MW2 / 2, my2[2] + 160))

# Shared StatefulSet node (middle column, aligned with column C's first box)
STX2, STY2, STW2, STH2 = 760, 320, 220, 90
d3.append(box(STX2, STY2, STW2, STH2, "AppDB\nStatefulSet\n(shared)", fill=WHITE, stroke=SLATE, stroke_width=1.5))
d3.append(arrow(MX2 + MW2, my2[1] + 35, STX2, STY2 + 25, label="detach", color=FOREST))
d3.append(arrow(STX2 + 40, STY2 + STH2, MX2 + MW2 + 40, my2[2] + 30, label="annotate", color=FOREST))

# Right lane: OM controller re-adopts — single vertical happy-path column;
# the blocked/retry loop lives in a separate far-right margin lane.
OX, OW = 1040, 460
oy2 = [320, 440, 570, 670, 760]
d3.append(box(OX, oy2[0], OW, 70, "OM controller sees\nexternalApplicationDatabaseRef removed"))
d3.append(diamond(OX + OW / 2 - 150, oy2[1], 300, 90, "migration-ready\nannotation present?"))
d3 += [
    box(
        OX,
        oy2[2],
        OW,
        90,
        "Set own OwnerReference back on STS,\nclear annotation, resume ReconcileAppDB()\n+ SetupCommonWatchers, tear down watch\non the (now-deleted) external CR",
    ),
    box(
        OX,
        oy2[3],
        OW,
        90,
        "Read password via existing ensureAppDbPassword\nlogic -> same shared secret, no rotation.\nConnection string now computed the internal way\nagain (buildMongoConnectionUrl).",
        fill=SPRING,
        stroke_width=1.5,
    ),
]
d3.append(arrow(STX2 + STW2, STY2 + STH2 / 2, OX, oy2[0] + 35, label="re-adopt?", color=FOREST))
d3.append(arrow(OX + OW / 2, oy2[0] + 70, OX + OW / 2, oy2[1]))
d3.append(arrow(OX + OW / 2, oy2[1] + 90, OX + OW / 2, oy2[2], label="yes", color=FOREST))
d3.append(arrow(OX + OW / 2, oy2[2] + 90, OX + OW / 2, oy2[3]))

# Far-right margin lane: blocked/retry loop, fully isolated from the middle column
BX2 = OX + OW + 100
d3.append(arrow(OX + OW, oy2[1] + 45, BX2, oy2[1] + 45, label="no", color=SLATE, dash="dashed"))
d3.append(box(BX2, oy2[1] + 10, 260, 70, "Blocked: keep skipping\nReconcileAppDB, requeue", fill=WHITE, stroke=SLATE))
d3.append(arrow(BX2 + 130, oy2[1] + 10, BX2 + 130, oy2[0] + 35, color=SLATE, dash="dashed"))
d3.append(arrow(BX2 + 130, oy2[0] + 35, OX + OW, oy2[0] + 35, label="retry", color=SLATE, dash="dashed"))

d3.append(
    box(
        400,
        890,
        640,
        60,
        "Internal AppDB management resumes; StatefulSet pod\ntemplate rewritten back to internal AppDB's shape",
        fill=LAVENDER,
    )
)
d3.append(arrow(OX + 100, oy2[3] + 90, 720, 890, color=FOREST))
d3.append(pad_anchor(BX2 + 320, 1010))

with open("d3_reverse_migration.json", "w") as f:
    json.dump(d3, f)

print("wrote d1_fresh_start.json, d2_forward_migration.json, d3_reverse_migration.json")
