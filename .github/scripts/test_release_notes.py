import unittest

from release_notes import extract_release_notes


class ReleaseNotesTests(unittest.TestCase):
    def test_extracts_only_matching_version(self):
        changelog = """# 更新日志
## [未发布]
- 尚未发布

## [v1.2.3] - 2026-09-27
### 新增
- 支持中文发布说明。

## [v1.2.2] - 2026-09-20
- 旧版本。
"""
        self.assertEqual(extract_release_notes(changelog, "v1.2.3"), "### 新增\n- 支持中文发布说明。\n")

    def test_rejects_missing_or_duplicate_version(self):
        section = "## [v1.2.3] - 2026-09-27\n- 修复登录问题。\n"
        with self.assertRaises(ValueError):
            extract_release_notes(section, "v1.2.4")
        with self.assertRaises(ValueError):
            extract_release_notes(section + section, "v1.2.3")

    def test_rejects_empty_or_english_only_notes(self):
        for body in ("", "### 修复\n", "- Fix login issue.\n"):
            with self.subTest(body=body), self.assertRaises(ValueError):
                extract_release_notes("## [v1.2.3] - 2026-09-27\n" + body, "v1.2.3")

    def test_rejects_invalid_date(self):
        with self.assertRaises(ValueError):
            extract_release_notes("## [v1.2.3] - 2026-02-30\n- 修复登录问题。\n", "v1.2.3")


if __name__ == "__main__":
    unittest.main()
