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
	"time"

	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/util"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = KubeDescribe("NetworkPolicy", func() {
	f := NewDefaultFramework("network-policy")

	It("should isolate containers when NetworkIsolation is enabled", func() {
        NetworkIsolationEnableDisable(f)
	})
})

func CreateServerPod(namespace *api.Namespace, port int) *api.Pod {
    pod := &api.Pod{
        ObjectMeta: api.ObjectMeta{
			Name:      "np-server-" + string(util.NewUUID()),
			Namespace: namespace.Name,
			Annotations: map[string]string{},
		},
        Spec: api.PodSpec{
            Containers: []api.Container{
                {
                    Name: "webserver",
                    Image: "gcr.io/google_containers/test-webserver:e2e",
                    Ports: []api.ContainerPort{
                        {
                            ContainerPort: port,
                        },
                    },
                },
            },
        },
    }
    return pod
}

func NetworkIsolationEnableDisable(f *Framework) {
	// We need two namespaces, so create a second one.
	ns1 := f.Namespace
	ns2, err := f.CreateNamespace(f.BaseName + "2", map[string]string{
		"e2e-framework": f.BaseName + "2",
	})
	Expect(err).NotTo(HaveOccurred())

    podClient := f.Client.Pods(ns1.Name)
	pod := CreateServerPod(ns1, 80)

	podClient2 := f.Client.Pods(ns2.Name)
	pod2 := CreateServerPod(ns2, 443)

	// Create a pod (with a deferred cleanup delete)
    defer func() {
		By("deleting the pod")
		defer GinkgoRecover()
		podClient.Delete(pod.Name, api.NewDeleteOptions(0))
	}()
	if _, err := podClient.Create(pod); err != nil {
		Failf("Failed to create %s pod: %v", pod.Name, err)
	}

    defer func() {
		By("deleting the pod")
		defer GinkgoRecover()
		podClient2.Delete(pod2.Name, api.NewDeleteOptions(0))
	}()
	if _, err := podClient2.Create(pod2); err != nil {
		Failf("Failed to create %s pod: %v", pod2.Name, err)
	}

	By("======SLEEPING======")
	time.Sleep(1000 * time.Second)
}
