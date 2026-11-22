"""The logo's box and lid in 3D, animated, and each frame of the lid as outlines on the page.

    blender -b --python docs/logo/scene.py

builds the scene (docs/logo/schuhkarton.blend, open it to look at it or change
the animation) and writes docs/logo/lid.json, the lid frame by frame as the
page shows it; generate.py makes the logo and the loading animation from it.

The box, the photos, the star and the rays stay the drawing of the logo; the
scene has the box and the photos only to see the lid with them. The camera
looks at the box the way the drawing does (orthographic, 25 degrees from
above, one unit a pixel), so the page coordinates of the drawing and of the
scene are the same. The lid is a rounded slab with a lip and a recess; its
open pose was fitted to the lid of the original drawing (lifted, tilted to
the back and turned to the left), so it still comes in from the side.

Each frame of the lid becomes three outlines on the page: the silhouette
(the sides), the top face while it faces the viewer and the inside while
that does, each the convex hull of its points (the lid is convex, its top
face and its recess too). Whether the lid is drawn in front of the box or
behind the photos follows from where it is: behind once it is lifted above
the box's rim.
"""
import json
import math
import pathlib

import bpy
import bmesh
from bpy_extras.object_utils import world_to_camera_view
from mathutils import Euler, Matrix, Quaternion, Vector
from mathutils.geometry import convex_hull_2d

HERE = pathlib.Path(__file__).resolve().parent

# --- the page and the camera ---------------------------------------------------
VIEWBOX = (260, 155, 720)          # x, y, size of the drawing's viewBox
ELEV = math.radians(25)            # the rim, 237 deep, is 100 px tall on the page
ORIGIN = (623.0, 576.0)            # the middle of the box's opening on the page
RIM_TOP = 526                      # the back of the rim on the page: a lid above it is behind the photos

# --- the box (as drawn: 564 wide, its opening 237 deep, 213 high) -------------------
BOX_W, BOX_D, BOX_H, BOX_R = 564.0, 237.0, 213.0, 60.0

# --- the lid ------------------------------------------------------------------------
LID_W, LID_D, LID_R = 590.0, 252.0, 105.0  # footprint: a rounded rectangle, ends nearly round
LID_T = 46.0       # from the lip's lower edge to the top face
LID_BEVEL = 12.0   # the rounded edge of the top face
LID_WALL = 22.0    # the lip's thickness
LID_RECESS = 16.0  # the inside's floor below the top face
LID_N = 8          # points of a quarter circle
# closed: on the box, the lip over the rim; open: fitted to the drawing's lid
CLOSED = ((0.0, 0.0, -30.0), (0.0, 0.0, 0.0))
OPEN = ((-75.48, -12.4, 225.39), (-62.924, 0.218, 37.912))

# --- time (30 frames a second, the loop 3.2 s) ----------------------------------------
FPS, FRAMES = 30, 96
OPEN_AT, OPENED = 5, 20        # the lid swings open, a little past and back
PHOTOS_UP = (16, 28)           # the photos rise once the lid is up ...
PHOTOS_DOWN = (39, 49)         # ... and are back in the box ...
CLOSE_AT, CLOSED_AT = 51, 65   # ... before the lid swings shut,
HOP, HOP_DONE = 68, 71         # hops a little as it lands and settles
HOP_POSE = ((0.0, 4.0, -22.0), (-5.0, 0.0, 0.0))  # its front a hand's breadth up
PHOTO_RISE = 287               # how far the photos rise (in the drawing: 260 px on the page)


def clear():
    bpy.ops.wm.read_factory_settings(use_empty=True)


def material(name, rgb):
    m = bpy.data.materials.new(name)
    m.diffuse_color = (*rgb, 1)
    bsdf = m.node_tree.nodes.get("Principled BSDF") if m.node_tree else None
    if bsdf:
        bsdf.inputs["Base Color"].default_value = (*rgb, 1)
    return m


def ring(w, d, r, z, n=LID_N):
    pts = []
    for cx, cy, a0 in [(w / 2 - r, d / 2 - r, 0), (-w / 2 + r, d / 2 - r, 90), (-w / 2 + r, -d / 2 + r, 180), (w / 2 - r, -d / 2 + r, 270)]:
        for k in range(n + 1):
            a = math.radians(a0 + 90 * k / n)
            pts.append(Vector((cx + r * math.cos(a), cy + r * math.sin(a), z)))
    return pts


def lid_rings():
    """Top face, its rounded edge, outer and inner lower edge, the inside's floor (local:
    the middle of the footprint, z up from the lip's lower edge)."""
    return {
        "top": ring(LID_W - 2 * LID_BEVEL, LID_D - 2 * LID_BEVEL, LID_R - LID_BEVEL, LID_T),
        "edge": ring(LID_W, LID_D, LID_R, LID_T - LID_BEVEL),
        "outer": ring(LID_W, LID_D, LID_R, 0),
        "inner": ring(LID_W - 2 * LID_WALL, LID_D - 2 * LID_WALL, LID_R - LID_WALL, 0),
        "floor": ring(LID_W - 2 * LID_WALL, LID_D - 2 * LID_WALL, LID_R - LID_WALL, LID_T - LID_RECESS),
    }


