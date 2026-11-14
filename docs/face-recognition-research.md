# Face recognition: options for better models and AI support

Research notes, November 2026. Nothing here is implemented yet, except the
k-nearest-neighbour matching used for name suggestions (see the end). Numbers
come from the papers and model cards; they were measured with different
protocols, so treat them as rough. Points we could not verify are marked.

## Where we are

- **Detection:** YuNet (`face_detection_yunet_2023mar.onnx`, OpenCV zoo, MIT).
  `internal/helper/helper.py` keeps faces with a score of at least 0.86 and a
  short side of at least max(36 px, 3.5 % of the image), on pictures scaled to
  1600 px.
- **Recognition:** SFace (`face_recognition_sface_2021dec.onnx`, Apache-2.0).
  It gets a 112×112 crop aligned on five points (`alignCrop`) and gives a
  128-d embedding, which is L2-normalised before it is stored.
- **Grouping:** leader clustering on cosine similarity
  (`faces.cluster_threshold` 0.48, `faces.named_threshold` 0.42,
  `faces.same_photo_threshold` 0.75). A person is matched by their centroid.
- **Not done today:**
  - no flip augmentation;
  - no filter for blur or pose;
  - the raw embedding norm is thrown away;
  - stored embeddings carry no tag saying which model made them.
- **Strength:** it runs fully local in the `schuhkarton-faces` container, with
  no API and no paid plan. That should stay the default.

## 1. Open models

| Model | Type | Accuracy (published) | Size / dim | Licence of the weights |
|---|---|---|---|---|
| YuNet 2023mar (current) | detection | WIDER Hard AP 0.736 | ~0.3 MB | MIT |
| YuNet 2026may (opencv_zoo, dynamic input) | detection | about the same | tiny | MIT (not verified) |
| SCRFD-10G (InsightFace) | detection | WIDER Hard ~83 | 16 MB | non-commercial |
| RetinaFace R50 / mnet (biubug6) | detection | Hard ~90 (R50) | 1.7–100 MB | MIT code; trained on WIDER (grey area) |
| YOLOv8 / v11-face | detection | Hard ~81–84 | 6+ MB | AGPL-3.0 |
| SFace (current) | recognition | LFW ~99.4–99.6; no IJB-C figure published (estimate ~93) | ~37 MB, 128-d | Apache-2.0 |
| **AuraFace-v1** (fal) | recognition | LFW 99.65, CFP-FP 95.2, AgeDB 96.1 | ~250 MB ResNet100, 512-d | **Apache-2.0** |
| buffalo_l / antelopev2 (ArcFace R50/R100) | recognition | IJB-C ~96–97 | 170–250 MB, 512-d | non-commercial |
| AdaFace IR-50/101 | recognition | IJB-C ~96–97.4 | 170–250 MB, 512-d | MIT repo; training data (MS1M/WebFace) murky; needs our own ONNX export |
| EdgeFace S / XS | recognition | IJB-C 95.6 / 94.8; LFW 99.7 | 1.8–3.7 M params, 512-d | CC BY-NC-SA 4.0 |
| MobileFaceNet / GhostFaceNet | recognition | IJB-C ~94.7 / ~96 | small | per repo; GhostFaceNet not verified |

### Recommendation

Keep YuNet for detection. The 2026may file is worth trying once the OpenCV
build in the container supports it.

Add **AuraFace-v1 (`glintr100.onnx`)** as an optional recognition model:

- **Licence:** it is the only ArcFace-class model with Apache-2.0 weights, so
  it can ship in the public image.
- **Integration:** it takes the same 112×112 ArcFace alignment that
  `alignCrop` already produces, so detection and alignment stay as they are.
  Run it with onnxruntime, which has arm64 wheels, rather than `cv2.dnn`.
- **Cost:** about 24 GFLOPs per face, estimated (not measured) at 0.2–0.5 s
  per face on an arm64 CPU. That is fine for background work.
- **Expected gain:** clearly better than SFace for children, ageing and side
  views. There is no head-to-head benchmark, so test it on our own named
  people before switching.
