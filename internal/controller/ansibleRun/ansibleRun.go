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
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/apenella/go-ansible/pkg/stdoutcallback/results"
	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/pkg/event"
	"github.com/crossplane/crossplane-runtime/pkg/logging"
	"github.com/crossplane/crossplane-runtime/pkg/meta"
	"github.com/crossplane/crossplane-runtime/pkg/ratelimiter"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/pkg/resource"
	"github.com/crossplane/provider-ansible/apis/v1alpha1"
	"github.com/crossplane/provider-ansible/internal/ansible"
	"github.com/crossplane/provider-ansible/pkg/galaxyutil"
	"github.com/crossplane/provider-ansible/pkg/runnerutil"
	"github.com/pkg/errors"
	"github.com/spf13/afero"
	"gopkg.in/yaml.v2"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
)

const (
	errNotAnsibleRun       = "managed resource is not a AnsibleRun custom resource"
	errTrackPCUsage        = "cannot track ProviderConfig usage"
	errGetPC               = "cannot get ProviderConfig"
	errGetCreds            = "cannot get credentials"
	errWriteGitCreds       = "cannot write .git-credentials to /tmp dir"
	errWriteConfig         = "cannot write ansible collection requirements in" + galaxyutil.RequirementsFile
	errWriteCreds          = "cannot write Playbook credentials"
	errRemoteConfiguration = "cannot get remote AnsibleRun configuration"
	errWriteAnsibleRun     = "cannot write AnsibleRun configuration in" + runnerutil.PlaybookYml
	errMarshalRoles        = "cannot marshal Roles into yaml document"
	errMkdir               = "cannot make Playbook directory"
	errInit                = "cannot initialize Ansible client"
	gitCredentialsFilename = ".git-credentials"

	errGetAnsibleRun     = "cannot get AnsibleRun"
	errGetLastApplied    = "cannot get last applied"
	errUnmarshalTemplate = "cannot unmarshal template"
)

const (
	baseWorkingDir = "/ansibleDir"
)

type params interface {
	Init(ctx context.Context, cr *v1alpha1.AnsibleRun, pc *v1alpha1.ProviderConfig, behaviorVars map[string]string) (*ansible.Runner, error)
	GalaxyInstall(ctx context.Context, behaviorVars map[string]string, requirementsType string) error
}

type ansibleRunner interface {
	GetAnsibleRunPolicy() *ansible.RunPolicy
	WriteExtraVar(extraVar map[string]interface{}) error
	EnableCheckMode(checkMode bool)
	Run() (*exec.Cmd, error)
}

// Setup adds a controller that reconciles AnsibleRun managed resources.
func Setup(mgr ctrl.Manager, l logging.Logger, rl workqueue.RateLimiter, ansibleCollectionsPath, ansibleRolesPath string) error {
	name := managed.ControllerName(v1alpha1.AnsibleRunGroupKind)

	o := controller.Options{
		RateLimiter: ratelimiter.NewDefaultManagedRateLimiter(rl),
	}

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
				WorkingDirPath:  dir,
				GalaxyBinary:    galaxyBinary,
				RunnerBinary:    runnerBinary,
				CollectionsPath: ansibleCollectionsPath,
				RolesPath:       ansibleRolesPath,
			}
		},
	}

	r := managed.NewReconciler(mgr,
		resource.ManagedKind(v1alpha1.AnsibleRunGroupVersionKind),
		managed.WithExternalConnecter(c),
		managed.WithLogger(l.WithValues("controller", name)),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))))

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithOptions(o).
		For(&v1alpha1.AnsibleRun{}).
		Complete(r)
}

// A connector is expected to produce an ExternalClient when its Connect method
// is called.
type connector struct {
	kube    client.Client
	usage   resource.Tracker
	fs      afero.Afero
	ansible func(dir string) params
}

