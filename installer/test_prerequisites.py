import importlib.util
from pathlib import Path
import subprocess
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('prerequisites', Path(__file__).with_name('prerequisites.py'))
prerequisites = importlib.util.module_from_spec(spec); spec.loader.exec_module(prerequisites)


class PrerequisitesTest(unittest.TestCase):
    def test_containerd_default_does_not_install_docker_or_postgres_server(self):
        self.assertNotIn('docker.io', prerequisites.requested({}))
        self.assertNotIn('postgresql', prerequisites.requested({}))
        self.assertIn('postgresql-client', prerequisites.requested({'database_mode': 'external'}))

    def test_explicit_docker_preserves_an_existing_daemon(self):
        with patch.object(prerequisites.shutil, 'which', return_value='/usr/bin/dockerd'):
            self.assertNotIn('docker.io', prerequisites.requested({'install_docker': True}))
        with patch.object(prerequisites.shutil, 'which', return_value=None):
            self.assertIn('docker.io', prerequisites.requested({'install_docker': True}))
        with patch.object(prerequisites.shutil, 'which', side_effect=lambda value: '/usr/bin/docker' if value == 'docker' else None):
            with self.assertRaisesRegex(ValueError, 'instead of replacing'):
                prerequisites.requested({'install_docker': True})

    def test_installs_only_missing_packages_without_global_upgrade(self):
        with patch.object(prerequisites.platform, 'system', return_value='Linux'), patch.object(prerequisites.os, 'geteuid', return_value=0), \
                patch.object(prerequisites.shutil, 'which', return_value='/usr/bin/apt-get'), \
                patch.object(prerequisites, 'missing', side_effect=[['kmod', 'postgresql-client'], []]), \
                patch.object(prerequisites.subprocess, 'run') as run:
            prerequisites.install({'database_mode': 'external'})
            commands = [call.args[0] for call in run.call_args_list]
            self.assertEqual(len(commands), 2)
            self.assertEqual(commands[1][-2:], ['kmod', 'postgresql-client'])
            self.assertFalse(any('upgrade' in command for command in commands))
            self.assertTrue(all(call.kwargs['timeout'] <= 1200 for call in run.call_args_list))

    def test_prepared_host_has_no_package_mutations(self):
        with patch.object(prerequisites.platform, 'system', return_value='Linux'), patch.object(prerequisites.os, 'geteuid', return_value=0), \
                patch.object(prerequisites.shutil, 'which', return_value='/usr/bin/apt-get'), \
                patch.object(prerequisites, 'missing', return_value=[]), patch.object(prerequisites.subprocess, 'run') as run:
            prerequisites.install({})
            run.assert_not_called()


if __name__ == '__main__':
    unittest.main()
