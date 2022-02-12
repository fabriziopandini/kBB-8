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

package provider

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fabriziopandini/kBB-8/third_party/controller-runtime/addr"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	crdhelpers "k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/fabriziopandini/kBB-8/pkg/process"
	"github.com/fabriziopandini/kBB-8/third_party/controller-runtime/certs"
)

var scheme = runtime.NewScheme()

func init() {
	_ = apiextensionsv1.AddToScheme(scheme)
	_ = admissionv1.AddToScheme(scheme)
}

const (
	binaryName   = "manager"
	manifestName = "components.yaml"
)

type Provider struct {
	// TODO: make private and create constructor
	PackagePath string
	Args        []string

	processState *process.State

	logFile       *os.File
	logFileWriter *bufio.Writer
}

type providerURL struct {
	host        string
	webhookPort int
	healthPort  int
}

func (u *providerURL) webhookHostPort() string {
	return net.JoinHostPort(u.host, fmt.Sprintf("%d", u.webhookPort))
}

func (u *providerURL) healthHostPort() string {
	return net.JoinHostPort(u.host, fmt.Sprintf("%d", u.healthPort))
}

type providerPKI struct {
	dir    string
	caData []byte
}

func (p *Provider) Name() string {
	// TODO: check if there is a more straight forward/explicit way to get the provider name.
	return strings.ToUpper(strings.TrimPrefix(filepath.Base(p.PackagePath), "bootstrap-"))
}

func (p *Provider) Start(ctx context.Context, kubeConfig string) error {
	if err := p.setProcessState(ctx, kubeConfig); err != nil {
		return err
	}

	if err := p.processState.Start(p.logFileWriter, p.logFileWriter); err != nil {
		return err
	}

	if err := wait.PollImmediateInfiniteWithContext(ctx, 100*time.Millisecond, func(ctx context.Context) (bool, error) {
		return p.processState.Ready(), nil
	}); err != nil {
		return fmt.Errorf("error starting %s: %w", p.PackagePath, err)
	}
	return nil
}

func (p *Provider) Stop() error {
	if err := p.processState.Stop(); err != nil {
		return err
	}

	if p.logFileWriter != nil {
		if err := p.logFileWriter.Flush(); err != nil {
			return err
		}
	}

	if p.logFile != nil {
		if err := p.logFile.Close(); err != nil {
			return err
		}
	}

	// TODO: Cleanup dir? What about logs? What about idempotent restart?
	return nil
}

func (p *Provider) setProcessState(ctx context.Context, kubeConfig string) error {
	currentDir, err := os.Getwd()
	if err != nil {
		return err
	}

	// Set up the log file.
	localPath := filepath.Join(currentDir, ".tmp", "provider", strings.ToLower(p.Name()))
	if err := os.MkdirAll(localPath, 0744); err != nil {
		return err
	}

	if p.logFile, err = os.OpenFile(filepath.Join(localPath, "manager.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600); err != nil {
		return err
	}
	p.logFileWriter = bufio.NewWriter(p.logFile)

	// Set up the webhook url.
	pURL := &providerURL{}
	pURL.webhookPort, pURL.host, err = addr.Suggest("")
	if err != nil {
		return fmt.Errorf("unable to grab random port for serving webhooks on: %v", err)
	}

	// Set up the health url.
	pURL.healthPort, _, err = addr.Suggest("")
	if err != nil {
		return fmt.Errorf("unable to grab random port for serving health on: %v", err)
	}

	// Set up the PKI.
	pki, err := setupPKI(localPath, pURL)
	if err != nil {
		return err
	}

	// Create a subset of objects from the provider manifest (CRDs, WebhookConfigurations).
	manifestPath := filepath.Join(p.PackagePath, manifestName)
	if err := createManifestObjects(ctx, manifestPath, kubeConfig, pki, pURL); err != nil {
		return err
	}

	// Starts the provider.
	args := append(p.Args,
		fmt.Sprintf("--kubeconfig=%s", kubeConfig),
		fmt.Sprintf("--webhook-cert-dir=%s", pki.dir),
		fmt.Sprintf("--webhook-port=%d", pURL.webhookPort),
		fmt.Sprintf("--health-addr=:%d", pURL.healthPort), // TODO: add host
		"--metrics-bind-addr=0",
	)

	p.processState = &process.State{
		Args: args,
		Path: filepath.Join(p.PackagePath, binaryName),
	}

	p.processState.HealthCheck.URL = url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(pURL.host, fmt.Sprintf("%d", pURL.healthPort)),
	}
	p.processState.HealthCheck.Path = "/healthz"

	if err := p.processState.Init(); err != nil {
		return err
	}
	return nil
}

