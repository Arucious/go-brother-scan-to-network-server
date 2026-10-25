from pathlib import Path
import json
import os
import tempfile
import unittest
from unittest.mock import patch
from upload import Uploader


class QueueTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.source, self.remote, self.state = (self.root / n for n in ('scans', 'nas', 'state'))
        self.source.mkdir()
        self.remote.mkdir()
        config = self.root / 'rclone.conf'
        config.write_text('')
        self.uploader = Uploader(self.source, self.state, str(self.remote),
                                 os.environ['RCLONE_TEST_BINARY'], config, min_age=0)

    def scan(self, name='scan.pdf', data=b'%PDF-test'):
        path = self.source / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(data)
        return path

    def test_verified_transfer_then_nas_reorganization_does_not_reupload(self):
        self.scan('year/scan.pdf')
        self.assertEqual(self.uploader.run_once(), 1)
        self.assertFalse((self.source / 'year/scan.pdf').exists())
        self.assertEqual((self.source / 'uploaded/year/scan.pdf').read_bytes(), b'%PDF-test')
        (self.remote / 'year/scan.pdf').rename(self.remote / 'organized.pdf')
        self.assertEqual(self.uploader.run_once(), 0)
        self.assertFalse((self.remote / 'year/scan.pdf').exists())

    def test_upload_failure_keeps_original_and_has_no_receipt(self):
        source = self.scan()
        with patch.object(self.uploader, 'upload', side_effect=RuntimeError('offline')):
            with self.assertRaises(RuntimeError):
                self.uploader.run_once()
        self.assertTrue(source.exists())
        self.assertFalse((self.state / 'receipts.json').exists())

    def test_bad_verification_keeps_original(self):
        source = self.scan()
        with patch.object(self.uploader, 'remote_digest', return_value='wrong'):
            with self.assertRaises(RuntimeError):
                self.uploader.run_once()
        self.assertTrue(source.exists())
        self.assertFalse((self.state / 'receipts.json').exists())

    def test_existing_different_nas_file_is_not_overwritten(self):
        source = self.scan(data=b'source differs in length')
        (self.remote / 'scan.pdf').write_bytes(b'original NAS file')
        with self.assertRaises(RuntimeError):
            self.uploader.run_once()
        self.assertEqual((self.remote / 'scan.pdf').read_bytes(), b'original NAS file')
        self.assertTrue(source.exists())

    def test_receipt_survives_failed_local_move_and_nas_reorganization(self):
        source = self.scan()
        with patch.object(self.uploader, 'archive_local', side_effect=RuntimeError('move failed')):
            with self.assertRaises(RuntimeError):
                self.uploader.run_once()
        self.assertTrue(source.exists())
        self.assertIn('scan.pdf', json.loads((self.state / 'receipts.json').read_text()))
        (self.remote / 'scan.pdf').rename(self.remote / 'organized.pdf')
        with patch.object(self.uploader, 'upload', side_effect=AssertionError('must not upload again')):
            self.assertEqual(self.uploader.run_once(), 1)
        self.assertFalse((self.remote / 'scan.pdf').exists())
        self.assertFalse(source.exists())

    def test_receipt_write_failure_keeps_original(self):
        source = self.scan()
        with patch('upload.save_json', side_effect=OSError('disk full')):
            with self.assertRaises(OSError):
                self.uploader.run_once()
        self.assertTrue(source.exists())

    def test_unfinished_archived_and_symlink_files_are_excluded(self):
        self.scan('.scan-secret.part')
        self.scan('unfinished.pdf.part')
        self.scan('uploaded/old.pdf')
        (self.source / 'link.pdf').symlink_to(self.source / 'uploaded/old.pdf')
        self.assertEqual(self.uploader.run_once(), 0)
        self.assertEqual(list(self.remote.iterdir()), [])


if __name__ == '__main__':
    unittest.main(verbosity=2)
