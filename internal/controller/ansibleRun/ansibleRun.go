/*
Copyright 2020 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package ansiblerun

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/apenella/go-ansible/pkg/stdoutcallback/results"
	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/pkg/controller"
	"github.com/crossplane/crossplane-runtime/pkg/event"
	"github.com/crossplane/crossplane-runtime/pkg/logging"
	"github.com/crossplane/crossplane-runtime/pkg/meta"
	"github.com/crossplane/crossplane-runtime/pkg/ratelimiter"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/pkg/resource"
	"github.com/crossplane/crossplane-runtime/pkg/statemetrics"
	"github.com/google/uuid"
	"github.com/spf13/afero"
	coordinationv1 "k8s.io/api/coordination/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/crossplane-contrib/provider-ansible/apis/v1alpha1"
	"github.com/crossplane-contrib/provider-ansible/internal/ansible"
	"github.com/crossplane-contrib/provider-ansible/pkg/galaxyutil"
	"github.com/crossplane-contrib/provider-ansible/pkg/runnerutil"
	"github.com/crossplane-contrib/provider-ansible/pkg/shardutil"
)

const (
	errNotAnsibleRun       = "managed resource is not a AnsibleRun custom resource"
	errTrackPCUsage        = "cannot track ProviderConfig usage"
	errGetPC               = "cannot get ProviderConfig"
	errGetCreds            = "cannot get credentials"
	errGetInventory        = "cannot get Inventory"
	errWriteGitCreds       = "cannot write .git-credentials to /tmp dir"
	errWriteConfig         = "cannot write ansible collection requirements in" + galaxyutil.RequirementsFile
	errWriteCreds          = "cannot write Playbook credentials"
	errRemoteConfiguration = "cannot get remote AnsibleRun configuration"
	errWriteAnsibleRun     = "cannot write AnsibleRun configuration in" + runnerutil.PlaybookYml
	errWriteInventory      = "cannot write AnsibleRun inventory in"
	errChmodInventory      = "cannot change permissions of inventory file"
	errMarshalRoles        = "cannot marshal Roles into yaml document"
	errMkdir               = "cannot make directory"
	errInit                = "cannot initialize Ansible client"
	gitCredentialsFilename = ".git-credentials"

	errGetAnsibleRun     = "cannot get AnsibleRun"
	errGetLastApplied    = "cannot get last applied"
	errUnmarshalTemplate = "cannot unmarshal template"
)

const (
	leaseNameTemplate           = "provider-ansible-lease-%d"
	leaseDurationSeconds        = 30
	leaseRenewalInterval        = 5 * time.Second
	leaseAcquireAttemptInterval = 5 * time.Second
)

const (
	baseWorkingDir = "/ansibleDir"
)

type params interface {
	Init(ctx context.Context, cr *v1alpha1.AnsibleRun, behaviorVars map[string]string) (*ansible.Runner, error)
	GalaxyInstall(ctx context.Context, behaviorVars map[string]string, requirementsType string) error
}

type ansibleRunner interface {
	GetAnsibleRunPolicy() *ansible.RunPolicy
	WriteExtraVar(extraVar map[string]interface{}) error
	EnableCheckMode(checkMode bool)
	Run(ctx context.Context) (io.Reader, error)
}

// SetupOptions constains settings specific to the ansible run controller.
type SetupOptions struct {
	AnsibleCollectionsPath string
	AnsibleRolesPath       string
	Timeout                time.Duration
	ArtifactsHistoryLimit  int
	ReplicasCount          uint32
	ProviderCtx            context.Context
	ProviderCancel         context.CancelFunc
}

// Setup adds a controller that reconciles AnsibleRun managed resources.
func Setup(mgr ctrl.Manager, o controller.Options, s SetupOptions) error {
	name := managed.ControllerName(v1alpha1.AnsibleRunGroupKind)

	fs := afero.Afero{Fs: afero.NewOsFs()}

	galaxyBinary, err := galaxyutil.GalaxyBinary()
	if err != nil {
		return err
	}
	runnerBinary, err := runnerutil.RunnerBinary()
	if err != nil {
		return err
	}

	c := &connector{
		kube:  mgr.GetClient(),
		usage: resource.NewProviderConfigUsageTracker(mgr.GetClient(), &v1alpha1.ProviderConfigUsage{}),
		fs:    fs,
		ansible: func(dir string) params {
			return ansible.Parameters{
				WorkingDirPath:        dir,
				GalaxyBinary:          galaxyBinary,
				RunnerBinary:          runnerBinary,
				CollectionsPath:       s.AnsibleCollectionsPath,
				RolesPath:             s.AnsibleRolesPath,
				ArtifactsHistoryLimit: s.ArtifactsHistoryLimit,
			}
		},
		replicaID: uuid.New().String(),
		logger:    o.Logger,
	}

	opts := []managed.ReconcilerOption{
		managed.WithTypedExternalConnector(c),
		managed.WithLogger(o.Logger.WithValues("controller", name)),
		managed.WithTimeout(s.Timeout),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))),
	}

	if o.MetricOptions != nil {
		opts = append(opts, managed.WithMetricRecorder(o.MetricOptions.MRMetrics))
		if o.MetricOptions.MRStateMetrics != nil {
			stateMetricsRecorder := statemetrics.NewMRStateRecorder(mgr.GetClient(), o.Logger, o.MetricOptions.MRStateMetrics, &v1alpha1.AnsibleRunList{}, o.MetricOptions.PollStateMetricInterval)
			if err := mgr.Add(stateMetricsRecorder); err != nil {
				return fmt.Errorf("cannot register MR state metrics recorder for kind v1alpha1.AnsibleRunList: %w", err)
			}
		}
	}

	r := managed.NewReconciler(mgr, resource.ManagedKind(v1alpha1.AnsibleRunGroupVersionKind), opts...)

	currentShard, err := c.acquireAndHoldShard(o, s)
	if err != nil {
		return fmt.Errorf("cannot acquire and hold shard: %w", err)
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithOptions(o.ForControllerRuntime()).
		For(&v1alpha1.AnsibleRun{}).
		WithEventFilter(shardutil.IsResourceForShard(currentShard, s.ReplicasCount)).
		Complete(ratelimiter.NewReconciler(name, r, o.GlobalRateLimiter))
}

// A connector is expected to produce an ExternalClient when its Connect method
// is called.
type connector struct {
	kube      client.Client
	usage     resource.Tracker
	fs        afero.Afero
	ansible   func(dir string) params
	replicaID string
	logger    logging.Logger
}

func (c *connector) Connect(ctx context.Context, cr *v1alpha1.AnsibleRun) (managed.TypedExternalClient[*v1alpha1.AnsibleRun], error) { //nolint:gocyclo
	// NOTE(negz): This method is slightly over our complexity goal, but I
	// can't immediately think of a clean way to decompose it without
	// affecting readability.

	// NOTE(negz): This directory will be garbage collected by the workdir
	// garbage collector that is started in Setup.
	dir := filepath.Join(baseWorkingDir, string(cr.GetUID()))
	if err := c.fs.MkdirAll(dir, 0700); resource.Ignore(os.IsExist, err) != nil {
		return nil, fmt.Errorf("%s: %s: %w", baseWorkingDir, errMkdir, err)
	}

	if err := c.usage.Track(ctx, cr); err != nil {
		return nil, fmt.Errorf("%s: %w", errTrackPCUsage, err)
	}

	pc := &v1alpha1.ProviderConfig{}
	if err := c.kube.Get(ctx, types.NamespacedName{Name: cr.GetProviderConfigReference().Name}, pc); err != nil {
		return nil, fmt.Errorf("%s: %w", errGetPC, err)
	}
	var inventoryPerm os.FileMode = 0600
	if cr.Spec.ForProvider.ExecutableInventory {
		inventoryPerm = 0700
	}
	// Saved inventory needed for ansible content hosts
	var buff bytes.Buffer
	for _, i := range cr.Spec.ForProvider.Inventories {
		data, err := resource.CommonCredentialExtractor(ctx, i.Source, c.kube, i.CommonCredentialSelectors)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", errGetInventory, err)
		}
		if _, err := buff.WriteString(string(data) + "\n"); err != nil {
			return nil, err
		}
	}
	if cr.Spec.ForProvider.InventoryInline != nil {
		if _, err := buff.WriteString(*cr.Spec.ForProvider.InventoryInline + "\n"); err != nil {
			return nil, err
		}
	}
	if buff.Len() != 0 {
		if err := c.fs.WriteFile(filepath.Join(dir, runnerutil.Hosts), buff.Bytes(), inventoryPerm); err != nil {
			return nil, fmt.Errorf("%s %s: %w", errWriteInventory, runnerutil.Hosts, err)
		}
		// WriteFile only sets permissions for new files, do an explicit chmod to ensure changing permissions are updated
		// on existing files
		err := c.fs.Chmod(filepath.Join(dir, runnerutil.Hosts), inventoryPerm)
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", errChmodInventory, runnerutil.Hosts, err)
		}
	}

	var requirementRoles []byte
	if len(cr.Spec.ForProvider.Roles) != 0 {
		// marshall cr.Spec.ForProvider.Roles entries into yaml document
		rolesMap := make(map[string][]v1alpha1.Role)
		rolesMap["roles"] = cr.Spec.ForProvider.Roles
		var err error
		requirementRoles, err = yaml.Marshal(&rolesMap)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", errMarshalRoles, err)
		}
		// prepare git credentials for ansible-galaxy to fetch remote roles
		// TODO(fahed) support other private remote repository
		// NOTE(ytsarev): Retrieve .git-credentials from Spec to /tmp outside of AnsibleRun directory
		gitCredDir := filepath.Clean(filepath.Join("/tmp", dir))
		if err := c.fs.MkdirAll(gitCredDir, 0700); err != nil {
			return nil, fmt.Errorf("%s: %w", errWriteGitCreds, err)
		}
		for _, cd := range pc.Spec.Credentials {
			if cd.Filename != gitCredentialsFilename {
				continue
			}
			data, err := resource.CommonCredentialExtractor(ctx, cd.Source, c.kube, cd.CommonCredentialSelectors)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", errGetCreds, err)
			}
			p := filepath.Clean(filepath.Join(gitCredDir, filepath.Base(cd.Filename)))
			if err := c.fs.WriteFile(p, data, 0600); err != nil {
				return nil, fmt.Errorf("%s: %w", errWriteGitCreds, err)
			}
			// NOTE(ytsarev): Make go-getter pick up .git-credentials, see /.gitconfig in the container image
			// TODO: check wether go-getter is used in the ansible case
			err = os.Setenv("GIT_CRED_DIR", gitCredDir)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", errRemoteConfiguration, err)
			}
		}
	} else if cr.Spec.ForProvider.PlaybookInline != nil {
		if err := c.fs.WriteFile(filepath.Join(dir, runnerutil.PlaybookYml), []byte(*cr.Spec.ForProvider.PlaybookInline), 0600); err != nil {
			return nil, fmt.Errorf("%s: %w", errWriteAnsibleRun, err)
		}
	}

	// Saved credentials needed for ansible playbooks execution
	for _, cd := range pc.Spec.Credentials {
		data, err := resource.CommonCredentialExtractor(ctx, cd.Source, c.kube, cd.CommonCredentialSelectors)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", errGetCreds, err)
		}
		p := filepath.Clean(filepath.Join(dir, filepath.Base(cd.Filename)))
		if err := c.fs.WriteFile(p, data, 0600); err != nil {
			return nil, fmt.Errorf("%s: %w", errWriteCreds, err)
		}
	}

	ps := c.ansible(dir)

	// prepare behavior vars
	behaviorVars := addBehaviorVars(pc)

	// Requirements is a list of collections/roles to be installed, it is stored in requirements file
	requirementRolesStr := string(requirementRoles)
	if pc.Spec.Requirements != nil || requirementRolesStr != "" {
		var installCollections, installRoles bool
		var reqSlice []string
		if pc.Spec.Requirements != nil {
			reqSlice = append(reqSlice, *pc.Spec.Requirements)
			installCollections = true
			installRoles = true
		}
		if requirementRolesStr != "" {
			reqSlice = append(reqSlice, requirementRolesStr)
			installRoles = true
		}

		// write requirements to requirements.yml
		req := strings.Join(reqSlice, "\n")
		if err := c.fs.WriteFile(filepath.Join(dir, galaxyutil.RequirementsFile), []byte(req), 0600); err != nil {
			return nil, fmt.Errorf("%s: %w", errWriteConfig, err)
		}
		// install ansible requirements using ansible-galaxy
		if installCollections {
			if err := ps.GalaxyInstall(ctx, behaviorVars, "collection"); err != nil {
				return nil, err
			}
		}
		if installRoles {
			if err := ps.GalaxyInstall(ctx, behaviorVars, "role"); err != nil {
				return nil, err
			}
		}

	}

	r, err := ps.Init(ctx, cr, behaviorVars)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", errInit, err)

	}

	return &external{runner: r, kube: c.kube}, nil
}

type external struct {
	runner ansibleRunner
	kube   client.Client
}

func (e *external) Disconnect(ctx context.Context) error {
	// Unimplemented, required by newer versions of crossplane-runtime
	return nil
}

// nolint: gocyclo
// TODO reduce cyclomatic complexity
func (c *external) Observe(ctx context.Context, cr *v1alpha1.AnsibleRun) (managed.ExternalObservation, error) {
	/* set Deletion Policy to Orphan as we cannot observe the external resource.
	   So we won't wait for external resource deletion before attempting
	   to delete the managed resource */
	cr.SetDeletionPolicy(xpv1.DeletionOrphan)

	switch c.runner.GetAnsibleRunPolicy().Name {
	case "ObserveAndDelete", "":
		if c.runner.GetAnsibleRunPolicy().Name == "" {
			ansible.SetPolicyRun(cr, "ObserveAndDelete")
		}
		if meta.WasDeleted(cr) {
			return managed.ExternalObservation{ResourceExists: true}, nil
		}
		observed := cr.DeepCopy()
		if err := c.kube.Get(ctx, types.NamespacedName{
			Namespace: observed.GetNamespace(),
			Name:      observed.GetName(),
		}, observed); err != nil {
			if kerrors.IsNotFound(err) {
				return managed.ExternalObservation{ResourceExists: false}, nil
			}
			return managed.ExternalObservation{}, fmt.Errorf("%s: %w", errGetAnsibleRun, err)
		}
		var lastParameters *v1alpha1.AnsibleRunParameters
		var err error
		lastParameters, err = getLastAppliedParameters(observed)
		if err != nil {
			return managed.ExternalObservation{}, fmt.Errorf("%s: %w", errGetLastApplied, err)
		}
		return c.handleLastApplied(ctx, lastParameters, cr)
	case "CheckWhenObserve":
		stateVar := make(map[string]string)
		stateVar["state"] = "present"
		nestedMap := make(map[string]interface{})
		nestedMap[cr.GetName()] = stateVar
		if err := c.runner.WriteExtraVar(nestedMap); err != nil {
			return managed.ExternalObservation{}, err
		}
		c.runner.EnableCheckMode(true)
		stdoutBuf, err := c.runner.Run(ctx)
		if err != nil {
			return managed.ExternalObservation{}, err
		}
		res, err := results.ParseJSONResultsStream(stdoutBuf)
		if err != nil {
			return managed.ExternalObservation{}, err
		}
		changes := ansible.Diff(res)

		// At this level, the ansible cannot detect the existence or not of the external resource
		// due to the lack of the state in the ansible technology. So we consider that the externl resource
		// exists and trigger post-observation step(s) based on changes returned by the ansible-runner stats
		return managed.ExternalObservation{
			ResourceExists:          true,
			ResourceUpToDate:        !changes,
			ResourceLateInitialized: false,
		}, nil
	default:

	}

	return managed.ExternalObservation{}, nil
}

