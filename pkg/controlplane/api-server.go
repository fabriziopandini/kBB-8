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
	"bufio"
	"fmt"
	"io/ioutil"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

	"github.com/fabriziopandini/kBB-8/pkg/process"
	"github.com/fabriziopandini/kBB-8/third_party/controller-runtime/addr"
	"github.com/fabriziopandini/kBB-8/third_party/controller-runtime/certs"
)

type APIServer struct {
	EtcdURL *url.URL
	Path    string

	URL *url.URL
	CA  *certs.TinyCA

	// processState contains the actual details about this running process
	processState *process.State

	logFile       *os.File
	logFileWriter *bufio.Writer
}

type apiServerPKI struct {
	ca         *certs.TinyCA
	caFile     string
	certFile   string
	keyFile    string
	saCertFile string
	saKeyFile  string
}

func (a *APIServer) Start() error {
	if err := a.setProcessState(); err != nil {
		return err
	}
	return a.processState.Start(a.logFileWriter, a.logFileWriter)
}

func (a *APIServer) Stop() error {
	if err := a.processState.Stop(); err != nil {
		return err
	}

	if a.logFileWriter != nil {
		if err := a.logFileWriter.Flush(); err != nil {
			return err
		}
	}

	if a.logFile != nil {
		if err := a.logFile.Close(); err != nil {
			return err
		}
	}

	// TODO: Cleanup dir? What about logs? What about idempotent restart?
	return nil
}

func (a *APIServer) setProcessState() error {
	currentDir, err := os.Getwd()
	if err != nil {
		return err
	}

	// Set up the log file.
	localPath := filepath.Join(currentDir, ".tmp", "kubernetes", "api-server")
	if err := os.MkdirAll(localPath, 0744); err != nil {
		return err
	}
	if a.logFile, err = os.OpenFile(filepath.Join(localPath, "api-server.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600); err != nil {
		return err
	}
	a.logFileWriter = bufio.NewWriter(a.logFile)

	// Set up the listening url.
	port, host, err := addr.Suggest("")
	if err != nil {
		return err
	}
	a.URL = &url.URL{
		Scheme: "https",
		Host:   net.JoinHostPort(host, strconv.Itoa(port)),
	}

	// Set up the PKI.
	pki, err := setupPKI(localPath, host)
	if err != nil {
		return err
	}
	a.CA = pki.ca

	// Starts the API server.
	args := []string{
		// Set up the API server endpoint.
		fmt.Sprintf("--advertise-address=%s", host),
		fmt.Sprintf("--secure-port=%s", strconv.Itoa(port)),
		fmt.Sprintf("--client-ca-file=%s", pki.caFile),
		fmt.Sprintf("--tls-cert-file=%s", pki.certFile),
		fmt.Sprintf("--tls-private-key-file=%s", pki.keyFile),

		// Use a default CIDR for cluster ip services.
		fmt.Sprintf("--service-cluster-ip-range=%s", "10.0.0.0/24"),

		// Setup authorizations.
		fmt.Sprintf("--authorization-mode=%s", "RBAC"),

		// Set up a service account signer
		fmt.Sprintf("--service-account-key-file=%s", pki.saCertFile),
		fmt.Sprintf("--service-account-signing-key-file=%s", pki.saKeyFile),
		fmt.Sprintf("--service-account-issuer=%s", fmt.Sprintf("https://kubernetes.default.svc.%s", "cluster.local")),

		// Connect to etcd
		fmt.Sprintf("--etcd-servers=%s", a.EtcdURL.String()),
	}

	a.processState = &process.State{
		Path: a.Path,
		Args: args,
	}

	a.processState.HealthCheck.URL = *a.URL
	a.processState.HealthCheck.Path = "/readyz"

	if err := a.processState.Init(); err != nil {
		return err
	}
	return nil
}

func setupPKI(localPath string, host string) (*apiServerPKI, error) {
	// TODO: Skip create if pki already exists for idempotent restart?

	// Set up the api server certificate.
	names := []string{
		host,
		// TODO: Check if the following are required
		// "kubernetes",
		// "kubernetes.default",
		// "kubernetes.default.svc",
		// "kubernetes.default.svc.cluster.local",
	}

	ca, err := certs.NewTinyCA()
	if err != nil {
		return nil, err
	}

	servingCert, err := ca.NewServingCert(names...)
	if err != nil {
		return nil, err
	}

	localServingCertDir := filepath.Join(localPath, "ca")
	if err := os.MkdirAll(localServingCertDir, 0744); err != nil {
		return nil, err
	}

	certData, keyData, err := servingCert.AsBytes()
	if err != nil {
		return nil, fmt.Errorf("unable to marshal Kubernetes CA: %v", err)
	}

	caFile := filepath.Join(localServingCertDir, "ca.crt")
	if err := ioutil.WriteFile(caFile, ca.CA.CertBytes(), 0640); err != nil {
		return nil, fmt.Errorf("unable to write Kubernetes CA cert to disk: %v", err)
	}
	certFile := filepath.Join(localServingCertDir, "tls.crt")
	if err := ioutil.WriteFile(certFile, certData, 0640); err != nil {
		return nil, fmt.Errorf("unable to write API Server serving cert to disk: %v", err)
	}
	keyFile := filepath.Join(localServingCertDir, "tls.key")
	if err := ioutil.WriteFile(keyFile, keyData, 0640); err != nil {
		return nil, fmt.Errorf("unable to write API Server serving cert key to disk: %v", err)
	}

	// service account signing files too
	saCA, err := certs.NewTinyCA()
	if err != nil {
		return nil, err
	}

	saCert, saKey, err := saCA.CA.AsBytes()
	if err != nil {
		return nil, fmt.Errorf("unable to marshal Kubernetes sa-signer: %v", err)
	}

	saCertFile := filepath.Join(localServingCertDir, "sa-signer.crt")
	if err := ioutil.WriteFile(saCertFile, saCert, 0640); err != nil {
		return nil, fmt.Errorf("unable to write Kubernetes sa-signer cert to disk: %v", err)
	}
	saKeyFile := filepath.Join(localServingCertDir, "sa-signer.key")
	if err := ioutil.WriteFile(saKeyFile, saKey, 0640); err != nil {
		return nil, fmt.Errorf("unable to write Kubernetes sa-signer cert key to disk: %v", err)
	}
	return &apiServerPKI{
		ca:         ca,
		caFile:     caFile,
		certFile:   certFile,
		keyFile:    keyFile,
		saCertFile: saCertFile,
		saKeyFile:  saKeyFile,
	}, nil
}