- **Licence trap:** do **not** take `scrfd_10g_bnkps.onnx` from the AuraFace
  repo. It is InsightFace's file and stays non-commercial, even though the
  repo says Apache-2.0.

InsightFace, EdgeFace and YOLO weights are fine for a private gallery, but they
must not be baked into the public Docker image. If we support them, users put
the files into `SCHUHKARTON_FACES_MODELS_DIR` themselves (`find_model` already
searches there), and the setup shows a licence note.

## 2. Vision LLMs and mittwald AI hosting

### What mittwald offers

- **Vision models:**
  - Ministral-3-14B, which replaced Mistral-Small-3.2 (the old name redirects);
  - Qwen3.5-122B-A10B;
  - Qwen3.6-35B-A3B;
  - Qwen3.8-27B.
- **Other models:** gpt-oss-120b, Qwen3.5-0.8B, GLM-OCR, Whisper,
  Cohere-Transcribe, Qwen3-TTS.
- **Qwen3-Embedding-8B:** text only, so no use for faces.
- **Qwen3-VL-Reranker-2B:** gives text-and-image relevance scores, but is not
  built for identity.

The API is OpenAI-compatible, and `internal/gallery/ai.go` already talks to it
for photo descriptions.

### What they are good for

- **Identity: no.** Vision LLMs are not calibrated for identity. They often
  refuse, and their answers to "are these two crops the same person?" are
  unreliable, above all for siblings and children. They must never merge
  groups or assign a person.
- **As a weak second opinion on doubtful faces:**
  - rejecting things that are not faces: posters, statues, screens, pets,
    paintings;
  - a rough child or adult bucket;
  - whether a crop is usable (blur, occlusion, extreme angle).

Most of these checks can also be done locally for free (section 3).

### Privacy

- **Biometric data:** face crops used to identify people are biometric data
  under GDPR Art. 9.
- **What mittwald says:** the service is stateless. Prompts and outputs are
  not stored or used for training, only metadata (IP, key, time, tokens) is
  logged, and processing stays on its German infrastructure.
- **Open questions:** the page does not mention images explicitly or a data
  processing agreement (AVV). Check both in the account.
- **Household exemption:** a private family gallery probably falls under
  Art. 2(2)(c). Even so, an LLM step must be opt-in and off by default. It
  should send only the 200 px crops, never names or the identity question.
- **Photo descriptions:** they already send whole photos to the same host,
  when they are switched on.

## 3. Proposed architecture: several variants

1. **Tag every embedding with its model**, for example
   `{model: "sface-2021dec", dim: 128}`.
   - Never compare embeddings across models.
   - Switching models re-embeds in the background, and grouping runs per model
     until that is done.
   - Keep thresholds per model. 0.48 / 0.42 are tuned for SFace; 512-d
     ArcFace-style models usually land around 0.30–0.40, so retune them on our
     own named people.
2. **Default:** YuNet + SFace, local, no API. Unchanged.
3. **Optional better recogniser:** AuraFace, plus user-supplied
   ArcFace / AdaFace / EdgeFace ONNX files (same input), with a licence note.
4. **Optional LLM check** (mittwald or any OpenAI-compatible endpoint):
   - only for doubtful cases: a low detector score, a match near the
     threshold, or a probable child;
   - it may reject or flag a face, never assign one.

## 4. Cheap accuracy wins, no new model (`helper.py`)

- **Flip augmentation:** embed the aligned crop and its mirror, add the two
  embeddings, and normalise. A small but typical gain.
- **Quality filter:**
  - Measure blur (variance of the Laplacian) and yaw (estimated from the
    eye/nose landmarks YuNet already gives).
  - Low-quality faces may still join a person, but should not move the
    centroid.
- **Size:** require roughly 60–80 px for recognition. The 36 px minimum can
  stay for detection.
- **Keep the raw embedding norm** before normalising. For AdaFace- or
  MagFace-style models it is a quality score.