func (c *external) Create(ctx context.Context, cr *v1alpha1.AnsibleRun) (managed.ExternalCreation, error) {
	// No difference from the provider side which lifecycle method to choose in this case of Create() or Update()
	u, err := c.Update(ctx, cr)
	return managed.ExternalCreation(u), err
}

func (c *external) Update(ctx context.Context, cr *v1alpha1.AnsibleRun) (managed.ExternalUpdate, error) {
	// disable checkMode for real action
	c.runner.EnableCheckMode(false)
	if err := c.runAnsible(ctx, cr); err != nil {
		return managed.ExternalUpdate{}, fmt.Errorf("running ansible: %w", err)
	}

	// TODO handle ConnectionDetails https://github.com/multicloudlab/crossplane-provider-ansible/pull/74#discussion_r888467991
	return managed.ExternalUpdate{ConnectionDetails: nil}, nil
}

func (c *external) Delete(ctx context.Context, cr *v1alpha1.AnsibleRun) (managed.ExternalDelete, error) {
	cr.Status.SetConditions(xpv1.Deleting())

	stateVar := make(map[string]string)
	stateVar["state"] = "absent"
	nestedMap := make(map[string]interface{})
	nestedMap[cr.GetName()] = stateVar
	if err := c.runner.WriteExtraVar(nestedMap); err != nil {
		return managed.ExternalDelete{}, err
	}
	_, err := c.runner.Run(ctx)
	if err != nil {
		return managed.ExternalDelete{}, err
	}
	return managed.ExternalDelete{}, nil
}

