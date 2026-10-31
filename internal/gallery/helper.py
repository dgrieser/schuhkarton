"""schuhkarton helper: decodes images Go cannot read (HEIC, AVIF, ...) and finds faces.

Protocol: one JSON request per line on stdin, one JSON response per line on
stdout. The first line written is a hello with the available features.
Requires Pillow; pillow_heif for HEIC/AVIF; OpenCV (cv2) for faces.
"""
import base64
import json
import os
import sys

from PIL import Image, ImageOps

try:
    import pillow_heif
    pillow_heif.register_heif_opener()
except Exception:  # pragma: no cover - optional
    pass

cv2 = np = det = rec = None


def find_model(dirs, names):
    for d in dirs:
        for n in names:
            p = os.path.join(d, n)
            if os.path.isfile(p):
                return p
    return None


def load_models(dirs):
    global cv2, np, det, rec
    try:
        import cv2 as _cv2
        import numpy as _np
    except Exception:
        return
    cv2, np = _cv2, _np
    cv2.setNumThreads(1)
    y = find_model(dirs, ['face_detection_yunet_2023mar.onnx', 'yunet.onnx'])
    s = find_model(dirs, ['face_recognition_sface_2021dec.onnx', 'sface.onnx'])
    if y:
        det = cv2.FaceDetectorYN.create(y, '', (320, 320), 0.8, 0.3, 5000)
    if s and det is not None:
        rec = cv2.FaceRecognizerSF.create(s, '')


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


def op_faces(req):
    if det is None:
        return {'faces': []}
    im = open_image(req['src'], 1600)
    im = fit(im, {'long': 1600})
    rgb = np.asarray(im)
    bgr = np.ascontiguousarray(rgb[:, :, ::-1])
    h, w = bgr.shape[:2]
    det.setInputSize((w, h))
    _, found = det.detect(bgr)
    faces = []
    if found is None:
        return {'faces': [], 'w': w, 'h': h}
    minside = max(36, min(w, h) * 0.035)
    for i, row in enumerate(found):
        x, y, fw, fh, score = float(row[0]), float(row[1]), float(row[2]), float(row[3]), float(row[14])
        if score < 0.86 or min(fw, fh) < minside:
            continue
        face = {'x': max(0, x / w), 'y': max(0, y / h), 'w': fw / w, 'h': fh / h, 'score': score}
        if rec is not None:
            aligned = rec.alignCrop(bgr, row)
            feat = rec.feature(aligned).astype('float32').ravel()
            n = float(np.linalg.norm(feat)) or 1.0
            face['emb'] = base64.b64encode((feat / n).astype('<f4').tobytes()).decode()
        # square crop with margin for the people view
        cx, cy, side = x + fw / 2, y + fh / 2, max(fw, fh) * 1.6
        box = (int(max(0, cx - side / 2)), int(max(0, cy - side / 2)),
               int(min(w, cx + side / 2)), int(min(h, cy + side / 2)))
        crop = im.crop(box).resize((200, 200), Image.LANCZOS)
        path = f"{req['crops']}_{len(faces)}.jpg"
        crop.save(path + '.tmp', 'JPEG', quality=85)
        os.replace(path + '.tmp', path)
        faces.append(face)
    return {'faces': faces, 'w': w, 'h': h}


def main():
    dirs = [d for d in sys.argv[1:] if d]
    load_models(dirs)
    out = sys.stdout
    out.write(json.dumps({'hello': True, 'faces': det is not None, 'recognition': rec is not None}) + '\n')
    out.flush()
    for line in sys.stdin:
        try:
            req = json.loads(line)
            res = {'thumb': op_thumb, 'faces': op_faces}[req['op']](req)
            res['ok'] = True
        except Exception as e:  # report and keep serving
            res = {'ok': False, 'error': f'{type(e).__name__}: {e}'}
        out.write(json.dumps(res) + '\n')
        out.flush()


if __name__ == '__main__':
    main()
