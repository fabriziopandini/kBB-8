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

package kubeconfig

import (
	"os"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/fabriziopandini/kBB-8/third_party/controller-runtime/certs"
)

const (
	systemPrivilegedGroup = "system:masters"
)

func CreateOrMerge(ca *certs.TinyCA, url string, clusterName string, explicitPath string) (string, string, error) {
	rules := getConfigLoadingRules(explicitPath)
	existingConfig, err := rules.Load()
	if err != nil {
		if !(explicitPath != "" && os.IsNotExist(err)) {
			return "", "", err
		}
		existingConfig = clientcmdapi.NewConfig()
	}

	newConfig, err := create(ca, clusterName, url)

	if err := merge(newConfig, existingConfig); err != nil {
		return "", "", err
	}

	kubeConfigPath := rules.GetDefaultFilename()

	if err := clientcmd.WriteToFile(*existingConfig, kubeConfigPath); err != nil {
		return "", "", err
	}

	return kubeConfigPath, existingConfig.CurrentContext, nil
}

func Remove(clusterName string, explicitPath string) error {
	rules := getConfigLoadingRules(explicitPath)
	for _, kubeConfigPath := range rules.GetLoadingPrecedence() {
		existingConfig, err := clientcmd.LoadFromFile(kubeConfigPath)
		if err != nil {
			return err
		}
		if remove(clusterName, existingConfig) {
			if err := clientcmd.WriteToFile(*existingConfig, kubeConfigPath); err != nil {
				return err
			}
		}
	}
	return nil
}

func getConfigLoadingRules(explicitPath string) *clientcmd.ClientConfigLoadingRules {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if explicitPath != "" {
		rules.ExplicitPath = explicitPath
	}

	return rules
}

func create(ca *certs.TinyCA, clusterName string, url string) (*clientcmdapi.Config, error) {
	clientCert, err := ca.NewClientCert(certs.ClientInfo{
		Name:   userKey(clusterName),
		Groups: []string{systemPrivilegedGroup},
	})
	if err != nil {
		return nil, err
	}

	certBytes, keyBytes, err := clientCert.AsBytes()
	if err != nil {
		return nil, err
	}

	config := &clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			clusterKey(clusterName): {
				Server:                   url,
				CertificateAuthorityData: ca.CA.CertBytes(),
			},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			userKey(clusterName): {
				ClientKeyData:         keyBytes,
				ClientCertificateData: certBytes,
			},
		},
		Contexts: map[string]*clientcmdapi.Context{
			contextKey(clusterName): {
				Cluster:  clusterKey(clusterName),
				AuthInfo: userKey(clusterName),
			},
		},
		CurrentContext: contextKey(clusterName),
	}
	return config, nil
}

// TODO: make prefix configurable
// TODO: make user name / groups configurable with defaults for admin

func clusterKey(clusterName string) string {
	return "kBB-8-" + clusterName
}

func contextKey(clusterName string) string {
	return "kBB-8-" + clusterName
}

func userKey(clusterName string) string {
	return "kBB-8-" + clusterName + "-admin"
}

func merge(new, existing *clientcmdapi.Config) error {
	for newName, newCluster := range new.Clusters {
		shouldAppend := true
		for existingName := range existing.Clusters {
			if existingName == newName {
				existing.Clusters[existingName] = newCluster
				shouldAppend = false
			}
		}
		if shouldAppend {
			existing.Clusters[newName] = newCluster
		}
	}

	for newName, newAuthInfo := range new.AuthInfos {
		shouldAppend := true
		for existingName := range existing.AuthInfos {
			if existingName == newName {
				existing.AuthInfos[existingName] = newAuthInfo
				shouldAppend = false
			}
		}
		if shouldAppend {
			existing.AuthInfos[newName] = newAuthInfo
		}
	}

	for newName, newContext := range new.Contexts {
		shouldAppend := true
		for existingName := range existing.Contexts {
			if existingName == newName {
				existing.Contexts[existingName] = newContext
				shouldAppend = false
			}
		}
		if shouldAppend {
			existing.Contexts[newName] = newContext
		}
	}

	existing.CurrentContext = new.CurrentContext
	return nil
}

func remove(clusterName string, config *clientcmdapi.Config) bool {
	mutated := false

	if _, ok := config.Clusters[clusterKey(clusterName)]; ok {
		delete(config.Clusters, clusterKey(clusterName))
		mutated = true
	}

	if _, ok := config.AuthInfos[userKey(clusterName)]; ok {
		delete(config.AuthInfos, userKey(clusterName))
		mutated = true
	}

	if _, ok := config.Contexts[contextKey(clusterName)]; ok {
		delete(config.Contexts, contextKey(clusterName))
		mutated = true
	}

	if config.CurrentContext == contextKey(clusterName) {
		config.CurrentContext = ""
		mutated = true
	}
	return mutated
}
