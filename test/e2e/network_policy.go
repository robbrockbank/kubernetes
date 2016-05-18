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
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
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

Each pod is running a network monitor container (see test/images/network-monitor):
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

// Summary is the data returned by /summary API on the network monitor container
type Summary struct {
	TCPNumOutboundConnected int
	TCPNumInboundConnected  int
}

const (
	serviceAName        = "service-a"
	serviceBName        = "service-b"
	netMonitorContainer = "robbrockbank/netmonitor:1.0"
	convergenceTimeout  = 1 // Connection convergence timeout in minutes
)

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

	It("should isolate containers when NetworkIsolation is enabled [Policy]", func() {
		runIsolatedToBidirectionalTest(f)
	})
})

// Run a test that starts with fully isolated namespaces and adds policy objects to
// allow bi-directional (and mono-directional) traffic between namespaces.
func runIsolatedToBidirectionalTest(f *framework.Framework) {
	// These tests use two namespaces.  A single namespace is created by
	// default.  Create another and store both separately for clarity.
	nsA := f.Namespace
	nsB, err := f.CreateNamespace(f.BaseName+"-b", map[string]string{
		"e2e-framework": f.BaseName + "-b",
	})
	Expect(err).NotTo(HaveOccurred())

	// Turn on isolation
	setNetworkIsolationAnnotations(f, nsA, true)
	setNetworkIsolationAnnotations(f, nsB, true)

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

	// Create service A and B.  These are really just used
	// for pod discovery by the net monitor containers.
	serviceA := createService(f, nsA, serviceAName)
	serviceB := createService(f, nsB, serviceBName)

	// Clean up services
	defer func() {
		By("Cleaning up the service A")
		if err = f.Client.Services(nsA.Name).Delete(serviceA.Name); err != nil {
			framework.Failf("unable to delete svc %v: %v", serviceA.Name, err)
		}
	}()
	defer func() {
		By("Cleaning up the service B")
		if err = f.Client.Services(nsB.Name).Delete(serviceB.Name); err != nil {
			framework.Failf("unable to delete svc %v: %v", serviceB.Name, err)
		}
	}()

	By("Creating a webserver (pending) pod on each node")
	podAName, podBNames := launchNetMonitorPods(f, nsA, nsB, nodes)

	// Deferred clean up of the pods.
	defer func() {
		By("Cleaning up the webserver pods")
		if err = f.Client.Pods(nsA.Name).Delete(podAName, nil); err != nil {
			framework.Logf("Failed to delete pod %s: %v", podAName, err)
		}
		for _, podName := range podBNames {
			if err = f.Client.Pods(nsB.Name).Delete(podName, nil); err != nil {
				framework.Logf("Failed to delete pod %s: %v", podName, err)
			}
		}
	}()

	// Wait for all pods to be running.
	By(fmt.Sprintf("Waiting for pod %q to be running", podAName))
	err = framework.WaitForPodRunningInNamespace(f.Client, podAName, nsA.Name)
	Expect(err).NotTo(HaveOccurred())
	for _, podName := range podBNames {
		By(fmt.Sprintf("Waiting for pod %q to be running", podName))
		err = framework.WaitForPodRunningInNamespace(f.Client, podName, nsB.Name)
		Expect(err).NotTo(HaveOccurred())
	}

	// Open up port 8080 so that we can access the network monitor port
	By("Checking full isolation")
	addNetworkPolicyOpenPort(f, nsA, "ui1", "8080", "TCP")
	addNetworkPolicyOpenPort(f, nsB, "ui2", "8080", "TCP")

	// We expect full isolation between namespaces.  Monitor, wait for a little and
	// re-check we are still isolated.
	expected := Summary{
		TCPNumOutboundConnected: 0,
		TCPNumInboundConnected:  0,
	}
	monitorConnectivity(f, nsA, serviceA.Name, expected)
	monitorConnectivity(f, nsB, serviceB.Name, expected)

	By("Waiting and rechecking full isolation")
	time.Sleep(10 * time.Second)
	monitorConnectivity(f, nsA, serviceA.Name, expected)
	monitorConnectivity(f, nsB, serviceB.Name, expected)

	// Add policy to one namespaces to accept all traffic to the TCP port.  We should see
	// mono-directional traffic.
	By("Checking mono-directional isolation between namespaces with isolation enabled and policy applied to one namespace")
	addNetworkPolicyOpenPort(f, nsB, "tcp", "8081", "TCP")

	// We now expect the ingress TCP to service B to be allowed.
	expected = Summary{
		TCPNumOutboundConnected: len(nodes.Items),
		TCPNumInboundConnected:  0,
	}
	monitorConnectivity(f, nsA, serviceA.Name, expected)
	expected = Summary{
		TCPNumOutboundConnected: 0,
		TCPNumInboundConnected:  1,
	}
	monitorConnectivity(f, nsB, serviceB.Name, expected)

	// Add policy to one namespaces to accept all traffic to the TCP port.  We should see
	// mono-directional traffic.
	By("Checking bi-direction TCP between namespaces with isolation enabled and policy applied to both namespaces")
	addNetworkPolicyOpenPort(f, nsA, "tcp", "8081", "TCP")

	// We now expect the ingress TCP to both services A and B to be allowed.
	expected = Summary{
		TCPNumOutboundConnected: len(nodes.Items),
		TCPNumInboundConnected:  len(nodes.Items),
	}
	monitorConnectivity(f, nsA, serviceA.Name, expected)
	expected = Summary{
		TCPNumOutboundConnected: 1,
		TCPNumInboundConnected:  1,
	}
	monitorConnectivity(f, nsB, serviceB.Name, expected)
}