func (c *connector) Connect(ctx context.Context, mg resource.Managed) (managed.ExternalClient, error) { //nolint:gocyclo
	// NOTE(negz): This method is slightly over our complexity goal, but I
	// can't immediately think of a clean way to decompose it without
	// affecting readability.

	cr, ok := mg.(*v1alpha1.AnsibleRun)
	if !ok {
		return nil, errors.New(errNotAnsibleRun)
	}

	// NOTE(negz): This directory will be garbage collected by the workdir
	// garbage collector that is started in Setup.
	dir := filepath.Join(baseWorkingDir, string(cr.GetUID()))
	if err := c.fs.MkdirAll(dir, 0700); resource.Ignore(os.IsExist, err) != nil {
		return nil, errors.Wrap(err, errMkdir)
	}

	if err := c.usage.Track(ctx, mg); err != nil {
		return nil, errors.Wrap(err, errTrackPCUsage)
	}

	pc := &v1alpha1.ProviderConfig{}
	if err := c.kube.Get(ctx, types.NamespacedName{Name: cr.GetProviderConfigReference().Name}, pc); err != nil {
		return nil, errors.Wrap(err, errGetPC)
	}

	var requirementRoles []byte
	if len(cr.Spec.ForProvider.Roles) != 0 {
		// marshall cr.Spec.ForProvider.Roles entries into yaml document
		rolesMap := make(map[string][]v1alpha1.Role)
		rolesMap["roles"] = cr.Spec.ForProvider.Roles
		var err error
		requirementRoles, err = yaml.Marshal(&rolesMap)
		if err != nil {
			return nil, errors.Wrap(err, errMarshalRoles)
		}
		// prepare git credentials for ansible-galaxy to fetch remote roles
		// TODO(fahed) support other private remote repository
		// NOTE(ytsarev): Retrieve .git-credentials from Spec to /tmp outside of AnsibleRun directory
		gitCredDir := filepath.Clean(filepath.Join("/tmp", dir))
		if err := c.fs.MkdirAll(gitCredDir, 0700); err != nil {
			return nil, errors.Wrap(err, errWriteGitCreds)
		}
		for _, cd := range pc.Spec.Credentials {
			if cd.Filename != gitCredentialsFilename {
				continue
			}
			data, err := resource.CommonCredentialExtractor(ctx, cd.Source, c.kube, cd.CommonCredentialSelectors)
			if err != nil {
				return nil, errors.Wrap(err, errGetCreds)
			}
			p := filepath.Clean(filepath.Join(gitCredDir, filepath.Base(cd.Filename)))
			if err := c.fs.WriteFile(p, data, 0600); err != nil {
				return nil, errors.Wrap(err, errWriteGitCreds)
			}
			// NOTE(ytsarev): Make go-getter pick up .git-credentials, see /.gitconfig in the container image
			// TODO: check wether go-getter is used in the ansible case
			err = os.Setenv("GIT_CRED_DIR", gitCredDir)
			if err != nil {
				return nil, errors.Wrap(err, errRemoteConfiguration)
			}
		}
	} else if cr.Spec.ForProvider.PlaybookInline != nil {
		if err := c.fs.WriteFile(filepath.Join(dir, runnerutil.PlaybookYml), []byte(*cr.Spec.ForProvider.PlaybookInline), 0600); err != nil {
			return nil, errors.Wrap(err, errWriteAnsibleRun)
		}
	}

	// Saved credentials needed for ansible playbooks execution
	for _, cd := range pc.Spec.Credentials {
		data, err := resource.CommonCredentialExtractor(ctx, cd.Source, c.kube, cd.CommonCredentialSelectors)
		if err != nil {
			return nil, errors.Wrap(err, errGetCreds)
		}
		p := filepath.Clean(filepath.Join(dir, filepath.Base(cd.Filename)))
		if err := c.fs.WriteFile(p, data, 0600); err != nil {
			return nil, errors.Wrap(err, errWriteCreds)
		}
	}

	ps := c.ansible(dir)

	// prepare behavior vars
	behaviorVars, err := addBehaviorVars(pc)
	if err != nil {
		return nil, err
	}

	// Requirements is a list of collections/roles to be installed, it is stored in requirements file
	requirementRolesStr := string(requirementRoles)
	if pc.Spec.Requirements != nil || requirementRolesStr != "" {
		var requirementsType string
		var reqSlice []string
		if pc.Spec.Requirements != nil {
			reqSlice = append(reqSlice, *pc.Spec.Requirements)
			requirementsType = "collection"
		}
		if requirementRolesStr != "" {
			reqSlice = append(reqSlice, requirementRolesStr)
			requirementsType = "role"
		}

		// write requirements to requirements.yml
		req := strings.Join(reqSlice, "\n")
		if err := c.fs.WriteFile(filepath.Join(dir, galaxyutil.RequirementsFile), []byte(req), 0600); err != nil {
			return nil, errors.Wrap(err, errWriteConfig)
		}
		// install ansible requirements using ansible-galaxy
		switch requirementsType {
		case "collection":
			if err := ps.GalaxyInstall(ctx, behaviorVars, requirementsType); err != nil {
				return nil, err
			}
		case "role":
			if err := ps.GalaxyInstall(ctx, behaviorVars, requirementsType); err != nil {
				return nil, err
			}
		}

	}

	r, err := ps.Init(ctx, cr, pc, behaviorVars)
	if err != nil {
		return nil, errors.Wrap(err, errInit)
	}

	return &external{runner: r, kube: c.kube}, nil
}

type external struct {
	runner ansibleRunner
	kube   client.Client
}

