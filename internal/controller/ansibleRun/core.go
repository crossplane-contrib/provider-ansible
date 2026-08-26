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
	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
	"github.com/spf13/afero"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	shared "github.com/crossplane-contrib/provider-ansible/apis/shared/v1alpha1"
	"github.com/crossplane-contrib/provider-ansible/internal/ansible"
	"github.com/crossplane-contrib/provider-ansible/pkg/galaxyutil"
	"github.com/crossplane-contrib/provider-ansible/pkg/runnerutil"
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
	errForeignSecret       = "a namespaced resource may only reference Secrets in its own namespace"
	errIncompleteScope     = "cannot build an AnsibleRun connector: these required Scope fields are nil"
	errNilPCSpec           = "the resolved ProviderConfig has no spec"
	gitCredentialsFilename = ".git-credentials"

	errGetAnsibleRun     = "cannot get AnsibleRun"
	errGetLastApplied    = "cannot get last applied"
	errUnmarshalTemplate = "cannot unmarshal template"
)

const (
	baseWorkingDir = "/ansibleDir"
)

// Run is what the shared core needs from either group's AnsibleRun type.
type Run interface {
	resource.Managed
	GetForProvider() *shared.AnsibleRunParameters
}

// Params is the Ansible engine entrypoint the core drives.
type Params interface {
	Init(ctx context.Context, params *shared.AnsibleRunParameters, o metav1.Object, behaviorVars map[string]string) (*ansible.Runner, error)
	GalaxyInstall(ctx context.Context, behaviorVars map[string]string, requirementsType string) error
}

// Scope carries the per-API-group behaviour the shared core cannot infer.
type Scope[T Run] struct {
	// Track records that the supplied managed resource is using its
	// ProviderConfig.
	Track func(ctx context.Context, cr T) error

	// ResolveProviderConfig returns the provider config the supplied managed
	// resource refers to, together with the secret-namespace policy that
	// applies to that config's own credentials.
	ResolveProviderConfig func(ctx context.Context, kube client.Client, cr T) (ResolvedProviderConfig, error)

	// SecretNamespace returns the namespace Secrets must be read from, and
	// whether the managed resource's own selectors may instead name a
	// namespace of its own choosing. It governs everything the managed
	// resource itself declares - spec.forProvider.inventories. The provider
	// config's own credentials additionally consult
	// ResolvedProviderConfig.AllowExplicitSecretNamespace.
	SecretNamespace func(cr T) (ns string, allowExplicit bool)

	// BeforeObserve, when set, runs at the very start of every Observe.
	BeforeObserve func(cr T)
}

// ResolvedProviderConfig is a provider config the core has resolved, together
// with the secret-namespace policy that applies to its own credentials.
//
// The policy travels with the config rather than with the managed resource
// because the two are not the same question. What a namespaced AnsibleRun
// declares for itself is confined to its own namespace; what a cluster-scoped
// ClusterProviderConfig declares is not. See the README's
// "ClusterProviderConfig credentials are cluster-wide" section.
type ResolvedProviderConfig struct {
	// Spec is the resolved provider config's spec.
	Spec *shared.ProviderConfigSpec

	// AllowExplicitSecretNamespace reports whether this provider config's own
	// credentials may name a namespace other than the managed resource's.
	AllowExplicitSecretNamespace bool
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
	LeaseNamespace         string
	ProviderCtx            context.Context
	ProviderCancel         context.CancelFunc

	// Shard is the index of the shard this provider process owns, as returned
	// by ShardLeaser.AcquireAndHoldShard. It is acquired once per process,
	// before any per-API-group Setup runs, and every group's controller
	// filters on it.
	Shard uint32
}

