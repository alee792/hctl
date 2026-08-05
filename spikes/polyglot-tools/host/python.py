import asyncio
import importlib.util
import inspect
import json
import os
from pathlib import Path
import sys
from typing import Any

from pydantic import BaseModel


def load_tools(root: Path) -> dict[str, Any]:
    tools: dict[str, Any] = {}
    for path in sorted((root / "tools").glob("*.py")):
        if path.name.startswith("_"):
            continue
        name = path.stem.replace("_", "-")
        spec = importlib.util.spec_from_file_location(f"hctl_tool_{path.stem}", path)
        if spec is None or spec.loader is None:
            raise RuntimeError(f"cannot import {path.name}")
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        input_model = getattr(module, "Input", None)
        output_model = getattr(module, "Output", None)
        execute = getattr(module, "execute", None)
        description = getattr(module, "description", None)
        if not (
            isinstance(description, str)
            and inspect.isclass(input_model)
            and issubclass(input_model, BaseModel)
            and inspect.isclass(output_model)
            and issubclass(output_model, BaseModel)
            and callable(execute)
        ):
            raise RuntimeError(
                f"{path.name} must export description, Input, Output, and execute"
            )
        tools[name] = (description, input_model, output_model, execute)
    return tools


async def dispatch(tools: dict[str, Any], request: dict[str, Any]) -> dict[str, Any]:
    instance_id = f"python:{os.getpid()}"
    if request.get("method") == "list":
        return {
            "instanceId": instance_id,
            "tools": [
                {
                    "name": name,
                    "description": definition[0],
                    "inputSchema": definition[1].model_json_schema(),
                    "outputSchema": definition[2].model_json_schema(),
                }
                for name, definition in tools.items()
            ],
        }
    params = request.get("params") or {}
    name = params.get("name", "")
    if name not in tools:
        raise RuntimeError(f"unknown Python tool {name!r}")
    _, input_model, output_model, execute = tools[name]
    args = input_model.model_validate(params.get("arguments"))
    result = execute(args, {"requestId": request.get("id", "")})
    if inspect.isawaitable(result):
        result = await result
    output = output_model.model_validate(result)
    return {"instanceId": instance_id, "output": output.model_dump(mode="json")}


async def main() -> None:
    tools = load_tools(Path(sys.argv[1]))
    while line_bytes := await asyncio.to_thread(
        sys.stdin.buffer.readline, (64 * 1024) + 1
    ):
        if len(line_bytes) > 64 * 1024:
            raise RuntimeError("protocol line exceeds 64 KiB")
        line = line_bytes.decode("utf-8")
        request: dict[str, Any] | None = None
        try:
            request = json.loads(line)
            response = {
                "id": request.get("id", ""),
                "result": await dispatch(tools, request),
            }
        except Exception as error:
            response = {
                "id": request.get("id", "") if request else "",
                "error": str(error),
            }
        print(json.dumps(response, separators=(",", ":")), flush=True)


if __name__ == "__main__":
    asyncio.run(main())
