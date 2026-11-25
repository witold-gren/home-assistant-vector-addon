"""Without Home Assistant: the files in standalone/ agree with the add-on."""

from __future__ import annotations

import json
import os
from pathlib import Path
import shutil
import stat
import subprocess

import pytest

ROOT = Path(__file__).resolve().parents[1]
FOLDER = ROOT / "standalone"


@pytest.fixture
def copy(tmp_path):
    shutil.copytree(
        FOLDER,
        tmp_path / "standalone",
        ignore=shutil.ignore_patterns("data", "__pycache__"),
    )
    return tmp_path / "standalone"


def setup(folder: Path) -> str:
    return subprocess.run([folder / "setup.sh"], check=True, capture_output=True, text=True).stdout


def test_options_are_those_of_the_add_on():
    yaml = pytest.importorskip("yaml")
    config = yaml.safe_load((ROOT / "vector" / "config.yaml").read_text(encoding="utf-8"))
    assert set(json.loads((FOLDER / "options.example.json").read_text(encoding="utf-8"))) == set(config["options"])


def test_setup_makes_a_private_token_once(copy):
    first = setup(copy)
    options = copy / "data" / "options.json"
    token_file = options
    token = json.loads(options.read_text())["studio_token"]
    assert len(token) >= 24 and token not in first
    assert json.loads(options.read_text())  # valid JSON
    assert stat.S_IMODE(token_file.stat().st_mode) == 0o600
    before = (options.read_text(), token_file.read_text())
    setup(copy)
    assert (options.read_text(), token_file.read_text()) == before


def test_compose_shares_what_the_robot_needs():
    compose = (FOLDER / "compose.yaml").read_text(encoding="utf-8")
    assert "context: ../vector" in compose and "./data:/data" in compose and "init: true" in compose
    assert ("/run/dbus:/run/dbus" in compose) is False
    assert ("network_mode: host" in compose) is False
    assert "privileged" not in compose
    assert "name: vector-bridge" in compose  # else all the add-ons are one Docker project "standalone"
    assert os.access(FOLDER / "setup.sh", os.X_OK)
    assert "standalone/data/" in (ROOT / ".gitignore").read_text(encoding="utf-8")


@pytest.mark.parametrize("name", ["README.md", "README.pl.md"])
def test_the_description_names_the_port_and_the_steps(name):
    text = (FOLDER / name).read_text(encoding="utf-8")
    for needed in ("3012", "./setup.sh", "docker compose up -d --build", "/ws"):
        assert needed in text
    assert "standalone/" + name in (ROOT / name).read_text(encoding="utf-8")
