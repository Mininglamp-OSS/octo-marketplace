import unittest
import json
import pathlib
import tempfile
from report import main, validate_counts


class MeasurementTests(unittest.TestCase):
    def test_report_uses_actual_counts(self):
        definition = {"skills": [{}], "experts": [{"skill_names": ["a"]}]}
        measured = {"skills": 1, "experts": 1, "bindings": 1, "teams": 0}
        self.assertEqual(validate_counts(definition, measured, [{}]), measured)

    def test_every_mismatched_count_is_rejected(self):
        definition = {"skills": [{}], "experts": [{"skill_names": ["a"]}], "expert_team": {"name": "team"}}
        expected = {"skills": 1, "experts": 1, "bindings": 1, "teams": 1}
        for key in expected:
            with self.subTest(key=key):
                with self.assertRaises(ValueError):
                    validate_counts(definition, dict(expected, **{key: 2}), [{}])
        with self.assertRaises(ValueError):
            validate_counts(definition, expected, [])

    def test_mismatch_removes_stale_pass_report(self):
        with tempfile.TemporaryDirectory() as directory:
            out = pathlib.Path(directory)
            (out / "result.json").write_text('{"old": "PASS"}')
            (out / "request.json").write_text(json.dumps({"definition": {"skills": [{}], "experts": []}}))
            (out / "fleet-stored-skills.json").write_text("[]")
            (out / "fleet-measurements.json").write_text(json.dumps({"skills": 0, "experts": 0, "bindings": 0, "teams": 0}))
            with self.assertRaises(ValueError):
                main(directory)
            self.assertFalse((out / "result.json").exists())


if __name__ == "__main__":
    unittest.main()
