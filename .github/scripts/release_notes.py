#!/usr/bin/env python3
"""Extract and validate the Chinese changelog section for a release tag."""

import re
import sys
from datetime import date
from pathlib import Path


def extract_release_notes(changelog: str, version: str) -> str:
    lines = changelog.splitlines()
    heading = re.compile(rf"^## \[{re.escape(version)}\] - (\d{{4}}-\d{{2}}-\d{{2}})$")
    matches = [(index, heading.fullmatch(line)) for index, line in enumerate(lines)]
    matches = [(index, match) for index, match in matches if match]

    if len(matches) != 1:
        raise ValueError(f"CHANGELOG.md 必须恰有一个 ## [{version}] - YYYY-MM-DD 章节")

    start, match = matches[0]
    date.fromisoformat(match.group(1))
    end = next((index for index in range(start + 1, len(lines)) if lines[index].startswith("## ")), len(lines))
    body_lines = lines[start + 1 : end]
    body = "\n".join(body_lines).strip()
    bullets = [line for line in body_lines if re.match(r"^\s*[-*]\s+\S", line)]
    if not body or not any(any("\u3400" <= char <= "\u9fff" for char in line) for line in bullets):
        raise ValueError(f"CHANGELOG.md 的 {version} 章节至少需要一条中文变更项")

    return body + "\n"


if __name__ == "__main__":
    if len(sys.argv) != 3:
        raise SystemExit("用法: release_notes.py <版本 tag> <CHANGELOG.md 路径>")

    try:
        notes = extract_release_notes(Path(sys.argv[2]).read_text(encoding="utf-8"), sys.argv[1])
    except (OSError, ValueError) as error:
        raise SystemExit(f"发布说明校验失败：{error}") from error
    sys.stdout.write(notes)
