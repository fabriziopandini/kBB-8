/*
Copyright 2022 The kBB-8 Authors.

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

package controlplane

import (
	"path/filepath"

	"github.com/fabriziopandini/kBB-8/pkg/kubeconfig"
)

type ControlPlane struct {
	// TODO: make private and create constructor
	PackagePath string

	// TODO: make private and create getter
	KubeConfigFile    string
	KubeConfigContext string

	etcd      *Etcd
	apiServer *APIServer
}

func (cp *ControlPlane) Start() error {
	cp.etcd = &Etcd{
		Path: filepath.Join(cp.PackagePath, "etcd"),
	}
	if err := cp.etcd.Start(); err != nil {
		return err
	}

	cp.apiServer = &APIServer{
		EtcdURL: cp.etcd.URL,
		Path:    filepath.Join(cp.PackagePath, "kube-apiserver"),
	}
	if err := cp.apiServer.Start(); err != nil {
		return err
	}

	// TODO: review this to provide a better library UX vs create and merge in the user's KubeConfig file
	var err error
	cp.KubeConfigFile, cp.KubeConfigContext, err = kubeconfig.CreateOrMerge(cp.apiServer.CA, cp.apiServer.URL.String(), "bootstrap", "")
	if err != nil {
		return err
	}
	return nil
}

func (cp *ControlPlane) Stop() error {
	if err := cp.apiServer.Stop(); err != nil {
		return err
	}
	if err := cp.etcd.Stop(); err != nil {
		return err
	}

	if err := kubeconfig.Remove("bootstrap", ""); err != nil {
		return err
	}

	// TODO: Cleanup dir? What about logs? What about idempotent restart?
	return nil
}