func setupPKI(localPath string, u *providerURL) (*providerPKI, error) {
	// TODO: Skip create if pki already exists for idempotent restart?

	localServingCertDir := filepath.Join(localPath, "ca")
	if err := os.MkdirAll(localServingCertDir, 0744); err != nil {
		return nil, fmt.Errorf("unable to create directory for webhook serving certs: %v", err)
	}

	hookCA, err := certs.NewTinyCA()
	if err != nil {
		return nil, fmt.Errorf("unable to create webhook CA: %v", err)
	}

	names := []string{"localhost", u.host}
	hookCert, err := hookCA.NewServingCert(names...)
	if err != nil {
		return nil, fmt.Errorf("unable to create webhook serving certs: %v", err)
	}

	certData, keyData, err := hookCert.AsBytes()
	if err != nil {
		return nil, fmt.Errorf("unable to marshal webhook serving certs to bytes: %v", err)
	}

	if err := ioutil.WriteFile(filepath.Join(localServingCertDir, "tls.crt"), certData, 0640); err != nil { //nolint:gosec
		return nil, fmt.Errorf("unable to write webhook serving cert to disk: %v", err)
	}
	if err := ioutil.WriteFile(filepath.Join(localServingCertDir, "tls.key"), keyData, 0640); err != nil { //nolint:gosec
		return nil, fmt.Errorf("unable to write webhook serving cert key to disk: %v", err)
	}

	return &providerPKI{
		dir:    localServingCertDir,
		caData: certData,
	}, nil
}

