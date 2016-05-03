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
	"net/http/httptest"
	"strings"
	"time"

	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/util/intstr"
	"k8s.io/kubernetes/test/e2e/framework"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

/*
These Network Policy tests create two services A and B.
There is a single pod in the service A , and a single pod on each node in
the service B.

Each pod is running nettest container:
-  The A pod uses service discovery to locate the B pods and waits
   to establish communication (if expected) with those pods.
-  Each B pod uses service discovery to locate the A pod and waits to
   establish communication (if expected) with that pod.

We run a number of permutations of the following:
-  Policy on, or off.  When policy is on we expect isolation between the
   namespaces.
-  Policy on, but with rules allowing communication between the namespaces.
-  Both services in the same namespace (so should never be isolated)
-  Both services in different namespaces (so will be isolated based on policy)
 */

// Define the names of the two services
var serviceAName = "network-policy-A"
var serviceBName = "network-policy-B"

var _ = framework.KubeDescribe("NetworkPolicy", func() {
	f := framework.NewDefaultFramework("network-policy")

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

	It("should isolate containers when NetworkIsolation is enabled [Policy]", func() {
		runTests(f)
	})
})

func runTests(f *framework.Framework) {
	// These tests use two namespaces.  A single namespace is created by
	// default.  Create another and store both separately for clarity.
	ns1 := f.Namespace
	ns2, err := f.CreateNamespace(f.BaseName + "2", map[string]string{
		"e2e-framework": f.BaseName + "2",
	})
	Expect(err).NotTo(HaveOccurred())

	// Add policy to
	setGlobalNetworkPolicy(ns1)
	setGlobalNetworkPolicy(ns2)

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

	// No policy applied, there should be no isolation between services in
	// different namespaces.
	networkPolicyTest(f, ns1, ns2, nodes, false, false, false)

	// Enable isolation.  We expect isolation between different namespaces,
	// but not within a namespace.
	networkPolicyTest(f, ns1, ns1, nodes, true, false, false)
	//networkPolicyTest(f, ns1, ns2, nodes, true, false, true)
}


func networkPolicyTest(f *framework.Framework, namespaceA *api.Namespace, namespaceB *api.Namespace, nodes *api.NodeList,
			enableIsolation bool, installRules bool, expectIsolation bool) {
	var err error

	setNetworkIsolationAnnotations(f, namespaceA, enableIsolation)
	setNetworkIsolationAnnotations(f, namespaceB, enableIsolation)

	// Create service A and B.  These are really just used
	// for pod discovery by the nettest containers.
	serviceA := createService(f, namespaceA, serviceAName)
	serviceB := createService(f, namespaceB, serviceBName)

	// Clean up services
	defer func() {
		By("Cleaning up the service A")
		if err = f.Client.Services(namespaceA.Name).Delete(serviceA.Name); err != nil {
			framework.Failf("unable to delete svc %v: %v", serviceA.Name, err)
		}
	}()
	defer func() {
		By("Cleaning up the service B")
		if err = f.Client.Services(namespaceB.Name).Delete(serviceB.Name); err != nil {
			framework.Failf("unable to delete svc %v: %v", serviceB.Name, err)
		}
	}()

	By("Creating a webserver (pending) pod on each node")

	podAName, podBNames := launchNetTestPods(f, namespaceA, namespaceB, nodes, "1.8")

	// Deferred clean up of the pods.
	defer func() {
		By("Cleaning up the webserver pods")
		if err = f.Client.Pods(namespaceA.Name).Delete(podAName, nil); err != nil {
			framework.Logf("Failed to delete pod %s: %v", podAName, err)
		}
		for _, podName := range podBNames {
			if err = f.Client.Pods(namespaceB.Name).Delete(podName, nil); err != nil {
				framework.Logf("Failed to delete pod %s: %v", podName, err)
			}
		}
	}()

	// Wait for all pods to be running.
	By(fmt.Sprintf("Waiting for pod %q to be running", podAName))
	err = framework.WaitForPodRunningInNamespace(f.Client, podAName, namespaceA.Name)
	Expect(err).NotTo(HaveOccurred())
	for _, podName := range podBNames {
		By(fmt.Sprintf("Waiting for pod %q to be running", podName))
		err = framework.WaitForPodRunningInNamespace(f.Client, podName, namespaceB.Name)
		Expect(err).NotTo(HaveOccurred())
	}

	testConnectivity(f, namespaceA, serviceA.Name)
	testConnectivity(f, namespaceB, serviceB.Name)
}

