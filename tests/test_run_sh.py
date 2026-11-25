"""The options in run.sh: a false stays false (jq's `false // true` is true)."""

from __future__ import annotations

import json
from pathlib import Path
import re
import shutil
import subprocess

import pytest

RUN = (Path(__file__).resolve().parents[1] / "vector" / "run.sh").read_text(encoding="utf-8")


def jq_filter(option: str) -> str:
    (found,) = [f for f in re.findall(r"jq -r '([^']*)' \"\$\{OPTIONS\}\"", RUN) if f".{option}" in f]
    return found


@pytest.mark.skipif(shutil.which("jq") is None, reason="jq is not installed")
@pytest.mark.parametrize("option", ["live_stream", "program_stay_still"])
@pytest.mark.parametrize(("options", "expected"), [(True, "true"), (False, "false"), (None, "true")])
def test_an_option_on_by_default_can_be_switched_off(option, options, expected):
    data = {} if options is None else {option: options}
    result = subprocess.run(
        ["jq", "-r", jq_filter(option)], input=json.dumps(data), capture_output=True, text=True, check=True
    )
    assert result.stdout.strip() == expected


def test_no_option_is_read_with_true_as_the_alternative():
    commands = "\n".join(line for line in RUN.splitlines() if not line.lstrip().startswith("#"))
    assert "// true" not in commands
