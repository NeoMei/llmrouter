#!/usr/bin/env python3
"""Exercise the backup entry point against isolated SQLite databases."""
import hashlib
import os
from pathlib import Path
import sqlite3
import subprocess
import tempfile
import unittest


SCRIPT = Path(__file__).resolve().with_name('backup.sh')


class SQLiteBackupTest(unittest.TestCase):
    def setUp(self):
        self.fixture = tempfile.TemporaryDirectory(prefix='sqlite backup ', dir=os.environ.get('TMPDIR'))
        self.addCleanup(self.fixture.cleanup)
        self.root = Path(self.fixture.name)
        self.source = self.root / "source ?#% ' $(false).sqlite"
        self.backups = self.root / 'private backups'
        self.tools = self.root / 'tools'
        self.tools.mkdir()
        # Fix the clock at the command boundary so collision checks cannot race a second.
        clock = self.tools / 'date'
        clock.write_text('#!/bin/sh\nprintf "20261028T010203Z\\n"\n')
        clock.chmod(0o700)
        self.env = dict(os.environ, DB_TYPE='sqlite', SQLITE_PATH=str(self.source),
                        BACKUP_DIR=str(self.backups), PATH=f'{self.tools}:{os.environ["PATH"]}')
        self.destination = self.backups / 'gateway-20261028T010203Z.sqlite'

    def database(self):
        connection = sqlite3.connect(self.source)
        self.addCleanup(connection.close)
        connection.execute('CREATE TABLE published(id INTEGER PRIMARY KEY, body TEXT NOT NULL)')
        connection.execute("INSERT INTO published VALUES (1, 'committed')")
        connection.commit()
        return connection

    def run_backup(self, *args):
        return subprocess.run(['/bin/bash', str(SCRIPT), *args], env=self.env,
                              text=True, capture_output=True, timeout=15)

    def test_live_wal_snapshot_retains_committed_schema_and_rows(self):
        connection = sqlite3.connect(self.source)
        self.addCleanup(connection.close)
        self.assertEqual('wal', connection.execute('PRAGMA journal_mode=WAL').fetchone()[0])
        connection.execute('PRAGMA wal_autocheckpoint=0')
        connection.execute('CREATE TABLE published(id INTEGER PRIMARY KEY, body TEXT NOT NULL)')
        connection.execute("INSERT INTO published VALUES (1, 'committed')")
        connection.execute("INSERT INTO published VALUES (2, 'second committed row')")
        connection.commit()
        connection.execute('BEGIN IMMEDIATE')
        connection.execute("INSERT INTO published VALUES (3, 'uncommitted writer')")
        result = self.run_backup()
        self.assertEqual(0, result.returncode, result.stderr)
        with sqlite3.connect(self.destination) as restored:
            schema = restored.execute("SELECT sql FROM sqlite_master WHERE name='published'").fetchone()
            self.assertEqual(('CREATE TABLE published(id INTEGER PRIMARY KEY, body TEXT NOT NULL)',), schema)
            self.assertEqual([(1, 'committed'), (2, 'second committed row')],
                             restored.execute('SELECT * FROM published ORDER BY id').fetchall())
            self.assertEqual([('ok',)], restored.execute('PRAGMA integrity_check').fetchall())
        checksum = Path(str(self.destination) + '.sha256').read_text().split()[0]
        self.assertEqual(hashlib.sha256(self.destination.read_bytes()).hexdigest(), checksum)
        self.assertEqual(0o600, self.destination.stat().st_mode & 0o777)
        self.assertEqual(str(self.destination), result.stdout.strip())

    def test_corrupt_source_does_not_publish_backup_or_checksum(self):
        self.database().close()
        with self.source.open('r+b') as source:
            source.write(b'corrupted SQLite header!')
        result = self.run_backup()
        self.assertNotEqual(0, result.returncode)
        self.assertEqual('', result.stdout)
        self.assertFalse(self.backups.exists() and list(self.backups.iterdir()))

    def test_same_timestamp_never_overwrites_existing_backup(self):
        connection = self.database()
        first = self.run_backup()
        self.assertEqual(0, first.returncode, first.stderr)
        before = {p.name: p.read_bytes() for p in self.backups.iterdir()}
        connection.execute("INSERT INTO published VALUES (2, 'after first backup')")
        connection.commit()
        second = self.run_backup()
        self.assertNotEqual(0, second.returncode)
        self.assertEqual('', second.stdout)
        self.assertEqual(before, {p.name: p.read_bytes() for p in self.backups.iterdir()})

    def test_checksum_collision_does_not_clobber_or_publish_database(self):
        self.database()
        self.backups.mkdir()
        sidecar = Path(str(self.destination) + '.sha256')
        sidecar.write_text('existing checksum artifact\n')
        result = self.run_backup()
        self.assertNotEqual(0, result.returncode)
        self.assertFalse(self.destination.exists())
        self.assertEqual('existing checksum artifact\n', sidecar.read_text())
        self.assertEqual([sidecar], list(self.backups.iterdir()))

    def test_check_validates_readable_database_without_creating_backup(self):
        self.database()
        result = self.run_backup('--check')
        self.assertEqual(0, result.returncode, result.stderr)
        self.assertFalse(self.backups.exists())
        self.source.unlink()
        result = self.run_backup('--check')
        self.assertNotEqual(0, result.returncode)
        self.assertFalse(self.source.exists())

    def test_check_requires_python_sqlite_support(self):
        self.database()
        self.env['PATH'] = str(self.tools)
        result = self.run_backup('--check')
        self.assertNotEqual(0, result.returncode)
        self.assertFalse(self.backups.exists())

    def test_exclusive_writer_fails_without_publishing_incomplete_backup(self):
        connection = self.database()
        connection.execute('BEGIN EXCLUSIVE')
        connection.execute("INSERT INTO published VALUES (2, 'locked transaction')")
        result = self.run_backup()
        self.assertNotEqual(0, result.returncode)
        self.assertEqual('', result.stdout)
        self.assertFalse(self.backups.exists() and list(self.backups.iterdir()))

    def test_check_rejects_corruption(self):
        self.database().close()
        with self.source.open('r+b') as source:
            source.write(b'corrupted SQLite header!')
        result = self.run_backup('--check')
        self.assertNotEqual(0, result.returncode)
        self.assertFalse(self.backups.exists())


if __name__ == '__main__':
    unittest.main()