func createManifestObjects(ctx context.Context, manifestPath string, kubeConfig string, pki *providerPKI, u *providerURL) error {
	// Create the client
	config, err := clientcmd.LoadFromFile(kubeConfig)
	if err != nil {
		panic(err)
	}

	restConfig, err := clientcmd.NewDefaultClientConfig(*config, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		panic(err)
	}

	c, err := client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		panic(err)
	}

	// Read the provider manifest and make it ready to work with kBB-8.
	objs, err := readAndAdaptManifestObjects(manifestPath, pki, u)
	if err != nil {
		return fmt.Errorf("unable to get provider crds: %w", err)
	}

	fns := []func() error{}

	// Create CRDs
	for i := range objs.crds {
		crd := objs.crds[i].DeepCopy()

		fns = append(fns, func() error {
			crdResource := &apiextensionsv1.CustomResourceDefinition{}
			if err := c.Get(ctx, client.ObjectKeyFromObject(crd), crdResource); err != nil {
				if apierrors.IsNotFound(err) {
					if err := c.Create(ctx, crd); err != nil {
						return fmt.Errorf("error creating CRD %s: %w", crd.Name, err)
					}
				} else {
					return fmt.Errorf("error fetching CRD %s: %w", crd.Name, err)
				}
			} else {
				crd.ResourceVersion = crdResource.ResourceVersion
				if err = c.Update(ctx, crd); err != nil {
					return fmt.Errorf("error updating CRD %s: %w", crd.Name, err)
				}
			}

			if err := wait.PollImmediateInfiniteWithContext(ctx, 100*time.Millisecond, func(ctx context.Context) (bool, error) {
				actualCRD := &apiextensionsv1.CustomResourceDefinition{}
				if err := c.Get(ctx, client.ObjectKeyFromObject(crd), actualCRD); err != nil {
					if apierrors.IsNotFound(err) {
						return false, fmt.Errorf("CRD %s was deleted before being established", crd.Name)
					}
					return false, fmt.Errorf("error fetching CRD %s: %w", crd.Name, err)
				}

				return crdhelpers.IsCRDConditionTrue(actualCRD, apiextensionsv1.Established), nil
			}); err != nil {
				return fmt.Errorf("error starting CRD %s: %w", crd.Name, err)
			}
			return nil
		})
	}

	// Create mutating web hooks
	for i := range objs.mutHooks {
		hook := objs.mutHooks[i].DeepCopy()

		fns = append(fns, func() error {
			hookResource := &admissionv1.MutatingWebhookConfiguration{}
			if err := c.Get(ctx, client.ObjectKeyFromObject(hook), hookResource); err != nil {
				if apierrors.IsNotFound(err) {
					if err := c.Create(ctx, hook); err != nil {
						return fmt.Errorf("error creating MutatingWebhookConfiguration %s: %w", hook.Name, err)
					}
				} else {
					return fmt.Errorf("error fetching MutatingWebhookConfiguration %s: %w", hook.Name, err)
				}
			} else {
				hook.ResourceVersion = hookResource.ResourceVersion
				if err = c.Update(ctx, hook); err != nil {
					return fmt.Errorf("error updating MutatingWebhookConfiguration %s: %w", hook.Name, err)
				}
			}

			if err := wait.PollImmediateInfiniteWithContext(ctx, 100*time.Millisecond, func(ctx context.Context) (bool, error) {
				actualHook := &admissionv1.MutatingWebhookConfiguration{}
				if err := c.Get(ctx, client.ObjectKeyFromObject(hook), actualHook); err != nil {
					if apierrors.IsNotFound(err) {
						return false, fmt.Errorf("MutatingWebhookConfiguration %s was deleted before being established", hook.Name)
					}
					return false, fmt.Errorf("error fetching MutatingWebhookConfiguration %s: %w", hook.Name, err)
				}
				return true, nil
			}); err != nil {
				return fmt.Errorf("error starting MutatingWebhookConfiguration %s: %w", hook.Name, err)
			}

			return nil
		})
	}

	// Create validation web hooks
	for i := range objs.valHooks {
		hook := objs.valHooks[i].DeepCopy()

		fns = append(fns, func() error {
			hookResource := &admissionv1.ValidatingWebhookConfiguration{}
			if err := c.Get(ctx, client.ObjectKeyFromObject(hook), hookResource); err != nil {
				if apierrors.IsNotFound(err) {
					if err := c.Create(ctx, hook); err != nil {
						return fmt.Errorf("error creating ValidatingWebhookConfiguration %s: %w", hook.Name, err)
					}
				} else {
					return fmt.Errorf("error fetching ValidatingWebhookConfiguration %s: %w", hook.Name, err)
				}
			} else {
				hook.ResourceVersion = hookResource.ResourceVersion
				if err = c.Update(ctx, hook); err != nil {
					return fmt.Errorf("error updating ValidatingWebhookConfiguration %s: %w", hook.Name, err)
				}
			}

			if err := wait.PollImmediateInfiniteWithContext(ctx, 100*time.Millisecond, func(ctx context.Context) (bool, error) {
				actualHook := &admissionv1.ValidatingWebhookConfiguration{}
				if err := c.Get(ctx, client.ObjectKeyFromObject(hook), actualHook); err != nil {
					if apierrors.IsNotFound(err) {
						return false, fmt.Errorf("ValidatingWebhookConfiguration %s was deleted before being established", hook.Name)
					}
					return false, fmt.Errorf("error fetching ValidatingWebhookConfiguration %s: %w", hook.Name, err)
				}
				return true, nil
			}); err != nil {
				return fmt.Errorf("error starting ValidatingWebhookConfiguration %s: %w", hook.Name, err)
			}
			return nil
		})
	}

	// TODO: Explore running all those tasks in parallel.
	for i := range fns {
		f := fns[i]

		if err := f(); err != nil {
			panic(err)
		}
	}

	return nil
}

type manifestObjects struct {
	crds     []*apiextensionsv1.CustomResourceDefinition
	mutHooks []*admissionv1.MutatingWebhookConfiguration
	valHooks []*admissionv1.ValidatingWebhookConfiguration
}

