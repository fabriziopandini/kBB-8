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
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

	"github.com/fabriziopandini/kBB-8/pkg/process"
	"github.com/fabriziopandini/kBB-8/third_party/controller-runtime/addr"
)

type Etcd struct {
	// TODO: make private and create constructor
	Path string

	// TODO: make private and create getter
	URL     *url.URL
	dataDir string

	// processState contains the actual details about this running process
	processState *process.State

	logFile       *os.File
	logFileWriter *bufio.Writer
}

func (e *Etcd) Start() error {
	if err := e.setProcessState(); err != nil {
		return err
	}
	return e.processState.Start(e.logFileWriter, e.logFileWriter)
}

func (e *Etcd) Stop() error {
	if err := e.processState.Stop(); err != nil {
		return err
	}

	if e.logFileWriter != nil {
		if err := e.logFileWriter.Flush(); err != nil {
			return err
		}
	}

	if e.logFile != nil {
		if err := e.logFile.Close(); err != nil {
			return err
		}
	}

	// TODO: Cleanup dir? What about logs? What about idempotent restart?
	return os.RemoveAll(e.dataDir)
}

func (e *Etcd) setProcessState() error {
	currentDir, err := os.Getwd()
	if err != nil {
		return err
	}

	// Set up the log file.
	localPath := filepath.Join(currentDir, ".tmp", "kubernetes", "etcd")
	if err := os.MkdirAll(localPath, 0744); err != nil {
		return err
	}
	if e.logFile, err = os.OpenFile(filepath.Join(localPath, "etcd.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600); err != nil {
		return err
	}
	e.logFileWriter = bufio.NewWriter(e.logFile)

	// Set up the data dir.
	e.dataDir = filepath.Join(localPath, "data")
	if err := os.MkdirAll(e.dataDir, 0744); err != nil {
		return err
	}

	// Set the listen url.
	port, host, err := addr.Suggest("")
	if err != nil {
		return err
	}
	e.URL = &url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(host, strconv.Itoa(port)),
	}

	// Set the listen peer URL.
	port, host, err = addr.Suggest("")
	if err != nil {
		return err
	}
	listenPeerURL := &url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(host, strconv.Itoa(port)),
	}

	// Starts etcd.
	args := []string{
		// TODO: Secure ETCD
		fmt.Sprintf("--listen-client-urls=%s", e.URL.String()),
		fmt.Sprintf("--advertise-client-urls=%s", e.URL.String()),
		fmt.Sprintf("--listen-peer-urls=%s", listenPeerURL.String()),
		fmt.Sprintf("--data-dir=%s", e.dataDir),
	}

	e.processState = &process.State{
		Path: e.Path,
		Args: args,
	}

	e.processState.HealthCheck.URL = *e.URL
	e.processState.HealthCheck.Path = "/health"

	if err := e.processState.Init(); err != nil {
		return err
	}
	return nil
}
