#!/usr/bin/env python3
"""Apply the reviewed TLS and backup fixes to immutable upstream checkouts.

Run in VM build scratch space. This script changes source only; it does not
fetch repositories, install controllers, build images or touch Kubernetes.
"""
import argparse
from pathlib import Path
import subprocess

VITESS_REVISION = "0f1ed062dec171e0adfab796110549752901e299"
OPERATOR_REVISION = "10a3b742c02c38f97d554739d5a257197daa48f9"


def checkout(path, expected):
    root = Path(path).resolve()
    revision = subprocess.check_output(
        ["git", "-C", str(root), "rev-parse", "HEAD"], text=True, timeout=10
    ).strip()
    if revision != expected:
        raise ValueError(f"Unexpected upstream revision in {root}")
    if subprocess.check_output(
        ["git", "-C", str(root), "status", "--porcelain"], text=True, timeout=10
    ).strip():
        raise ValueError(f"Refusing to replace dirty upstream source in {root}")
    return root


def replace(text, old, new, count=1):
    if text.count(old) != count:
        raise ValueError("Pinned source context changed; inspect the patch")
    return text.replace(old, new)


def vitess_changes(root):
    path = root / "go/mysql/flavor_mysql.go"
    text = path.read_text()
    text = replace(text, '"vitess.io/vitess/go/vt/vterrors"',
                   '"vitess.io/vitess/go/vt/vterrors"\n\t"vitess.io/vitess/go/vt/vttls"')
    text = replace(text, 'args = append(args, "SOURCE_SSL = 1")',
                   '''args = append(args, "SOURCE_SSL = 1")
		if params.EffectiveSslMode() == vttls.VerifyIdentity {
			args = append(args, "SOURCE_SSL_VERIFY_SERVER_CERT = 1")
		}''')
    text = replace(text, 'if params.SslCa != "" || params.SslCert != "" {',
                   'if params.SslEnabled() || params.SslCa != "" || params.SslCert != "" {')
    text = replace(text, '\t\tif params.SslCa != "" {\n\t\t\tcmd +=',
                   '''		if params.EffectiveSslMode() == vttls.VerifyIdentity {
			cmd += ", SOURCE_SSL_VERIFY_SERVER_CERT=1"
		}
		if params.SslCa != "" {
			cmd +=''')
    tests = root / "go/mysql/flavor_mysql_test.go"
    expected = replace(tests.read_text(), "  SOURCE_SSL = 1,\n  SOURCE_SSL_CA = 'ssl-ca',",
                       "  SOURCE_SSL = 1,\n  SOURCE_SSL_VERIFY_SERVER_CERT = 1,\n  SOURCE_SSL_CA = 'ssl-ca',", count=2)
    monitor = root / "go/vt/vttablet/tabletmanager/semisyncmonitor/monitor.go"
    monitor_text = replace(monitor.read_text(),
                           'm.appPool.Open(m.config.DB.AppWithDB())',
                           'm.appPool.Open(m.config.DB.AllPrivsWithDB())')
    return {path: text, tests: expected, monitor: monitor_text}