def make_lid(mat_top, mat_inside):
    rings = lid_rings()
    mesh = bpy.data.meshes.new("Lid")
    bm = bmesh.new()
    vs = {k: [bm.verts.new(p) for p in v] for k, v in rings.items()}
    n = len(vs["top"])

    def band(a, b, mat):
        for i in range(n):
            j = (i + 1) % n
            bm.faces.new((vs[a][i], vs[a][j], vs[b][j], vs[b][i])).material_index = mat

    bm.faces.new(vs["top"]).material_index = 0
    band("edge", "top", 0)
    band("outer", "edge", 0)
    band("inner", "outer", 0)
    band("floor", "inner", 1)
    bm.faces.new(list(reversed(vs["floor"]))).material_index = 1
    bm.normal_update()
    bm.to_mesh(mesh)
    bm.free()
    mesh.materials.append(mat_top)
    mesh.materials.append(mat_inside)
    ob = bpy.data.objects.new("Lid", mesh)
    bpy.context.collection.objects.link(ob)
    ob.rotation_mode = "QUATERNION"
    return ob


def make_box(mat_box, mat_rim, mat_inside):
    """The box as drawn, for looking at the lid with it: a rounded tray, its opening at z 0."""
    bpy.ops.mesh.primitive_cube_add(size=1, location=(0, 0, -BOX_H / 2))
    box = bpy.context.object
    box.name = "Box"
    box.scale = (BOX_W, BOX_D, BOX_H)
    bpy.ops.object.transform_apply(scale=True)
    bev = box.modifiers.new("round", "BEVEL")
    bev.width, bev.segments = BOX_R, 8
    sol = box.modifiers.new("hollow", "SOLIDIFY")
    box.data.materials.append(mat_box)
    # the opening: the top face away, the walls thickened inwards
    bm = bmesh.new()
    bm.from_mesh(box.data)
    top = [f for f in bm.faces if f.normal.z > 0.9]
    bmesh.ops.delete(bm, geom=top, context="FACES")
    bm.to_mesh(box.data)
    bm.free()
    sol.thickness, sol.offset = 16, -1
    sol.material_offset_rim = 1
    sol.material_offset = 2
    box.data.materials.append(mat_rim)
    box.data.materials.append(mat_inside)
    return box


def make_photo(name, w, h, x, z, tilt, mat):
    bpy.ops.mesh.primitive_plane_add(size=1, location=(x, 20, z))
    p = bpy.context.object
    p.name = name
    p.scale = (w, h, 1)  # the plane lies in x, y; turned upright below
    p.rotation_euler = (math.radians(90), math.radians(tilt), 0)
    p.data.materials.append(mat)
    return p


def make_camera():
    cam_data = bpy.data.cameras.new("Camera")
    cam_data.type = "ORTHO"
    cam_data.ortho_scale = VIEWBOX[2]
    cam = bpy.data.objects.new("Camera", cam_data)
    bpy.context.collection.objects.link(cam)
    # looking along +y, tilted down by ELEV
    rot = Euler((math.pi / 2 - ELEV, 0, 0))
    cam.rotation_euler = rot
    right, up, fwd = (rot.to_matrix() @ Vector(v) for v in ((1, 0, 0), (0, 1, 0), (0, 0, -1)))
    # the middle of the viewBox is where the camera looks: the box's opening at ORIGIN
    mx, my = VIEWBOX[0] + VIEWBOX[2] / 2, VIEWBOX[1] + VIEWBOX[2] / 2
    cam.location = right * (mx - ORIGIN[0]) + up * (ORIGIN[1] - my) - fwd * 2000
    cam_data.clip_end = 5000
    scene = bpy.context.scene
    scene.camera = cam
    scene.render.resolution_x = scene.render.resolution_y = VIEWBOX[2]
    bpy.context.view_layer.update()
    return cam


def to_page(scene, cam, p):
    v = world_to_camera_view(scene, cam, p)
    return (VIEWBOX[0] + v.x * VIEWBOX[2], VIEWBOX[1] + (1 - v.y) * VIEWBOX[2])


def key(ob, frame, pose):
    loc, rot = pose
    ob.location = loc
    ob.rotation_quaternion = Euler([math.radians(a) for a in rot], "XYZ").to_quaternion()
    ob.keyframe_insert("location", frame=frame)
    ob.keyframe_insert("rotation_quaternion", frame=frame)


def set_interp(ob, frame, interp, easing):
    for fc in fcurves(ob):
        for kp in fc.keyframe_points:
            if abs(kp.co.x - frame) < 1e-6:
                kp.interpolation, kp.easing = interp, easing


def fcurves(ob):
    act = ob.animation_data.action
    if hasattr(act, "fcurves"):
        return list(act.fcurves)
    out = []  # layered actions (Blender 4.4 on)
    for layer in act.layers:
        for strip in layer.strips:
            for bag in strip.channelbags:
                out += list(bag.fcurves)
    return out


