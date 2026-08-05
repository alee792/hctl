from pydantic import BaseModel, ConfigDict

description = "Duplicate Python tool."


class Input(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)


class Output(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    ok: bool


def execute(_args: Input, _context: dict[str, object]) -> Output:
    return Output(ok=True)