func createService(f *framework.Framework, namespace *api.Namespace, name string) (*api.Service) {
	By(fmt.Sprintf("Creating a service named %q in namespace %q", name, namespace.Name))
	svc, err := f.Client.Services(namespace.Name).Create(&api.Service{
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

func launchNetTestPods(f *framework.Framework, namespaceA *api.Namespace, namespaceB *api.Namespace, nodes *api.NodeList, version string) (string, []string) {
	podBNames := []string{}

	totalRemotePods := len(nodes.Items)

	Expect(totalRemotePods).NotTo(Equal(0))

	// Create the A pod on the first node.  It will find all of the B
	// pods (one for each node).
	podAName := createPod(f, namespaceA, namespaceB, serviceAName, serviceBName, totalRemotePods, &nodes.Items[0], version)

	// Now create the B pods, one on each node - each should just search
	// for the single A pod peer.
	for _, node := range nodes.Items {
		podName := createPod(f, namespaceB, namespaceA, serviceBName, serviceAName, 1, &node, version)
		podBNames = append(podBNames, podName)
	}

	return podAName, podBNames
}

func createPod(f *framework.Framework,
		namespace *api.Namespace, peerNamespace *api.Namespace,
		serviceName string, peerServiceName string,
		numPeers int, node *api.Node, version string) string {
	pod, err := f.Client.Pods(namespace.Name).Create(&api.Pod{
		ObjectMeta: api.ObjectMeta{
			GenerateName: serviceName + "-",
			Labels: map[string]string{
				"name": serviceName,
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
						// The A pod searches for the B pods.
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

	return pod.ObjectMeta.Name
}


func testConnectivity(f *framework.Framework, namespace *api.Namespace, serviceName string) {
	By("Waiting for connectivity to be verified")
	passed := false

	//once response OK, evaluate response body for pass/fail.
	var err error
	var body []byte
	getDetails := func() ([]byte, error) {
		proxyRequest, errProxy := framework.GetServicesProxyRequest(f.Client, f.Client.Get())
		if errProxy != nil {
			return nil, errProxy
		}
		return proxyRequest.Namespace(namespace.Name).
			Name(serviceName).
			Suffix("read").
			DoRaw()
	}

	getStatus := func() ([]byte, error) {
		proxyRequest, errProxy := framework.GetServicesProxyRequest(f.Client, f.Client.Get())
		if errProxy != nil {
			return nil, errProxy
		}
		return proxyRequest.Namespace(namespace.Name).
			Name(serviceName).
			Suffix("status").
			DoRaw()
	}

	// nettest containers will wait for all service endpoints to come up for 2 minutes
	// apply a 3 minutes observation period here to avoid this test to time out before the nettest starts to contact peers
	timeout := time.Now().Add(3 * time.Minute)
	for i := 0; !passed && timeout.After(time.Now()); i++ {
		time.Sleep(2 * time.Second)
		framework.Logf("About to make a proxy status call")
		start := time.Now()
		body, err = getStatus()
		framework.Logf("Proxy status call returned in %v", time.Since(start))
		if err != nil {
			framework.Logf("Attempt %v: service/pod still starting. (error: '%v')", i, err)
			continue
		}
		// Finally, we pass/fail the test based on if the container's response body, as to whether or not it was able to find peers.
		switch {
		case string(body) == "pass":
			framework.Logf("Passed on attempt %v. Cleaning up.", i)
			passed = true
		case string(body) == "running":
			framework.Logf("Attempt %v: test still running", i)
		case string(body) == "fail":
			if body, err = getDetails(); err != nil {
				framework.Failf("Failed on attempt %v. Cleaning up. Error reading details: %v", i, err)
			} else {
				framework.Failf("Failed on attempt %v. Cleaning up. Details:\n%s", i, string(body))
			}
		case strings.Contains(string(body), "no endpoints available"):
			framework.Logf("Attempt %v: waiting on service/endpoints", i)
		default:
			framework.Logf("Unexpected response:\n%s", body)
		}
	}

	if !passed {
		if body, err = getDetails(); err != nil {
			framework.Failf("Timed out. Cleaning up. Error reading details: %v", err)
		} else {
			framework.Failf("Timed out. Cleaning up. Details:\n%s", string(body))
		}
	}
	Expect(string(body)).To(Equal("pass"))
}

func setNetworkIsolationAnnotations(f *framework.Framework, namespace *api.Namespace, enableIsolation bool) {
	var annotations = map[string]string{}
	if enableIsolation {
		By("Enabling isolation through namespace annotations")
		annotations["net.alpha.kubernetes.io/network-isolation"] = "yes"
	} else {
		By("Disabling isolation through namespace annotations")
		annotations["net.alpha.kubernetes.io/network-isolation"] = "no"
	}

	// Update the namespace.  We set the resource version to be an empty
	// string, this forces the update.  If we weren't to do this, we would
	// either need to re-query the namespace, or update the namespace
	// references with the one returned by the update.  This approach
	// requires less plumbing.
	namespace.ObjectMeta.Annotations = annotations
	namespace.ObjectMeta.ResourceVersion = ""
	_, err := f.Client.Namespaces().Update(namespace)
	Expect(err).NotTo(HaveOccurred())
}

func setGlobalNetworkPolicy(namespace *api.Namespace) {
	body := `{
  "kind": "NetworkPolicy",
  "metadata": {
    "name": "Proxy",
    "namespace": "` + namespace.Name + `"
  },
  "spec": {
    "podSelector": {}
    "ingress": [
      {
        "ports": [
          {
            "port": 8080
          }
        ]
      }
    ]
  }
}`
	url := fmt.Sprintf("/apis/net.alpha.kubernetes.io/v1alpha1/namespaces/%v/networkpolicys", namespace.Name)
	response, err := http.NewRequest("POST", url, bytes.NewReader([]byte(body)))
	if err != nil {
		framework.Logf("unexpected error: %v", err)
	}
	if response.StatusCode != http.StatusBadRequest {
		framework.Logf("Unexpected response %#v", response)
	}
}
