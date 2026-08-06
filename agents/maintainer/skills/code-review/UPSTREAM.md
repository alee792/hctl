# Upstream provenance

This directory is an adapted import of Matt Pocock's `code-review` Agent
Skill. It is source-controlled locally; hctl makes no remote dependency or
update check.

- Upstream repository: <https://github.com/mattpocock/skills>
- Upstream skill path: `skills/engineering/code-review/`
- Pinned revision: `8b36d4fb2635b3c21998dcd8144439c9e5ba7302`
- Retrieved: 2026-08-05
- Upstream `SKILL.md` SHA-256: `7b2611d766ed7b9f375e73c821c7727535a6c036cf66870882770cd5a8188f70`
- Upstream `agents/openai.yaml` SHA-256: `8229ca854e11dc8e6aef2131ee03f31fb1561cf905fab9ccc325180cf3331352` (not imported; it is optional OpenAI-host metadata)
- Upstream `LICENSE` SHA-256: `0e7ac423bf2c6e223b7c5b156f8cf72da49d748e56a1641402c31f22ad07dbb5`

## Local adaptation

`SKILL.md` keeps the upstream Standards and Spec two-axis methodology intact.
Only two harness-specific instructions were adapted:

1. Its required `docs/agents/issue-tracker.md` and `/setup-matt-pocock-skills`
   workflow became an optional configured issue-tracker integration.
2. Its Claude `Agent`-tool invocation became a native-harness-neutral
   parallel-sub-agent instruction, with isolated sequential review as the
   fallback.

No other Matt Pocock skills, setup workflow, issue-tracker workflow, or remote
dependency mechanism is included. Hctl-specific review guidance remains in the
separate `product-review` and `simplicity-pass` skills.

## License

MIT License

Copyright (c) 2026 Matt Pocock

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