// nolint: gocyclo
// TODO reduce cyclomatic complexity
func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*v1alpha1.AnsibleRun)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotAnsibleRun)
	}
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
			return managed.ExternalObservation{}, errors.Wrap(err, errGetAnsibleRun)
		}
		var lastParameters *v1alpha1.AnsibleRunParameters
		var err error
		lastParameters, err = getLastAppliedParameters(observed)
		if err != nil {
			return managed.ExternalObservation{}, errors.Wrap(err, errGetLastApplied)
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
		dc, err := c.runner.Run()
		if err != nil {
			return managed.ExternalObservation{}, err
		}
		res, err := results.ParseJSONResultsStream(os.Stdout)
		if err != nil {
			return managed.ExternalObservation{}, err
		}
		if err = dc.Wait(); err != nil {
			return managed.ExternalObservation{}, err
		}
		changes, exists := ansible.Diff(res)

		return managed.ExternalObservation{
			ResourceExists:          exists,
			ResourceUpToDate:        !changes,
			ResourceLateInitialized: false,
		}, nil
	default:

	}

	return managed.ExternalObservation{}, nil
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	// No difference from the provider side which lifecycle method to choose in this case of Create() or Update()
	u, err := c.Update(ctx, mg)
	return managed.ExternalCreation{ConnectionDetails: u.ConnectionDetails}, err
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	_, ok := mg.(*v1alpha1.AnsibleRun)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNotAnsibleRun)
	}

	// disable checkMode for real action
	c.runner.EnableCheckMode(false)
	dc, err := c.runner.Run()
	if err != nil {
		return managed.ExternalUpdate{}, err
	}
	if err = dc.Wait(); err != nil {
		return managed.ExternalUpdate{}, err
	}

	// TODO handle ConnectionDetails https://github.com/multicloudlab/crossplane-provider-ansible/pull/74#discussion_r888467991
	return managed.ExternalUpdate{ConnectionDetails: nil}, nil
}

func (c *external) Delete(_ context.Context, mg resource.Managed) error {
	cr, ok := mg.(*v1alpha1.AnsibleRun)
	if !ok {
		return errors.New(errNotAnsibleRun)
	}

	cr.Status.SetConditions(xpv1.Deleting())

	stateVar := make(map[string]string)
	stateVar["state"] = "absent"
	nestedMap := make(map[string]interface{})
	nestedMap[cr.GetName()] = stateVar
	if err := c.runner.WriteExtraVar(nestedMap); err != nil {
		return err
	}
	dc, err := c.runner.Run()
	if err != nil {
		return err
	}
	if err = dc.Wait(); err != nil {
		return err
	}
	return nil
}

func getLastAppliedParameters(observed *v1alpha1.AnsibleRun) (*v1alpha1.AnsibleRunParameters, error) {
	lastApplied, ok := observed.GetAnnotations()[v1.LastAppliedConfigAnnotation]
	if !ok {
		return nil, nil
	}
	lastParameters := &v1alpha1.AnsibleRunParameters{}
	if err := json.Unmarshal([]byte(lastApplied), lastParameters); err != nil {
		return nil, errors.Wrap(err, errUnmarshalTemplate)
	}

	return lastParameters, nil
}

// nolint: gocyclo
// TODO reduce cyclomatic complexity
func (c *external) handleLastApplied(ctx context.Context, lastParameters *v1alpha1.AnsibleRunParameters, desired *v1alpha1.AnsibleRun) (managed.ExternalObservation, error) {
	isUpToDate := false
	if lastParameters != nil {
		if equality.Semantic.DeepEqual(*lastParameters, desired.Spec.ForProvider) {
			// Mark as up-to-date since last is equal to desired
			isUpToDate = true
		}
	}

	if !isUpToDate {
		stateVar := make(map[string]string)
		stateVar["state"] = "present"
		nestedMap := make(map[string]interface{})
		nestedMap[desired.GetName()] = stateVar
		if err := c.runner.WriteExtraVar(nestedMap); err != nil {
			return managed.ExternalObservation{}, err
		}
		dc, err := c.runner.Run()
		if err != nil {
			return managed.ExternalObservation{}, err
		}
		if err = dc.Wait(); err != nil {
			return managed.ExternalObservation{}, err
		}

		out, err := json.Marshal(desired.Spec.ForProvider)
		if err != nil {
			return managed.ExternalObservation{}, err
		}
		// set LastAppliedConfig Annotation to avoid useless cmd run
		meta.AddAnnotations(desired, map[string]string{
			v1.LastAppliedConfigAnnotation: string(out),
		})
		// set Deletion Policy to Orphan for non-existence of external resource
		desired.SetDeletionPolicy(xpv1.DeletionOrphan)

		if err := c.kube.Update(ctx, desired); err != nil {
			return managed.ExternalObservation{}, err
		}
	}

	// The crossplane runtime is not aware of the external resource created by ansible content.
	// Nothing will notify us if and when the ansible content we manage
	// changes, so we requeue a speculative reconcile after the specified poll
	// interval in order to observe it and react accordingly.
	return managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: true}, nil
}

func addBehaviorVars(pc *v1alpha1.ProviderConfig) (map[string]string, error) {
	behaviorVars := make(map[string]string, len(pc.Spec.Vars))
	for _, v := range pc.Spec.Vars {
		varB, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(varB, &behaviorVars); err != nil {
			return nil, err
		}
	}
	return behaviorVars, nil
}
