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
    mysqld = root / "go/vt/mysqlctl/mysqld.go"
    mysqld_text = mysqld.read_text()
    # exec.Cmd contains the complete environment. Database and backup
    # credentials must never become process-command diagnostics.
    for old, new in (
        ('log.Infof("%v %#v", ts, cmd)',
         'log.Infof("%v: starting local MySQL process", ts)'),
        ('log.Infof("ApplyBinlogFile: running mysqlbinlog command: %#v with errfile=%v", mysqlbinlogCmd, mysqlbinlogErrFile.Name())',
         'log.Info("ApplyBinlogFile: starting mysqlbinlog process")'),
        ('log.Infof("ApplyBinlogFile: running mysql command: %#v with errfile=%v", mysqlCmd, mysqlErrFile.Name())',
         'log.Info("ApplyBinlogFile: starting local MySQL process")'),
        ('log.Infof("ApplyBinlogFile: running mysqlbinlog command: %#v", mysqlbinlogCmd)',
         'log.Info("ApplyBinlogFile: starting binlog timestamp process")'),
        ('log.Errorf("%v: not removing socket lock file: %v with pid %v for %q", ts, lockPath, p, name)',
         'log.Errorf("%v: not removing socket lock file: %v with live pid %v", ts, lockPath, p)'),
    ):
        mysqld_text = replace(mysqld_text, old, new)
    mysqld_text = replace(mysqld_text,
                          '\t\t\tname := string(bytes.ReplaceAll(cmdline, []byte{0}, []byte(" ")))\n',
                          '\t\t\t_ = cmdline // Do not log another process\'s command arguments.\n')
    s3 = root / "go/vt/mysqlctl/s3backupstorage/s3.go"
    s3_text = s3.read_text()
    s3_text = replace(s3_text, '''	reader, writer := io.Pipe()
	bh.handleAddFile(ctx, filename, partSizeBytes, reader, func(err error) {
		reader.CloseWithError(err)
	})

	return writer, nil''', '''	uploadCtx, cancel := context.WithCancel(ctx)
	reader, writer := io.Pipe()
	stop := context.AfterFunc(uploadCtx, func() {
		reader.CloseWithError(uploadCtx.Err())
		writer.CloseWithError(uploadCtx.Err())
	})
	done := bh.handleAddFile(uploadCtx, filename, partSizeBytes, reader, func(err error) {
		reader.CloseWithError(err)
	})
	return &completedUploadWriter{PipeWriter: writer, done: done, finish: func() { stop(); cancel() }}, nil''')
    s3_text = replace(s3_text,
                      'func (bh *S3BackupHandle) handleAddFile(ctx context.Context, filename string, partSizeBytes int64, reader io.Reader, closer func(error)) {',
                      'func (bh *S3BackupHandle) handleAddFile(ctx context.Context, filename string, partSizeBytes int64, reader io.Reader, closer func(error)) <-chan error {\n\tdone := make(chan error, 1)')
    s3_text = replace(s3_text, '\t\t\tu.PartSize = partSizeBytes',
                      '\t\t\tu.PartSize = partSizeBytes\n\t\t\t// One upload worker retains at most two multipart buffers.\n\t\t\tu.Concurrency = 1')
    s3_text = replace(s3_text, '''		if err != nil {
			closer(err)
			bh.RecordError(filename, err)
		}
	}()
}''', '''		closer(err)
		if err != nil {
			bh.RecordError(filename, err)
		}
		done <- err
	}()
	return done
}''')
    s3_tests = root / "go/vt/mysqlctl/s3backupstorage/s3_test.go"
    s3_expected = s3_tests.read_text()
    for name in ("TestAddFileError", "TestAddFileErrorStats"):
        start = s3_expected.index("func " + name + "(")
        end = s3_expected.index("\nfunc ", start + 1)
        original = s3_expected[start:end]
        updated = replace(original,
                          'require.NoErrorf(t, err, "TestAddFile() could not close writer, got %s", err)',
                          'require.ErrorContains(t, err, "some error", "Close must report upload failure")')
        s3_expected = s3_expected[:start] + updated + s3_expected[end:]
    return {path: text, tests: expected, monitor: monitor_text, mysqld: mysqld_text, s3: s3_text, s3_tests: s3_expected}


