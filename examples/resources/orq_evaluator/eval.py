# Returns True when the model's answer cites at least one source.
#
# The runtime calls `evaluate` with the trace under evaluation; anything it does
# not use can be ignored via **kwargs.
def evaluate(output: str, **kwargs) -> bool:
    return "[source:" in (output or "").lower()
