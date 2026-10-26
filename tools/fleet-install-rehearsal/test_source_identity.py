import importlib.util
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

module = importlib.util.spec_from_file_location('source_identity', Path(__file__).with_name('source_identity.py'))
source = importlib.util.module_from_spec(module)
module.loader.exec_module(source)


class SourceIdentityTests(unittest.TestCase):
    def test_archive_does_not_inherit_parent_git_commit(self):
        with tempfile.TemporaryDirectory() as tmp:
            parent = Path(tmp)
            subprocess.run(['git', 'init', '-q', str(parent)], check=True)
            subprocess.run(['git', '-C', str(parent), '-c', 'user.name=Fixture', '-c',
                            'user.email=fixture@example.invalid', 'commit', '-q', '--allow-empty',
                            '-m', 'fixture'], check=True)
            archive = parent / 'archive'
            archive.mkdir()
            (archive / 'main.go').write_text('package main\n')
            self.assertIsNone(source.identity(archive)['git_commit'])
            self.assertIsNotNone(source.identity(parent)['git_commit'])

    def test_database_guard_refuses_remote_or_nondisposable_databases(self):
        script = Path(__file__).with_name('run.sh').resolve()
        for url in ('postgres://localhost:5432/production',
                    'postgres://remote.example:35439/marketplace_rehearsal',
                    'postgres://localhost:543/marketplace_rehearsal'):
            with self.subTest(url=url), tempfile.TemporaryDirectory() as tmp:
                env = dict(os.environ, DATABASE_URL=url)
                result = subprocess.run(['bash', str(script), tmp, tmp, tmp], env=env,
                                        text=True, capture_output=True)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn('Refusing database', result.stderr)

    def test_source_digest_detects_path_and_content_changes(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            file = root / 'main.go'
            file.write_text('one')
            first = source.identity(root)['source_tree_sha256']
            file.write_text('two')
            second = source.identity(root)['source_tree_sha256']
            file.rename(root / 'other.go')
            third = source.identity(root)['source_tree_sha256']
            self.assertEqual(3, len({first, second, third}))


if __name__ == '__main__':
    unittest.main()
