#!/usr/bin/env python3
"""Generate labeled sample media for a LINE bridge test plan.

Every file carries its own name as big text, a frame counter on animated ones, and
coloured corner markers (red top-left, green top-right, blue bottom-left, yellow
bottom-right) so a screenshot shows how the image was cropped, resized or flipped.
No real photos or personal data are used.

Works on macOS, Linux and Windows. Needs Python 3.8+ and Pillow. ffmpeg is optional
(video, and AVIF/HEIC when pillow-heif is missing). A format that cannot be encoded
here is skipped and listed at the end; nothing is faked.

Usage:
    python3 make-samples.py OUT_DIR [--kinds image,animated,video,shape,ladder] [--only 01,10,13]

`ladder` is the size ladder: animated GIFs of about 5, 10, 20 and 27 MB (22 to 27 MB
of random noise in total is too heavy for a default run), so it is only built when asked
for with --kinds ladder or --only. The other kinds are the default.

Writes OUT_DIR/<NN>-<name>.<ext> and OUT_DIR/MANIFEST.txt (file name, label, size).
Copy the labels into the `## Test data` section of PLAN.md.
"""

import argparse
import os
import shutil
import subprocess
import sys
import tempfile

try:
    from PIL import Image, ImageDraw, ImageFont
except ImportError:
    sys.exit("Pillow is missing (pip name: Pillow). Run line-test-plan-preflight.sh.")

try:  # optional: HEIC/AVIF through Pillow
    import pillow_heif

    pillow_heif.register_heif_opener()
    try:
        pillow_heif.register_avif_opener()
    except AttributeError:
        pass
    HAVE_HEIF = True
except ImportError:
    HAVE_HEIF = False

FFMPEG = shutil.which("ffmpeg")
MARKERS = [((220, 40, 40), (0, 0)), ((40, 170, 60), (1, 0)), ((40, 80, 220), (0, 1)), ((240, 200, 30), (1, 1))]

# (number, name, extension, kind, width, height)
SPECS = [
    (1, "static", "jpg", "image", 800, 800),
    (2, "static-opaque", "png", "image", 800, 800),
    (3, "static-transparent", "png", "image", 800, 800),
    (4, "static", "webp", "image", 800, 800),
    (5, "static", "heic", "image", 800, 800),
    (6, "static", "avif", "image", 800, 800),
    (7, "static", "bmp", "image", 800, 800),
    (8, "static", "tiff", "image", 800, 800),
    (9, "static", "gif", "image", 800, 800),
    (10, "animated", "gif", "animated", 480, 480),
    (11, "animated", "webp", "animated", 480, 480),
    (12, "animated", "apng", "animated", 480, 480),
    (13, "h264", "mp4", "video", 640, 480),
    (14, "hevc", "mp4", "video", 640, 480),
    (15, "h264", "mov", "video", 640, 480),
    (16, "vp9", "webm", "video", 640, 480),
    (17, "wide-1280x480", "jpg", "shape", 1280, 480),
    (18, "tall-480x1280", "jpg", "shape", 480, 1280),
    (19, "large-4096", "jpg", "shape", 4096, 4096),
    (20, "tiny-64x64", "png", "shape", 64, 64),
    (21, "animated-large", "gif", "animated", 1080, 1080),
    (22, "animated-transparent", "gif", "animated", 480, 480),
    (23, "animated-tiny", "gif", "animated", 32, 32),
    (24, "animated-27mb", "gif", "ladder", 480, 480),
    (25, "animated-wide", "gif", "animated", 960, 360),
    (26, "animated-tall", "gif", "animated", 360, 960),
    (27, "animated-square", "gif", "animated", 600, 600),
    (28, "animated-5mb", "gif", "ladder", 480, 480),
    (29, "animated-10mb", "gif", "ladder", 480, 480),
    (30, "animated-20mb", "gif", "ladder", 480, 480),
]

# Size ladder targets in megabytes (decimal). The file lands a little under the target,
# so "20mb" stays below a 20 MB limit measured in either MB or MiB.
LADDER_MB = {24: 27, 28: 5, 29: 10, 30: 20}


def font(size):
    for name in ("DejaVuSans-Bold.ttf", "Arial Bold.ttf", "arialbd.ttf", "Helvetica.ttc"):
        try:
            return ImageFont.truetype(name, size)
        except OSError:
            continue
    try:
        return ImageFont.load_default(size=size)  # Pillow >= 10.1
    except TypeError:
        return ImageFont.load_default()