// ResolveSecretSelectors applies a secret-namespace policy to a credential
// selector. When allowExplicit is true the selector is returned untouched, so
// it may name any namespace. When it is false the selector is confined to ns:
// an empty namespace is filled in, and a differing one is refused.
//
// Callers take allowExplicit from Scope.SecretNamespace for selectors the
// managed resource itself declares, and from
// ResolvedProviderConfig.AllowExplicitSecretNamespace for the provider
// config's own credentials.
func ResolveSecretSelectors(sel xpv2.CommonCredentialSelectors, ns string, allowExplicit bool) (xpv2.CommonCredentialSelectors, error) {
	if allowExplicit || sel.SecretRef == nil {
		return sel, nil
	}
	if sel.SecretRef.Namespace != "" && sel.SecretRef.Namespace != ns {
		return sel, fmt.Errorf("%s: secretRef namespace %q differs from the resource namespace %q", errForeignSecret, sel.SecretRef.Namespace, ns)
	}
	ref := *sel.SecretRef
	ref.Namespace = ns
	out := sel
	out.SecretRef = &ref
	return out, nil
}

// A Connector is expected to produce an ExternalClient when its Connect method
// is called.
type Connector[T Run] struct {
	kube    client.Client
	fs      afero.Afero
	ansible func(dir string) Params
	scope   Scope[T]
}

// NewConnector returns a Connector that drives Ansible for the API group
// described by the supplied Scope.
//
// Every Scope field except BeforeObserve is required. They are validated here
// so that a mis-wired API group fails when its controller is set up, rather
// than panicking deep in the reconcile loop the first time a resource of that
// group is observed.
func NewConnector[T Run](kube client.Client, fs afero.Afero, ansible func(dir string) Params, scope Scope[T]) (*Connector[T], error) {
	var missing []string
	if scope.Track == nil {
		missing = append(missing, "Track")
	}
	if scope.ResolveProviderConfig == nil {
		missing = append(missing, "ResolveProviderConfig")
	}
	if scope.SecretNamespace == nil {
		missing = append(missing, "SecretNamespace")
	}
	if len(missing) != 0 {
		return nil, fmt.Errorf("%s: %s", errIncompleteScope, strings.Join(missing, ", "))
	}

	return &Connector[T]{
		kube:    kube,
		fs:      fs,
		ansible: ansible,
		scope:   scope,
	}, nil
}