func readAndAdaptManifestObjects(manifestPath string, pki *providerPKI, u *providerURL) (*manifestObjects, error) {
	ret := &manifestObjects{}

	// Unmarshal doc fragments from the provider manifest
	docs, err := readDocuments(manifestPath)
	if err != nil {
		return nil, err
	}

	// Converts the doc fragment we care about into Kubernetes manifestObjects (CRD, Webhooks)
	for _, doc := range docs {
		var generic metav1.PartialObjectMetadata
		if err = yaml.Unmarshal(doc, &generic); err != nil {
			return nil, err
		}

		switch {
		case generic.Kind == "CustomResourceDefinition":
			if generic.APIVersion != "apiextensions.k8s.io/v1" {
				return nil, fmt.Errorf("only v1 is supported right now for CustomResourceDefinition (name: %s)", generic.Name)
			}
			crd := &apiextensionsv1.CustomResourceDefinition{}
			if err = yaml.Unmarshal(doc, crd); err != nil {
				return nil, err
			}
			ret.crds = append(ret.crds, crd)
		case generic.Kind == "MutatingWebhookConfiguration":
			if generic.APIVersion != "admissionregistration.k8s.io/v1" {
				return nil, fmt.Errorf("only v1 is supported right now for MutatingWebhookConfiguration (name: %s)", generic.Name)
			}
			hook := &admissionv1.MutatingWebhookConfiguration{}
			if err := yaml.Unmarshal(doc, hook); err != nil {
				return nil, err
			}
			ret.mutHooks = append(ret.mutHooks, hook)
		case generic.Kind == "ValidatingWebhookConfiguration":
			if generic.APIVersion != "admissionregistration.k8s.io/v1" {
				return nil, fmt.Errorf("only v1 is supported right now for ValidatingWebhookConfiguration (name: %s)", generic.Name)
			}
			hook := &admissionv1.ValidatingWebhookConfiguration{}
			if err := yaml.Unmarshal(doc, hook); err != nil {
				return nil, err
			}
			ret.valHooks = append(ret.valHooks, hook)
		default:
			continue
		}
	}

	localServingUrl := &url.URL{
		Scheme: "https",
		Host:   u.webhookHostPort(),
	}

	// Adapt CustomResourceDefinition to work in kBB-8 (fixup the conversion webhook ClientConfig)
	for i := range ret.crds {
		if ret.crds[i].Spec.Conversion == nil {
			ret.crds[i].Spec.Conversion = &apiextensionsv1.CustomResourceConversion{
				Webhook: &apiextensionsv1.WebhookConversion{},
			}
		}
		ret.crds[i].Spec.Conversion.Strategy = apiextensionsv1.WebhookConverter
		ret.crds[i].Spec.Conversion.Webhook.ConversionReviewVersions = []string{"v1", "v1beta1"}
		ret.crds[i].Spec.Conversion.Webhook.ClientConfig = &apiextensionsv1.WebhookClientConfig{
			Service:  nil,
			URL:      pointer.StringPtr(fmt.Sprintf("%s/convert", localServingUrl.String())),
			CABundle: pki.caData,
		}
	}

	// Adapt MutatingWebhookConfiguration to work in kBB-8 (fixup ClientConfig)
	for i := range ret.mutHooks {
		for j := range ret.mutHooks[i].Webhooks {
			ret.mutHooks[i].Webhooks[j].ClientConfig = admissionv1.WebhookClientConfig{
				Service:  nil,
				URL:      pointer.StringPtr(fmt.Sprintf("%s/%s", localServingUrl.String(), *ret.mutHooks[i].Webhooks[j].ClientConfig.Service.Path)),
				CABundle: pki.caData,
			}
		}
	}

	// Adapt ValidatingWebhookConfiguration to work in kBB-8 (fixup ClientConfig)
	for i := range ret.valHooks {
		for j := range ret.valHooks[i].Webhooks {
			ret.valHooks[i].Webhooks[j].ClientConfig = admissionv1.WebhookClientConfig{
				Service:  nil,
				URL:      pointer.StringPtr(fmt.Sprintf("%s/%s", localServingUrl.String(), *ret.valHooks[i].Webhooks[j].ClientConfig.Service.Path)),
				CABundle: pki.caData,
			}
		}
	}

	return ret, nil
}

func readDocuments(fp string) ([][]byte, error) {
	b, err := ioutil.ReadFile(fp) //nolint:gosec
	if err != nil {
		return nil, err
	}

	docs := [][]byte{}
	reader := k8syaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(b)))
	for {
		// Read document
		doc, err := reader.Read()
		if err != nil {
			if err == io.EOF {
				break
			}

			return nil, err
		}

		docs = append(docs, doc)
	}

	return docs, nil
}
