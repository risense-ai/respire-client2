"""Apply the pinned Wails 3.0.0 GTK3 size-type correction for 32-bit Linux."""
import pathlib
import shutil
import sys

source, destination = map(pathlib.Path, sys.argv[1:])
relative = pathlib.Path("pkg/application/linux_cgo_gtk3.go")
content = (source / relative).read_text(encoding="utf-8")
replacements = {
    "C.ulong(len(icon))": ("C.gsize(len(icon))", 2),
    "C.ulong(len(img.Pix))": ("C.gsize(len(img.Pix))", 3),
    "C.long(len(js))": ("C.gssize(len(js))", 1),
    "C.long(n)": ("C.gssize(n)", 1),
}
for old, (new, count) in replacements.items():
    if content.count(old) != count:
        raise SystemExit(f"Pinned Wails GTK3 source changed: expected {count} occurrences of {old}")
    content = content.replace(old, new)
shutil.copytree(source, destination, dirs_exist_ok=True)
target = destination / relative
target.chmod(0o644)
target.write_text(content, encoding="utf-8", newline="\n")
print("Applied 7 typed gsize/gssize conversions in an isolated Wails source copy")
