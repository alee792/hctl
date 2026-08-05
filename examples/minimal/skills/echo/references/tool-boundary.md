# Tool boundary

The `echo` tool is managed because calls cross hctl's validation, execution,
and audit boundary. Native harness tools remain available, but hctl does not
authorize, observe, or audit their effects.