def operator_changes(root):
    changes = {}
    path = root / "pkg/operator/vttablet/constants.go"
    changes[path] = replace(path.read_text(),
                            '\tvtRootVolumeName   = "vt-root"',
                            '\tvtRootVolumeName   = "vt-root"\n\tvtSocketVolumeName = "rundir"')
    changes[path] = replace(changes[path], '\trestoreConcurrency = 10', '\trestoreConcurrency = 1')
    changes[path] = replace(changes[path], '\tvtbackupConcurrency = 10', '\tvtbackupConcurrency = 1')
    path = root / "pkg/operator/vttablet/mysqlctld.go"
    text = path.read_text()
    text = replace(text, '''\t\treturn []corev1.Volume{
\t\t\t{
\t\t\t\tName: vtRootVolumeName,
\t\t\t\tVolumeSource: corev1.VolumeSource{
\t\t\t\t\tEmptyDir: &corev1.EmptyDirVolumeSource{},
\t\t\t\t},
\t\t\t},
\t\t}''', '''\t\treturn []corev1.Volume{
\t\t\t{
\t\t\t\tName: vtRootVolumeName,
\t\t\t\tVolumeSource: corev1.VolumeSource{
\t\t\t\t\tEmptyDir: &corev1.EmptyDirVolumeSource{},
\t\t\t\t},
\t\t\t},
\t\t\t{
\t\t\t\tName: vtSocketVolumeName,
\t\t\t\tVolumeSource: corev1.VolumeSource{
\t\t\t\t\tEmptyDir: &corev1.EmptyDirVolumeSource{
\t\t\t\t\t\tMedium: corev1.StorageMediumMemory,
\t\t\t\t\t\tSizeLimit: resource.NewQuantity(16*1024*1024, resource.BinarySI),
\t\t\t\t\t},
\t\t\t\t},
\t\t\t},
\t\t}''')
    text = replace(text, '''\t\t\t{
\t\t\t\tName:      vtRootVolumeName,
\t\t\t\tReadOnly:  false,
\t\t\t\tMountPath: vtSocketPath,
\t\t\t\tSubPath:   "socket",
\t\t\t},''', '''\t\t\t{
\t\t\t\tName:      vtSocketVolumeName,
\t\t\t\tReadOnly:  false,
\t\t\t\tMountPath: vtSocketPath,
\t\t\t},''')
    changes[path] = text
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
    text = replace(text, '''\t// The object name for the initial backup Pod, if we end up needing one.
\tinitPodName := vttablet.InitialBackupPodName(clusterName, keyspaceName, vts.Spec.KeyRange)
\tinitPodKey := client.ObjectKey{
\t\tNamespace: vts.Namespace,
\t\tName:      initPodName,
\t}

\tif len(completeBackups) == 0 && vts.Status.HasMaster != corev1.ConditionTrue {
\t\t// Until we see at least one complete backup, we attempt to create an
\t\t// "initial backup", which is a special imaginary backup created from
\t\t// scratch (not from any tablet). If we're wrong and a backup exists
\t\t// already, the idempotent vtbackup "initial backup" mode will just do
\t\t// nothing and return success.
\t\tinitSpec := MakeVtbackupSpec(initPodKey, vts, labels, vitessbackup.TypeInit)
\t\tif initSpec != nil {
\t\t\tpodKeys = append(podKeys, initPodKey)
\t\t\tif initSpec.TabletSpec.DataVolumePVCSpec != nil {
\t\t\t\tpvcKeys = append(pvcKeys, initPodKey)
\t\t\t}
\t\t\tspecMap[initPodKey] = initSpec
\t\t}
\t} else {
\t\t// We have at least one complete backup already.
\t\tvts.Status.HasInitialBackup = corev1.ConditionTrue
\t}''', '''\t// Empty initialization is valid only before a shard has a primary. Once
\t// writes are serving, seed recovery from a temporary replica that catches
\t// up to the exact topology primary instead of uploading an empty database.
\tbackupPodKey := client.ObjectKey{
\t\tNamespace: vts.Namespace,
\t\tName:      vttablet.InitialBackupPodName(clusterName, keyspaceName, vts.Spec.KeyRange),
\t}
\tif len(completeBackups) == 0 {
\t\tbackupSpec := MakeVtbackupSpec(backupPodKey, vts, labels, vitessbackup.TypeInit)
\t\tif backupSpec != nil && vts.Status.HasMaster == corev1.ConditionTrue {
\t\t\tif !firstLiveBackupReady(vts) {
\t\t\t\tresultBuilder.RequeueAfter(5 * time.Second)
\t\t\t\tbackupSpec = nil
\t\t\t} else {
\t\t\t\tbackupPodKey.Name = vttablet.FirstLiveBackupPodName(clusterName, keyspaceName, vts.Spec.KeyRange)
\t\t\t\tbackupSpec = MakeVtbackupSpec(backupPodKey, vts, labels, vitessbackup.TypeInit)
\t\t\t\tconfigureFirstLiveBackup(backupSpec)
\t\t\t}
\t\t}
\t\tif backupSpec != nil {
\t\t\tpodKeys = append(podKeys, backupPodKey)
\t\t\tif backupSpec.TabletSpec.DataVolumePVCSpec != nil {
\t\t\t\tpvcKeys = append(pvcKeys, backupPodKey)
\t\t\t}
\t\t\tspecMap[backupPodKey] = backupSpec
\t\t}
\t} else {
\t\tvts.Status.HasInitialBackup = corev1.ConditionTrue
\t}''')
    text = replace(text, 'if key == initPodKey {', 'if isBootstrapBackup(key, backupPodKey) {')
    text = replace(text, '''func MakeVtbackupSpec(key client.ObjectKey, vts *planetscalev2.VitessShard, parentLabels map[string]string, typ string) *vttablet.BackupSpec {''', '''func firstLiveBackupReady(vts *planetscalev2.VitessShard) bool {
\tif vts.Status.HasMaster != corev1.ConditionTrue || vts.Status.ServingWrites != corev1.ConditionTrue || vts.Status.MasterAlias == "" {
\t\treturn false
\t}
\tprimary, ok := vts.Status.Tablets[vts.Status.MasterAlias]
\treturn ok && primary.Type == "primary" && primary.Running == corev1.ConditionTrue &&
\t\tprimary.Ready == corev1.ConditionTrue && primary.Available == corev1.ConditionTrue &&
\t\tprimary.DataVolumeBound == corev1.ConditionTrue
}

func configureFirstLiveBackup(spec *vttablet.BackupSpec) {
\tspec.InitialBackup = false
\tspec.AllowFirstBackup = true
}

func isBootstrapBackup(key, desired client.ObjectKey) bool {
\treturn key == desired
}

func MakeVtbackupSpec(key client.ObjectKey, vts *planetscalev2.VitessShard, parentLabels map[string]string, typ string) *vttablet.BackupSpec {''')
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
    text = replace(text, 'corev1 "k8s.io/api/core/v1"',
                   'corev1 "k8s.io/api/core/v1"\n\t"k8s.io/apimachinery/pkg/api/resource"')
    text = replace(text, '\tupdate.Env(&env, tabletSpec.ExtraEnv)',
                   '''	update.Env(&env, tabletSpec.ExtraEnv)
	// Backup multipart buffers need a larger Go budget than a tablet. This
	// stays below the separately reserved 512 MiB backup-process allocation.
	update.Env(&env, []corev1.EnvVar{{Name: "GOMAXPROCS", Value: "1"}, {Name: "GOMEMLIMIT", Value: "448MiB"}})''')
    text = replace(text, '''\tInitialBackup bool
\t// MinBackupInterval''', '''\tInitialBackup bool
\t// AllowFirstBackup seeds the first recovery copy by replicating from the
\t// live shard primary. It must never be combined with InitialBackup.
\tAllowFirstBackup bool
\t// MinBackupInterval''')
    text = replace(text, '''func InitialBackupPodName(clusterName, keyspaceName string, keyRange planetscalev2.VitessKeyRange) string {
\treturn names.JoinWithConstraints(names.DefaultConstraints, clusterName, keyspaceName, keyRange.SafeName(), planetscalev2.VtbackupComponentName, "init")
}''', '''func InitialBackupPodName(clusterName, keyspaceName string, keyRange planetscalev2.VitessKeyRange) string {
\treturn names.JoinWithConstraints(names.DefaultConstraints, clusterName, keyspaceName, keyRange.SafeName(), planetscalev2.VtbackupComponentName, "init")
}

// FirstLiveBackupPodName returns the stable name for the first backup copied
// from a serving shard. A stable name makes retries reuse one Pod and PVC.
func FirstLiveBackupPodName(clusterName, keyspaceName string, keyRange planetscalev2.VitessKeyRange) string {
\treturn names.JoinWithConstraints(names.DefaultConstraints, clusterName, keyspaceName, keyRange.SafeName(), planetscalev2.VtbackupComponentName, "first-live")
}''')
    text = replace(text, 'volumeMounts = append(volumeMounts, vttabletVolumeMounts.Get(tabletSpec)...)',
                   '''volumeMounts = append(volumeMounts, vttabletVolumeMounts.Get(tabletSpec)...)
	update.VolumeMounts(&volumeMounts, tabletSpec.ExtraVolumeMounts)''')
    text = replace(text, '\tupdate.ResourceRequirements(&containerResources, &tabletSpec.Mysqld.Resources)',
                   '''	update.ResourceRequirements(&containerResources, &tabletSpec.Mysqld.Resources)
	// vtbackup and its mysqld child share one cgroup. Reserve both process
	// budgets, as the ordinary tablet pod does with separate containers.
	for _, name := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
		if containerResources.Requests == nil { containerResources.Requests = corev1.ResourceList{} }
		if containerResources.Limits == nil { containerResources.Limits = corev1.ResourceList{} }
		request := containerResources.Requests[name]
		extraRequest, extraLimit := tabletSpec.Vttablet.Resources.Requests[name], tabletSpec.Vttablet.Resources.Limits[name]
		if name == corev1.ResourceMemory {
			// The supported 1 TiB maximum can require two 105 MiB S3 parts.
			extraRequest, extraLimit = resource.MustParse("512Mi"), resource.MustParse("512Mi")
		}
		request.Add(extraRequest)
		containerResources.Requests[name] = request
		limit := containerResources.Limits[name]
		limit.Add(extraLimit)
		containerResources.Limits[name] = limit
	}''')
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

    path = root / "pkg/operator/vttablet/flags.go"
    text = changes[path]
    text = replace(text, '''\t\t\t"initial_backup":      backupSpec.InitialBackup,
\t\t\t"min_backup_interval": backupSpec.MinBackupInterval,''', '''\t\t\t"initial_backup":      backupSpec.InitialBackup,
\t\t\t"allow_first_backup":  backupSpec.AllowFirstBackup,
\t\t\t"min_backup_interval": backupSpec.MinBackupInterval,''')
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
    text = replace(text, '''\tbackupType := vitessbackup.TypeUpdate
\tif len(completedBackups) == 0 {
\t\tif vts.Status.HasMaster == corev1.ConditionTrue {
\t\t\treturn nil, nil, fmt.Errorf("this shard has 0 backup and a running primary, the schedule cannot create an empty backup, please create a backup manually first")
\t\t} else {
\t\t\tbackupType = vitessbackup.TypeInit
\t\t}
\t}''', '''\tbackupType := vitessbackup.TypeUpdate
\tif len(completedBackups) == 0 {
\t\treturn nil, nil, fmt.Errorf("the shard controller has not completed the first native backup")
\t}''')
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
    pod = root / "pkg/operator/vttablet/pod.go"
    pod_text = pod.read_text()
    pod_text = replace(
        pod_text,
        '''ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					// We can't use /debug/health for vttablet as we do for
					// other Vitess servers. On vttablet, that handler has been
					// corrupted into a useless hybrid of readiness and liveness
					// that can't be fixed because it would break legacy users.
					// Instead, vttablet (and only vttablet) has /healthz for
					// actual readiness.
					Path: "/healthz",
					Port: intstr.FromString(planetscalev2.DefaultWebPortName),
				},
			},
		},''',
        '''ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{
				"bash", "-ceu",
				"curl --fail --silent --show-error --max-time 2 http://127.0.0.1:15000/healthz >/dev/null; test -S /vt/socket/mysql.sock; test -S /vt/socket/mysqlctl.sock",
			}}},
			TimeoutSeconds:   3,
			PeriodSeconds:    10,
			SuccessThreshold: 1,
			FailureThreshold: 3,
		},''')
    pod_text = replace(
        pod_text,
        '''ReadinessProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					TCPSocket: &corev1.TCPSocketAction{
						Port: intstr.FromInt(planetscalev2.DefaultMysqlPort),
					},
				},
				PeriodSeconds: 2,
			},''',
        '''ReadinessProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{
					"bash", "-ceu",
					"exec 3<>/dev/tcp/127.0.0.1/3306; exec 3>&-; test -S /vt/socket/mysql.sock; test -S /vt/socket/mysqlctl.sock",
				}}},
				TimeoutSeconds:   1,
				PeriodSeconds:    2,
				SuccessThreshold: 1,
				FailureThreshold: 3,
			},''')
    changes[pod] = pod_text
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
        ("vitess-command-log-test.go.txt", vitess / "go/vt/mysqlctl/hakopod_command_log_test.go"),
        ("vitess-s3-completion.go.txt", vitess / "go/vt/mysqlctl/s3backupstorage/hakopod_completion.go"),
        ("vitess-s3-completion-test.go.txt", vitess / "go/vt/mysqlctl/s3backupstorage/hakopod_completion_test.go"),
        ("vitess-shared-socket-test.go.txt", operator / "pkg/operator/vttablet/hakopod_shared_socket_test.go"),
        ("vitess-backup-schedule.go.txt", operator / "pkg/controller/vitessbackupschedule/hakopod_backup.go"),
        ("vitess-backup-schedule-test.go.txt", operator / "pkg/controller/vitessbackupschedule/hakopod_backup_test.go"),
        ("vitess-api-compatibility-test.go.txt", operator / "pkg/controller/vitessbackupschedule/hakopod_api_test.go"),
        ("vitess-control-permissions-test.go.txt", operator / "pkg/controller/vitessbackupschedule/hakopod_control_permissions_test.go"),
        ("vitess-backup-pod-test.go.txt", operator / "pkg/operator/vttablet/hakopod_backup_test.go"),
        ("vitess-first-live-backup-test.go.txt", operator / "pkg/controller/vitessshard/hakopod_first_live_backup_test.go"),
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
