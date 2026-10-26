"""Build a report only after measured Fleet counts match the prepared plan."""
import json
import pathlib
import sys


def validate_counts(definition, measured, stored):
    expected = {
        "skills": len(definition["skills"]),
        "experts": len(definition["experts"]),
        "bindings": sum(len(e.get("skill_names", [])) for e in definition["experts"]),
        "teams": int(bool(definition.get("expert_team"))),
    }
    if measured != expected or measured["skills"] != len(stored):
        raise ValueError(f"Fleet counts differ from prepared plan: measured={measured}, expected={expected}")
    return measured


def main(directory):
    out = pathlib.Path(directory)
    result = out / "result.json"
    result.unlink(missing_ok=True)
    definition = json.loads((out / "request.json").read_text())["definition"]
    stored = json.loads((out / "fleet-stored-skills.json").read_text())
    counts = validate_counts(definition, json.loads((out / "fleet-measurements.json").read_text()), stored)
    report = dict(counts, source_identity=json.loads((out / "source-after.json").read_text()),
                  real_fleet_handler_and_postgres=True, real_daemon_materialization=True,
                  online_daemon_dispatch=False, llm_execution=False)
    result.write_text(json.dumps(report, indent=2) + "\n")
    print("PASS: measured Fleet/Postgres graph and Daemon materialization; " + str(counts))


if __name__ == "__main__":
    main(sys.argv[1])
