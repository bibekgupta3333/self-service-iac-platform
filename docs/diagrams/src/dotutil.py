"""Helper to render hand-written Graphviz DOT sources to PNG."""

import shutil
import subprocess

from common import out


def render_dot(dot_src: str, name: str, dpi: int = 150) -> str:
    if not shutil.which("dot"):
        raise SystemExit("graphviz 'dot' not found on PATH")
    path = out(name) + ".png"
    subprocess.run(["dot", "-Tpng", f"-Gdpi={dpi}", "-o", path],
                   input=dot_src.encode(), check=True)
    return path
