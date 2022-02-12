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

package main

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/briandowns/spinner"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/fabriziopandini/kBB-8/pkg/controlplane"
	"github.com/fabriziopandini/kBB-8/pkg/provider"
)

var spinnerFrames = []string{
	"⠈⠁",
	"⠈⠑",
	"⠈⠱",
	"⠈⡱",
	"⢀⡱",
	"⢄⡱",
	"⢄⡱",
	"⢆⡱",
	"⢎⡱",
	"⢎⡰",
	"⢎⡠",
	"⢎⡀",
	"⢎⠁",
	"⠎⠁",
	"⠊⠁",
}

func main() {
	ctx := ctrl.SetupSignalHandler()

	fmt.Println()

	s := spinner.New(spinnerFrames, 200*time.Millisecond)
	s.Prefix = " "
	s.Suffix = " Starting kBB-8 ..."
	s.FinalMSG = " \u001B[32m✓\u001B[0m kBB-8 started!\n"
	s.Start()

	// Start the control plane (only what we need to run providers).
	// TODO: make the Kubernetes version configurable (from yaml or flags); download kubernetes package...
	cp := controlplane.ControlPlane{
		PackagePath: "./test/packages/bootstrap-kubernetes",
	}
	if err := cp.Start(); err != nil {
		panic(err)
	}

	defer cp.Stop()
	s.Stop()

	s.Suffix = " Starting Cluster API ..."
	s.Start()

	// Start providers
	// TODO: make the list of providers configurable (from yaml or flags); download providers packages...
	providers := []provider.Provider{
		{
			PackagePath: "./test/packages/bootstrap-capi",
			Args:        []string{"--feature-gates=MachinePool=true,ClusterResourceSet=true,ClusterTopology=true"},
		},
		{
			PackagePath: "./test/packages/bootstrap-cabpk",
			Args:        []string{"--feature-gates=MachinePool=true"},
		},
		{
			PackagePath: "./test/packages/bootstrap-kcp",
			Args:        []string{"--feature-gates=ClusterTopology=true"},
		},
		{
			PackagePath: "./test/packages/bootstrap-capd",
			Args:        []string{"--feature-gates=MachinePool=true,ClusterTopology=true", "--loadbalancer-use-host-port"},
		},
		// TODO: CPI for cloud providers
	}

	var wg sync.WaitGroup
	names := make([]string, 0, len(providers))
	for i := range providers {
		p := providers[i]
		wg.Add(1)
		go func() {
			if err := p.Start(ctx, cp.KubeConfigFile); err != nil {
				panic(err)
			}
			names = append(names, p.Name())

			wg.Done()
		}()
		defer p.Stop()
	}
	wg.Wait()

	s.FinalMSG = fmt.Sprintf(" \u001B[32m✓\u001B[0m Cluster API with %s Ready!\n\n", strings.Join(names, ", ")) +
		fmt.Sprintf("Set kubectl context to \"%s\"\n", cp.KubeConfigContext) +
		"You can now use your bootstrap cluster with:\n\n kubectl cluster-info \n\n" +
		"Enjoy Cluster API with kBB-8! 😊\n"

	s.Stop()

	select {
	case <-ctx.Done():
	}
}
