"""Render every diagram in docs/diagrams/src into docs/diagrams/images.

Usage (from the repository root):
    .venv/bin/python docs/diagrams/src/render_all.py

Requires the Python packages in docs/diagrams/requirements.txt and the
Graphviz `dot` binary on PATH.
"""

import os
import shutil
import subprocess
import sys

SRC = os.path.dirname(os.path.abspath(__file__))
IMAGES = os.path.normpath(os.path.join(SRC, "..", "images"))

# script -> image it produces
SCRIPTS = {
    "architecture.py": "architecture.png",
    "aws_infrastructure.py": "aws-infrastructure.png",
    "cicd_pipeline.py": "cicd-pipeline.png",
    "component_backend.py": "component-backend.png",
    "data_model.py": "data-model.png",
    "state_machine_request.py": "state-machine-request.png",
    "seq_provision_request.py": "seq-provision-request.png",
    "seq_apply_on_merge.py": "seq-apply-on-merge.png",
    "seq_drift_detection.py": "seq-drift-detection.png",
    "seq_ttl_teardown.py": "seq-ttl-teardown.png",
    "seq_module_release.py": "seq-module-release.png",
}


def main() -> int:
    if not shutil.which("dot"):
        print("error: Graphviz 'dot' not found on PATH (brew install graphviz / "
              "apt-get install graphviz)", file=sys.stderr)
        return 1
    os.makedirs(IMAGES, exist_ok=True)
    failed = []
    for script, image in SCRIPTS.items():
        # run inside src/ so the `diagrams` library writes nothing stray elsewhere
        proc = subprocess.run([sys.executable, os.path.join(SRC, script)], cwd=SRC,
                              capture_output=True, text=True)
        target = os.path.join(IMAGES, image)
        if proc.returncode != 0 or not os.path.exists(target):
            failed.append(script)
            print(f"FAIL {script}\n{proc.stdout}{proc.stderr}", file=sys.stderr)
        else:
            print(f"ok   {image}")
    if failed:
        print(f"{len(failed)} diagram(s) failed: {', '.join(failed)}", file=sys.stderr)
        return 1
    print(f"rendered {len(SCRIPTS)} diagrams into {IMAGES}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