def animate(lid, photos):
    scene = bpy.context.scene
    scene.render.fps = FPS
    scene.frame_start, scene.frame_end = 0, FRAMES
    key(lid, 0, CLOSED)
    key(lid, OPEN_AT, CLOSED)
    key(lid, OPENED, OPEN)
    key(lid, CLOSE_AT, OPEN)
    key(lid, CLOSED_AT, CLOSED)
    key(lid, HOP, HOP_POSE)
    key(lid, HOP_DONE, CLOSED)
    key(lid, FRAMES, CLOSED)
    set_interp(lid, 0, "CONSTANT", "AUTO")
    set_interp(lid, OPEN_AT, "BACK", "EASE_OUT")   # past open and back
    set_interp(lid, OPENED, "CONSTANT", "AUTO")
    set_interp(lid, CLOSE_AT, "CUBIC", "EASE_IN")    # falls shut
    set_interp(lid, CLOSED_AT, "SINE", "EASE_OUT")   # the hop up ...
    set_interp(lid, HOP, "SINE", "EASE_IN")          # ... and down
    set_interp(lid, HOP_DONE, "CONSTANT", "AUTO")
    for ob in photos:
        z = ob.location.z
        for f, dz in ((0, -PHOTO_RISE), (PHOTOS_UP[0], -PHOTO_RISE), (PHOTOS_UP[1], 0), (PHOTOS_DOWN[0], 0), (PHOTOS_DOWN[1], -PHOTO_RISE), (FRAMES, -PHOTO_RISE)):
            ob.location.z = z + dz
            ob.keyframe_insert("location", index=2, frame=f)


def lid_frame(scene, cam, lid):
    """The lid of the current frame as outlines on the page."""
    mw = lid.matrix_world
    rings = {k: [mw @ p for p in v] for k, v in lid_rings().items()}
    page = {k: [to_page(scene, cam, p) for p in v] for k, v in rings.items()}

    def hull(pts):
        idx = convex_hull_2d(pts)
        return [[round(pts[i][0], 2), round(pts[i][1], 2)] for i in idx]

    # which way the top face looks: towards the camera (seen) or away (the inside seen)
    normal = (mw.to_3x3() @ Vector((0, 0, 1))).normalized()
    towards = (cam.matrix_world.to_3x3() @ Vector((0, 0, 1))).normalized()
    facing = normal.dot(towards)
    allpts = [p for v in page.values() for p in v]
    sil = hull(allpts)
    # an anchor for each outline: where the first point of a ring lands, so the outlines of
    # one frame and the next start at the same place of the lid
    return {
        "sil": sil, "silAnchor": page["outer"][0],
        "top": hull(page["top"] + page["edge"]) if facing > 0 else None, "topAnchor": page["edge"][0],
        "inside": hull(page["inner"]) if facing < 0 else None, "insideAnchor": page["inner"][0],
        "facing": round(facing, 4),
        # behind the photos once lifted above the box's rim
        "behind": max(p[1] for p in allpts) <= RIM_TOP + 2,
    }


def main():
    clear()
    scene = bpy.context.scene
    lid_top = material("lidTop", (0.55, 0.96, 0.92))
    lid_inside = material("lidInside", (0.46, 0.57, 0.93))
    box = make_box(material("box", (0.70, 0.95, 0.98)), material("rim", (0.55, 0.82, 0.94)), material("interior", (0.44, 0.56, 0.93)))
    lid = make_lid(lid_top, lid_inside)
    paper = material("paper", (1, 0.99, 0.98))
    photos = [make_photo("Landscape", 281, 307, -71, 70, -12, paper), make_photo("Heart", 180, 255, 148, 40, 12, paper)]
    cam = make_camera()
    sun = bpy.data.objects.new("Sun", bpy.data.lights.new("Sun", "SUN"))
    sun.rotation_euler = (math.radians(40), math.radians(-20), math.radians(10))
    bpy.context.collection.objects.link(sun)
    animate(lid, photos)

    # the camera and the drawing agree: the box's opening lands at ORIGIN
    o = to_page(scene, cam, Vector((0, 0, 0)))
    assert abs(o[0] - ORIGIN[0]) < 0.5 and abs(o[1] - ORIGIN[1]) < 0.5, o
    back = to_page(scene, cam, Vector((0, BOX_D / 2, 0)))
    assert abs(back[1] - RIM_TOP) < 1, back

    frames = []
    for f in range(FRAMES + 1):
        scene.frame_set(f)
        frames.append({"frame": f, **lid_frame(scene, cam, lid)})
    out = {
        "fps": FPS, "frames": FRAMES, "open": OPENED + 5,
        "photos": {"up": PHOTOS_UP, "down": PHOTOS_DOWN},
        "lid": frames,
    }
    (HERE / "lid.json").write_text(json.dumps(out, separators=(",", ":")) + "\n")
    scene.frame_set(OPENED + 5)
    bpy.ops.wm.save_as_mainfile(filepath=str(HERE / "schuhkarton.blend"), compress=True)
    print(f"lid.json: {len(frames)} frames")


main()
