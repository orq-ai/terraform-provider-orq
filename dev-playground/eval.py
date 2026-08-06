def evaluate(log):
    """
    Orq Python Evaluator
    --------------------
    Available fields on `log`:

    log["input"]       <str>         The last message sent to the model
    log["output"]      <str>         The model's generated response
    log["reference"]   <str | None>  The reference value to compare against (None if unset)
    log["messages"]    list[dict]    All conversation messages prior to the last user message
    log["retrievals"]  list[str]     Knowledge Base chunks retrieved for this log

    Return types:
      - float / int    Numeric score (e.g. similarity, word count)
      - bool           Pass/fail verdict (True = pass, False = fail)
    """

    output = log["output"]

    # -----------------------------------------------------------------------
    # Write your evaluation logic below.
    # Example: pass if the output cites at least one source (returns bool)
    # -----------------------------------------------------------------------

    output_clean = output.strip().lower()
    markers = ("http://", "https://", "source:", "[1]", "according to")

    return any(marker in output_clean for marker in markers)
