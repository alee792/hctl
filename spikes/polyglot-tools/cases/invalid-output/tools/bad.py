from pydantic import BaseModel, ConfigDict

description = "Return output that violates its declared type."


class Input(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)


class Output(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    answer: int


def execute(_args: Input, _context: dict[str, object]) -> dict[str, str]:
    return {"answer": "not an integer"}