def operator_changes(root):
    changes = {}
    path = root / "pkg/operator/vttablet/flags.go"
    changes[path] = replace(path.read_text(),
                            '"tablet_hostname": "$(POD_IP)",',
                            '"tablet-hostname": "$(POD_IP)",')
    path = root / 'pkg/operator/controllermanager/flags.go'
    changes[path] = replace(path.read_text(), '\t\t"s3-backup-aws-region":           false,',
                            '\t\t"s3-backup-aws-region":           false,\n\t\t"s3-backup-aws-min-partsize":      false,')
    for component in ('vtgate', 'vtctld', 'vtorc'):
        path = root / ('pkg/operator/' + component + '/deployment.go')
        signature = 'func UpdateDeployment(obj *appsv1.Deployment, spec *Spec'
        signature += ') {' if component == 'vtorc' else ', mysqldImage string) {'
        changes[path] = replace(path.read_text(), signature, signature + '''
	// Match tablet and backup pods so non-root processes can read owned
	// TLS Secrets projected with group-only permissions.
	if planetscalev2.DefaultVitessFSGroup >= 0 {
		if obj.Spec.Template.Spec.SecurityContext == nil {
			obj.Spec.Template.Spec.SecurityContext = &corev1.PodSecurityContext{}
		}
		obj.Spec.Template.Spec.SecurityContext.FSGroup = ptr.To(planetscalev2.DefaultVitessFSGroup)
	}
''')
    path = root / "pkg/controller/vitessshard/reconcile_backup_job.go"
    text = path.read_text()
    text = replace(text, '\t\tvitessbackup.LocationLabel: backupLocation.Name,',
                   '\t\tplanetscalev2.ComponentLabel: planetscalev2.VtbackupComponentName,\n\t\tvitessbackup.LocationLabel: backupLocation.Name,')
    text = replace(text, '\t// Fill in the parts of a vttablet spec that make sense for vtbackup.',
                   '''	// Copy shared TLS flags without changing the tablet pool itself.
	vttabletConfig := pool.Vttablet.DeepCopy()
	vttabletConfig.ExtraFlags = make(map[string]string)
	for key, value := range vts.Spec.ExtraVitessFlags {
		vttabletConfig.ExtraFlags[key] = value
	}
	for key, value := range pool.Vttablet.ExtraFlags {
		vttabletConfig.ExtraFlags[key] = value
	}
	// Fill in the parts of a vttablet spec that make sense for vtbackup.''')
    text = replace(text, 'Vttablet:                 &pool.Vttablet,',
                   'Vttablet:                 vttabletConfig,')
    text = replace(text, 'ExtraEnv:                 pool.ExtraEnv,',
                   '''ExtraEnv:                 pool.ExtraEnv,
		ExtraVolumes:             pool.ExtraVolumes,
		ExtraVolumeMounts:        pool.ExtraVolumeMounts,''')
    changes[path] = text

    path = root / "pkg/operator/vttablet/vtbackup_pod.go"
    text = path.read_text()
    text = replace(text, 'volumeMounts = append(volumeMounts, vttabletVolumeMounts.Get(tabletSpec)...)',
                   '''volumeMounts = append(volumeMounts, vttabletVolumeMounts.Get(tabletSpec)...)
	update.VolumeMounts(&volumeMounts, tabletSpec.ExtraVolumeMounts)''')
    text = replace(text, '\tvtbackupAllFlags := vtbackupFlags.Get(backupSpec)',
                   '''	vtbackupAllFlags := vtbackupFlags.Get(backupSpec)
	// vtbackup accepts a smaller flag set than vttablet. Forward only
	// transport and credentials flags shared by these two binaries.
	for _, key := range []string{
		"topo-etcd-tls-ca", "topo-etcd-tls-cert", "topo-etcd-tls-key",
		"tablet-manager-grpc-ca", "tablet-manager-grpc-server-name",
		"tablet-manager-grpc-cert", "tablet-manager-grpc-key",
		"db-credentials-file", "db-ssl-mode", "db-ssl-ca",
		"db-app-use-ssl", "db-appdebug-use-ssl", "db-allprivs-use-ssl",
		"db-dba-use-ssl", "db-filtered-use-ssl", "db-repl-use-ssl",
	} {
		if value, ok := tabletSpec.Vttablet.ExtraFlags[key]; ok {
			vtbackupAllFlags[key] = value
		}
	}''')
    text = replace(text, '\t\t\t\t\tName:            "init-vt-root",',
                   '\t\t\t\t\tName:            "init-vt-root",\n\t\t\t\t\tResources:       tabletSpec.Vttablet.Resources,')
    text = replace(text, '\tif planetscalev2.DefaultVitessServiceAccount != "" {',
                   '\tupdate.Volumes(&pod.Spec.Volumes, tabletSpec.ExtraVolumes)\n\n\tif planetscalev2.DefaultVitessServiceAccount != "" {')
    changes[path] = text

    path = root / "pkg/controller/vitessbackupstorage/reconcile_subcontroller.go"
    text = replace(path.read_text(),
                   '\tcontainer.Resources.Limits = corev1.ResourceList{\n',
                   '\tcontainer.Resources.Limits = corev1.ResourceList{\n\t\tcorev1.ResourceCPU: *resource.NewMilliQuantity(subcontrollerCPUMillis, resource.DecimalSI),\n')
    changes[path] = text

    path = root / "pkg/controller/vitessbackupschedule/vitessbackupschedule_controller.go"
    text = path.read_text()
    text = replace(text, '&planetscalev2.VitessBackupStorage{},', '&planetscalev2.VitessBackupSchedule{},')
    text = replace(text, '\t&kbatch.Job{},', '\t&kbatch.Job{},\n\t&corev1.PersistentVolumeClaim{},')
    text = replace(text, '\t\tif jobStartTime.Add(time.Minute * time.Duration(timeout)).Before(time.Now()) {',
                   '\t\tif jobStartTime != nil && jobStartTime.Add(time.Minute * time.Duration(timeout)).Before(time.Now()) {')
    text = replace(text, '\t\tSpec: kbatch.JobSpec{\n',
                   '''		Spec: kbatch.JobSpec{
			BackoffLimit: new(int32),
			ActiveDeadlineSeconds: &deadlineSeconds,
''')
    text = replace(text, '\tjob := &kbatch.Job{',
                   '\tmaps.Copy(meta.Labels, pod.Labels)\n\tdeadlineSeconds := int64(vbsc.Spec.JobTimeoutMinutes) * 60\n\tjob := &kbatch.Job{')
    text = replace(text, '\tvtbackupSpec := vitessshard.MakeVtbackupSpec(podKey, &vts, labels, backupType)\n',
                   '''	vtbackupSpec := vitessshard.MakeVtbackupSpec(podKey, &vts, labels, backupType)
	if vtbackupSpec == nil {
		return nil, nil, fmt.Errorf("native backup configuration is unavailable")
	}
	if err := configureScheduledBackup(vtbackupSpec, vbsc, strategy); err != nil {
		return nil, nil, err
	}
''')
    text = replace(text, '\tp.Spec.Affinity = vbsc.Spec.Affinity',
                   '\tif vbsc.Spec.Affinity != nil { p.Spec.Affinity = vbsc.Spec.Affinity }')
    text = replace(text, '// Re-queuing here does not make sense as we have an error with the template and the user needs to fix it first.',
                   '// Storage cleanup and API failures can be temporary; retry without allocating beyond the shard budget.')
    text = replace(text, 'log.WithError(err).Error("unable to construct job from template")\n\t\treturn resultBuilder.Error(reconcile.TerminalError(err))',
                   'log.WithError(err).Error("unable to construct job from template")\n\t\treturn resultBuilder.Error(err)')
    start = text.index('\t// Create the corresponding PVC for the new vtbackup pod\n')
    end = text.index('\treturn job, nil\n', start)
    text = text[:start] + '''\tif err := r.ensureScheduledBackupStorage(ctx, &vbsc, job, vtbackupSpec.TabletSpec); err != nil {
\t\treturn nil, err
\t}
''' + text[end:]
    changes[path] = text
    return changes


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--vitess-source", required=True)
    parser.add_argument("--operator-source", required=True)
    args = parser.parse_args()
    vitess = checkout(args.vitess_source, VITESS_REVISION)
    operator = checkout(args.operator_source, OPERATOR_REVISION)
    # Validate every source context before changing either checkout.
    changes = vitess_changes(vitess) | operator_changes(operator)
    patch_root = Path(__file__).resolve().parent.parent / "patches"
    for template, target in (
        ("vitess-replication-tls-test.go.txt", vitess / "go/mysql/hakopod_replication_tls_test.go"),
        ("vitess-semisync-account-test.go.txt", vitess / "go/vt/vttablet/tabletmanager/semisyncmonitor/hakopod_account_test.go"),
        ("vitess-backup-schedule.go.txt", operator / "pkg/controller/vitessbackupschedule/hakopod_backup.go"),
        ("vitess-backup-schedule-test.go.txt", operator / "pkg/controller/vitessbackupschedule/hakopod_backup_test.go"),
        ("vitess-api-compatibility-test.go.txt", operator / "pkg/controller/vitessbackupschedule/hakopod_api_test.go"),
        ("vitess-control-permissions-test.go.txt", operator / "pkg/controller/vitessbackupschedule/hakopod_control_permissions_test.go"),
        ("vitess-backup-pod-test.go.txt", operator / "pkg/operator/vttablet/hakopod_backup_test.go"),
        ("vitess-tablet-hostname-test.go.txt", operator / "pkg/operator/vttablet/hakopod_hostname_test.go"),
        ("vitess-backup-flags-test.go.txt", operator / "pkg/operator/controllermanager/hakopod_backup_flags_test.go"),
    ):
        if target.exists():
            raise ValueError(f"Refusing existing patch source {target}")
        changes[target] = (patch_root / template).read_text()
    for path, text in changes.items():
        path.write_text(text)
    print("Applied source patches. Formatting, focused tests and native acceptance remain required.")


if __name__ == "__main__":
    main()
