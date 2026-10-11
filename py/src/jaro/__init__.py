"""JARO: a JSON-model data format with a text form and a binary form."""

from __future__ import annotations

from .binary import dumps_binary, loads_binary
from .errors import JaroError
from .text import dumps, loads

__all__ = ["JaroError", "dumps", "dumps_binary", "loads", "loads_binary"]
__version__ = "0.1.0"