func getLastAppliedParameters(observed *v1alpha1.AnsibleRun) (*v1alpha1.AnsibleRunParameters, error) {
	lastApplied, ok := observed.GetAnnotations()[v1.LastAppliedConfigAnnotation]
	if !ok {
		return nil, nil
	}
	lastParameters := &v1alpha1.AnsibleRunParameters{}
	if err := json.Unmarshal([]byte(lastApplied), lastParameters); err != nil {
		return nil, fmt.Errorf("%s: %w", errUnmarshalTemplate, err)
	}

	return lastParameters, nil
}

func (c *external) handleLastApplied(ctx context.Context, lastParameters *v1alpha1.AnsibleRunParameters, desired *v1alpha1.AnsibleRun) (managed.ExternalObservation, error) {
	// Mark as up-to-date if last is equal to desired
	isUpToDate := (lastParameters != nil && equality.Semantic.DeepEqual(*lastParameters, desired.Spec.ForProvider))

	isLastSyncOK := (desired.GetCondition(xpv1.TypeSynced).Status == v1.ConditionTrue)

	if isUpToDate && isLastSyncOK {
		desired.SetConditions(xpv1.Available())
		if err := c.kube.Status().Update(ctx, desired); err != nil {
			return managed.ExternalObservation{}, fmt.Errorf("updating status: %w", err)
		}
		// nothing to do for this run
		return managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: true}, nil
	}

	out, err := json.Marshal(desired.Spec.ForProvider)
	if err != nil {
		return managed.ExternalObservation{}, err
	}
	// set LastAppliedConfig Annotation to avoid useless cmd run
	meta.AddAnnotations(desired, map[string]string{
		v1.LastAppliedConfigAnnotation: string(out),
	})

	if err := c.kube.Update(ctx, desired); err != nil {
		return managed.ExternalObservation{}, err
	}
	stateVar := make(map[string]string)
	stateVar["state"] = "present"
	nestedMap := make(map[string]interface{})
	nestedMap[desired.GetName()] = stateVar
	if err := c.runner.WriteExtraVar(nestedMap); err != nil {
		return managed.ExternalObservation{}, err
	}

	if err := c.runAnsible(ctx, desired); err != nil {
		return managed.ExternalObservation{}, fmt.Errorf("running ansible: %w", err)
	}

	// The crossplane runtime is not aware of the external resource created by ansible content.
	// Nothing will notify us if and when the ansible content we manage
	// changes, so we requeue a speculative reconcile after the specified poll
	// interval in order to observe it and react accordingly.
	return managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: true}, nil
}

