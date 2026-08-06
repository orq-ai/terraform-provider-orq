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

    input_text = log["input"]
    output = log["output"]
    reference = log["reference"]  # str or None

    # -----------------------------------------------------------------------
    # Write your evaluation logic below.
    # Example: pass if the output matches the reference and mentions a key
    # word from the input (returns bool)
    # -----------------------------------------------------------------------

    output_clean = output.strip().lower()
    reference_clean = (reference or "").strip().lower()
    input_words = set(input_text.strip().lower().split())

    exact_match = output_clean == reference_clean
    mentions_input_keyword = any(word in output_clean for word in input_words)

    return exact_match and mentions_input_keyword