def frame(w, h, label, index=None, total=None, transparent=False):
    """One labeled frame. Animated frames lighten the background and show N/total."""
    short = min(w, h)
    if transparent:
        img = Image.new("RGBA", (w, h), (0, 0, 0, 0))
    else:
        t = 0 if index is None else index / max(total, 1)
        grey = int(50 + 110 * t)  # grey keeps the four corner markers visible on every frame
        img = Image.new("RGB", (w, h), (grey, grey, grey))
    d = ImageDraw.Draw(img)
    m = max(short // 8, 3)
    for color, (cx, cy) in MARKERS:
        x0 = 0 if cx == 0 else w - m
        y0 = 0 if cy == 0 else h - m
        d.rectangle([x0, y0, x0 + m - 1, y0 + m - 1], fill=color)
    text = label if index is None else f"{label}\n{index + 1}/{total}"
    size = max(short // 12, 8)
    f = font(size)
    while size > 6 and max(d.textlength(line, font=f) for line in text.split("\n")) > w - 2 * m - 4:
        size -= 2
        f = font(size)
    d.multiline_text((w / 2, h / 2), text, font=f, fill=(255, 255, 255, 255), anchor="mm", align="center",
                     stroke_width=max(size // 12, 1), stroke_fill=(0, 0, 0, 255))
    return img


def frames(w, h, label, count=12):
    return [frame(w, h, label, i, count) for i in range(count)]


def noisy_frame(w, h, label, index):
    """A frame whose top and bottom thirds are random noise (it barely compresses, so the
    file size is predictable) and whose middle band and corners keep the label and markers."""
    base = frame(w, h, label, index, 1000)
    # 6 bits of noise per pixel leaves most of the 256 colours to the label and markers
    noise = Image.frombytes("L", (w, h), bytes(b & 0xFC for b in os.urandom(w * h))).convert("RGB")
    band = (0, h // 3, w, 2 * h // 3)
    noise.paste(base.crop(band), band[:2])
    m = max(min(w, h) // 8, 3)
    for _, (cx, cy) in MARKERS:
        box = (0 if cx == 0 else w - m, 0 if cy == 0 else h - m)
        noise.paste(base.crop(box + (box[0] + m, box[1] + m)), box)
    return noise.convert("P", palette=Image.ADAPTIVE)


def save_gif(path, fr):
    fr[0].save(path, save_all=True, append_images=fr[1:], duration=100, loop=0)
    return os.path.getsize(path)


def build_ladder(out, w, h, label, megabytes):
    """Animated GIF of about `megabytes` MB: add noisy frames until just under the target."""
    target = int(megabytes * 1_000_000 * 0.985)
    fr = [noisy_frame(w, h, label, 0)]
    per_frame = save_gif(out, fr)
    count = max(2, target // max(per_frame, 1))
    for _ in range(8):
        while len(fr) < count:
            fr.append(noisy_frame(w, h, label, len(fr)))
        del fr[count:]
        size = save_gif(out, fr)
        if size <= target and size >= target * 0.97:
            return
        count = max(2, int(count * target / size))
    while size > target and len(fr) > 2:  # settle just under the target
        fr.pop()
        size = save_gif(out, fr)


def ffmpeg(args):
    r = subprocess.run([FFMPEG, "-hide_banner", "-loglevel", "error", "-y"] + args,
                       stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, text=True)
    return r.returncode == 0, r.stderr.strip()


def via_ffmpeg_image(png_path, out, codec_args):
    return ffmpeg(["-i", png_path] + codec_args + [out])


def make_video(out, w, h, label, vcodec_args):
    tmp = tempfile.mkdtemp()
    try:
        for i, im in enumerate(frames(w, h, label, 30)):
            im.convert("RGB").save(os.path.join(tmp, f"f{i:03d}.png"))
        return ffmpeg(["-framerate", "10", "-i", os.path.join(tmp, "f%03d.png"), "-pix_fmt", "yuv420p"]
                      + vcodec_args + [out])
    finally:
        shutil.rmtree(tmp, ignore_errors=True)


def build(num, name, ext, kind, w, h, out_dir):
    """Returns (ok, note). note explains a skip."""
    label = f"{num:02d}-{name}"
    out = os.path.join(out_dir, f"{label}.{ext}")
    if min(w, h) < 64:
        label = f"{num:02d}"  # too small for the full name; MANIFEST.txt maps the number to the file
    transparent = name == "static-transparent"
    try:
        if kind == "ladder":
            build_ladder(out, w, h, label, LADDER_MB[num])
            return True, ""
        if kind == "animated":
            if name == "animated-transparent":
                fr = [frame(w, h, label, i, 12, transparent=True) for i in range(12)]
                pal = []
                for f in fr:  # index 255 is the transparent colour; disposal 2 clears each frame
                    p = f.convert("RGB").convert("P", palette=Image.ADAPTIVE, colors=255)
                    p.paste(255, f.getchannel("A").point(lambda a: 255 if a < 128 else 0))
                    p.info["transparency"] = 255
                    pal.append(p)
                pal[0].save(out, save_all=True, append_images=pal[1:], duration=100, loop=0,
                            transparency=255, disposal=2)
                return True, ""
            fr = frames(w, h, label)
            if ext == "gif":
                fr = [f.convert("P", palette=Image.ADAPTIVE) for f in fr]
                fr[0].save(out, save_all=True, append_images=fr[1:], duration=100, loop=0)
            elif ext in ("webp", "apng"):
                fmt = "WEBP" if ext == "webp" else "PNG"
                fr[0].save(out, format=fmt, save_all=True, append_images=fr[1:], duration=100, loop=0)
            return True, ""
        if kind == "video":
            args = {"h264": ["-c:v", "libx264"], "hevc": ["-c:v", "libx265", "-tag:v", "hvc1"],
                    "vp9": ["-c:v", "libvpx-vp9"]}[name]
            if not FFMPEG:
                return False, "needs ffmpeg"
            ok, err = make_video(out, w, h, label, args)
            return ok, "" if ok else f"ffmpeg cannot encode {name}: {err.splitlines()[-1] if err else 'unknown'}"
        # still images and shapes
        im = frame(w, h, label, transparent=transparent)
        if ext == "jpg":
            im.convert("RGB").save(out, quality=90)
        elif ext == "png":
            im.save(out)
        elif ext == "bmp":
            im.convert("RGB").save(out)
        elif ext == "tiff":
            im.convert("RGB").save(out)
        elif ext == "gif":
            im.convert("P", palette=Image.ADAPTIVE).save(out)
        elif ext == "webp":
            im.convert("RGB").save(out, quality=90)
        elif ext in ("heic", "avif"):
            if HAVE_HEIF:
                im.convert("RGB").save(out, format="HEIF" if ext == "heic" else "AVIF")
            elif FFMPEG:
                tmp = out + ".src.png"
                im.convert("RGB").save(tmp)
                codec = ["-c:v", "libx265", "-tag:v", "hvc1", "-frames:v", "1"] if ext == "heic" else \
                    ["-c:v", "libaom-av1", "-still-picture", "1", "-frames:v", "1"]
                ok, err = via_ffmpeg_image(tmp, out, codec)
                os.remove(tmp)
                if not ok:
                    return False, f"{ext.upper()} needs pillow-heif; ffmpeg could not encode it"
            else:
                return False, f"{ext.upper()} needs pillow-heif or ffmpeg"
        return True, ""
    except Exception as e:  # a codec missing in this Pillow build, for example
        if os.path.exists(out):
            os.remove(out)
        return False, f"{type(e).__name__}: {e}"


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("out_dir")
    ap.add_argument("--kinds", default="image,animated,video,shape",
                    help="comma list of: image, animated, video, shape, ladder "
                         "(default: all but ladder, the 5-27 MB size ladder)")
    ap.add_argument("--only", default="", help="comma list of sample numbers, e.g. 1,10,13")
    a = ap.parse_args()

    # --only names files outright, whatever --kinds says (the ladder is only built that way or with --kinds ladder)
    kinds = {k.strip() for k in a.kinds.split(",") if k.strip()}
    only = {int(x) for x in a.only.split(",") if x.strip()}
    os.makedirs(a.out_dir, exist_ok=True)

    made, skipped = [], []
    # Build into a scratch folder and move a file in only when it succeeded: a failed
    # encoder (ffmpeg can leave a partial file) never replaces or leaves a file in out_dir,
    # and a sample from an earlier run survives a rerun that cannot rebuild it.
    scratch = tempfile.mkdtemp(prefix=".build-", dir=a.out_dir)
    try:
        for spec in SPECS:
            num, name, ext, kind, w, h = spec
            if only:
                if num not in only:
                    continue
            elif kind not in kinds:
                continue
            ok, note = build(*spec, scratch)
            fname = f"{num:02d}-{name}.{ext}"
            path = os.path.join(a.out_dir, fname)
            if ok:
                os.replace(os.path.join(scratch, fname), path)
            (made if ok else skipped).append((fname, f"{num:02d}-{name}", note, path))
    finally:
        shutil.rmtree(scratch, ignore_errors=True)

    # Rebuild the manifest from every sample on disk, so a partial rerun
    # (for example --kinds video after images) keeps the earlier labels.
    with open(os.path.join(a.out_dir, "MANIFEST.txt"), "w", encoding="utf-8") as f:
        f.write("file\tlabel\tbytes\n")
        for num, name, ext, _, _, _ in SPECS:
            fname = f"{num:02d}-{name}.{ext}"
            path = os.path.join(a.out_dir, fname)
            if os.path.isfile(path):
                f.write(f"{fname}\t{num:02d}-{name}\t{os.path.getsize(path)}\n")

    print(f"made {len(made)} sample(s) in {a.out_dir}")
    for fname, _, _, path in made:
        print(f"  {fname}  {os.path.getsize(path)} bytes")
    if skipped:
        print(f"skipped {len(skipped)}:")
        for fname, _, note, _ in skipped:
            print(f"  {fname}  {note}")
        print("Skipped formats cannot be tested with generated files; say so in the plan or install the missing tool.")
    print("Labels for PLAN.md `## Test data`: see MANIFEST.txt")


if __name__ == "__main__":
    main()
