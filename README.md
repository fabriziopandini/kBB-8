# kBB-8

This repository contains an experiment for making the [Cluster API](https://github.com/kubernetes-sigs/cluster-api/) bootstrap sequence faster and simpler.

It is an early POC, but it bootstraps a minimal Kubernetes Cluster in 8s, then it bootstraps Cluster providers 
in another 8s, so I have named it kBB-8 :smile:

## Try it!
(only on darwin/amd64 with Docker installed)

Clone the project locally, open a terminal in the project folder and then run:

````shell
# Preliminary step to download all the packages with Kubernetes and Cluster API (performances can be improved!).
$ test/prepare-packages.sh 
...

# Start kBB-8 and get a Cluster API bootstrap cluster in few seconds!
$ go run kBB-8.go

 ✓ kBB-8 started!
 ✓ Cluster API with CABPK, KCP, CAPD, CAPI Ready!

Set kubectl context to "kBB-8-bootstrap"
You can now use your bootstrap cluster with:

 kubectl cluster-info 

Enjoy Cluster API with kBB-8! 😊
````

Now that your Cluster API bootstrap cluster is up (it is fast!), you can test it actually works by creating
your first Workload Cluster; from another terminal window

```sh
# Create a Cluster Class
$  k apply -f test/templates/clusterclass1.yaml 
clusterclass.cluster.x-k8s.io/clusterclass1 created
dockerclustertemplate.infrastructure.cluster.x-k8s.io/clusterclass1-infrastructure-cluster-template created
kubeadmcontrolplanetemplate.controlplane.cluster.x-k8s.io/clusterclass1-controlplane-template created
dockermachinetemplate.infrastructure.cluster.x-k8s.io/clusterclass1-controlplane-machinetemplate created
kubeadmconfigtemplate.bootstrap.cluster.x-k8s.io/clusterclass1-md-class-1-bootstraptemplate created
dockermachinetemplate.infrastructure.cluster.x-k8s.io/clusterclass1-md-class-1-machinetemplate created

# Create a ClusterResourceSet so a CNI will be automatically applied to new clusters
$  k apply -f test/templates/crs.yaml 
configmap/kindnet created
clusterresourceset.addons.cluster.x-k8s.io/cni created

# Create the first cluster
$  k apply -f test/templates/cluster1.yaml 
cluster.cluster.x-k8s.io/my-cluster1 created
```
After the last command the Workload Cluster provisioning starts, and given that we are using CAPD it creates a Kubernetes 
Cluster running in docker containers on your local machine:a


```sh
# Wait for machines in the the new Cluster to be provisioned, it takes ~1m (1 control-plane, 1 worker)
$ watch kubectl get machines
Every 2.0s: kubectl get machines                                                                                                                                                                                                                                                                                             fpandini-a01.vmware.com: Sat Feb 12 16:47:17 2022

NAME                                     CLUSTER       NODENAME                                 PROVIDERID                                          PHASE     AGE     VERSION
my-cluster1-kn22x-ghp8r                  my-cluster1   my-cluster1-kn22x-ghp8r                  docker:////my-cluster1-kn22x-ghp8r                  Running   2m47s   v1.21.2
my-cluster1-md1-spr2f-78686b44bd-rltbv   my-cluster1   my-cluster1-md1-spr2f-78686b44bd-rltbv   docker:////my-cluster1-md1-spr2f-78686b44bd-rltbv   Running   2m50s   v1.21.2

# After machine has been provisioned, you can check the containers hosting the CAPD machines actually exists.
$ docker ps
CONTAINER ID   IMAGE                                COMMAND                  CREATED              STATUS              PORTS                                  NAMES
5797f7e6e79c   kindest/node:v1.21.2                 "/usr/local/bin/entr…"   About a minute ago   Up About a minute                                          my-cluster1-md1-spr2f-78686b44bd-rltbv
8ca99305fcbb   kindest/node:v1.21.2                 "/usr/local/bin/entr…"   2 minutes ago        Up 2 minutes        49364/tcp, 127.0.0.1:49364->6443/tcp   my-cluster1-kn22x-ghp8r
d1266dc93a98   kindest/haproxy:v20210715-a6da3463   "haproxy -sf 7 -W -d…"   3 minutes ago        Up 2 minutes        49359/tcp, 0.0.0.0:49359->6443/tcp     my-cluster1-lb
```

## How kBB-8 it works

1. it downloads bootstrap packages from Cluster API/providers (not implemented yet, Cluster API/providers are not building those artifacts so we are using a local copy fetched form a GCS bucket :stuck_out_tongue_winking_eye:).
2. it creates CAs & Certificates, it runs API server and etcd, it runs the providers as out-of-cluster processes controllers and then connects everything to get a minimal Cluster API bootstrap environment.

Important!:
- kBB-8 mimics the [kind](https://github.com/kubernetes-sigs/kind) CLI, but the intent is to move it into clusterctl (it is not a kind replacement).
- kBB-8 is heavily inspired by [controller-runtime](https://github.com/kubernetes-sigs/controller-runtime) 's [envtest](https://github.com/kubernetes-sigs/controller-runtime/tree/v0.11.0/pkg/envtest), All the credits to the awesome contributors who created this code :heart: :pray:  :rocket: :rainbow:
- kBB-8 does not create a compliant/fully working Kubernetes cluster e.g. no scheduler, no controller manager also, there is no cert-manager;
  there is only the Kubernetes bits required to run Cluster API components out of cluster.
- the prototype works, you can create your first workload cluster, but there is still a lot to do (e.g pivot, idempotence etc)"

## Cleanup

You can stop kBB-8 with CTRL+c, and cleanup all the docker containers with:

```shell
$ docker ps | grep my-cluster1- | awk '{ print $1; }' | xargs docker rm -f
```

