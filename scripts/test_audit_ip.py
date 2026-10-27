import json
import os
import pathlib
import subprocess
import sys
import tempfile
import unittest

from audit_ip import REMOTE


class AuditTest(unittest.TestCase):
    @unittest.skipIf(os.geteuid() == 0, "root can read mode-000 fixtures")
    def test_reports_denied_directories_as_well_as_files(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            good, denied_file, denied_dir = root / "config", root / "secret", root / "private"
            good.write_text("server=203.0.113.1")
            denied_file.write_text("server=203.0.113.1")
            denied_dir.mkdir()
            (denied_dir / "hidden").write_text("server=203.0.113.1")
            denied_file.chmod(0)
            denied_dir.chmod(0)
            try:
                result = subprocess.run(
                    [sys.executable, "-c", REMOTE, "203.0.113.1", directory],
                    text=True, capture_output=True, check=True,
                )
                audit = json.loads(result.stdout)
                self.assertEqual(audit["matches"], [{"path": str(good), "occurrences": 1}])
                self.assertEqual(set(audit["unreadable_paths"]), {str(denied_file), str(denied_dir)})
                self.assertEqual(audit["unreadable"], 2)
                self.assertNotIn("server=", result.stdout)
            finally:
                denied_file.chmod(0o600)
                denied_dir.chmod(0o700)


if __name__ == "__main__":
    unittest.main()
