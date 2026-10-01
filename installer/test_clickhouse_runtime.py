"""Pure runtime-template tests; no host or cluster mutation."""
from pathlib import Path
import unittest
import subprocess
from unittest.mock import patch
import clickhouse_runtime as runtime
from clickhouse_runtime import extend_template, profile


class ClickHouseRuntimeTests(unittest.TestCase):
    def test_attestation_removal_failure_stops_before_runtime_download(self):
        review={'created_at':int(runtime.time.time()),'node':'owned-node'}
        with patch.object(runtime,'plan',return_value=review),patch.object(runtime.TEMPLATE.__class__,'exists',return_value=False),patch.object(runtime.subprocess,'run',side_effect=subprocess.CalledProcessError(1,['kubectl'])) as run,patch.object(runtime.urllib.request,'urlopen') as download:
            with self.assertRaises(subprocess.CalledProcessError):runtime.apply(review)
            self.assertTrue(run.call_args.kwargs['check'])
            self.assertIn(runtime.LABEL+'-',run.call_args.args[0])
            download.assert_not_called()

    def test_attestation_must_be_absent_after_successful_removal(self):
        for labels,valid in [({},True),({runtime.LABEL:runtime.PROFILE},False)]:
            with self.subTest(labels=labels),patch.object(runtime.subprocess,'run'),patch.object(runtime,'read',return_value={'metadata':{'labels':labels}}):
                if valid:runtime.clear_attestation('owned-node')
                else:
                    with self.assertRaisesRegex(ValueError,'attestation remains'):runtime.clear_attestation('owned-node')

    def test_keeps_existing_actions_and_default_runtime(self):
        current='{{ template "base" . }}\n[custom]\nvalue="unchanged"\n# actions runtime is configured separately\n'
        root=Path('/opt/hakopod/clickhouse-runtime/release')
        result=extend_template(current,root)
        self.assertTrue(result.startswith(current))
        self.assertEqual(extend_template(result,root),result)
        self.assertNotIn('default_runtime_name',result)
        self.assertNotIn('allow-packet-socket-write',profile(root))
        self.assertIn('systrap-disable-syscall-patching = "true"',profile(root))

    def test_does_not_replace_an_unowned_or_changed_handler(self):
        root=Path('/opt/hakopod/clickhouse-runtime/release')
        for original in ('[runtimes.hakopod-clickhouse]',extend_template('',root).replace('io.containerd.runsc.v1','io.containerd.runc.v2')):
            with self.assertRaises(ValueError):extend_template(original,root)
