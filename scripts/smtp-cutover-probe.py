#!/usr/bin/env python3
"""Verify an SMTP STARTTLS endpoint before changing production traffic."""
import argparse
import json
import os
from pathlib import Path
import smtplib
import ssl
import stat
import sys
from email.message import EmailMessage


def run(args):
    if not 1 <= args.port <= 65535:
        raise ValueError('port must be 1–65535')
    if bool(args.username) != bool(args.password_file):
        raise ValueError('provide username and password-file together')
    if args.send and (not args.username or not args.sender or not args.recipient):
        raise ValueError('sending requires username, password-file, sender and recipient')
    context = ssl.create_default_context(cafile=args.ca_file)
    password = None
    if args.password_file:
        with open(args.password_file, 'rb') as source:
            info = os.fstat(source.fileno())
            if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o077 or info.st_size > 8192:
                raise ValueError('password-file must be a private regular file of at most 8 KiB')
            password = source.read(8193).decode().rstrip('\r\n')
        if not password:
            raise ValueError('password-file is empty')
    result = {'starttls_verified': False, 'authenticated': False, 'message_accepted': False}
    with smtplib.SMTP(args.host, args.port, timeout=15) as smtp:
        smtp.ehlo_or_helo_if_needed()
        smtp.starttls(context=context)
        smtp.ehlo()
        result['starttls_verified'] = True
        if password is not None:
            smtp.login(args.username, password)
            result['authenticated'] = True
        if args.send:
            message = EmailMessage()
            message['From'], message['To'] = args.sender, args.recipient
            message['Subject'] = 'Hakopod SMTP migration check'
            message.set_content('This message was explicitly requested to verify the SMTP migration. Confirm receipt before moving production traffic.\n')
            refused = smtp.send_message(message)
            if refused:
                raise ValueError('the SMTP server did not accept every recipient')
            result['message_accepted'] = True
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--host', required=True)
    parser.add_argument('--port', type=int, default=587)
    parser.add_argument('--ca-file', help='Optional trust bundle for an isolated test certificate')
    parser.add_argument('--username')
    parser.add_argument('--password-file')
    parser.add_argument('--send', action='store_true', help='Explicitly send one verification email')
    parser.add_argument('--sender')
    parser.add_argument('--recipient')
    args = parser.parse_args()
    try:
        print(json.dumps(run(args)))
    except (OSError, ValueError, smtplib.SMTPException) as error:
        # Server responses can echo addresses or authentication material.
        print(json.dumps({'passed': False, 'failure_type': type(error).__name__, 'message': 'SMTP verification failed. Check DNS, firewall, certificate trust, credentials and server diagnostics.'}))
        return 1
    return 0

if __name__ == '__main__':
    sys.exit(main())
