# Evaluators are imported by the 26-character ULID the API returns as `_id`.
# Built-in evaluators (orq_pii_detection, …) are addressed by slug, have no
# evaluator record, and cannot be imported. `path` is not importable and stays
# null until the first apply re-asserts it from config.
terraform import orq_evaluator.example 01JMDPA3QW5C1V0NJ1PW34T4E5
