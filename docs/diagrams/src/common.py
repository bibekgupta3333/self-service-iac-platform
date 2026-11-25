"""Shared paths and styling for the `diagrams`/graphviz based renders."""

import os

SRC = os.path.dirname(os.path.abspath(__file__))
IMAGES = os.path.normpath(os.path.join(SRC, "..", "images"))
os.makedirs(IMAGES, exist_ok=True)


def out(name: str) -> str:
    """Output path without extension (the `diagrams` library appends .png)."""
    return os.path.join(IMAGES, name)


FONT = "Helvetica"

GRAPH = {
    "fontname": FONT,
    "fontsize": "22",
    "labelloc": "t",
    "pad": "0.4",
    "nodesep": "0.7",
    "ranksep": "1.0",
    "splines": "spline",
    "bgcolor": "white",
    "dpi": "150",
}
NODE = {"fontname": FONT, "fontsize": "12"}
EDGE = {"fontname": FONT, "fontsize": "11", "color": "#4A5568"}


def cluster(bg: str, border: str = "#9AA5B1", style: str = "rounded") -> dict:
    return {"bgcolor": bg, "pencolor": border, "style": style, "fontname": FONT,
            "fontsize": "14", "labeljust": "l", "margin": "18"}


BLUE = "#F1F5FB"
PURPLE = "#F3F1FA"
ORANGE = "#FFF8EC"
GREEN = "#EFF7F1"
GREY = "#F6F7F9"
