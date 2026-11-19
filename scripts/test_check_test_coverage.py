import tempfile
from pathlib import Path
import unittest

from check_test_coverage import compare, read_baseline, read_profile


class CoverageGateTests(unittest.TestCase):
    def profile(self, content):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory)/"coverage.out"
            path.write_text(content)
            return read_profile(path)

    def test_counts_both_scopes(self):
        values = self.profile("mode: atomic\nexample/pkg/a.go:1.1,2.2 3 4\nexample/pkg/wbc_generated.go:1.1,2.2 100 0\n")
        self.assertEqual(values, {"example/pkg": [3, 103, 3, 3]})

    def test_unchanged_and_improved_pass(self):
        baseline = {"pkg": [3, 10, 2, 5]}
        self.assertEqual(compare(baseline, baseline), [])
        self.assertEqual(compare({"pkg": [4, 10, 3, 5]}, baseline), [])

    def test_missing_package_fails(self):
        self.assertTrue(compare({}, {"pkg": [0, 1, 0, 1]}))

    def test_covered_count_and_fraction_cannot_drop(self):
        baseline = {"pkg": [5, 10, 5, 10]}
        self.assertTrue(compare({"pkg": [4, 8, 4, 8]}, baseline))
        self.assertTrue(compare({"pkg": [5, 11, 5, 11]}, baseline))

    def test_generated_gains_cannot_mask_handwritten_loss(self):
        self.assertTrue(compare({"pkg": [100, 100, 4, 10]}, {"pkg": [50, 100, 5, 10]}))

    def test_new_uncovered_package_cannot_dilute_total_coverage(self):
        baseline = {"pkg": [5, 10, 5, 10]}
        current = {**baseline, "new/pkg": [0, 100, 0, 100]}
        self.assertTrue(compare(current, baseline))

    def test_invalid_profiles_fail_closed(self):
        for content in ("", "mode: atomic\n", "mode: unknown\n", "mode: atomic\nx/a.go:1.1,2.2 -1 1\n", "mode: atomic\nx/a.go:1.1,2.2 1 1\nx/a.go:1.1,2.2 1 1\n"):
            with self.subTest(content=content), self.assertRaises(ValueError):
                self.profile(content)

    def test_empty_baseline_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory)/"baseline.tsv"
            path.write_text("# no entries\n")
            with self.assertRaises(ValueError):
                read_baseline(path)


if __name__ == "__main__":
    unittest.main()
