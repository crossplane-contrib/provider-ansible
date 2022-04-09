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
	"fmt"
	"os"
	"path/filepath"

	"github.com/crossplane/crossplane-runtime/pkg/event"
	"github.com/crossplane/crossplane-runtime/pkg/logging"
	"github.com/crossplane/crossplane-runtime/pkg/ratelimiter"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/pkg/resource"
	"github.com/crossplane/provider-ansible/apis/v1alpha1"
	"github.com/crossplane/provider-ansible/internal/ansible"
	"github.com/crossplane/provider-ansible/pkg/galaxyutil"
	"github.com/crossplane/provider-ansible/pkg/runnerutil"
	getter "github.com/hashicorp/go-getter"
	"github.com/pkg/errors"
	"github.com/spf13/afero"
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
	errRemoteConfiguration = "cannot get remote AnsibleRun configuration "
	errWriteAnsibleRun     = "cannot write AnsibleRun configuration in" + runnerutil.PlaybookYml
	errMkdir               = "cannot make Playbook directory"
	errInit                = "cannot initialize Ansible client"
	gitCredentialsFilename = ".git-credentials"
)

const (
	baseWorkingDir = "ansibleDir"
)

type params interface {
	Init(ctx context.Context, cr *v1alpha1.AnsibleRun) (*ansible.Runner, error)
}

// Setup adds a controller that reconciles AnsibleRun managed resources.
func Setup(mgr ctrl.Manager, l logging.Logger, rl workqueue.RateLimiter, ansibleCollectionsPath, ansibleRolesPath string) error {
	name := managed.ControllerName(v1alpha1.AnsibleRunGroupKind)

	o := controller.Options{
		RateLimiter: ratelimiter.NewDefaultManagedRateLimiter(rl),
	}

	fs := afero.Afero{Fs: afero.NewOsFs()}

	c := &connector{
		kube:  mgr.GetClient(),
		usage: resource.NewProviderConfigUsageTracker(mgr.GetClient(), &v1alpha1.ProviderConfigUsage{}),
		fs:    fs,
		ansible: func(dir string) params {
			return ansible.Parameters{
				WorkingDir:      dir,
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

	switch cr.Spec.ForProvider.Source {
	case v1alpha1.ConfigurationSourceRemote:
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

		client := getter.Client{
			Src:  cr.Spec.ForProvider.Module,
			Dst:  dir,
			Pwd:  dir,
			Mode: getter.ClientModeDir,
		}
		err := client.Get()
		if err != nil {
			return nil, errors.Wrap(err, errRemoteConfiguration)
		}
	case v1alpha1.ConfigurationSourceInline:
		if err := c.fs.WriteFile(filepath.Join(dir, runnerutil.PlaybookYml), []byte(cr.Spec.ForProvider.Module), 0600); err != nil {
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

	// Requirements is a list of collections to be installed, it is stored in requirements file
	if pc.Spec.Requirements != nil {
		if err := c.fs.WriteFile(filepath.Join(dir, galaxyutil.RequirementsFile), []byte(*pc.Spec.Requirements), 0600); err != nil {
			return nil, errors.Wrap(err, errWriteConfig)
		}
	}

	ps := c.ansible(dir)

	r, err := ps.Init(ctx, cr)
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
	/*re, changes, _ := c.pbCmd.ParseResults(ctx, mg)

	if err != nil {
		return managed.ExternalObservation{}, err
	}*/
	return managed.ExternalObservation{
		//ResourceExists:          re,
		//ResourceUpToDate:        !changes,
		ResourceLateInitialized: false,
	}, nil
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
