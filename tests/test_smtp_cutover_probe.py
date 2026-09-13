import argparse
import importlib.util
from pathlib import Path
import smtplib
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('smtp_probe', Path(__file__).resolve().parents[1] / 'scripts' / 'smtp-cutover-probe.py')
probe = importlib.util.module_from_spec(spec)
spec.loader.exec_module(probe)

class ProbeTests(unittest.TestCase):
    def args(self, **values):
        return argparse.Namespace(**dict(dict(host='smtp.example.test', port=587, ca_file=None, username=None, password_file=None, send=False, sender=None, recipient=None), **values))

    def test_tls_is_required_before_authentication(self):
        with tempfile.TemporaryDirectory() as tmp:
            password = Path(tmp) / 'password'
            password.write_text('fixture-not-real')
            password.chmod(0o600)
            with patch.object(probe.smtplib, 'SMTP') as smtp:
                session = smtp.return_value.__enter__.return_value
                session.starttls.side_effect = smtplib.SMTPNotSupportedError('no TLS')
                with self.assertRaises(smtplib.SMTPNotSupportedError):
                    probe.run(self.args(username='test', password_file=str(password)))
                session.login.assert_not_called()
                session.send_message.assert_not_called()

    def test_handshake_never_sends_without_explicit_request(self):
        with patch.object(probe.smtplib, 'SMTP') as smtp:
            result = probe.run(self.args())
            self.assertTrue(result['starttls_verified'])
            self.assertFalse(result['message_accepted'])
            session = smtp.return_value.__enter__.return_value
            context = session.starttls.call_args.kwargs['context']
            self.assertTrue(context.check_hostname)
            self.assertEqual(context.verify_mode, probe.ssl.CERT_REQUIRED)
            session.login.assert_not_called()
            session.send_message.assert_not_called()

    def test_send_requires_explicit_recipient_and_authentication(self):
        with patch.object(probe.smtplib, 'SMTP') as smtp:
            with self.assertRaises(ValueError): probe.run(self.args(send=True))
            smtp.assert_not_called()

if __name__ == '__main__': unittest.main()
