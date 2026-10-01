#!/usr/bin/env python3
"""Apply the managed Oracle hardening profile to the exact upstream source.

This script edits source only. It does not fetch licensed database images,
accept Oracle terms, build containers, or enable the Enterprise runtime gate.
Run source builds and native qualification separately on the development VM.
"""

from pathlib import Path
import argparse
import shutil
import subprocess

SOURCE = "ff6f9178c1650df30afbf203ebdb633e9b80760a"
PROFILE = "hakopod-oracle-tcps-v1"


def replace(text, old, new, count=1):
    actual = text.count(old)
    if actual != count:
        raise SystemExit(f"Oracle upstream patch context changed: expected {count}, found {actual}")
    return text.replace(old, new)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("source", type=Path)
    args = parser.parse_args()
    root = args.source.resolve()
    head = subprocess.check_output(["git", "-C", str(root), "rev-parse", "HEAD"], text=True).strip()
    if head != SOURCE:
        raise SystemExit("Oracle source must be the pinned 2.2.0 commit")
    status = subprocess.check_output(["git", "-C", str(root), "status", "--porcelain"], text=True)
    if status.strip():
        raise SystemExit("Oracle source must be clean before applying the managed profile")

    changes = {}
    path = root / "controllers/dataguard/dataguardbroker_topology_execution.go"
    text = path.read_text()
    text = replace(text, '\t"encoding/base64"\n', '\t"encoding/base64"\n\t"encoding/json"\n')
    text = replace(text, '\tlines := []string{\n\t\t"set -euo pipefail",\n\t\tfmt.Sprintf("WALLET_DIR=%s", shellQuote(walletDir)),', '''\tmanagedLines := []string{"set -euo pipefail", "umask 077", fmt.Sprintf("mkdir -p %s", shellQuote(walletDir))}
\tmanagedRequest := func(operation, value string, arguments []string) {
\t\traw, _ := json.Marshal(map[string]any{"operation": operation, "wallet": walletDir, "password": walletPassword, "value": value, "arguments": arguments})
\t\tmanagedLines = append(managedLines, fmt.Sprintf("python3 /etc/hakopod-oracle/wallet.py <<'__HAKOPOD_WALLET__'\\n%s\\n__HAKOPOD_WALLET__", string(raw)))
\t}
\tmanagedLines = append(managedLines, fmt.Sprintf("rm -f -- %s/ewallet.p12 %s/cwallet.sso", shellQuote(walletDir), shellQuote(walletDir)))
\tmanagedRequest("create", "", []string{})
\tfor _, member := range members {
\t\tif member == nil { continue }
\t\taliases := []string{member.Alias, member.StaticAlias, member.ConnectString}
\t\tif descriptor, err := buildDataguardStaticConnectDescriptor(member); err == nil && strings.TrimSpace(descriptor) != "" { aliases = append(aliases, descriptor) }
\t\tseen := map[string]bool{}
\t\tfor _, alias := range aliases {
\t\t\talias = strings.TrimSpace(alias)
\t\t\tif alias != "" && !seen[alias] {
\t\t\t\tmanagedRequest("credential", member.AdminPassword, []string{alias, "sys"})
\t\t\t\tseen[alias] = true
\t\t\t}
\t\t}
\t}
\tlines := []string{
\t\t"set -euo pipefail",
\t\tfmt.Sprintf("WALLET_DIR=%s", shellQuote(walletDir)),''')
    text = replace(text, '\tlines = append(lines, "rm -f \\"$WALLET_DIR/.wallet.passwd\\"")\n\treturn strings.Join(lines, "\\n") + "\\n"', '\tlines = append(lines, "rm -f \\"$WALLET_DIR/.wallet.passwd\\"")\n\treturn "if [ \\\"${HAKOPOD_ORACLE_POLICY:-}\\\" = \\\"hakopod-oracle-tcps-v1\\\" ]; then\\n" + strings.Join(managedLines, "\\n") + "\\nelse\\n" + strings.Join(lines, "\\n") + "\\nfi\\n"')
    text = replace(text, "SSL_SERVER_DN_MATCH = NO", "SSL_SERVER_DN_MATCH = YES")
    text = replace(text, "SSL_SERVER_DN_MATCH=NO", "SSL_SERVER_DN_MATCH=YES", 2)
    text = replace(text, '"SSL_SERVER_DN_MATCH=YES")', '"SSL_SERVER_DN_MATCH=YES", "SSL_VERSION=1.2", "SQLNET.INBOUND_CONNECT_TIMEOUT=15", "SQLNET.RECV_TIMEOUT=60", "SQLNET.SEND_TIMEOUT=60")')
    text = replace(text, 'command_status=0; { %s; } > >(tee /proc/1/fd/1) 2> >(tee /proc/1/fd/2 >&2) || command_status=$?; %s; exit $command_status', 'command_status=0; { %s; } || command_status=$?; %s; exit $command_status')
    # Stream the generated shell through stdin so passwords cannot enter the
    # Kubernetes exec URL or its API audit record.
    text = replace(text, 'dbcommons.ExecCommand(r, r.Config, podName, broker.Namespace, dataguardBrokerRunnerContainerName, ctx, req, nolog, "bash", "-c", command)', 'dbcommons.ExecCommandWithInput(r, r.Config, podName, broker.Namespace, dataguardBrokerRunnerContainerName, ctx, req, true, command, "bash", "-s")')
    # The configured initial primary is not necessarily the former primary on a
    # second switchover. Re-enable apply on every now-physical standby, using the
    # observed broker roles. This remains idempotent when the request retries.
    text = replace(text, '\tformerPrimary := state.Primary\n\tif formerPrimary != nil && !strings.EqualFold(formerPrimary.DBUniqueName, targetDBUniqueName) {', '\tfor _, formerPrimary := range state.Members {\n\t\tif formerPrimary == nil || strings.EqualFold(formerPrimary.DBUniqueName, targetDBUniqueName) { continue }')
    changes[path] = text

    path = root / "controllers/dataguard/dataguardbroker_reconcile_helpers.go"
    text = path.read_text()
    text = replace(text, '\t\tlogDataguardBrokerRunnerCreation(ctx, r, broker, runtime, podName, runtimeHash)\n', '''\t\tif broker.Annotations["hakopod.io/oracle-pod-policy"] != "" {
\t\t\tprevious, listErr := listDataguardBrokerRunnerPods(ctx, r.Client, broker)
\t\t\tif listErr != nil { return false, "", listErr }
\t\t\tfor i := range previous {
\t\t\t\told := &previous[i]
\t\t\t\tif !metav1.IsControlledBy(old, broker) { return false, "", fmt.Errorf("managed Oracle runner ownership changed") }
\t\t\t\tif old.DeletionTimestamp.IsZero() {
\t\t\t\t\tif deleteErr := clientIgnoreNotFound(r.Delete(ctx, old, client.Preconditions{UID: &old.UID, ResourceVersion: &old.ResourceVersion})); deleteErr != nil { return false, "", deleteErr }
\t\t\t\t}
\t\t\t\treturn false, "waiting for the previous managed Oracle runner to terminate", nil
\t\t\t}
\t\t}
\t\tlogDataguardBrokerRunnerCreation(ctx, r, broker, runtime, podName, runtimeHash)
''')
    text = replace(text, '\t\t\tdataguardBrokerRunnerLabelComponent: dataguardBrokerRunnerComponentValue,\n\t\t},\n\t); err != nil {', '\t\t\tdataguardBrokerRunnerLabelComponent: dataguardBrokerRunnerComponentValue,\n\t\t},\n\t\tclient.Limit(3),\n\t); err != nil {')
    text = replace(text, '\treturn pods.Items, nil\n', '\tif pods.Continue != "" || len(pods.Items) > 2 { return nil, fmt.Errorf("Oracle runner inventory exceeds its bound") }\n\treturn pods.Items, nil\n')
    text = replace(text, '\t\tpod := buildDataguardBrokerRunnerPod(broker, runtime, runtimeHash)\n', '\t\tpod := buildDataguardBrokerRunnerPod(broker, runtime, runtimeHash)\n\t\tif err := dbcommons.ApplyHakopodPodPolicy(pod, broker.ObjectMeta); err != nil {\n\t\t\treturn false, "", err\n\t\t}\n')
    text = replace(text, '\t\t"executionKey":     executionCandidateKey(candidate),', '\t\t"executionKey":     executionCandidateKey(candidate),\n\t\t"hakopodPodPolicy": broker.Annotations["hakopod.io/oracle-pod-policy"],')
    changes[path] = text

    path = root / "controllers/database/singleinstancedatabase_controller.go"
    text = path.read_text()
    text = replace(text, '\tapplyPrimarySeparationPreference(pod, m, rp)\n', '\tapplyPrimarySeparationPreference(pod, m, rp)\n\tif err := dbcommons.ApplyHakopodPodPolicy(pod, m.ObjectMeta); err != nil {\n\t\treturn nil, err\n\t}\n')
    text = replace(text, '\tadminPassword := string(secret.Data[GetAdminPasswordSecretFileName(m)])\n', '''\tadminPassword := string(secret.Data[GetAdminPasswordSecretFileName(m)])
\tif m.Annotations["hakopod.io/oracle-pod-policy"] != "" {
\t\t_, err = dbcommons.ExecCommandWithInput(r, r.Config, pod.Name, pod.Namespace, walletContainer, ctx, req, true, dbcommons.ManagedOracleAdminWalletCommand, "bash", "-s")
\t\tif err != nil { return requeueY, err }
\t\treturn requeueN, nil
\t}
''')
    changes[path] = text

    path = root / "commons/database/constants.go"
    text = path.read_text()
    text = replace(text, 'const EnableTcpsCMD string = "if [ -x ', 'const EnableTcpsCMD string = "if [ \\"${HAKOPOD_ORACLE_POLICY:-}\\" = \\"hakopod-oracle-tcps-v1\\" ]; then bash /etc/hakopod-oracle/config-tcps.sh; elif [ -x ')
    text = replace(text, 'const DisableTcpsCMD string = "if [ -x ', 'const DisableTcpsCMD string = "if [ \\"${HAKOPOD_ORACLE_POLICY:-}\\" = \\"hakopod-oracle-tcps-v1\\" ]; then exit 1; elif [ -x ')
    text = replace(text, 'const WalletExistsCMD string = "if [ -f ${WALLET_DIR}/ewallet.p12 ]; then echo present; fi"', 'const WalletExistsCMD string = "if [ \\"${HAKOPOD_ORACLE_POLICY:-}\\" = \\"hakopod-oracle-tcps-v1\\" ]; then if [ -f ${WALLET_DIR}/.hakopod-ready ]; then echo present; fi; elif [ -f ${WALLET_DIR}/ewallet.p12 ]; then echo present; fi"')
    changes[path] = text

    path = root / "commons/database/utils.go"
    text = path.read_text()
    text = replace(text, '\t"bytes"\n', '')
    text = replace(text, '\t\texecOut bytes.Buffer\n\t\texecErr bytes.Buffer', '\t\texecOut oracleCommandBuffer\n\t\texecErr oracleCommandBuffer', 2)
    text = replace(text, '\tlog := ctrllog.FromContext(ctx).WithValues("ExecCommand", req.NamespacedName)', '''\tctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
\tdefer cancel()
\tif len(command) == 3 && (command[0] == "bash" || command[0] == "/bin/bash" || command[0] == "sh" || command[0] == "/bin/sh") && command[1] == "-c" {
\t\treturn ExecCommandWithInput(r, config, podName, namespace, containerName, ctx, req, true, command[2], command[0], "-s")
\t}
\tlog := ctrllog.FromContext(ctx).WithValues("ExecCommand", req.NamespacedName)''')
    text = replace(text, '\tlog := ctrllog.FromContext(ctx).WithValues("ExecCommandWithInput", req.NamespacedName)', '''\tctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
\tdefer cancel()
\tlog := ctrllog.FromContext(ctx).WithValues("ExecCommandWithInput", req.NamespacedName)''')
    old = '''\tif err != nil {
\t\tstdout := strings.TrimSpace(execOut.String())
\t\tstderr := strings.TrimSpace(execErr.String())
\t\tif stdout != "" || stderr != "" {
\t\t\treturn stdout, fmt.Errorf("exec failed: %w; stdout: %s; stderr: %s", err, stdout, stderr)
\t\t}
\t\treturn stdout, err
\t}
\tif execErr.Len() > 0 {
\t\tstdout := strings.TrimSpace(execOut.String())
\t\tstderr := strings.TrimSpace(execErr.String())
\t\treturn stdout, fmt.Errorf("stderr: %s", stderr)
\t}'''
    new = '''\tif err != nil || execErr.Len() > 0 {
\t\treturn oracleCommandFailure(execOut.String(), execErr.String())
\t}'''
    text = replace(text, old, new, 2)
    changes[path] = text

    # Upstream regression expectations must require name verification too.
    path = root / "controllers/dataguard/dataguardbroker_topology_execution_test.go"
    text = path.read_text().replace("SSL_SERVER_DN_MATCH=NO", "SSL_SERVER_DN_MATCH=YES").replace("SSL_SERVER_DN_MATCH = NO", "SSL_SERVER_DN_MATCH = YES")
    changes[path] = text

    # Resolve every patch before writing so a changed source context leaves the
    # checkout untouched. The caller still runs the operator's own tests.
    overlay = Path(__file__).resolve().parents[1] / "patches/oracle-operator/hakopod_policy.go.txt"
    if not overlay.is_file():
        raise SystemExit("Oracle hardening source overlay is missing")
    for path, text in changes.items():
        path.write_text(text)
    shutil.copyfile(overlay, root / "commons/database/hakopod_policy.go")
    print(f"Applied {PROFILE} to Oracle operator source {SOURCE}; build and qualification remain required")


if __name__ == "__main__":
    main()
