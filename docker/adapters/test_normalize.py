import json
import pathlib
import subprocess
import sys
import tempfile
import unittest


SCRIPT = pathlib.Path(__file__).with_name("normalize.py")


class NormalizeTests(unittest.TestCase):
    def run_normalizer(self, kind, raw_lines, session_lines=(), exit_code=0, stderr=""):
        with tempfile.TemporaryDirectory() as td:
            root = pathlib.Path(td)
            raw = root / "raw"
            raw.write_text(raw_lines, encoding="utf-8")
            sessions = root / "sessions" / "one"
            sessions.mkdir(parents=True)
            if session_lines:
                (sessions / "session.jsonl").write_text(
                    "\n".join(json.dumps(line) for line in session_lines) + "\n",
                    encoding="utf-8",
                )
            result = root / "result.json"
            final = root / "final.md"
            stderr_file = root / "stderr"
            stderr_file.write_text(stderr, encoding="utf-8")
            if kind == "codex":
                final.write_text("done", encoding="utf-8")
            subprocess.run(
                [sys.executable, str(SCRIPT), kind, str(raw), str(root / "sessions"),
                 str(result), str(final), str(exit_code), "1.25", str(stderr_file)],
                check=True,
            )
            return json.loads(result.read_text(encoding="utf-8"))

    def test_codex_terminal_usage_and_nulls(self):
        record = {
            "type": "turn.completed",
            "usage": {"input_tokens": 21, "output_tokens": 8},
        }
        result = self.run_normalizer("codex", json.dumps(record) + "\n")
        self.assertEqual(result["status"], "completed")
        self.assertEqual(result["elapsed_millis"], 1250)
        self.assertEqual(result["usage"]["input_tokens"], 21)
        self.assertIsNone(result["usage"]["cached_input_tokens"])
        self.assertIsNone(result["usage"]["reasoning_output_tokens"])
        self.assertEqual(result["usage"]["token_quality"], "provider_reported")

    def test_dsh_sums_provider_usage_without_mirrors(self):
        header = {"event": {"type": "request/header", "data": {"header": {"config": {
            "provider": "openai-codex", "model": "gpt-5.6-luna", "reasoningEffort": "low"
        }}}}}
        usage1 = {"event": {"type": "assistant/chunk", "data": {"chunk": {
            "type": "usage", "usage": {"outputTokens": 3, "totalTokens": 10, "cacheReadTokens": 2}
        }}}}
        usage2 = {"event": {"type": "assistant/chunk", "data": {"chunk": {
            "type": "usage", "usage": {"outputTokens": 4, "totalTokens": 13}
        }}}}
        mirrored = {"event": {"type": "assistant/message", "data": {
            "usage": {"outputTokens": 999, "totalTokens": 1000}
        }}}
        result = self.run_normalizer("dsh", "answer", [header, usage1, usage2, mirrored])
        self.assertEqual(result["status"], "completed")
        self.assertEqual(result["usage"]["input_tokens"], 16)
        self.assertEqual(result["usage"]["output_tokens"], 7)
        self.assertIsNone(result["usage"]["cached_input_tokens"])
        self.assertIsNone(result["usage"]["reasoning_output_tokens"])

    def test_dsh_missing_effective_config_is_unsupported(self):
        result = self.run_normalizer("dsh", "answer", [])
        self.assertEqual(result["status"], "unsupported")
        self.assertIn("not pinned", result["failure_reason"])

    def test_rate_limit_is_failed_even_when_dsh_header_is_missing(self):
        result = self.run_normalizer("dsh", "", stderr="RATE_LIMIT: subscription exhausted")
        self.assertEqual(result["status"], "failed")
        self.assertEqual(result["failure_reason"], "provider quota or rate limit")

    def test_codex_quota_message_is_a_failed_attempt(self):
        result = self.run_normalizer("codex", "", stderr="quota exceeded", exit_code=1)
        self.assertEqual(result["status"], "failed")
        self.assertEqual(result["failure_reason"], "provider quota or rate limit")


if __name__ == "__main__":
    unittest.main()
