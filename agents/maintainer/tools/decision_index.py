from pathlib import Path
import re
from typing import Any

from pydantic import BaseModel, ConfigDict, Field


description = "Index settled, deferred, and open decisions in bounded project Markdown."


class Input(BaseModel):
    model_config = ConfigDict(extra="forbid")


class Item(BaseModel):
    path: str
    heading: str


class Output(BaseModel):
    settled: list[Item] = Field(default_factory=list)
    deferred: list[Item] = Field(default_factory=list)
    open: list[Item] = Field(default_factory=list)
    files_checked: int


def execute(_: Input, _context: dict[str, Any]) -> Output:
    root = Path.cwd()
    paths = [root / "README.md", *sorted((root / "docs").rglob("*.md"))]
    result: dict[str, list[Item]] = {"settled": [], "deferred": [], "open": []}
    checked = 0
    total = 0
    for path in paths[:128]:
        if not path.is_file() or path.is_symlink():
            continue
        data = path.read_bytes()
        total += len(data)
        if total > 2 * 1024 * 1024:
            break
        checked += 1
        text = data.decode("utf-8")
        relative = path.relative_to(root).as_posix()
        for heading in re.findall(r"^#{1,6}\s+(.+?)\s*$", text, re.MULTILINE):
            lowered = heading.lower()
            category = next(
                (name for name in ("settled", "deferred", "open") if name in lowered),
                None,
            )
            if category and len(result[category]) < 64:
                result[category].append(Item(path=relative, heading=heading))
        if relative.startswith("docs/adr/") and "- Status: accepted" in text:
            title = re.search(r"^#\s+(.+)$", text, re.MULTILINE)
            if title and len(result["settled"]) < 64:
                result["settled"].append(Item(path=relative, heading=title.group(1)))
    return Output(files_checked=checked, **result)