- **Alignment:** keep the five-point similarity transform to 112×112 that
  `alignCrop` does.
- **Matching:**
  - Score against the k nearest members of a person, not only the centroid.
  - Or keep several centroids per person, for example by EXIF year.
  - A single centroid drifts badly for children who grow up.

The name suggestions in `internal/gallery/facesuggest.go` already do the k-NN
part. They take the better of the centroid similarity and the mean of the
three most alike faces. Since v0.9.43 grouping does the same when a face
joins a named person, and weak faces (detector score below 0.90, or a short
side under 80 px) need `named_threshold` + 0.12. Unnamed groups are still
formed by centroids.

## Measured (November 2026, a real family library)

4,509 named faces of 32 people, SFace embeddings, leave one out (the face is
matched against everybody else's named faces). Caveat: many names were given
to whole groups the centroid clustering had formed, so the figures flatter
all methods a little; they compare the methods fairly.

| Matching | top 1 | top 3 | at 0.42: answered / right |
|---|---|---|---|
| centroid (grouping until v0.9.42) | 85.4 % | 96.3 % | 96.8 % / 86.7 % |
| centroid of the faces within ±2 years | 88.0 % | 96.7 % | 98.0 % / 88.8 % |
| 1 nearest face | 92.3 % | 98.8 % | 99.7 % / 92.5 % |
| mean of the 3 nearest faces | 93.0 % | 98.7 % | 99.2 % / 93.3 % |
| mean of the 5 nearest faces | 93.2 % | 98.6 % | 99.0 % / 93.7 % |
| better of centroid and 3 nearest | 93.1 % | 99.0 % | 99.5 % / 93.2 % |

- **Quality:** faces with a detector score below 0.90 were right 86 % of the
  time (centroid: 69 %), the others 94.5 %; strongly turned heads 88.7 %
  against 96.2 % for frontal ones; faces under 80 px about 83 % against 93 %.
  Blur made no difference. Leaving weak faces out of a person's references
  made it worse (91.1 %); a higher threshold for weak faces asking to join is
  better: at 0.54 they were right 90.5 % (from 87.4 %), 88 % of them still
  answered.
- **Flip augmentation** (478 crops of 12 people re-embedded with YuNet +
  SFace): 93.3 % with flip, 93.9 % without — no gain.
- **Grouping, before and after** (half of the named items had their names
  hidden, then grouped): right 77.7 % → 82.9 %, wrong 11.3 % → 6.0 %, left
  unnamed 10.9 % → 11.1 %; a grouping run took 0.70 → 0.97 s on a laptop.
- **AuraFace-v1** (`glintr100.onnx`, 260 MB, sha256 `a7933ea5…25c60`, same
  478 crops, same YuNet alignment, RGB (x−127.5)/127.5):

  | | centroid | 3 nearest | median margin |
  |---|---|---|---|
  | SFace (re-embedded) | 93.7 % | 93.9 % | +0.285 |
  | AuraFace | 93.5 % | 94.6 % | +0.304 |
  | AuraFace + flip | 94.1 % | 95.2 % | +0.328 |

  Gains mostly on unsure and turned faces (turned: 91.2 → 93.1 %), about 1
  point overall (3–6 of 478 faces, within noise). Per face on a 24-core
  laptop: SFace 28 ms, AuraFace 126 ms (407 ms on one thread); on a
  Raspberry Pi probably seconds. Its cosines run lower: about 0.35 matches
  SFace's 0.42, about 0.40 its 0.48. Verdict: not worth the switch for now;
  the nearest-face matching gave far more.

## Not verified

- SFace's IJB-C score.
- All CPU timings.
- Whether OpenCV 4.10 supports YuNet 2026may.
- GhostFaceNet's licence.
- Whether mittwald's data-protection statement covers images and comes with
  an AVV.

## Sources

- mittwald: [models](https://developer.mittwald.de/docs/v2/platform/aihosting/models/),
  [data protection](https://developer.mittwald.de/docs/v2/platform/aihosting/access-and-usage/data-protection/),
  [deprecation note](https://www.drupal.org/i/3561268)
- InsightFace: [model zoo licence](https://github.com/deepinsight/insightface/blob/e68b8b076fb11e9f75c87244d234385f0f318181/model_zoo/README.md),
  [commercial licensing](https://www.insightface.ai/solutions/face-recognition-licensing)
- [AuraFace-v1](https://huggingface.co/fal/AuraFace-v1)
- EdgeFace: [model](https://huggingface.co/Idiap/EdgeFace-Base),
  [paper](https://arxiv.org/abs/2307.01838)
- [UniFace weights mirror (AdaFace, MIT)](https://huggingface.co/yakhyo/uniface-weights)
- YuNet: [README (WIDER AP)](https://huggingface.co/opencv/opencv_zoo/blob/39d77a4dcc7b24c4b8c0fda67e76d0dabc2b0722/models/face_detection_yunet/README.md),
  [YuNet 2026may](https://huggingface.co/pollen-robotics/face_detection_yunet_2026may),
  [OpenCV 5 face tutorial](https://docs.opencv.org/5.0/tutorials/dnn/dnn_face/dnn_face.html)
- Detection comparison (YOLO AGPL): [overview](https://ufme-docs.pages.dev/research/sota-detection),
  [YOLOv11n-face](https://hackernoon.com/yolov11n-face-detection-lightweight-face-detection-with-yolov11)

## Both systems together (November 2026)

Measured on the same library (478 named crops of 12 people; 250 original
photos with named faces), then built in v0.9.46.

**Naming with both models** (leave one out; FAR = how often a face still got
somebody else's name above the threshold with its own person removed, a
stand-in for strangers and relatives):

| rule | top 1 | at 0.42: precision / coverage / FAR |
|---|---|---|
| SFace | 93.3 % | 94.6 / 96.7 / 30.3 |
| AuraFace ×1.2 | 93.9 % | 95.6 / 95.4 / 30.8 |
| mean of both | 94.1 % | 96.1 / 95.8 / 28.0 |
| mean, 0.05 ahead of the next person | | 98.2 / 90.6 |

The gain is in unsure faces (detector score low third: precision 90.6 →
95.2 %).

**A second search** (extra = no stored face there, IoU < 0.3, not half
inside one; share of real faces eyeballed on samples of 60):

| search | extras in 250 photos | real |
|---|---|---|
| YuNet ≥ 0.6, ≥ 20 px | 67 | 72 % |
| YuNet on a 2x zoom | 123 | 40 % |
| RetinaFace ≥ 0.6 | 98 | 65 % |
| YuNet ≥ 0.6 confirmed by RetinaFace ≥ 0.9 | 35 | 86 % |
| RetinaFace ≥ 0.95 | 41 | 23 of 25 |

Junk: dogs, dolls, teddies, hands, food, backs of heads, faces in pictures.
Many real extras are not tiny: clear faces of 30–42 px that fail the size
rule, and selfies YuNet scores 0.6–0.86. The 2x zoom costs 4–5 times more and
finds mostly junk of its own, so it is not used.

**Taking extras in** (197 extras, references from other photos, names already
in the photo blocked): AuraFace 3 nearest ≥ 0.40, 0.05 ahead of the next
person, SFace agreeing → 15 taken in, all right by eye (without the margin:
17, two of them sibling mix-ups).

**Faces drawn by hand** (16 with references): SFace and AuraFace each name 8
right, but different ones; the mean helps.

**Time per photo** (laptop, one thread): YuNet 86 ms, RetinaFace 118 ms, SFace
14 ms per face, AuraFace 198 ms per face — AuraFace is the cost that counts.

RetinaFace model: yakhyo/retinaface-pytorch v0.0.1 `retinaface_mv1_0.25.onnx`
(MIT; biubug6's architecture, retrained), 1,736,694 bytes, sha256
`b7a7acab55e104dce6f32cdfff929bd83946da5cd869b9e2e9bdffafd1b7e4a5`.
