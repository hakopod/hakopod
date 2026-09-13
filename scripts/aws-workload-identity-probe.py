#!/usr/bin/env python3
"""Read-only AWS acceptance probe; run inside a configured workload using stdin."""

import argparse
import datetime
import hashlib
import hmac
import os
import re
import sys
import urllib.error
import urllib.parse
import urllib.request
import xml.etree.ElementTree as ET


class ProbeFailure(Exception):
    pass


def request(endpoint, fields, headers=None):
    body = urllib.parse.urlencode(fields).encode()
    req = urllib.request.Request(endpoint, data=body, headers=headers or {})
    # Never send the bearer token to a proxy or follow redirects to another host.
    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, req, fp, code, msg, headers, newurl):
            return None

    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    try:
        with opener.open(req, timeout=15) as response:
            data = response.read(65537)
    except urllib.error.HTTPError as err:
        try:
            tree = ET.fromstring(err.read(65536))
            code = next((item.text for item in tree.iter() if item.tag.endswith('}Code') or item.tag == 'Code'), 'HTTPError')
        except ET.ParseError:
            code = 'HTTPError'
        # An allowlisted status code is enough for diagnosis; AWS response bodies
        # and tokens never reach stdout or exception tracebacks.
        code = code if code in {'AccessDenied', 'InvalidIdentityToken', 'IDPRejectedClaim', 'ExpiredToken', 'IDPCommunicationError'} else 'HTTPError'
        raise ProbeFailure(code) from None
    if len(data) > 65536:
        raise ProbeFailure('AWS response exceeds probe limit')
    tree = ET.fromstring(data)
    return {item.tag.rsplit('}', 1)[-1]: item.text for item in tree.iter() if item.text}


def caller_identity(endpoint, region, credentials):
    fields = {'Action': 'GetCallerIdentity', 'Version': '2011-06-15'}
    body = urllib.parse.urlencode(fields).encode()
    now = datetime.datetime.now(datetime.timezone.utc)
    stamp, day = now.strftime('%Y%m%dT%H%M%SZ'), now.strftime('%Y%m%d')
    host = urllib.parse.urlparse(endpoint).netloc
    headers = {
        'content-type': 'application/x-www-form-urlencoded; charset=utf-8',
        'host': host,
        'x-amz-date': stamp,
        'x-amz-security-token': credentials['SessionToken'],
    }
    signed_headers = ';'.join(sorted(headers))
    canonical_headers = ''.join(key + ':' + headers[key] + '\n' for key in sorted(headers))
    canonical = '\n'.join(['POST', '/', '', canonical_headers, signed_headers, hashlib.sha256(body).hexdigest()])
    scope = '/'.join([day, region, 'sts', 'aws4_request'])
    to_sign = '\n'.join(['AWS4-HMAC-SHA256', stamp, scope, hashlib.sha256(canonical.encode()).hexdigest()])
    signing_key = ('AWS4' + credentials['SecretAccessKey']).encode()
    for value in [day, region, 'sts', 'aws4_request']:
        signing_key = hmac.new(signing_key, value.encode(), hashlib.sha256).digest()
    signature = hmac.new(signing_key, to_sign.encode(), hashlib.sha256).hexdigest()
    headers['authorization'] = 'AWS4-HMAC-SHA256 Credential=' + credentials['AccessKeyId'] + '/' + scope + ', SignedHeaders=' + signed_headers + ', Signature=' + signature
    return request(endpoint, fields, headers)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--deny-role-arn', required=True, help='A real test role whose trust policy excludes this workload subject')
    args = parser.parse_args()
    role, region = os.environ['AWS_ROLE_ARN'], os.environ['AWS_REGION']
    match = re.fullmatch(r'arn:(aws|aws-cn|aws-us-gov):iam::([0-9]{12}):role/([A-Za-z0-9+=,.@_/-]+)', role)
    if not match or not re.fullmatch(r'[a-z]{2}(-[a-z]+){1,3}-[0-9]', region):
        raise ProbeFailure('invalid configured role or region')
    if args.deny_role_arn == role or not re.fullmatch(r'arn:(aws|aws-cn|aws-us-gov):iam::[0-9]{12}:role/[A-Za-z0-9+=,.@_/-]+', args.deny_role_arn):
        raise ProbeFailure('negative probe requires a different, existing IAM test role')
    with open(os.environ['AWS_WEB_IDENTITY_TOKEN_FILE']) as source:
        token = source.read(65537)
    if len(token) > 65536:
        raise ProbeFailure('token exceeds probe limit')
    suffix = 'amazonaws.com.cn' if match[1] == 'aws-cn' else 'amazonaws.com'
    endpoint = 'https://sts.' + region + '.' + suffix + '/'
    fields = {'Action': 'AssumeRoleWithWebIdentity', 'Version': '2011-06-15', 'RoleArn': role, 'RoleSessionName': 'hakopod-acceptance', 'WebIdentityToken': token, 'DurationSeconds': '900'}
    credentials = request(endpoint, fields)
    identity = caller_identity(endpoint, region, credentials)
    expected = 'arn:' + match[1] + ':sts::' + match[2] + ':assumed-role/' + match[3].rsplit('/', 1)[-1] + '/'
    if not identity['Arn'].startswith(expected) or identity['Account'] != match[2]:
        raise ProbeFailure('STS returned an unexpected account or role')
    print('PASS: AWS STS issued short-lived credentials for the approved workload role.')
    fields['RoleArn'] = args.deny_role_arn
    try:
        request(endpoint, fields)
    except ProbeFailure as err:
        if str(err) != 'AccessDenied':
            raise ProbeFailure('negative role probe did not return AccessDenied') from None
    else:
        raise ProbeFailure('another IAM role accepted this workload; tighten its trust policy')
    print('PASS: another IAM role refused this workload subject.')
    print('AWS identity verified. Application-specific SES/S3 permissions still need their own acceptance checks.')


if __name__ == '__main__':
    try:
        main()
    except ProbeFailure as error:
        sys.exit('FAIL: ' + str(error))
    except Exception:
        sys.exit('FAIL: AWS identity probe could not complete; check network, clock, token and IAM configuration.')
