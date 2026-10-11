from __future__ import annotations


class JaroError(ValueError):
    """Raised for invalid JARO input or an unencodable value.

    offset is the byte offset of the failure in the input, or None when it does not apply.
    """

    def __init__(self, message: str, offset: int | None = None) -> None:
        self.message = message
        self.offset = offset
        super().__init__(
            f"jaro: offset {offset}: {message}"
            if offset is not None
            else f"jaro: {message}"
        )
