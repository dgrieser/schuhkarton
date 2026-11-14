"""schuhkarton helper: finds faces (OpenCV YuNet + SFace) and decodes images with Pillow.

Protocol: one JSON request per line on stdin, one JSON response per line on
stdout. The first line written is a hello with the available features.
Requires Pillow; pillow_heif for HEIC/AVIF; OpenCV (cv2) for faces.

Optional second system (when its model and onnxruntime are there): AuraFace
gives every face a second embedding ("emb2", 512-d), computed in parallel
with SFace. Asked for "extra", a second search returns the faces the main
search left out, marked "extra": YuNet down to 0.6 confirmed by RetinaFace
(mobilenet 0.25) at 0.9, or RetinaFace alone at 0.95 (without RetinaFace:
YuNet at 0.75). On 250 family photos these were about 86 % real faces. The
gallery keeps them aside and uses them only where they match a known person.
Asked with a "box" (a face drawn by hand) that no search found, the box
itself is embedded ("src": "box").
"""
import base64
import json
import os
import sys
from concurrent.futures import ThreadPoolExecutor

MISSING = []
VERSIONS = {}
HEIC = False
try:
    from PIL import Image, ImageOps
    import PIL
    VERSIONS['pillow'] = PIL.__version__
except Exception:  # pragma: no cover - reported in the hello
    MISSING.append('pillow')
try:
    import pillow_heif
    pillow_heif.register_heif_opener()
    HEIC = True
    VERSIONS['pillow_heif'] = pillow_heif.__version__
except Exception:  # pragma: no cover - optional
    MISSING.append('pillow_heif')

cv2 = np = det = rec = aura = retina = None
MIN_SCORE = 0.86
# the second search: what it may find besides the main one
EXTRA_SCORE = 0.6     # YuNet, when RetinaFace confirms it
EXTRA_ALONE = 0.75    # YuNet without RetinaFace in the models
RETINA_CONFIRM = 0.9  # RetinaFace confirming YuNet (IoU >= 0.5)
RETINA_ALONE = 0.95   # RetinaFace on its own
EXTRA_MIN = 20        # px, short side at the 1600 px scale
POOL = None           # SFace and AuraFace side by side
PRIORS = {}
# where the five points of a face lie in the 112x112 ArcFace crop
ARC = [(38.2946, 51.6963), (73.5318, 51.5014), (56.0252, 71.7366), (41.5493, 92.3655), (70.7299, 92.2041)]


def find_model(dirs, names):
    for d in dirs:
        for n in names:
            p = os.path.join(d, n)
            if os.path.isfile(p):
                return p
    return None


def load_models(dirs, with_aura=True, only_aura=False):
    global cv2, np, det, rec
    try:
        import numpy as _np
        VERSIONS['numpy'] = _np.__version__
    except Exception:
        MISSING.append('numpy')
        return
    try:
        import cv2 as _cv2
        VERSIONS['opencv'] = _cv2.__version__
    except Exception:
        MISSING.append('opencv')
        return
    cv2, np = _cv2, _np
    cv2.setNumThreads(1)
    if only_aura:  # a process of the second system's pool: that model alone
        a = find_model(dirs, ['auraface.onnx', 'glintr100.onnx'])
        if a:
            load_aura(a)
        else:
            MISSING.append('auraface.onnx')
        return
    y = find_model(dirs, ['face_detection_yunet_2023mar.onnx', 'yunet.onnx'])
    s = find_model(dirs, ['face_recognition_sface_2021dec.onnx', 'sface.onnx'])
    if y:
        det = cv2.FaceDetectorYN.create(y, '', (320, 320), 0.8, 0.3, 5000)
    else:
        MISSING.append('face_detection_yunet_2023mar.onnx')
    if s and det is not None:
        rec = cv2.FaceRecognizerSF.create(s, '')
    elif not s:
        MISSING.append('face_recognition_sface_2021dec.onnx')
    a = find_model(dirs, ['auraface.onnx', 'glintr100.onnx'])
    if a and rec is not None and with_aura:
        load_aura(a)
    r = find_model(dirs, ['retinaface_mv1_0.25.onnx', 'retinaface.onnx'])
    if r and det is not None:
        load_retina(r)


