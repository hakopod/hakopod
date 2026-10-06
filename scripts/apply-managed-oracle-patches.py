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
    parser.add_argument("--free-only", action="store_true", help="Build the namespace-scoped Free-only controller")
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
    runner_bound = 'dbcommons.HakopodOracleListExceedsBound(pods.Continue, len(pods.Items))' if args.free_only else 'pods.Continue != "" || len(pods.Items) > 2'
    text = replace(text, '\treturn pods.Items, nil\n', '\tif ' + runner_bound + ' { return nil, fmt.Errorf("Oracle runner inventory exceeds its bound") }\n\treturn pods.Items, nil\n')
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

    if args.free_only:
        path = root / "main.go"
        text = path.read_text()
        text = replace(text, '\t"context"\n', '')
        text = replace(text, "func main() {\n", """func main() {
	if os.Getenv("HAKOPOD_ORACLE_POLICY") != "hakopod-oracle-free-tcps-v1" || os.Getenv("ENABLE_WEBHOOKS") != "false" || !strings.HasPrefix(os.Getenv("WATCH_NAMESPACE"), "hdb-") || len(os.Getenv("WATCH_NAMESPACE")) != 36 || strings.Contains(os.Getenv("WATCH_NAMESPACE"), ",") {
		setupLog.Error(fmt.Errorf("managed Oracle Free requires one database namespace and its fixed profile"), "invalid managed scope")
		os.Exit(1)
	}
""")
        text = replace(text, '\tsetupLog.Info("starting manager")', '''\tif err := mgr.AddReadyzCheck("oracle-free", hakopodFreeReadiness(mgr.GetAPIReader(), mgr.GetCache().WaitForCacheSync, os.Getenv("WATCH_NAMESPACE"))); err != nil {
\t\tsetupLog.Error(err, "unable to register controller readiness")
\t\tos.Exit(1)
\t}
\tsetupLog.Info("starting manager")''')
        text = replace(text, "\t\tScheme: scheme,", '\t\tScheme: scheme,\n\t\tHealthProbeBindAddress: ":8081",')
        start = text.index("func setupControllers(mgr ctrl.Manager, interval int64) error {")
        end = text.index("\nfunc setupAutonomousDatabaseController", start)
        text = text[:start] + "func setupControllers(mgr ctrl.Manager, interval int64) error {\n\treturn setupSingleInstanceDatabaseController(mgr, interval)\n}\n" + text[end:]
        start = text.index("func setupIndexes(mgr ctrl.Manager) error {")
        end = text.index("\n// parseReconcileInterval", start)
        text = text[:start] + "func setupIndexes(mgr ctrl.Manager) error { return nil }\n" + text[end:]
        changes[path] = text

        path = root / "controllers/database/singleinstancedatabase_controller.go"
        text = changes[path]
        text = replace(text, '\t"fmt"\n', '\t"fmt"\n\t"os"\n')
        text = replace(text, "MaxConcurrentReconciles: 100", "MaxConcurrentReconciles: 1")
        text = replace(text, "func (r *SingleInstanceDatabaseReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {\n", """func (r *SingleInstanceDatabaseReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
""")
        text = replace(text, "\tif phaseCtx.singleInstanceDatabase.DeletionTimestamp == nil {", """	managed := phaseCtx.singleInstanceDatabase
	if err := validateHakopodFreeResource(managed); err != nil { return requeueN, err }
	if managed.Name != "database" || managed.Namespace != os.Getenv("WATCH_NAMESPACE") || managed.Labels["app.kubernetes.io/managed-by"] != "hakopod" || managed.Namespace != "hdb-"+managed.Labels["hakopod.io/database-id"] || managed.Annotations["hakopod.io/oracle-free-pod-policy"] == "" || managed.Spec.Edition != "free" || managed.Spec.CreateAs != "primary" || managed.Spec.Replicas != 1 || managed.Spec.Image.PullFrom != "container-registry.oracle.com/database/free:23.26.3.0@sha256:f988b0c04c4c386cd306a2a914c0d7a9702d83acc31b064a28ad8eb6278a8fba" || !getTcpsEnabled(managed) || getTcpsTLSSecret(managed) != "database-tls" {
		return requeueN, fmt.Errorf("managed Oracle Free resource is outside its fixed contract")
	}
	if managed.Spec.Services == nil || len(managed.Spec.Services.Endpoints) != 1 {
		return requeueN, fmt.Errorf("managed Oracle Free requires one private TCPS endpoint")
	}
	endpoint := managed.Spec.Services.Endpoints[0]
	if endpoint.Name != "cluster" || endpoint.Type != "ClusterIP" || endpoint.TCP == nil || endpoint.TCP.Enabled || endpoint.TCPS == nil || !endpoint.TCPS.Enabled || endpoint.TCPS.Port != 2484 || endpoint.IsKeep || len(endpoint.Annotations) != 0 || endpoint.TCPS.NodePort != 0 || endpoint.TCP.Port != 0 || endpoint.TCP.NodePort != 0 {
		return requeueN, fmt.Errorf("managed Oracle Free endpoint contract changed")
	}
	oradata := getOradataPersistenceConfig(managed)
	if oradata.PvcName != "" || oradata.StorageClass == "" || oradata.Size == "" || oradata.AccessMode != "ReadWriteOnce" || len(managed.Spec.Persistence.AdditionalPVCs) != 1 { return requeueN, fmt.Errorf("managed Oracle Free requires two operator-owned claims") }
	backup := managed.Spec.Persistence.AdditionalPVCs[0]
	if backup.PvcName != "" || backup.StorageClass != oradata.StorageClass || backup.StorageSizeInGb <= 0 || backup.MountPath != "/opt/oracle/hakopod-backup" { return requeueN, fmt.Errorf("managed Oracle Free backup claim changed") }
	if phaseCtx.singleInstanceDatabase.DeletionTimestamp == nil {""")
        text = replace(text, "\tif err := dbcommons.ApplyHakopodPodPolicy(pod, m.ObjectMeta); err != nil {", "\tif err := dbcommons.ApplyHakopodFreePodPolicy(pod, m.ObjectMeta); err != nil {")

        text = replace(text, "desiredPublishNotReady := cfg.Name == dbapi.SingleInstanceDatabaseServiceEndpointNameCluster || shouldPublishNotReadyExternalService(m)", "desiredPublishNotReady := false")
        text = replace(text, "\tif cfg.IsKeep {\n\t\tdesiredSvc.OwnerReferences = nil\n\t}", "\tdesiredSvc.Spec.Selector = map[string]string{\"app\": m.Name, \"hakopod.io/database-id\": m.Labels[\"hakopod.io/database-id\"], \"hakopod.io/oracle-member\": \"true\"}")
        text = replace(text, "\tswitch {\n\tcase !reflect.DeepEqual(current.OwnerReferences, desiredSvc.OwnerReferences):", "\tif err := dbcommons.ValidateHakopodFreeService(current, m.ObjectMeta); err != nil { return nil, requeueN, err }\n\tswitch {\n\tcase !reflect.DeepEqual(current.OwnerReferences, desiredSvc.OwnerReferences):")
        text = replace(text, "\t\tm.Status.Status = dbcommons.StatusUpdating\n\t\tif err := r.Delete(ctx, svc); err != nil {\n\t\t\tlog.Error(err, \"Failed to delete optional service\", \"Service.Namespace\", svc.Namespace, \"Service.Name\", svc.Name)\n\t\t\treturn requeueY, err\n\t\t}\n\t\treturn requeueY, nil", "\t\treturn requeueN, fmt.Errorf(\"managed Oracle Free found an unexpected optional service\")")

        text = replace(text, '"listener-tcps"', '"tcps"', 3)
        text = replace(text, "\t\tvar delOpts *client.DeleteOptions = &client.DeleteOptions{}\n\t\tif replicasRequired == 0 {\n\t\t\tvar gracePeriodSeconds int64 = 0\n\t\t\tpolicy := metav1.DeletePropagationForeground\n\t\t\tdelOpts.GracePeriodSeconds = &gracePeriodSeconds\n\t\t\tdelOpts.PropagationPolicy = &policy\n\t\t}", "\t\tif err := dbcommons.ValidateHakopodFreeObservedMember(&availablePod, m.ObjectMeta); err != nil { return requeueN, err }\n\t\tdelOpts := &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &availablePod.UID, ResourceVersion: &availablePod.ResourceVersion}}")
        # The retry paths must carry the same identity fence as normal deletion.
        # An image-pull failure is not permission to force-remove a named pod.
        text = replace(text, "if err := r.Delete(ctx, pod); err != nil {", "if err := r.Delete(ctx, pod, client.Preconditions{UID: &pod.UID, ResourceVersion: &pod.ResourceVersion}); err != nil {")
        text = replace(text, "\t\t\t\t\t\tvar gracePeriodSeconds int64 = 0\n\t\t\t\t\t\tpolicy := metav1.DeletePropagationForeground\n\t\t\t\t\t\tif err := r.Delete(ctx, &allAvailable[i], &client.DeleteOptions{\n\t\t\t\t\t\t\tGracePeriodSeconds: &gracePeriodSeconds, PropagationPolicy: &policy}); err != nil {", "\t\t\t\t\t\tif err := r.Delete(ctx, &allAvailable[i], client.Preconditions{UID: &allAvailable[i].UID, ResourceVersion: &allAvailable[i].ResourceVersion}); err != nil {")
        start = text.index("func (r *SingleInstanceDatabaseReconciler) deletePods(")
        end = text.index("func (r *SingleInstanceDatabaseReconciler) validateDBReadiness(", start)
        deletion = replace(text[start:end], "\t\t\t// Don't requeue\n", "\t\t\treturn requeueY, err\n")
        text = text[:start] + deletion + text[end:]
        start = text.index("func (r *SingleInstanceDatabaseReconciler) manageSingleInstanceDatabaseDeletion(")
        end = text.index("func (r *SingleInstanceDatabaseReconciler) manageConvPhysicalToSnapshot(", start)
        deletion = replace(text[start:end], "if result.Requeue {", "if err != nil || result.Requeue {", 2)
        deletion = replace(deletion, "client.MatchingLabels(dbcommons.GetLabelsForController(\"\", req.Name))}", "client.MatchingLabels(dbcommons.GetLabelsForController(\"\", req.Name)), client.Limit(3)}")
        deletion = replace(deletion, "\t\tif len(podList.Items) == 0 {", "\t\tif dbcommons.HakopodOracleListExceedsBound(podList.Continue, len(podList.Items)) { return requeueY, fmt.Errorf(\"managed Oracle pod inventory exceeds its bound\") }\n\t\tfor i := range podList.Items { if err := dbcommons.ValidateHakopodFreeObservedMember(&podList.Items[i], m.ObjectMeta); err != nil { return requeueY, err } }\n\t\tif len(podList.Items) == 0 {")
        claim_start = deletion.index("func (r *SingleInstanceDatabaseReconciler) cleanupManagedSingleInstanceDatabasePVCs(")
        claim_end = deletion.index("\n// #############################################################################################", claim_start)
        deletion = deletion[:claim_start] + "func (r *SingleInstanceDatabaseReconciler) cleanupManagedSingleInstanceDatabasePVCs(ctx context.Context, m *dbapi.SingleInstanceDatabase) (bool, error) {\n\treturn r.cleanupHakopodFreeClaims(ctx, m)\n}\n" + deletion[claim_end:]
        text = text[:start] + deletion + text[end:]
        # Validate immutable storage before upstream replacement can delete data.
        # Free forbids custom-script storage. The upstream legacy cleanup also
        # treats declared additional claims as stale scripts and can delete the
        # backup claim, so do not enter it after validating the fixed Free spec.
        start = text.index("func (r *SingleInstanceDatabaseReconciler) createOrReplacePVCforCustomScriptsVol(")
        end = text.index("func (r *SingleInstanceDatabaseReconciler) createOrReplacePVCforDatafilesVol(", start)
        text = text[:start] + '''func (r *SingleInstanceDatabaseReconciler) createOrReplacePVCforCustomScriptsVol(ctx context.Context, req ctrl.Request,
    m *dbapi.SingleInstanceDatabase) (ctrl.Result, error) {
    if err := validateHakopodFreeResource(m); err != nil { return requeueN, err }
    return requeueN, nil
}

''' + text[end:]

        start = text.index("func (r *SingleInstanceDatabaseReconciler) createOrReplacePVCforDatafilesVol(")
        end = text.index("func (r *SingleInstanceDatabaseReconciler) createOrReplacePVCforFRAVol(", start)
        data = text[start:end]
        data = replace(data, "\tpvcDeleted := false\n", "")
        old_start = data.index("\tif err == nil {")
        old_end = data.index("\n\tif pvcDeleted || err != nil && apierrors.IsNotFound(err) {", old_start)
        data = data[:old_start] + "\tif err == nil {\n\t\tif err := dbcommons.ValidateHakopodFreePVC(pvc, m.ObjectMeta, m.Name, oradataCfg.StorageClass, oradataCfg.Size); err != nil { return requeueN, err }\n\t\treturn requeueN, nil\n\t}\n" + data[old_end:]
        data = replace(data, "if pvcDeleted || err != nil && apierrors.IsNotFound(err) {", "if apierrors.IsNotFound(err) {")
        data = replace(data, "\t\terr = r.Create(ctx, pvc)", "\t\tcontrollerutil.AddFinalizer(pvc, dbcommons.HakopodOracleFreeStorageFinalizer)\n\t\terr = r.Create(ctx, pvc)")
        text = text[:start] + data + text[end:]
        text = replace(text, "\t\tif err == nil {\n\t\t\tif !metav1.IsControlledBy(pvc, m) {", "\t\tif err == nil {\n\t\t\tif err := dbcommons.ValidateHakopodFreePVC(pvc, m.ObjectMeta, claimName, cfg.StorageClass, fmt.Sprintf(\"%dGi\", cfg.StorageSizeInGb)); err != nil { return requeueN, err }\n\t\t\tif !metav1.IsControlledBy(pvc, m) {", 1)
        start = text.index("func (r *SingleInstanceDatabaseReconciler) createOrReplacePVCsForAdditionalPVCs(")
        end = text.index("func (r *SingleInstanceDatabaseReconciler) reconcileSIDBEndpointService(", start)
        additional = replace(text[start:end], "\t\tif err := r.Create(ctx, pvc); err != nil {", "\t\tcontrollerutil.AddFinalizer(pvc, dbcommons.HakopodOracleFreeStorageFinalizer)\n\t\tif err := r.Create(ctx, pvc); err != nil {")
        text = text[:start] + additional + text[end:]

        text = replace(text, '\teventReason := "Configuring TCPS"\n', """	// Hakopod's guarded entrypoint owns the wallet and listener. The API
	// separately verifies the live certificate fingerprint before readiness.
	if !getTcpsEnabled(m) || getTcpsTLSSecret(m) != "database-tls" { return requeueN, fmt.Errorf("managed Oracle Free TCPS is required") }
	if _, err := dbcommons.ExecCommand(r, r.Config, readyPod.Name, readyPod.Namespace, "database", ctx, req, true, "test", "-f", "/tmp/hakopod/ready"); err != nil { return requeueY, err }
	listening, _, err := hasTCPSListenerEndpointInPod(r, readyPod, ctx, req)
	if err != nil || !listening { return requeueY, fmt.Errorf("managed Oracle Free TCPS listener is not ready") }
	m.Status.IsTcpsEnabled = true
	m.Status.TcpsTlsSecret = "database-tls"
	return requeueN, nil
}

// The upstream TCPS path remains for source provenance, but is not called by
// the Free-only controller because it publishes Data Guard wallet secrets.
func (r *SingleInstanceDatabaseReconciler) upstreamConfigTcps(m *dbapi.SingleInstanceDatabase,
	readyPod corev1.Pod, ctx context.Context, req ctrl.Request, phaseCtx *sidbPhaseContext) (ctrl.Result, error) {
	eventReason := "Configuring TCPS"
""")
        changes[path] = text

        path = root / "commons/database/utils.go"
        text = changes[path]
        text = replace(text, 'metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"', 'metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"\n\t"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"')
        text = replace(text, "listOpts := []client.ListOption{client.InNamespace(namespace), client.MatchingLabels(GetLabelsForController(version, name))}", "listOpts := []client.ListOption{client.InNamespace(namespace), client.MatchingLabels(GetLabelsForController(version, name)), client.Limit(3)}")
        text = replace(text, "\t// r.List() lists all the pods", """	if HakopodOracleListExceedsBound(podList.Continue, len(podList.Items)) { return readyPod, 0, nil, nil, fmt.Errorf("managed Oracle pod inventory exceeds its bound") }
	root := &unstructured.Unstructured{}
	root.SetAPIVersion("database.oracle.com/v4")
	root.SetKind("SingleInstanceDatabase")
	if err := r.Get(ctx, types.NamespacedName{Name:name,Namespace:namespace}, root); err != nil { return readyPod, 0, nil, nil, err }
	owner := metav1.ObjectMeta{Name:root.GetName(), Namespace:root.GetNamespace(), UID:root.GetUID(), Labels:root.GetLabels(), Annotations:root.GetAnnotations()}
	for i := range podList.Items { if err := ValidateHakopodFreeObservedMember(&podList.Items[i],owner); err != nil { return readyPod, 0, nil, nil, err } }
	// r.List() lists all the pods""")
        changes[path] = text

    # Resolve every patch before writing so a changed source context leaves the
    # checkout untouched. The caller still runs the operator's own tests.
    overlay = Path(__file__).resolve().parents[1] / "patches/oracle-operator/hakopod_policy.go.txt"
    if not overlay.is_file():
        raise SystemExit("Oracle hardening source overlay is missing")
    for path, text in changes.items():
        path.write_text(text)
    shutil.copyfile(overlay, root / "commons/database/hakopod_policy.go")
    if args.free_only:
        free_overlay = overlay.with_name("hakopod_free_policy.go.txt")
        shutil.copyfile(free_overlay, root / "commons/database/hakopod_free_policy.go")
        free_tests = overlay.with_name("hakopod_free_policy_test.go.txt")
        shutil.copyfile(free_tests, root / "commons/database/hakopod_free_policy_test.go")
        shutil.copyfile(free_overlay.with_name("hakopod_free_resource.go.txt"), root / "controllers/database/hakopod_free_resource.go")
        shutil.copyfile(free_overlay.with_name("hakopod_free_resource_test.go.txt"), root / "controllers/database/hakopod_free_resource_test.go")
        shutil.copyfile(free_overlay.with_name("hakopod_free_health.go.txt"), root / "hakopod_free_health.go")
        shutil.copyfile(free_overlay.with_name("hakopod_free_health_test.go.txt"), root / "hakopod_free_health_test.go")
    print(f"Applied {PROFILE} to Oracle operator source {SOURCE}; build and qualification remain required")


if __name__ == "__main__":
    main()