// Connect produces an ExternalClient for the supplied managed resource.
func (c *Connector[T]) Connect(ctx context.Context, cr T) (managed.TypedExternalClient[T], error) { //nolint:gocyclo
	// NOTE(negz): This method is slightly over our complexity goal, but I
	// can't immediately think of a clean way to decompose it without
	// affecting readability.

	// NOTE(negz): This directory will be garbage collected by the workdir
	// garbage collector that is started in Setup.
	dir := filepath.Join(baseWorkingDir, string(cr.GetUID()))
	if err := c.fs.MkdirAll(dir, 0700); resource.Ignore(os.IsExist, err) != nil {
		return nil, fmt.Errorf("%s: %s: %w", baseWorkingDir, errMkdir, err)
	}

	if err := c.scope.Track(ctx, cr); err != nil {
		return nil, fmt.Errorf("%s: %w", errTrackPCUsage, err)
	}

	pc, err := c.scope.ResolveProviderConfig(ctx, c.kube, cr)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", errGetPC, err)
	}
	if pc.Spec == nil {
		return nil, fmt.Errorf("%s: %s", errGetPC, errNilPCSpec)
	}
	pcSpec := pc.Spec

	// ns and allowExplicit govern what the managed resource itself declares:
	// the inventories loop below. The provider config's own credentials are
	// read from the same namespace, but their permission to name a different
	// one comes from the resolved config, because a cluster-scoped
	// ClusterProviderConfig is written by an administrator and is meant to
	// serve every namespace.
	ns, allowExplicit := c.scope.SecretNamespace(cr)
	credsAllowExplicit := pc.AllowExplicitSecretNamespace

	var inventoryPerm os.FileMode = 0600
	if cr.GetForProvider().ExecutableInventory {
		inventoryPerm = 0700
	}
	// Saved inventory needed for ansible content hosts
	var buff bytes.Buffer
	for _, i := range cr.GetForProvider().Inventories {
		sel, err := ResolveSecretSelectors(i.CommonCredentialSelectors, ns, allowExplicit)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", errGetInventory, err)
		}
		data, err := resource.CommonCredentialExtractor(ctx, i.Source, c.kube, sel)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", errGetInventory, err)
		}
		if _, err := buff.WriteString(string(data) + "\n"); err != nil {
			return nil, err
		}
	}
	if cr.GetForProvider().InventoryInline != nil {
		if _, err := buff.WriteString(*cr.GetForProvider().InventoryInline + "\n"); err != nil {
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

	// prepare git credentials for ansible-galaxy to fetch remote roles
	// TODO(fahed) support other private remote repository
	// NOTE(ytsarev): Retrieve .git-credentials from Spec to /tmp outside of AnsibleRun directory
	gitCredDir := filepath.Clean(filepath.Join("/tmp", dir))
	if err := c.fs.MkdirAll(gitCredDir, 0700); err != nil {
		return nil, fmt.Errorf("%s: %w", errWriteGitCreds, err)
	}
	for _, cd := range pcSpec.Credentials {
		if cd.Filename != gitCredentialsFilename {
			continue
		}
		sel, err := ResolveSecretSelectors(cd.CommonCredentialSelectors, ns, credsAllowExplicit)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", errGetCreds, err)
		}
		data, err := resource.CommonCredentialExtractor(ctx, cd.Source, c.kube, sel)
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

	var requirementRoles []byte
	if len(cr.GetForProvider().Roles) != 0 {
		// marshall the Roles entries into a yaml document
		rolesMap := make(map[string][]shared.Role)
		rolesMap["roles"] = cr.GetForProvider().Roles
		var err error
		requirementRoles, err = yaml.Marshal(&rolesMap)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", errMarshalRoles, err)
		}
	} else if cr.GetForProvider().PlaybookInline != nil {
		if err := c.fs.WriteFile(filepath.Join(dir, runnerutil.PlaybookYml), []byte(*cr.GetForProvider().PlaybookInline), 0600); err != nil {
			return nil, fmt.Errorf("%s: %w", errWriteAnsibleRun, err)
		}
	}

	// Saved credentials needed for ansible playbooks execution
	for _, cd := range pcSpec.Credentials {
		sel, err := ResolveSecretSelectors(cd.CommonCredentialSelectors, ns, credsAllowExplicit)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", errGetCreds, err)
		}
		data, err := resource.CommonCredentialExtractor(ctx, cd.Source, c.kube, sel)
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
	behaviorVars := addBehaviorVars(pcSpec)

	// Requirements is a list of collections/roles to be installed, it is stored in requirements file
	requirementRolesStr := string(requirementRoles)
	if pcSpec.Requirements != nil || requirementRolesStr != "" {
		var installCollections, installRoles bool
		var reqSlice []string
		if pcSpec.Requirements != nil {
			reqSlice = append(reqSlice, *pcSpec.Requirements)
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

	r, err := ps.Init(ctx, cr.GetForProvider(), cr, behaviorVars)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", errInit, err)

	}

	return &external[T]{runner: r, kube: c.kube, scope: c.scope}, nil
}

type external[T Run] struct {
	runner ansibleRunner
	kube   client.Client
	scope  Scope[T]
}

func (c *external[T]) Disconnect(ctx context.Context) error {
	// Unimplemented, required by newer versions of crossplane-runtime
	return nil
}

// nolint: gocyclo
// TODO reduce cyclomatic complexity
func (c *external[T]) Observe(ctx context.Context, cr T) (managed.ExternalObservation, error) {
	if c.scope.BeforeObserve != nil {
		c.scope.BeforeObserve(cr)
	}

	switch c.runner.GetAnsibleRunPolicy().Name {
	case "ObserveAndDelete", "":
		if c.runner.GetAnsibleRunPolicy().Name == "" {
			ansible.SetPolicyRun(cr, "ObserveAndDelete")
		}
		if meta.WasDeleted(cr) {
			return managed.ExternalObservation{ResourceExists: true}, nil
		}
		observed, ok := cr.DeepCopyObject().(T)
		if !ok {
			return managed.ExternalObservation{}, fmt.Errorf("%s: %s", errGetAnsibleRun, errNotAnsibleRun)
		}
		if err := c.kube.Get(ctx, types.NamespacedName{
			Namespace: observed.GetNamespace(),
			Name:      observed.GetName(),
		}, observed); err != nil {
			if kerrors.IsNotFound(err) {
				return managed.ExternalObservation{ResourceExists: false}, nil
			}
			return managed.ExternalObservation{}, fmt.Errorf("%s: %w", errGetAnsibleRun, err)
		}
		var lastParameters *shared.AnsibleRunParameters
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

func (c *external[T]) Create(ctx context.Context, cr T) (managed.ExternalCreation, error) {
	// No difference from the provider side which lifecycle method to choose in this case of Create() or Update()
	u, err := c.Update(ctx, cr)
	return managed.ExternalCreation(u), err
}

func (c *external[T]) Update(ctx context.Context, cr T) (managed.ExternalUpdate, error) {
	// disable checkMode for real action
	c.runner.EnableCheckMode(false)
	if err := c.runAnsible(ctx, cr); err != nil {
		return managed.ExternalUpdate{}, fmt.Errorf("running ansible: %w", err)
	}

	// TODO handle ConnectionDetails https://github.com/multicloudlab/crossplane-provider-ansible/pull/74#discussion_r888467991
	return managed.ExternalUpdate{ConnectionDetails: nil}, nil
}

func (c *external[T]) Delete(ctx context.Context, cr T) (managed.ExternalDelete, error) {
	cr.SetConditions(xpv2.Deleting())

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

func getLastAppliedParameters(observed metav1.Object) (*shared.AnsibleRunParameters, error) {
	lastApplied, ok := observed.GetAnnotations()[v1.LastAppliedConfigAnnotation]
	if !ok {
		return nil, nil
	}
	lastParameters := &shared.AnsibleRunParameters{}
	if err := json.Unmarshal([]byte(lastApplied), lastParameters); err != nil {
		return nil, fmt.Errorf("%s: %w", errUnmarshalTemplate, err)
	}

	return lastParameters, nil
}

func (c *external[T]) handleLastApplied(ctx context.Context, lastParameters *shared.AnsibleRunParameters, desired T) (managed.ExternalObservation, error) {
	// Mark as up-to-date if last is equal to desired
	isUpToDate := (lastParameters != nil && equality.Semantic.DeepEqual(*lastParameters, *desired.GetForProvider()))

	isLastSyncOK := (desired.GetCondition(xpv2.TypeSynced).Status == v1.ConditionTrue)

	if isUpToDate && isLastSyncOK {
		desired.SetConditions(xpv2.Available())
		if err := c.kube.Status().Update(ctx, desired); err != nil {
			return managed.ExternalObservation{}, fmt.Errorf("updating status: %w", err)
		}
		// nothing to do for this run
		return managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: true}, nil
	}

	out, err := json.Marshal(*desired.GetForProvider())
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

func (c *external[T]) runAnsible(ctx context.Context, cr T) error {
	_, err := c.runner.Run(ctx)
	if err != nil {
		cond := xpv2.Unavailable()
		cond.Message = err.Error()
		cr.SetConditions(cond)
	} else {
		cr.SetConditions(xpv2.Available())
	}

	if err := c.kube.Status().Update(ctx, cr); err != nil {
		return fmt.Errorf("updating status: %w", err)
	}

	return err
}

func addBehaviorVars(pcSpec *shared.ProviderConfigSpec) map[string]string {
	behaviorVars := make(map[string]string, len(pcSpec.Vars))
	for _, v := range pcSpec.Vars {
		behaviorVars[v.Key] = v.Value
	}
	return behaviorVars
}