def load_retina(path):
    global retina
    try:
        import onnxruntime as ort
        VERSIONS['onnxruntime'] = ort.__version__
    except Exception:
        return  # the second search goes on with YuNet alone
    o = ort.SessionOptions()
    o.intra_op_num_threads = 1
    o.inter_op_num_threads = 1
    o.log_severity_level = 3
    try:
        sess = ort.InferenceSession(path, o, providers=['CPUExecutionProvider'])
    except Exception:  # an optional model: the second search goes on with YuNet alone
        return
    retina = (sess, sess.get_inputs()[0].name)


def priors(h, w):
    if (h, w) not in PRIORS:
        pr = []
        for mins, step in zip([[16, 32], [64, 128], [256, 512]], [8, 16, 32]):
            fh, fw = -(-h // step), -(-w // step)
            for i in range(fh):
                for j in range(fw):
                    for m in mins:
                        pr.append([(j + 0.5) * step / w, (i + 0.5) * step / h, m / w, m / h])
        PRIORS.clear()  # one size at a time is enough (pictures of one size come in a row)
        PRIORS[(h, w)] = np.array(pr, np.float32)
    return PRIORS[(h, w)]


def retina_rows(bgr, thr):
    """RetinaFace (biubug6 mobilenet 0.25): rows like YuNet's (box, five points, score)"""
    sess, name = retina
    h, w = bgr.shape[:2]
    x = (bgr.astype(np.float32) - np.array([104, 117, 123], np.float32)).transpose(2, 0, 1)[None]
    loc, conf, lm = [o[0] for o in sess.run(None, {name: np.ascontiguousarray(x)})]
    p = priors(h, w)
    sc = conf[:, 1]
    k = sc >= thr
    loc, lm, p, sc = loc[k], lm[k], p[k], sc[k]
    cxcy = p[:, :2] + loc[:, :2] * 0.1 * p[:, 2:]
    wh = p[:, 2:] * np.exp(loc[:, 2:] * 0.2)
    pts = np.concatenate([p[:, :2] + lm[:, 2 * i:2 * i + 2] * 0.1 * p[:, 2:] for i in range(5)], 1)
    sx = np.array([w, h], np.float32)
    rows = np.concatenate([(cxcy - wh / 2) * sx, wh * sx, pts * np.tile(sx, 5), sc[:, None]], 1).astype(np.float32)
    keep = []
    for i in np.argsort(-rows[:, 14]):
        if all(iou(rows[i][:4], rows[j][:4]) < 0.4 for j in keep):
            keep.append(i)
    return [rows[i] for i in keep]


def inside(a, b):
    """the share of box a that lies inside box b"""
    iw = min(a[0] + a[2], b[0] + b[2]) - max(a[0], b[0])
    ih = min(a[1] + a[3], b[1] + b[3]) - max(a[1], b[1])
    if iw <= 0 or ih <= 0:
        return 0.0
    return iw * ih / (a[2] * a[3])


def load_aura(path):
    global aura, POOL
    try:
        import onnxruntime as ort
        VERSIONS['onnxruntime'] = ort.__version__
    except Exception:
        MISSING.append('onnxruntime')
        return
    o = ort.SessionOptions()
    o.intra_op_num_threads = int(os.environ.get('SCHUHKARTON_FACES_AURA_THREADS', '2'))
    o.inter_op_num_threads = 1
    o.log_severity_level = 3
    try:
        sess = ort.InferenceSession(path, o, providers=['CPUExecutionProvider'])
    except Exception as e:  # an optional model: the rest goes on without it
        MISSING.append(f'auraface.onnx ({type(e).__name__})')
        return
    aura = (sess, sess.get_inputs()[0].name)
    POOL = ThreadPoolExecutor(max_workers=2)


def open_image(path, longest):
    im = Image.open(path)
    try:
        im.draft('RGB', (longest, longest))
    except Exception:
        pass
    im = ImageOps.exif_transpose(im)
    if im.mode not in ('RGB', 'L'):
        bg = Image.new('RGB', im.size, (255, 255, 255))
        rgba = im.convert('RGBA')
        bg.paste(rgba, mask=rgba.split()[3])
        im = bg
    return im.convert('RGB')


def fit(im, spec):
    w, h = im.size
    if 'short' in spec:
        s = spec['short'] / min(w, h)
        s = min(s, spec.get('maxlong', 10 ** 9) / max(w, h))
    else:
        s = spec['long'] / max(w, h)
    if s >= 1:
        return im
    return im.resize((max(1, round(w * s)), max(1, round(h * s))), Image.LANCZOS)


def op_thumb(req):
    outs = req['outs']
    need = max(o.get('long', o.get('maxlong', 0)) or 0 for o in outs)
    im = open_image(req['src'], max(need, 1024))
    for o in outs:
        out = fit(im, o)
        tmp = o['path'] + '.tmp'
        out.save(tmp, 'JPEG', quality=o.get('q', 82), optimize=True, progressive=True)
        os.replace(tmp, o['path'])
    return {'w': im.size[0], 'h': im.size[1]}


def enc(v):
    v = np.asarray(v, dtype='float32').ravel()
    n = float(np.linalg.norm(v)) or 1.0
    return base64.b64encode((v / n).astype('<f4').tobytes()).decode()


def iou(a, b):
    ax2, ay2, bx2, by2 = a[0] + a[2], a[1] + a[3], b[0] + b[2], b[1] + b[3]
    iw, ih = min(ax2, bx2) - max(a[0], b[0]), min(ay2, by2) - max(a[1], b[1])
    if iw <= 0 or ih <= 0:
        return 0.0
    inter = iw * ih
    return inter / (a[2] * a[3] + b[2] * b[3] - inter)


def detect_rows(bgr):
    h, w = bgr.shape[:2]
    det.setInputSize((w, h))
    _, found = det.detect(bgr)
    return [] if found is None else [r.copy() for r in found]


def box_row(box, w, h):
    """a face drawn by hand that no search found: a row whose five points sit where they would in a frontal face"""
    x, y, bw, bh = box[0] * w, box[1] * h, box[2] * w, box[3] * h
    side = max(bw, bh)
    cx, cy = x + bw / 2, y + bh / 2
    pts = []
    for px, py in ARC:
        pts += [cx + (px / 112 - 0.5) * side, cy + (py / 112 - 0.5) * side]
    return np.array([x, y, bw, bh] + pts + [1.0], dtype='float32')


def embed(bgr, rows):
    """SFace and (when there) AuraFace for each row, the two models side by side"""
    aligned = [rec.alignCrop(bgr, r) for r in rows]

    def sface():
        return [enc(rec.feature(a)) for a in aligned]

    def auraface():
        sess, name = aura
        out = []
        for a in aligned:  # one at a time: the model is declared for a batch of one
            blob = ((a[:, :, ::-1].astype('float32') - 127.5) / 127.5).transpose(2, 0, 1)[None]
            out.append(enc(sess.run(None, {name: np.ascontiguousarray(blob)})[0]))
        return out

    if not aligned:
        return [], []
    if aura is None:
        return sface(), [None] * len(aligned)
    f2 = POOL.submit(auraface)
    e1 = sface()
    return e1, f2.result()


def op_embed(req):
    """embeddings of aligned 112x112 crops (raw BGR bytes) by one system"""
    crops = [np.frombuffer(base64.b64decode(c), dtype=np.uint8).reshape(112, 112, 3) for c in req['crops']]
    if req.get('model') == 'aura':
        if aura is None:
            raise RuntimeError('no second model')
        sess, name = aura
        out = []
        for a in crops:
            blob = ((a[:, :, ::-1].astype('float32') - 127.5) / 127.5).transpose(2, 0, 1)[None]
            out.append(enc(sess.run(None, {name: np.ascontiguousarray(blob)})[0]))
        return {'embs': out}
    if rec is None:
        raise RuntimeError('no recognition model')
    return {'embs': [enc(rec.feature(np.ascontiguousarray(a))) for a in crops]}


def op_faces(req):
    if det is None:
        return {'faces': []}
    im = open_image(req['src'], 1600)
    im = fit(im, {'long': 1600})
    rgb = np.asarray(im)
    bgr = np.ascontiguousarray(rgb[:, :, ::-1])
    h, w = bgr.shape[:2]
    rows = detect_rows(bgr)
    minside = max(36, min(w, h) * 0.035)
    main, low = [], []
    for r in rows:
        score = float(r[14])
        if score >= MIN_SCORE and min(r[2], r[3]) >= minside:
            main.append((r, 'yunet'))
        elif score >= EXTRA_SCORE and min(r[2], r[3]) >= EXTRA_MIN:
            low.append(r)
    extra = []
    if req.get('extra'):
        ret = [r for r in retina_rows(bgr, RETINA_CONFIRM) if min(r[2], r[3]) >= EXTRA_MIN] if retina is not None else []
        for r in low:  # YuNet, confirmed by RetinaFace (or sure enough without it)
            if retina is not None and any(iou(r[:4], q[:4]) >= 0.5 for q in ret):
                extra.append((r, 'yunet+retina'))
            elif retina is None and float(r[14]) >= EXTRA_ALONE:
                extra.append((r, 'yunet-low'))
        for q in ret:  # RetinaFace sure on its own
            if float(q[14]) >= RETINA_ALONE:
                extra.append((q, 'retina'))
    # an extra face is one no other face covers: not the same box, not half
    # inside one (half faces, a hand at a face)
    kept = list(main)
    for r, src in extra:
        if all(iou(r[:4], k[:4]) < 0.3 and inside(r[:4], k[:4]) < 0.5 for k, _ in kept):
            kept.append((r, src))
    if req.get('box'):
        b = req['box']
        bb = [b[0] * w, b[1] * h, b[2] * w, b[3] * h]
        if all(iou(bb, k[:4]) < 0.25 for k, _ in kept):
            kept.append((box_row(b, w, h), 'box'))
    split = req.get('split') and rec is not None
    if split:  # the caller has the embeddings made, both systems side by side
        aligned = [base64.b64encode(np.ascontiguousarray(rec.alignCrop(bgr, r)).tobytes()).decode() for r, _ in kept]
        e1 = e2 = [None] * len(kept)
    else:
        e1, e2 = embed(bgr, [r for r, _ in kept]) if rec is not None else ([None] * len(kept), [None] * len(kept))
    faces = []
    for i, (row, src) in enumerate(kept):
        x, y, fw, fh, score = float(row[0]), float(row[1]), float(row[2]), float(row[3]), float(row[14])
        face = {'x': max(0, x / w), 'y': max(0, y / h), 'w': fw / w, 'h': fh / h, 'score': score}
        if src != 'yunet':
            face['extra'], face['src'] = True, src
        if e1[i] is not None:
            face['emb'] = e1[i]
        if e2[i] is not None:
            face['emb2'] = e2[i]
        if split:
            face['aligned'] = aligned[i]
        # square crop with margin for the people view
        cx, cy, side = x + fw / 2, y + fh / 2, max(fw, fh) * 1.6
        box = (int(max(0, cx - side / 2)), int(max(0, cy - side / 2)),
               int(min(w, cx + side / 2)), int(min(h, cy + side / 2)))
        crop = im.crop(box).resize((200, 200), Image.LANCZOS)
        path = f"{req['crops']}_{i}.jpg"
        crop.save(path + '.tmp', 'JPEG', quality=85)
        os.replace(path + '.tmp', path)
        faces.append(face)
    return {'faces': faces, 'w': w, 'h': h}


def main():
    global MIN_SCORE
    args = sys.argv[1:]
    with_aura, only_aura = True, False
    while args and args[0].startswith('--'):
        if args[0] == '--min-score':
            MIN_SCORE = float(args[1])
            args = args[2:]
        elif args[0] == '--no-aura':  # the second system runs in a pool of its own
            with_aura, args = False, args[1:]
        elif args[0] == '--only-aura':
            only_aura, args = True, args[1:]
        else:
            args = args[1:]
    load_models([d for d in args if d], with_aura, only_aura)
    if det is not None:  # the second search needs the lower threshold from the detector
        det.setScoreThreshold(min(EXTRA_SCORE, MIN_SCORE))
    out = sys.stdout
    out.write(json.dumps({'hello': True, 'detection': det is not None, 'recognition': rec is not None,
                          'aura': aura is not None, 'extra': det is not None, 'retina': retina is not None,
                          'heic': HEIC, 'missing': MISSING, 'versions': VERSIONS}) + '\n')
    out.flush()
    for line in sys.stdin:
        try:
            req = json.loads(line)
            res = {'thumb': op_thumb, 'faces': op_faces, 'embed': op_embed}[req['op']](req)
            res['ok'] = True
        except Exception as e:  # report and keep serving
            res = {'ok': False, 'error': f'{type(e).__name__}: {e}'}
        out.write(json.dumps(res) + '\n')
        out.flush()


if __name__ == '__main__':
    main()
