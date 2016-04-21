/*
Copyright 2015 The Kubernetes Authors All rights reserved.

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
	"time"

	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/api/pod"
	"k8s.io/kubernetes/pkg/labels"
	"k8s.io/kubernetes/pkg/util"
	"k8s.io/kubernetes/pkg/util/wait"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = framework.KubeDescribe("NetworkPolicy", func() {
	f := framework.NewDefaultFramework("network-policy")

	It("should isolate containers when NetworkIsolation is enabled [Serial]", func() {
        NetworkIsolationEnableDisable(f)
	})
})

func CreateServerPod(namespace, port) *api.Pod {
    pod := &api.Pod{
        TypeMeta: unversioned.TypeMeta{
			Kind:       "Pod",
			APIVersion: registered.GroupOrDie(api.GroupName).GroupVersion.String(),
        },
        ObjectMeta: api.ObjectMeta{
			Name:      "np-server-" + string(util.NewUUID()),
			Namespace: namespace,
			Annotations: map[string]string{
			},
		},
        Spec: api.PodSpec{
            Containers: []api.Container{
                {
                    Name: "webserver",
                    Image: "gcr.io/google_containers/test-webserver:e2e",
                    Ports: []api.ContainerPort{
                        {
                            ContainerPort: port
                        },
                    },
                },
            },
        },
    }
    return pod
}

func NetworkIsolationEnableDisable(f *framework.Framework) {
	// We need two namespaces, so create a second one.
	ns1 := f.Namespace
	ns2, err := f.CreateNamespace(f.BaseName + "2", map[string]string{
		"e2e-framework": f.BaseName + "2",
	})
	Expect(err).NotTo(HaveOccurred())

    podClient := f.Client.Pods(ns1.Name)
	pod := CreateServerPod(ns1.Name, 80)

	// Create a pod (with a deferred cleanup delete)
    defer func() {
		By("deleting the pod")
		defer GinkgoRecover()
		podClient.Delete(pod.Name, api.NewDeleteOptions(0))
	}()
	if _, err := podClient.Create(pod); err != nil {
		Failf("Failed to create %s pod: %v", pod.Name, err)
	}
}

// A basic test to check the deployment of an image using
// a replication controller. The image serves its hostname
// which is checked for each replica.
func ServeImageOrFail(f *framework.Framework, test string, image string) {
	name := "my-hostname-" + test + "-" + string(util.NewUUID())
	replicas := 2

	// Create a replication controller for a service
	// that serves its hostname.
	// The source for the Docker containter kubernetes/serve_hostname is
	// in contrib/for-demos/serve_hostname
	By(fmt.Sprintf("Creating replication controller %s", name))
	controller, err := f.Client.ReplicationControllers(f.Namespace.Name).Create(&api.ReplicationController{
		ObjectMeta: api.ObjectMeta{
			Name: name,
		},
		Spec: api.ReplicationControllerSpec{
			Replicas: replicas,
			Selector: map[string]string{
				"name": name,
			},
			Template: &api.PodTemplateSpec{
				ObjectMeta: api.ObjectMeta{
					Labels: map[string]string{"name": name},
				},
				Spec: api.PodSpec{
					Containers: []api.Container{
						{
							Name:  name,
							Image: image,
							Ports: []api.ContainerPort{{ContainerPort: 9376}},
						},
					},
				},
			},
		},
	})
	Expect(err).NotTo(HaveOccurred())
	// Cleanup the replication controller when we are done.
	defer func() {
		// Resize the replication controller to zero to get rid of pods.
		if err := framework.DeleteRC(f.Client, f.Namespace.Name, controller.Name); err != nil {
			framework.Logf("Failed to cleanup replication controller %v: %v.", controller.Name, err)
		}
	}()

	// List the pods, making sure we observe all the replicas.
	label := labels.SelectorFromSet(labels.Set(map[string]string{"name": name}))

	pods, err := framework.PodsCreated(f.Client, f.Namespace.Name, name, replicas)

	By("Ensuring each pod is running")

	// Wait for the pods to enter the running state. Waiting loops until the pods
	// are running so non-running pods cause a timeout for this test.
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp != nil {
			continue
		}
		err = f.WaitForPodRunning(pod.Name)
		Expect(err).NotTo(HaveOccurred())
	}

	// Verify that something is listening.
	By("Trying to dial each unique pod")
	retryTimeout := 2 * time.Minute
	retryInterval := 5 * time.Second
	err = wait.Poll(retryInterval, retryTimeout, framework.PodProxyResponseChecker(f.Client, f.Namespace.Name, label, name, true, pods).CheckAllResponses)
	if err != nil {
		framework.Failf("Did not get expected responses within the timeout period of %.2f seconds.", retryTimeout.Seconds())
	}
}
