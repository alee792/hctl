from pydantic import BaseModel, ConfigDict

description = "Add two integers."
calls = 0


class Input(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    left: int
    right: int


class Output(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)

    total: int
    calls: int


async def execute(args: Input, _context: dict[str, object]) -> Output:
    global calls
    calls += 1
    return Output(total=args.left + args.right, calls=calls)
