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
	"fmt"
	"os"
	"path/filepath"

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
	baseWorkingDir = "ansibleDir"
)

type params interface {
	Init(ctx context.Context, cr *v1alpha1.AnsibleRun, pc *v1alpha1.ProviderConfig, behaviorVars map[string]string) (*ansible.Runner, error)
	AddFile(path string, content []byte) error
	GalaxyInstall(ctx context.Context, behaviorVars map[string]string, isRoleRequirements, isCollectionRequirements bool) error
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
		var err error
		requirementRoles, err = yaml.Marshal(&cr.Spec.ForProvider.Roles)
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
		req := fmt.Sprintf("%s\n%s", *pc.Spec.Requirements, requirementRolesStr)
		if err := c.fs.WriteFile(filepath.Join(dir, galaxyutil.RequirementsFile), []byte(req), 0600); err != nil {
			return nil, errors.Wrap(err, errWriteConfig)
		}
		var isCollectionRequirements, isRoleRequirements bool
		if pc.Spec.Requirements != nil {
			isCollectionRequirements = true
		} else if requirementRolesStr != "" {
			isRoleRequirements = true
		}
		// install ansible requirements using ansible-galaxy
		if err := ps.GalaxyInstall(ctx, behaviorVars, isCollectionRequirements, isRoleRequirements); err != nil {
			return nil, err
		}
	}

	// Committing the AnsibleRun's desired state (contentVars) to the filesystem at p.WorkingDirPath.
	contentVars := map[string]interface{}{}
	if len(cr.Spec.ForProvider.Vars) != 0 {
		for _, v := range cr.Spec.ForProvider.Vars {
			contentVars[v.Key] = v.Value
		}
	}
	contentVarsBytes, err := json.Marshal(contentVars)
	if err != nil {
		return nil, err
	}

	// prepare ansible extravars
	ansibleEnvDir := filepath.Clean(filepath.Join(dir, "env"))
	if err := c.fs.MkdirAll(ansibleEnvDir, 0700); resource.Ignore(os.IsExist, err) != nil {
		return nil, errors.Wrap(err, errMkdir)
	}
	if err := ps.AddFile("env/extravars", contentVarsBytes); err != nil {
		return nil, err
	}

	r, err := ps.Init(ctx, cr, pc, behaviorVars)
	if err != nil {
		return nil, errors.Wrap(err, errInit)
	}

	return &external{runner: r, kube: c.kube}, nil
}

type external struct {
	runner *ansible.Runner
	kube   client.Reader
}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*v1alpha1.AnsibleRun)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotAnsibleRun)
	}
	switch c.runner.AnsibleRunPolicy.Name {
	case "ObserveAndDelete", "":
		if c.runner.AnsibleRunPolicy.Name == "" {
			ansible.SetPolicyRun(mg, "ObserveAndDelete")
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
		var last *v1alpha1.AnsibleRun
		var err error
		last, err = getLastApplied(observed)
		if err != nil {
			return managed.ExternalObservation{}, errors.Wrap(err, errGetLastApplied)
		}

		return c.handleLastApplied(last, cr)
	case "CheckWhenObserve":
		// TODO
	default:

	}

	return managed.ExternalObservation{}, nil
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {

	// TODO see ConnectionDetails
	/*err := c.pbCmd.CreateOrUpdate(ctx, mg)
	if err != nil {
		return managed.ExternalCreation{}, err
	}*/

	return managed.ExternalCreation{
		// Optionally return any details that may be required to connect to the
		// external resource. These will be stored as the connection secret.
		ConnectionDetails: managed.ConnectionDetails{},
	}, nil
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {

	/*err := c.pbCmd.CreateOrUpdate(ctx, mg)
	if err != nil {
		return managed.ExternalUpdate{}, err
	}*/
	return managed.ExternalUpdate{
		// Optionally return any details that may be required to connect to the
		// external resource. These will be stored as the connection secret.
		ConnectionDetails: managed.ConnectionDetails{},
	}, nil
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) error {
	cr, ok := mg.(*v1alpha1.AnsibleRun)
	if !ok {
		return errors.New(errNotAnsibleRun)
	}

	fmt.Printf("Deleting: %+v", cr)

	return nil
}

func getLastApplied(observed *v1alpha1.AnsibleRun) (*v1alpha1.AnsibleRun, error) {
	lastApplied, ok := observed.GetAnnotations()[v1.LastAppliedConfigAnnotation]
	if !ok {
		return nil, nil
	}

	last := &v1alpha1.AnsibleRun{}
	if err := json.Unmarshal([]byte(lastApplied), last); err != nil {
		return nil, errors.Wrap(err, errUnmarshalTemplate)
	}

	if last.GetName() == "" {
		last.SetName(observed.GetName())
	}

	return last, nil
}

// nolint: gocyclo
// TODO reduce cyclomatic complexity
func (c *external) handleLastApplied(last, desired *v1alpha1.AnsibleRun) (managed.ExternalObservation, error) {
	isUpToDate := false

	if last != nil && equality.Semantic.DeepEqual(last, desired) {
		// Mark as up-to-date since last is equal to desired
		isUpToDate = true
	}

	if !isUpToDate {
		extraVarsPath := filepath.Join(c.runner.Path, "env/extravars")
		contentVars := map[string]interface{}{}
		data, err := os.ReadFile(extraVarsPath)
		if err != nil {
			if !os.IsNotExist(err) {
				return managed.ExternalObservation{}, err
			}
		}
		if len(data) != 0 {
			if err := json.Unmarshal(data, &contentVars); err != nil {
				return managed.ExternalObservation{}, err
			}
		}

		stateVar := map[string]string{"state": "present"}
		nestedMap := map[string]interface{}{desired.GetName(): stateVar}
		contentVars["ansible_provider_meta"] = nestedMap
		contentVarsB, err := json.Marshal(contentVars)
		if err != nil {
			return managed.ExternalObservation{}, nil
		}
		if err := os.WriteFile(extraVarsPath, contentVarsB, 0644); err != nil {
			return managed.ExternalObservation{}, err
		}
		out, err := c.runner.Run()
		if err != nil {
			return managed.ExternalObservation{}, errors.Wrap(err, out)
		}
	}

	return managed.ExternalObservation{}, nil
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

/*func getDesired(cr *v1alpha1.AnsibleRun) (*unstructured.Unstructured, error) {
	desired := &unstructured.Unstructured{}
	if _, err := json.Unmarshal([]byte(cr.Spec.ForProvider), desired); err != nil {
		return nil, errors.Wrap(err, errUnmarshalTemplate)
	}

	if desired.GetName() == "" {
		desired.SetName(cr.GetName())
	}
	return desired, nil
}*/
