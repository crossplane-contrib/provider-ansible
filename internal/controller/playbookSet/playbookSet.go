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

package playbook

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
	"github.com/pkg/errors"
	"github.com/spf13/afero"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	"github.com/crossplane/provider-ansible/apis/v1alpha1"
	"github.com/crossplane/provider-ansible/internal/ansible"
	getter "github.com/hashicorp/go-getter"
)

const (
	errNotPlaybookSet      = "managed resource is not a PlaybookSet custom resource"
	errTrackPCUsage        = "cannot track ProviderConfig usage"
	errGetPC               = "cannot get ProviderConfig"
	errGetCreds            = "cannot get credentials"
	errWriteGitCreds       = "cannot write .git-credentials to /tmp dir"
	errWriteCreds          = "cannot write Playbook credentials"
	errRemoteConfiguration = "cannot get remote PlaybookSet configuration "
	errWritePlaybookSet    = "cannot write PlaybookSet configuration in" + playbookYml
	errMkdir               = "cannot make Playbook directory"
	gitCredentialsFilename = ".git-credentials"
)

const (
	playbookSetDir = "playbooks"
	playbookYml    = "playbook.yml"
)

// Setup adds a controller that reconciles PlaybookSet managed resources.
func Setup(mgr ctrl.Manager, l logging.Logger, rl workqueue.RateLimiter) error {
	name := managed.ControllerName(v1alpha1.PlaybookSetGroupKind)

	o := controller.Options{
		RateLimiter: ratelimiter.NewDefaultManagedRateLimiter(rl),
	}

	fs := afero.Afero{Fs: afero.NewOsFs()}

	c := &connector{
		kube:  mgr.GetClient(),
		usage: resource.NewProviderConfigUsageTracker(mgr.GetClient(), &v1alpha1.ProviderConfigUsage{}),
		fs:    fs,
		ansible: func(o ...ansible.PlaybookOption) *ansible.PbClient {
			return ansible.NewAnsiblePlaybook(o)
		},
	}

	r := managed.NewReconciler(mgr,
		resource.ManagedKind(v1alpha1.PlaybookSetGroupVersionKind),
		managed.WithExternalConnecter(c),
		managed.WithLogger(l.WithValues("controller", name)),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))))

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithOptions(o).
		For(&v1alpha1.PlaybookSet{}).
		Complete(r)
}

// A connector is expected to produce an ExternalClient when its Connect method
// is called.
type connector struct {
	kube    client.Client
	usage   resource.Tracker
	fs      afero.Afero
	ansible func(o ...ansible.PlaybookOption) *ansible.PbClient
}

func (c *connector) Connect(ctx context.Context, mg resource.Managed) (managed.ExternalClient, error) { //nolint:gocyclo
	// NOTE(negz): This method is slightly over our complexity goal, but I
	// can't immediately think of a clean way to decompose it without
	// affecting readability.

	cr, ok := mg.(*v1alpha1.PlaybookSet)
	if !ok {
		return nil, errors.New(errNotPlaybookSet)
	}

	// NOTE(negz): This directory will be garbage collected by the workdir
	// garbage collector that is started in Setup.
	dir := filepath.Join(playbookSetDir, string(cr.GetUID()))
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
		// NOTE(ytsarev): Retrieve .git-credentials from Spec to /tmp outside of playbookSet directory
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
			Src: cr.Spec.ForProvider.Configuration,
			Dst: dir,
			Pwd: dir,

			Mode: getter.ClientModeDir,
		}
		err := client.Get()
		if err != nil {
			return nil, errors.Wrap(err, errRemoteConfiguration)
		}
	case v1alpha1.ConfigurationSourceInline:
		if err := c.fs.WriteFile(filepath.Join(dir, playbookYml), []byte(cr.Spec.ForProvider.Configuration), 0600); err != nil {
			return nil, errors.Wrap(err, errWritePlaybookSet)
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

	// NOTE(fahed): handle spec pc.Spec.Configuration

	// Read playbooks filename from dir
	pbList, err := ansible.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	pbClient := c.ansible(ansible.WithPlaybooks(pbList))

	return &external{pbClient: pbClient, kube: c.kube}, nil
}

type external struct {
	pbClient *ansible.PbClient
	kube     client.Reader
}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*v1alpha1.PlaybookSet)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotPlaybookSet)
	}

	// These fmt statements should be removed in the real implementation.
	fmt.Printf("Observing: %+v", cr)

	return managed.ExternalObservation{
		// Return false when the external resource does not exist. This lets
		// the managed resource reconciler know that it needs to call Create to
		// (re)create the resource, or that it has successfully been deleted.
		ResourceExists: true,

		// Return false when the external resource exists, but it not up to date
		// with the desired managed resource state. This lets the managed
		// resource reconciler know that it needs to call Update.
		ResourceUpToDate: true,

		// Return any details that may be required to connect to the external
		// resource. These will be stored as the connection secret.
		ConnectionDetails: managed.ConnectionDetails{},
	}, nil
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr, ok := mg.(*v1alpha1.PlaybookSet)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotPlaybookSet)
	}

	fmt.Printf("Creating: %+v", cr)

	return managed.ExternalCreation{
		// Optionally return any details that may be required to connect to the
		// external resource. These will be stored as the connection secret.
		ConnectionDetails: managed.ConnectionDetails{},
	}, nil
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	cr, ok := mg.(*v1alpha1.PlaybookSet)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNotPlaybookSet)
	}

	fmt.Printf("Updating: %+v", cr)

	return managed.ExternalUpdate{
		// Optionally return any details that may be required to connect to the
		// external resource. These will be stored as the connection secret.
		ConnectionDetails: managed.ConnectionDetails{},
	}, nil
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) error {
	cr, ok := mg.(*v1alpha1.PlaybookSet)
	if !ok {
		return errors.New(errNotPlaybookSet)
	}

	fmt.Printf("Deleting: %+v", cr)

	return nil
}
