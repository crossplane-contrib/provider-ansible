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

package controller

import (
	"github.com/crossplane/crossplane-runtime/pkg/controller"
	ctrl "sigs.k8s.io/controller-runtime"

	ansiblerun "github.com/crossplane-contrib/provider-ansible/internal/controller/ansibleRun"

	"github.com/crossplane-contrib/provider-ansible/internal/controller/config"
)

// Setup creates all Template controllers with the supplied logger and adds them to
// the supplied manager.
func Setup(mgr ctrl.Manager, o controller.Options, s ansiblerun.SetupOptions) error {
	if err := config.Setup(mgr, o); err != nil {
		return err
	}

	if err := ansiblerun.Setup(mgr, o, s); err != nil {
		return err
	}

	return nil
}