func (c *external) runAnsible(ctx context.Context, cr *v1alpha1.AnsibleRun) error {
	_, err := c.runner.Run(ctx)
	if err != nil {
		cond := xpv1.Unavailable()
		cond.Message = err.Error()
		cr.SetConditions(cond)
	} else {
		cr.SetConditions(xpv1.Available())
	}

	if err := c.kube.Status().Update(ctx, cr); err != nil {
		return fmt.Errorf("updating status: %w", err)
	}

	return err
}

func addBehaviorVars(pc *v1alpha1.ProviderConfig) map[string]string {
	behaviorVars := make(map[string]string, len(pc.Spec.Vars))
	for _, v := range pc.Spec.Vars {
		behaviorVars[v.Key] = v.Value
	}
	return behaviorVars
}

func (c *connector) generateLeaseName(index uint32) string {
	return fmt.Sprintf(leaseNameTemplate, index)
}

func (c *connector) releaseLease(ctx context.Context, kube client.Client, index uint32) error {
	leaseName := c.generateLeaseName(index)
	ns := "upbound-system"

	lease := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: leaseName},
	}

	return kube.Delete(ctx, lease)
}

// Attempts to acquire or renew a lease for the current replica ID
// Returns an error when unable to obtain the lease
func (c *connector) acquireLease(ctx context.Context, kube client.Client, index uint32) error {
	lease := &coordinationv1.Lease{}
	leaseName := c.generateLeaseName(index)
	leaseDurationSeconds := ptr.To(int32(leaseDurationSeconds))

	ns := "upbound-system"

	if err := kube.Get(ctx, client.ObjectKey{Namespace: ns, Name: leaseName}, lease); err != nil {
		if !kerrors.IsNotFound(err) {
			return err
		}

		// Create a new Lease
		lease = &coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{
				Name:      leaseName,
				Namespace: ns,
			},
			Spec: coordinationv1.LeaseSpec{
				HolderIdentity:       &c.replicaID,
				RenewTime:            &metav1.MicroTime{Time: time.Now()},
				LeaseDurationSeconds: leaseDurationSeconds,
			},
		}
		if err := kube.Create(ctx, lease); err != nil {
			return err
		}
		c.logger.Debug("created lease", "lease", lease)
		return nil
	}

	// Check if the lease is held by another replica and is not expired
	if lease.Spec.HolderIdentity != nil && *lease.Spec.HolderIdentity != c.replicaID {
		if lease.Spec.RenewTime != nil && time.Since(lease.Spec.RenewTime.Time) < time.Duration(*lease.Spec.LeaseDurationSeconds)*time.Second {
			// Lease is held by another replica and is not expired
			return fmt.Errorf("lease is still held by %s", *lease.Spec.HolderIdentity)
		}
	}

	// Update the lease to acquire it
	lease.Spec.HolderIdentity = ptr.To(c.replicaID)
	lease.Spec.RenewTime = &metav1.MicroTime{Time: time.Now()}
	lease.Spec.LeaseDurationSeconds = leaseDurationSeconds
	if err := kube.Update(ctx, lease); err != nil {
		if kerrors.IsConflict(err) {
			// Another replica updated the lease concurrently, retry
			return err
		}
		return fmt.Errorf("failed to update lease: %w", err)
	}

	c.logger.Debug("updated lease", "lease", lease)
	return nil
}

