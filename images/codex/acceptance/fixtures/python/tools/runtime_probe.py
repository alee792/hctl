from typing import Any, Literal

from pydantic import BaseModel, ConfigDict


description = "Report the Python authored-tool runtime."


class Input(BaseModel):
    model_config = ConfigDict(extra="forbid")


class Output(BaseModel):
    runtime: Literal["python"]


def execute(_: Input, _context: dict[str, Any]) -> Output:
    return Output(runtime="python")
