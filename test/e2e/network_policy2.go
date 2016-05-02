/*
Copyright 2014 The Kubernetes Authors All rights reserved.

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

package e2e

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/util/intstr"
	"k8s.io/kubernetes/test/e2e/framework"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

// We have two services.  A local and a remote network policy
// service.
var localServiceName = "network-policy-local"
var remoteServiceName = "network-policy-remote"

var _ = framework.KubeDescribe("NetworkPolicy", func() {
	f := framework.NewDefaultFramework("network-policy")

	// These tests use two namespaces.  A single namespace is created by
	// default.  Create another and store both separately for clarity.
	ns1 := f.Namespace
	ns2, err := f.CreateNamespace(f.BaseName + "2", map[string]string{
		"e2e-framework": f.BaseName + "2",
	})
	Expect(err).NotTo(HaveOccurred())

	BeforeEach(func() {
		//Assert basic external connectivity.
		//Since this is not really a test of kubernetes in any way, we
		//leave it as a pre-test assertion, rather than a Gingko test.
		By("Executing a successful http request from the external internet")
		resp, err := http.Get("http://google.com")
		if err != nil {
			framework.Failf("Unable to connect/talk to the internet: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			framework.Failf("Unexpected error code, expected 200, got, %v (%v)", resp.StatusCode, resp)
		}
	})

	It("should provide Internet connection for containers [Conformance]", func() {
		By("Running container which tries to wget google.com")
		framework.ExpectNoError(framework.CheckConnectivityToHost(f, "", "wget-test", "google.com", 30))
	})

	networkPolicyTest(f, ns1, ns2)
})


func networkPolicyTest(f *framework.Framework, localNamespace *api.Namespace, remoteNamespace *api.Namespace) {
	// Now we can proceed with the test.
	It("should function for intra-pod communication [Conformance]", func() {

		// Get the available nodes.
		nodes, err := framework.GetReadyNodes(f)
		framework.ExpectNoError(err)

		if len(nodes.Items) == 1 {
			// in general, the test requires two nodes. But for local development, often a one node cluster
			// is created, for simplicity and speed. (see issue #10012). We permit one-node test
			// only in some cases
			if !framework.ProviderIs("local") {
				framework.Failf(fmt.Sprintf("The test requires two Ready nodes on %s, but found just one.", framework.TestContext.Provider))
			}
			framework.Logf("Only one ready node is detected. The test has limited scope in such setting. " +
			"Rerun it with at least two nodes to get complete coverage.")
		}

		// Create a "local" service and a "remote" service.  These are really just used
		// for pod discovery by the nettest containers.
		localService := createService(f, localNamespace, localServiceName)
		remoteService := createService(f, remoteNamespace, remoteServiceName)

		// Clean up services
		defer func() {
			By("Cleaning up the local service")
			if err = f.Client.Services(localNamespace.Name).Delete(localService.Name); err != nil {
				framework.Failf("unable to delete svc %v: %v", localService.Name, err)
			}
		}()
		defer func() {
			By("Cleaning up the remote service")
			if err = f.Client.Services(remoteNamespace.Name).Delete(remoteService.Name); err != nil {
				framework.Failf("unable to delete svc %v: %v", remoteService.Name, err)
			}
		}()

		By("Creating a webserver (pending) pod on each node")

		localPodName, remotePodNames := launchNetTestPods(f, localNamespace, remoteNamespace, nodes, "1.8")

		// Deferred clean up of the pods.
		defer func() {
			By("Cleaning up the webserver pods")
			if err = f.Client.Pods(localNamespace.Name).Delete(localPodName, nil); err != nil {
				framework.Logf("Failed to delete pod %s: %v", localPodName, err)
			}
			for _, podName := range remotePodNames {
				if err = f.Client.Pods(remoteNamespace.Name).Delete(podName, nil); err != nil {
					framework.Logf("Failed to delete pod %s: %v", podName, err)
				}
			}
		}()

		// Wait for all pods to be running.
		By(fmt.Sprintf("Waiting for pod %q to be running", localPodName))
		err = framework.WaitForPodRunningInNamespace(f.Client, localPodName, localNamespace.Name)
		Expect(err).NotTo(HaveOccurred())
		for _, podName := range remotePodNames {
			By(fmt.Sprintf("Waiting for pod %q to be running", podName))
			err = framework.WaitForPodRunningInNamespace(f.Client, podName, remoteNamespace.Name)
			Expect(err).NotTo(HaveOccurred())
		}
	})
}

// Launch the nettest pods.  This launches:
// -  A single local service pod on node 0 that finds the remote service pod
//    peers
// -  A single remote service pod on all nodes that each find the local service
//    pod peer
func launchNetTestPods(f *framework.Framework, localNamespace *api.Namespace, remoteNamespace *api.Namespace, nodes *api.NodeList, version string) (string, []string) {
	remotePodNames := []string{}

	totalRemotePods := len(nodes.Items)

	Expect(totalRemotePods).NotTo(Equal(0))

	// Create the local pod on the first node.  It will find all of the remote
	// pods (one for each node).
	pod = createPod(f, localNamespace, remoteNamespace, localServiceName, remoteServiceName, totalRemotePods, nodes.Items[0], version)
	localPodName := pod.ObjectMeta.Name

	// Now create the remote pods, one on each node - each should just search
	// for the single local pod peer.
	for _, node := range nodes.Items {
		pod = createPod(f, remoteNamespace, localNamespace, remoteServiceName, localServiceName, 1, node, version)
		remotePodNames = append(podNames, pod.ObjectMeta.Name)
	}

	return localPodName, remotePodNames
}

func createPod(f *framework.Framework,
podNamespace *api.Namespace, peerNamespace *api.Namespace,
podServiceName string, peerServiceName string,
numPeers int, node *api.Node, version string) string {
	pod, err := f.Client.Pods(podNamespace).Create(&api.Pod{
		ObjectMeta: api.ObjectMeta{
			GenerateName: podServiceName + "-",
			Labels: map[string]string{
				"name": podServiceName,
			},
		},
		Spec: api.PodSpec{
			Containers: []api.Container{
				{
					Name:  "webserver",
					Image: "gcr.io/google_containers/nettest:" + version,
					Args: []string{
						"-service=" + peerServiceName,
						// peers >= totalRemotePods should be asserted by the container.
						// the nettest container finds peers by looking up list of svc endpoints.
						// The local pod searches for the remote pods.
						fmt.Sprintf("-peers=%d", numPeers),
						"-namespace=" + peerNamespace.Name},
					Ports: []api.ContainerPort{{ContainerPort: 8080}},
				},
			},
			NodeName:      node.Name,
			RestartPolicy: api.RestartPolicyNever,
		},
	})
	Expect(err).NotTo(HaveOccurred())
	framework.Logf("Created pod %s on node %s", pod.ObjectMeta.Name, node.Name)

	return pod
}

func createService(f *framework.Framework, namespace *api.Namespace, name string) (*api.Service) {
	By(fmt.Sprintf("Creating a service named %q in namespace %q", name, namespace.Name))
	svc, err := f.Client.Services(local_ns.Name).Create(&api.Service{
		ObjectMeta: api.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"name": name,
			},
		},
		Spec: api.ServiceSpec{
			Ports: []api.ServicePort{{
				Protocol:   "TCP",
				Port:       8080,
				TargetPort: intstr.FromInt(8080),
			}},
			Selector: map[string]string{
				"name": name,
			},
		},
	})
	if err != nil {
		framework.Failf("unable to create test service named [%s] %v", svc.Name, err)
	}
	return svc
}