// Finds an available shard and acquires a lease for it. Will attempt to obtain one indefinitely.
// This will also start a background go-routine to renew the lease continuously and release it when the process receives a shutdown signal
func (c *connector) acquireAndHoldShard(o controller.Options, s SetupOptions) (uint32, error) {
	ctx := s.ProviderCtx
	var currentShard uint32

	cfg := ctrl.GetConfigOrDie()
	kube, err := client.New(cfg, client.Options{})
	if err != nil {
		return 0, err
	}

AcquireLease:
	for {
		for i := uint32(0); i < s.ReplicasCount; i++ {
			if err := c.acquireLease(ctx, kube, i); err == nil {
				currentShard = i
				o.Logger.Debug("acquired lease", "id", i)
				go func() {
					sigHandler := ctrl.SetupSignalHandler()

					for {
						select {
						case <-time.After(leaseRenewalInterval):
							if err := c.acquireLease(ctx, kube, i); err != nil {
								o.Logger.Info("failed to renew lease", "id", i, "err", err)
								s.ProviderCancel()
							} else {
								o.Logger.Debug("renewed lease", "id", i)
							}
						case <-sigHandler.Done():
							o.Logger.Info("controller is shutting down, releasing lease")
							if err := c.releaseLease(ctx, kube, i); err != nil {
								o.Logger.Info("failed to release lease", "lease", err)
							}
							o.Logger.Debug("released lease")
							s.ProviderCancel()
							return
						}
					}
				}()
				// Lease is acquired and background goroutine started for renewal, we can safely break to return the current shard
				break AcquireLease
			} else {
				o.Logger.Debug("cannot acquire lease", "id", i, "err", err)
				time.Sleep(leaseAcquireAttemptInterval)
			}
		}
	}

	return currentShard, nil
}