// Create a service which exposes TCP ports 8080 (ui) and 8081 (inter-pod-connectivity).
func createService(f *framework.Framework, namespace *api.Namespace, name string) *api.Service {
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
				Name:       "net-monitor-ui",
			}, {
				Protocol:   "TCP",
				Port:       8081,
				TargetPort: intstr.FromInt(8081),
				Name:       "net-monitor-tcp",
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

// Launch the required set of network monitor pods for the test.  This creates a single pod
// in namespaceA/serviceA which peers with a pod on each node in namespaceB/serviceB.
func launchNetMonitorPods(f *framework.Framework, namespaceA *api.Namespace, namespaceB *api.Namespace, nodes *api.NodeList) (string, []string) {
	podBNames := []string{}

	totalRemotePods := len(nodes.Items)

	Expect(totalRemotePods).NotTo(Equal(0))

	// Create the A pod on the first node.  It will find all of the B
	// pods (one for each node).
	podAName := createPod(f, namespaceA, namespaceB, serviceAName, serviceBName, totalRemotePods, &nodes.Items[0])

	// Now create the B pods, one on each node - each should just search
	// for the single A pod peer.
	for _, node := range nodes.Items {
		podName := createPod(f, namespaceB, namespaceA, serviceBName, serviceAName, 1, &node)
		podBNames = append(podBNames, podName)
	}

	return podAName, podBNames
}

// Create a network monitor pod which peers with other network monitor pods.
func createPod(f *framework.Framework,
	namespace *api.Namespace, peerNamespace *api.Namespace,
	serviceName string, peerServiceName string,
	numPeers int, node *api.Node) string {
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
					Image: netMonitorContainer,
					Args: []string{
						"--namespace=" + peerNamespace.Name,
						"--service=" + peerServiceName,
						// peers >= totalRemotePods should be asserted by the container.
						// the netmonitor container finds peers by looking up list of svc endpoints.
						// The A pod searches for the B pods.
						fmt.Sprintf("--num-peers=%d", numPeers)},
					Ports:           []api.ContainerPort{{ContainerPort: 8080}, {ContainerPort: 8081}},
					ImagePullPolicy: api.PullAlways,
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

// Monitor the connectivity matrix from the network monitor pods, until the returned
// connectivity summary matches the supplied summary.
//
// If convergence does not happen within the required time limit, the test fails.
func monitorConnectivity(f *framework.Framework, namespace *api.Namespace, serviceName string, expected Summary) {
	By(fmt.Sprintf("Verifying expected connectivity on service %v", serviceName))
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
			Name(serviceName + ":net-monitor-ui").
			Suffix("details").
			DoRaw()
	}

	getSummary := func() ([]byte, error) {
		proxyRequest, errProxy := framework.GetServicesProxyRequest(f.Client, f.Client.Get())
		if errProxy != nil {
			return nil, errProxy
		}
		return proxyRequest.Namespace(namespace.Name).
			Name(serviceName + ":net-monitor-ui").
			Suffix("summary").
			DoRaw()
	}

	// nettest containers will wait for all service endpoints to come up for 2 minutes
	// apply a 3 minutes observation period here to avoid this test to time out before the nettest starts to contact peers
	timeout := time.Now().Add(convergenceTimeout * time.Minute)
	for i := 0; !passed && timeout.After(time.Now()); i++ {
		time.Sleep(2 * time.Second)
		framework.Logf("About to make a proxy summary call")
		start := time.Now()
		body, err = getSummary()
		framework.Logf("Proxy summary call returned in %v", time.Since(start))
		if err != nil {
			framework.Logf("Attempt %v: service/pod still starting. (error: '%v')", i, err)
			continue
		}

		var summary Summary
		err = json.Unmarshal(body, &summary)
		if err != nil {
			framework.Logf("Warning: unable to unmarshal response (%v): '%v'", string(body), err)
			continue
		}

		framework.Logf("Summary: %v", string(body))
		passed = summary == expected
		if passed {
			break
		}
	}

	if !passed {
		if body, err = getDetails(); err != nil {
			framework.Failf("Timed out. Cleaning up. Error reading details: %v", err)
		} else {
			framework.Failf("Timed out. Cleaning up. Details:\n%s", string(body))
		}
	}
	Expect(passed).To(Equal(true))
}

// Configure namespace network isolation by setting the network-isolation annotation
// on the namespace.
func setNetworkIsolationAnnotations(f *framework.Framework, namespace *api.Namespace, enableIsolation bool) {
	var annotations = map[string]string{}
	if enableIsolation {
		By(fmt.Sprintf("Enabling isolation through namespace annotations on namespace %v", namespace.Name))
		annotations["net.alpha.kubernetes.io/network-isolation"] = "yes"
	} else {
		By(fmt.Sprintf("Disabling isolation through namespace annotations on namespace %v", namespace.Name))
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

// Add a network policy object to open up ingress traffic to a specific port on a namespace.
func addNetworkPolicyOpenPort(f *framework.Framework, namespace *api.Namespace, policy string, port string, protocol string) {
	By(fmt.Sprintf("Setting network policy to allow proxy traffic for namespace %v", namespace.Name))

	body := `{
  "kind": "NetworkPolicy",
  "metadata": {
    "name": "` + policy + `",
    "namespace": "` + namespace.Name + `"
  },
  "spec": {
    "podSelector": null,
    "ingress": [
      {
        "ports": [
          {
            "protocol": "` + protocol + `",
            "port": ` + port + `
          }
        ]
      }
    ]
  }
}`
	url := fmt.Sprintf("/apis/net.alpha.kubernetes.io/v1alpha1/namespaces/%v/networkpolicys", namespace.Name)

	response, err := f.Client.Post().
		AbsPath(url).
		SetHeader("Content-Type", "application/json").
		Body(bytes.NewReader([]byte(body))).
		Do().Raw()
	if err != nil {
		framework.Logf("unexpected error: %v", err)
	}
	framework.Logf("Response: %s", response)
}
