/*
Copyright 2024.

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

package standalone

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"reflect"
	"sort"
	"strings"

	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	resourcev1beta2 "k8s.io/api/resource/v1beta2"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	discoveryfake "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/testing"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	"k8s.io/apimachinery/pkg/version"

	"github.com/NVIDIA/k8s-nim-operator/internal/utils"

	appsv1alpha1 "github.com/NVIDIA/k8s-nim-operator/api/apps/v1alpha1"
	"github.com/NVIDIA/k8s-nim-operator/internal/conditions"
	"github.com/NVIDIA/k8s-nim-operator/internal/k8sutil"
	"github.com/NVIDIA/k8s-nim-operator/internal/render"
)

func sortEnvVars(envVars []corev1.EnvVar) {
	sort.SliceStable(envVars, func(i, j int) bool {
		return envVars[i].Name < envVars[j].Name
	})
}

func sortVolumeMounts(volumeMounts []corev1.VolumeMount) {
	sort.SliceStable(volumeMounts, func(i, j int) bool {
		return volumeMounts[i].Name < volumeMounts[j].Name
	})
}

func sortVolumes(volumes []corev1.Volume) {
	sort.SliceStable(volumes, func(i, j int) bool {
		return volumes[i].Name < volumes[j].Name
	})
}

func getCondition(obj *appsv1alpha1.NIMService, conditionType string) *metav1.Condition {
	for _, condition := range obj.Status.Conditions {
		if condition.Type == conditionType {
			return &condition
		}
	}
	return nil
}

// Custom transport that redirects requests to a specific host.
type mockTransport struct {
	targetHost        string
	testServer        *httptest.Server
	originalTransport http.RoundTripper
}

func (m *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Check if this request is going to our target IP
	hostname := strings.Split(req.URL.Host, ":")[0]
	if hostname == "" || req.URL.Host == m.targetHost {
		// Create a new URL pointing to our test server
		testURL, _ := url.Parse(m.testServer.URL)
		testURL.Path = req.URL.Path
		testURL.RawQuery = req.URL.RawQuery

		// Create a new request to our test server
		newReq := req.Clone(req.Context())
		newReq.URL = testURL
		newReq.Host = req.URL.Host // Preserve the original Host header

		// Send the request to our test server
		return http.DefaultClient.Do(newReq)
	}

	// For all other requests, use the original transport
	return m.originalTransport.RoundTrip(req)
}

var _ = Describe("NIMServiceReconciler for a standalone platform", func() {
	var (
		client            client.Client
		reconciler        *NIMServiceReconciler
		scheme            *runtime.Scheme
		nimService        *appsv1alpha1.NIMService
		nimCache          *appsv1alpha1.NIMCache
		volumeMounts      []corev1.VolumeMount
		volumes           []corev1.Volume
		testServerHandler http.HandlerFunc
		testServer        *httptest.Server
		originalTransport = http.DefaultTransport
		discoveryClient   *discoveryfake.FakeDiscovery
	)
	BeforeEach(func() {
		scheme = runtime.NewScheme()
		Expect(appsv1alpha1.AddToScheme(scheme)).To(Succeed())
		Expect(appsv1.AddToScheme(scheme)).To(Succeed())
		Expect(rbacv1.AddToScheme(scheme)).To(Succeed())
		Expect(autoscalingv2.AddToScheme(scheme)).To(Succeed())
		Expect(networkingv1.AddToScheme(scheme)).To(Succeed())
		Expect(corev1.AddToScheme(scheme)).To(Succeed())
		Expect(monitoringv1.AddToScheme(scheme)).To(Succeed())
		Expect(lwsv1.AddToScheme(scheme)).To(Succeed())

		client = fake.NewClientBuilder().WithScheme(scheme).
			WithStatusSubresource(&appsv1alpha1.NIMService{}).
			WithStatusSubresource(&appsv1alpha1.NIMCache{}).
			Build()
		boolTrue := true
		cwd, err := os.Getwd()
		if err != nil {
			panic(err)
		}

		// Create mock discovery client
		discoveryClient = &discoveryfake.FakeDiscovery{Fake: &testing.Fake{}}
		discoveryClient.Resources = []*metav1.APIResourceList{
			{
				GroupVersion: resourcev1beta2.SchemeGroupVersion.String(),
				APIResources: []metav1.APIResource{
					{Name: "resourceclaims"},
				},
			},
		}
		discoveryClient.FakedServerVersion = &version.Info{
			GitVersion: "v1.33.0",
		}

		reconciler = &NIMServiceReconciler{
			Client:          client,
			scheme:          scheme,
			updater:         conditions.NewUpdater(client),
			renderer:        render.NewRenderer(path.Join(strings.TrimSuffix(cwd, "internal/controller/platform/standalone"), "manifests")),
			recorder:        record.NewFakeRecorder(1000),
			discoveryClient: discoveryClient,
		}
		pvcName := "test-pvc"
		minReplicas := int32(1)
		nimService = &appsv1alpha1.NIMService{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-nimservice",
				Namespace: "default",
			},
			Spec: appsv1alpha1.NIMServiceSpec{
				Labels:      map[string]string{"app": "test-app"},
				Annotations: map[string]string{"annotation-key": "annotation-value"},
				Image:       appsv1alpha1.Image{Repository: "nvcr.io/nvidia/nim-llm", PullPolicy: "IfNotPresent", Tag: "v0.1.0", PullSecrets: []string{"ngc-secret"}},
				Storage: appsv1alpha1.NIMServiceStorage{
					PVC: appsv1alpha1.PersistentVolumeClaim{
						Name: pvcName,
					},
					NIMCache: appsv1alpha1.NIMCacheVolSpec{
						Name: "test-nimcache",
					},
				},
				Env: []corev1.EnvVar{
					{
						Name:  "custom-env",
						Value: "custom-value",
					},
					{
						Name:  "NIM_CACHE_PATH",
						Value: "/model-store",
					},
					{
						Name: "NGC_API_KEY",
						ValueFrom: &corev1.EnvVarSource{
							SecretKeyRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "ngc-api-secret",
								},
								Key: "NGC_API_KEY",
							},
						},
					},
					{
						Name:  "OUTLINES_CACHE_DIR",
						Value: "/tmp/outlines",
					},
					{
						Name:  "NIM_SERVER_PORT",
						Value: "9000",
					},
					{
						Name:  "NIM_HTTP_API_PORT",
						Value: "9000",
					},
					{
						Name:  "NIM_JSONL_LOGGING",
						Value: "1",
					},
					{
						Name:  "NIM_LOG_LEVEL",
						Value: "info",
					},
				},
				Resources: &corev1.ResourceRequirements{
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("128Mi"),
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("250m"),
						corev1.ResourceMemory: resource.MustParse("64Mi"),
					},
				},
				NodeSelector: map[string]string{"disktype": "ssd"},
				Tolerations: []corev1.Toleration{
					{
						Key:      "key1",
						Operator: corev1.TolerationOpEqual,
						Value:    "value1",
						Effect:   corev1.TaintEffectNoSchedule,
					},
				},
				Expose: appsv1alpha1.Expose{
					Service: appsv1alpha1.Service{Type: corev1.ServiceTypeLoadBalancer, Port: ptr.To[int32](8123), Annotations: map[string]string{"annotation-key-specific": "service"}},
					Ingress: appsv1alpha1.Ingress{
						Enabled:     ptr.To[bool](true),
						Annotations: map[string]string{"annotation-key-specific": "ingress"},
						Spec: networkingv1.IngressSpec{
							Rules: []networkingv1.IngressRule{
								{
									Host: "test-nimservice.default.example.com",
									IngressRuleValue: networkingv1.IngressRuleValue{
										HTTP: &networkingv1.HTTPIngressRuleValue{
											Paths: []networkingv1.HTTPIngressPath{
												{
													Path: "/",
													Backend: networkingv1.IngressBackend{
														Service: &networkingv1.IngressServiceBackend{
															Name: "test-nimservice",
															Port: networkingv1.ServiceBackendPort{
																Number: 8080,
															},
														},
													},
												},
											},
										},
									},
								},
							},
						},
					},
				},
				Scale: appsv1alpha1.Autoscaling{
					Enabled:     ptr.To[bool](true),
					Annotations: map[string]string{"annotation-key-specific": "HPA"},
					HPA: appsv1alpha1.HorizontalPodAutoscalerSpec{
						MinReplicas: &minReplicas,
						MaxReplicas: 10,
						Metrics: []autoscalingv2.MetricSpec{
							{
								Type: autoscalingv2.ResourceMetricSourceType,
								Resource: &autoscalingv2.ResourceMetricSource{
									Target: autoscalingv2.MetricTarget{
										Type: autoscalingv2.UtilizationMetricType,
									},
								},
							},
							{
								Type: autoscalingv2.PodsMetricSourceType,
								Pods: &autoscalingv2.PodsMetricSource{
									Target: autoscalingv2.MetricTarget{
										Type: autoscalingv2.UtilizationMetricType,
									},
								},
							},
						},
					},
				},
				Metrics: appsv1alpha1.Metrics{
					Enabled: &boolTrue,
					ServiceMonitor: appsv1alpha1.ServiceMonitor{
						Annotations:   map[string]string{"annotation-key-specific": "service-monitor"},
						Interval:      "1m",
						ScrapeTimeout: "30s",
					},
				},
				ReadinessProbe: appsv1alpha1.Probe{
					Enabled: &boolTrue,
					Probe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							HTTPGet: &corev1.HTTPGetAction{
								Path: "/ready",
								Port: intstr.IntOrString{IntVal: 8000},
							},
						},
					},
				},
				LivenessProbe: appsv1alpha1.Probe{
					Enabled: &boolTrue,
					Probe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							HTTPGet: &corev1.HTTPGetAction{
								Path: "/live",
								Port: intstr.IntOrString{IntVal: 8000},
							},
						},
					},
				},
				StartupProbe: appsv1alpha1.Probe{
					Enabled: &boolTrue,
					Probe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							HTTPGet: &corev1.HTTPGetAction{
								Path: "/start",
								Port: intstr.IntOrString{IntVal: 8000},
							},
						},
					},
				},
				RuntimeClassName: "nvidia",
			},
			Status: appsv1alpha1.NIMServiceStatus{
				State: conditions.NotReady,
			},
		}

		volumes = []corev1.Volume{
			{
				Name: "dshm",
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{
						Medium: corev1.StorageMediumMemory,
					},
				},
			},
			{
				Name: "model-store",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: "test-pvc",
						ReadOnly:  false,
					},
				},
			},
		}

		volumeMounts = []corev1.VolumeMount{
			{
				Name:      "model-store",
				MountPath: "/model-store",
				SubPath:   "subPath",
			},
			{
				Name:      "dshm",
				MountPath: "/dev/shm",
			},
		}
		nimCache = &appsv1alpha1.NIMCache{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-nimcache",
				Namespace: "default",
			},
			Spec: appsv1alpha1.NIMCacheSpec{
				Source:  appsv1alpha1.NIMSource{NGC: &appsv1alpha1.NGCSource{ModelPuller: "test-container", PullSecret: "my-secret"}},
				Storage: appsv1alpha1.NIMCacheStorage{PVC: appsv1alpha1.PersistentVolumeClaim{Create: ptr.To[bool](true), StorageClass: "standard", Size: "1Gi", SubPath: "subPath"}},
			},
			Status: appsv1alpha1.NIMCacheStatus{
				State: appsv1alpha1.NimCacheStatusReady,
				PVC:   pvcName,
				Profiles: []appsv1alpha1.NIMProfile{{
					Name:   "test-profile",
					Config: map[string]string{"tp": "4"}},
				},
			},
		}
		_ = client.Create(context.TODO(), nimCache)
		pvc := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      pvcName,
				Namespace: "default",
			},
		}
		_ = client.Create(context.TODO(), pvc)

		var buf bytes.Buffer
		log.SetOutput(&buf)
		defer func() {
			log.SetOutput(os.Stderr)
		}()

		// Start mock test server to serve nimservice endpoint.
		testServerHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				w.WriteHeader(http.StatusOK)
				_, err := w.Write([]byte(`{"object": "list", "data":[{"id": "dummy-model", "object": "model", "root": "dummy-model", "parent": null}]}`))
				Expect(err).ToNot(HaveOccurred())
			} else {
				w.WriteHeader(http.StatusNotFound)
			}
		})
		testServer = httptest.NewServer(testServerHandler)
		http.DefaultTransport = &mockTransport{
			targetHost:        "127.0.0.1:8123",
			testServer:        testServer,
			originalTransport: originalTransport,
		}
	})

	AfterEach(func() {
		defer func() { http.DefaultTransport = originalTransport }()
		defer testServer.Close()
		// Clean up the NIMService instance
		nimService := &appsv1alpha1.NIMService{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-nimservice",
				Namespace: "default",
			},
		}
		_ = client.Delete(context.TODO(), nimService)

		// Ensure that nimCache status is ready before each test
		nimCache.Status = appsv1alpha1.NIMCacheStatus{
			State: appsv1alpha1.NimCacheStatusReady,
		}

		// Update nimCache status
		Expect(client.Status().Update(context.TODO(), nimCache)).To(Succeed())
	})

	Describe("Reconcile", func() {
		It("should create all resources for the NIMService", func() {
			namespacedName := types.NamespacedName{Name: nimService.Name, Namespace: nimService.Namespace}
			err := client.Create(context.TODO(), nimService)
			Expect(err).NotTo(HaveOccurred())

			result, err := reconciler.reconcileNIMService(context.TODO(), nimService)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))

			// Role should be created
			role := &rbacv1.Role{}
			err = client.Get(context.TODO(), namespacedName, role)
			Expect(err).NotTo(HaveOccurred())
			Expect(role.Name).To(Equal(nimService.GetName()))
			Expect(role.Namespace).To(Equal(nimService.GetNamespace()))

			// RoleBinding should be created
			roleBinding := &rbacv1.RoleBinding{}
			err = client.Get(context.TODO(), namespacedName, roleBinding)
			Expect(err).NotTo(HaveOccurred())
			Expect(roleBinding.Name).To(Equal(nimService.GetName()))
			Expect(roleBinding.Namespace).To(Equal(nimService.GetNamespace()))

			// Service Account should be created
			serviceAccount := &corev1.ServiceAccount{}
			err = client.Get(context.TODO(), namespacedName, serviceAccount)
			Expect(err).NotTo(HaveOccurred())
			Expect(serviceAccount.Name).To(Equal(nimService.GetName()))
			Expect(serviceAccount.Namespace).To(Equal(nimService.GetNamespace()))

			// Service should be created
			service := &corev1.Service{}
			err = client.Get(context.TODO(), namespacedName, service)
			Expect(err).NotTo(HaveOccurred())
			Expect(service.Name).To(Equal(nimService.GetName()))
			Expect(string(service.Spec.Type)).To(Equal(nimService.GetServiceType()))
			Expect(service.Namespace).To(Equal(nimService.GetNamespace()))
			Expect(service.Annotations["annotation-key"]).To(Equal("annotation-value"))
			Expect(service.Annotations["annotation-key-specific"]).To(Equal("service"))
			// Verify the named ports
			expectedPorts := map[string]int32{
				"api": 8123,
			}

			foundPorts := make(map[string]int32)
			for _, port := range service.Spec.Ports {
				foundPorts[port.Name] = port.Port
			}

			for name, expectedPort := range expectedPorts {
				Expect(foundPorts).To(HaveKeyWithValue(name, expectedPort),
					fmt.Sprintf("Expected service to have named port %q with port %d", name, expectedPort))
			}

			// Ingress should be created
			ingress := &networkingv1.Ingress{}
			err = client.Get(context.TODO(), namespacedName, ingress)
			Expect(err).NotTo(HaveOccurred())
			Expect(ingress.Name).To(Equal(nimService.GetName()))
			Expect(ingress.Namespace).To(Equal(nimService.GetNamespace()))
			Expect(ingress.Annotations["annotation-key"]).To(Equal("annotation-value"))
			Expect(ingress.Annotations["annotation-key-specific"]).To(Equal("ingress"))
			Expect(service.Spec.Ports[0].Name).To(Equal("api"))
			Expect(service.Spec.Ports[0].Port).To(Equal(int32(8123)))

			// HPA should be deployed
			hpa := &autoscalingv2.HorizontalPodAutoscaler{}
			err = client.Get(context.TODO(), namespacedName, hpa)
			Expect(err).NotTo(HaveOccurred())
			Expect(hpa.Name).To(Equal(nimService.GetName()))
			Expect(hpa.Namespace).To(Equal(nimService.GetNamespace()))
			Expect(hpa.Annotations["annotation-key"]).To(Equal("annotation-value"))
			Expect(hpa.Annotations["annotation-key-specific"]).To(Equal("HPA"))
			Expect(*hpa.Spec.MinReplicas).To(Equal(int32(1)))
			Expect(hpa.Spec.MaxReplicas).To(Equal(int32(10)))

			// Service Monitor should be created
			sm := &monitoringv1.ServiceMonitor{}
			err = client.Get(context.TODO(), namespacedName, sm)
			Expect(err).NotTo(HaveOccurred())
			Expect(sm.Name).To(Equal(nimService.GetName()))
			Expect(sm.Namespace).To(Equal(nimService.GetNamespace()))
			Expect(sm.Annotations["annotation-key"]).To(Equal("annotation-value"))
			Expect(sm.Annotations["annotation-key-specific"]).To(Equal("service-monitor"))
			Expect(sm.Spec.Endpoints[0].Port).To(Equal("api"))
			Expect(sm.Spec.Endpoints[0].ScrapeTimeout).To(Equal(monitoringv1.Duration("30s")))
			Expect(sm.Spec.Endpoints[0].Interval).To(Equal(monitoringv1.Duration("1m")))

			// Deployment should be created
			deployment := &appsv1.Deployment{}
			err = client.Get(context.TODO(), namespacedName, deployment)
			Expect(err).NotTo(HaveOccurred())
			Expect(deployment.Name).To(Equal(nimService.GetName()))
			Expect(deployment.Namespace).To(Equal(nimService.GetNamespace()))
			Expect(deployment.Annotations["annotation-key"]).To(Equal("annotation-value"))
			Expect(deployment.Spec.Template.Spec.Containers[0].Name).To(Equal(nimService.GetContainerName()))
			Expect(deployment.Spec.Template.Spec.Containers[0].Image).To(Equal(nimService.GetImage()))
			Expect(deployment.Spec.Template.Spec.Containers[0].ReadinessProbe).To(Equal(nimService.Spec.ReadinessProbe.Probe))
			Expect(deployment.Spec.Template.Spec.Containers[0].LivenessProbe).To(Equal(nimService.Spec.LivenessProbe.Probe))
			Expect(deployment.Spec.Template.Spec.Containers[0].StartupProbe).To(Equal(nimService.Spec.StartupProbe.Probe))
			Expect(*deployment.Spec.Template.Spec.RuntimeClassName).To(Equal(nimService.Spec.RuntimeClassName))

			sortEnvVars(deployment.Spec.Template.Spec.Containers[0].Env)
			sortEnvVars(nimService.Spec.Env)
			Expect(deployment.Spec.Template.Spec.Containers[0].Env).To(Equal(nimService.Spec.Env))

			sortVolumes(deployment.Spec.Template.Spec.Volumes)
			sortVolumes(volumes)
			Expect(deployment.Spec.Template.Spec.Volumes).To(Equal(volumes))

			sortVolumeMounts(deployment.Spec.Template.Spec.Containers[0].VolumeMounts)
			sortVolumeMounts(volumeMounts)
			Expect(deployment.Spec.Template.Spec.Containers[0].VolumeMounts).To(Equal(volumeMounts))

			Expect(deployment.Spec.Template.Spec.NodeSelector).To(Equal(nimService.Spec.NodeSelector))
			Expect(deployment.Spec.Template.Spec.Tolerations).To(Equal(nimService.Spec.Tolerations))
		})

		Context("spec reconciliation with DRAResources", func() {
			It("should request resource claims", func() {
				nimService.Spec.DRAResources = []appsv1alpha1.DRAResource{
					{
						ResourceClaimName: ptr.To("test-resource-claim"),
					},
				}
				nimServiceKey := types.NamespacedName{Name: nimService.Name, Namespace: nimService.Namespace}
				err := client.Create(context.TODO(), nimService)
				Expect(err).NotTo(HaveOccurred())

				_, err = reconciler.reconcileNIMService(context.TODO(), nimService)
				Expect(err).NotTo(HaveOccurred())
				deployment := &appsv1.Deployment{}
				err = client.Get(context.TODO(), nimServiceKey, deployment)
				Expect(err).NotTo(HaveOccurred())

				// Pod spec validations.
				podSpec := deployment.Spec.Template.Spec
				Expect(podSpec.ResourceClaims).To(HaveLen(1))
				Expect(podSpec.ResourceClaims[0].ResourceClaimName).To(Equal(ptr.To("test-resource-claim")))

				// Container spec validations.
				Expect(podSpec.Containers[0].Resources.Claims).To(HaveLen(1))
				Expect(podSpec.Containers[0].Resources.Claims[0].Name).To(Equal(podSpec.ResourceClaims[0].Name))
			})

			It("should request resource claims templates", func() {
				nimService.Spec.DRAResources = []appsv1alpha1.DRAResource{
					{
						ResourceClaimTemplateName: ptr.To("test-resource-claim-template"),
					},
				}
				nimServiceKey := types.NamespacedName{Name: nimService.Name, Namespace: nimService.Namespace}
				err := client.Create(context.TODO(), nimService)
				Expect(err).NotTo(HaveOccurred())

				_, err = reconciler.reconcileNIMService(context.TODO(), nimService)
				Expect(err).NotTo(HaveOccurred())
				deployment := &appsv1.Deployment{}
				err = client.Get(context.TODO(), nimServiceKey, deployment)
				Expect(err).NotTo(HaveOccurred())

				// Pod spec validations.
				podSpec := deployment.Spec.Template.Spec
				Expect(podSpec.ResourceClaims).To(HaveLen(1))
				Expect(podSpec.ResourceClaims[0].ResourceClaimTemplateName).To(Equal(ptr.To("test-resource-claim-template")))

				// Container spec validations.
				Expect(podSpec.Containers[0].Resources.Claims).To(HaveLen(1))
				Expect(podSpec.Containers[0].Resources.Claims[0].Name).To(Equal(podSpec.ResourceClaims[0].Name))
			})

			It("should only contain the requests from the resource claims", func() {
				nimService.Spec.DRAResources = []appsv1alpha1.DRAResource{
					{
						ResourceClaimName: ptr.To("test-resource-claim"),
						Requests:          []string{"test-request-1", "test-request-2"},
					},
				}
				nimServiceKey := types.NamespacedName{Name: nimService.Name, Namespace: nimService.Namespace}
				err := client.Create(context.TODO(), nimService)
				Expect(err).NotTo(HaveOccurred())

				_, err = reconciler.reconcileNIMService(context.TODO(), nimService)
				Expect(err).NotTo(HaveOccurred())

				deployment := &appsv1.Deployment{}
				err = client.Get(context.TODO(), nimServiceKey, deployment)
				Expect(err).NotTo(HaveOccurred())

				// Pod spec validations.
				podSpec := deployment.Spec.Template.Spec
				Expect(podSpec.ResourceClaims).To(HaveLen(1))
				Expect(podSpec.ResourceClaims[0].ResourceClaimName).To(Equal(ptr.To("test-resource-claim")))

				// Container spec validations.
				Expect(podSpec.Containers[0].Resources.Claims).To(HaveLen(2))
				Expect(podSpec.Containers[0].Resources.Claims[0].Name).To(Equal(podSpec.ResourceClaims[0].Name))
				Expect(podSpec.Containers[0].Resources.Claims[0].Request).To(Equal("test-request-1"))
				Expect(podSpec.Containers[0].Resources.Claims[1].Name).To(Equal(podSpec.ResourceClaims[0].Name))
				Expect(podSpec.Containers[0].Resources.Claims[1].Request).To(Equal("test-request-2"))
			})

			It("should mark NIMService as failed when cluster version is less than v1.33.0", func() {
				reconciler.discoveryClient = &discoveryfake.FakeDiscovery{
					Fake: &testing.Fake{},
					FakedServerVersion: &version.Info{
						GitVersion: "v1.30.5",
					},
				}

				nimService.Spec.DRAResources = []appsv1alpha1.DRAResource{
					{
						ResourceClaimName: ptr.To("test-resource-claim"),
					},
				}

				nimServiceKey := types.NamespacedName{Name: nimService.Name, Namespace: nimService.Namespace}
				err := client.Create(context.TODO(), nimService)
				Expect(err).NotTo(HaveOccurred())

				_, err = reconciler.reconcileNIMService(context.TODO(), nimService)
				Expect(err).NotTo(HaveOccurred())

				obj := &appsv1alpha1.NIMService{}
				err = client.Get(context.TODO(), nimServiceKey, obj)
				Expect(err).NotTo(HaveOccurred())
				Expect(obj.Status.State).To(Equal(appsv1alpha1.NIMServiceStatusFailed))
				failedCondition := getCondition(obj, conditions.Failed)
				Expect(failedCondition).NotTo(BeNil())
				Expect(failedCondition.Status).To(Equal(metav1.ConditionTrue))
				Expect(failedCondition.Reason).To(Equal(conditions.ReasonDRAResourcesUnsupported))
				Expect(failedCondition.Message).To(Equal("DRA resources are not supported by NIM-Operator on this cluster, please upgrade to k8s version 'v1.33.0' or higher"))
			})

			It("should mark NIMService as failed when resource claim CRD is not enabled", func() {
				reconciler.discoveryClient = &discoveryfake.FakeDiscovery{
					Fake: &testing.Fake{},
					FakedServerVersion: &version.Info{
						GitVersion: "v1.33.0",
					},
				}
				nimService.Spec.DRAResources = []appsv1alpha1.DRAResource{
					{
						ResourceClaimName: ptr.To("test-resource-claim"),
					},
				}
				nimServiceKey := types.NamespacedName{Name: nimService.Name, Namespace: nimService.Namespace}
				err := client.Create(context.TODO(), nimService)
				Expect(err).NotTo(HaveOccurred())

				_, err = reconciler.reconcileNIMService(context.TODO(), nimService)
				Expect(err).NotTo(HaveOccurred())

				obj := &appsv1alpha1.NIMService{}
				err = client.Get(context.TODO(), nimServiceKey, obj)
				Expect(err).NotTo(HaveOccurred())
				Expect(obj.Status.State).To(Equal(appsv1alpha1.NIMServiceStatusFailed))
				failedCondition := getCondition(obj, conditions.Failed)
				Expect(failedCondition).NotTo(BeNil())
				Expect(failedCondition.Status).To(Equal(metav1.ConditionTrue))
				Expect(failedCondition.Reason).To(Equal(conditions.ReasonDRAResourcesUnsupported))
				Expect(failedCondition.Message).To(Equal("DRA resources are not supported by NIM-Operator on this cluster, please ensure resource.k8s.io/v1beta2 API group is enabled"))
			})

			It("should mark NIMService as failed when resource claim name is duplicated", func() {
				nimService.Spec.DRAResources = []appsv1alpha1.DRAResource{
					{
						ResourceClaimName: ptr.To("test-resource-claim"),
					},
					{
						ResourceClaimName: ptr.To("test-resource-claim"),
					},
				}
				nimServiceKey := types.NamespacedName{Name: nimService.Name, Namespace: nimService.Namespace}
				err := client.Create(context.TODO(), nimService)
				Expect(err).NotTo(HaveOccurred())

				_, err = reconciler.reconcileNIMService(context.TODO(), nimService)
				Expect(err).NotTo(HaveOccurred())

				obj := &appsv1alpha1.NIMService{}
				err = client.Get(context.TODO(), nimServiceKey, obj)
				Expect(err).NotTo(HaveOccurred())
				Expect(obj.Status.State).To(Equal(appsv1alpha1.NIMServiceStatusFailed))
				failedCondition := getCondition(obj, conditions.Failed)
				Expect(failedCondition).NotTo(BeNil())
				Expect(failedCondition.Status).To(Equal(metav1.ConditionTrue))
				Expect(failedCondition.Reason).To(Equal(conditions.ReasonDRAResourcesUnsupported))
				Expect(failedCondition.Message).To(Equal("spec.draResources[1].resourceClaimName: duplicate resource claim name: 'test-resource-claim'"))
			})
		})

		It("should delete Deployment when the NIMService is deleted", func() {
			nimServiceKey := types.NamespacedName{Name: nimService.Name, Namespace: nimService.Namespace}
			err := client.Create(context.TODO(), nimService)
			Expect(err).NotTo(HaveOccurred())

			_, err = reconciler.reconcileNIMService(context.TODO(), nimService)
			Expect(err).NotTo(HaveOccurred())

			deployment := &appsv1.Deployment{}
			err = client.Get(context.TODO(), nimServiceKey, deployment)
			Expect(err).NotTo(HaveOccurred())

			err = client.Delete(context.TODO(), nimService)
			Expect(err).NotTo(HaveOccurred())

			// Simulate the finalizer logic
			err = reconciler.cleanupNIMService(context.TODO(), nimService)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should delete HPA when NIMService is updated", func() {
			namespacedName := types.NamespacedName{Name: nimService.Name, Namespace: nimService.Namespace}
			err := client.Create(context.TODO(), nimService)
			Expect(err).NotTo(HaveOccurred())

			result, err := reconciler.reconcileNIMService(context.TODO(), nimService)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))

			// HPA should be deployed
			hpa := &autoscalingv2.HorizontalPodAutoscaler{}
			err = client.Get(context.TODO(), namespacedName, hpa)
			Expect(err).NotTo(HaveOccurred())
			Expect(hpa.Name).To(Equal(nimService.GetName()))
			Expect(hpa.Namespace).To(Equal(nimService.GetNamespace()))
			Expect(*hpa.Spec.MinReplicas).To(Equal(int32(1)))
			Expect(hpa.Spec.MaxReplicas).To(Equal(int32(10)))

			nimService := &appsv1alpha1.NIMService{}
			err = client.Get(context.TODO(), namespacedName, nimService)
			Expect(err).NotTo(HaveOccurred())
			nimService.Spec.Scale.Enabled = ptr.To(false)
			nimService.Spec.Expose.Ingress.Enabled = ptr.To(false)
			err = client.Update(context.TODO(), nimService)
			Expect(err).NotTo(HaveOccurred())

			result, err = reconciler.reconcileNIMService(context.TODO(), nimService)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))
			hpa = &autoscalingv2.HorizontalPodAutoscaler{}
			err = client.Get(context.TODO(), namespacedName, hpa)
			Expect(err).To(HaveOccurred())
			Expect(errors.IsNotFound(err)).To(Equal(true))
			ingress := &networkingv1.Ingress{}
			err = client.Get(context.TODO(), namespacedName, ingress)
			Expect(err).To(HaveOccurred())
			Expect(errors.IsNotFound(err)).To(Equal(true))
		})

	})

	It("should be NotReady when nimcache is not ready", func() {
		nimCache.Status = appsv1alpha1.NIMCacheStatus{
			State: appsv1alpha1.NimCacheStatusNotReady,
		}
		Expect(client.Status().Update(context.TODO(), nimCache)).To(Succeed())
		err := client.Create(context.TODO(), nimService)
		Expect(err).NotTo(HaveOccurred())

		result, err := reconciler.reconcileNIMService(context.TODO(), nimService)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(ctrl.Result{}))

		// Check that the NIMService is not ready.
		namespacedName := types.NamespacedName{Name: nimService.Name, Namespace: nimService.Namespace}
		obj := &appsv1alpha1.NIMService{}
		err = client.Get(context.TODO(), namespacedName, obj)
		Expect(err).NotTo(HaveOccurred())
		Expect(obj.Status.State).To(Equal(appsv1alpha1.NIMServiceStatusNotReady))
		readyCondition := getCondition(obj, conditions.Ready)
		Expect(readyCondition).NotTo(BeNil())
		Expect(readyCondition.Status).To(Equal(metav1.ConditionFalse))
		Expect(readyCondition.Reason).To(Equal(conditions.ReasonNIMCacheNotReady))
	})

	It("should be Failed when nimcache is not found", func() {
		testNimService := nimService.DeepCopy()
		testNimService.Spec.Storage.NIMCache.Name = "invalid-nimcache"
		err := client.Create(context.TODO(), testNimService)
		Expect(err).NotTo(HaveOccurred())

		result, err := reconciler.reconcileNIMService(context.TODO(), testNimService)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(ctrl.Result{}))

		// Check that the NIMService is in failed state.
		namespacedName := types.NamespacedName{Name: testNimService.Name, Namespace: testNimService.Namespace}
		obj := &appsv1alpha1.NIMService{}
		err = client.Get(context.TODO(), namespacedName, obj)
		Expect(err).NotTo(HaveOccurred())
		Expect(obj.Status.State).To(Equal(appsv1alpha1.NIMServiceStatusFailed))
		failedCondition := getCondition(obj, conditions.Failed)
		Expect(failedCondition).NotTo(BeNil())
		Expect(failedCondition.Status).To(Equal(metav1.ConditionTrue))
		Expect(failedCondition.Reason).To(Equal(conditions.ReasonNIMCacheNotFound))
	})

	It("should be Failed when nimcache is in failed state", func() {
		nimCache.Status = appsv1alpha1.NIMCacheStatus{
			State: appsv1alpha1.NimCacheStatusFailed,
			Conditions: []metav1.Condition{
				{
					Type:    conditions.Failed,
					Status:  metav1.ConditionTrue,
					Reason:  conditions.Failed,
					Message: "NIMCache failed",
				},
			},
		}
		Expect(client.Status().Update(context.TODO(), nimCache)).To(Succeed())

		err := client.Create(context.TODO(), nimService)
		Expect(err).NotTo(HaveOccurred())

		result, err := reconciler.reconcileNIMService(context.TODO(), nimService)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(ctrl.Result{}))

		// Check that the NIMService is in failed state.
		namespacedName := types.NamespacedName{Name: nimService.Name, Namespace: nimService.Namespace}
		obj := &appsv1alpha1.NIMService{}
		err = client.Get(context.TODO(), namespacedName, obj)
		Expect(err).NotTo(HaveOccurred())
		Expect(obj.Status.State).To(Equal(appsv1alpha1.NIMServiceStatusFailed))
		failedCondition := getCondition(obj, conditions.Failed)
		Expect(failedCondition).NotTo(BeNil())
		Expect(failedCondition.Status).To(Equal(metav1.ConditionTrue))
		Expect(failedCondition.Reason).To(Equal(conditions.ReasonNIMCacheFailed))
	})

	Describe("isDeploymentReady for setting status on NIMService", func() {
		AfterEach(func() {
			// Clean up the Deployment instance
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-nimservice",
					Namespace: "default",
				},
			}
			_ = client.Delete(context.TODO(), deployment)
		})
		It("Deployment exceeded in its progress", func() {
			nimServiceKey := types.NamespacedName{Name: nimService.Name, Namespace: nimService.Namespace}
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-nimservice",
					Namespace: "default",
				},
				Status: appsv1.DeploymentStatus{
					Conditions: []appsv1.DeploymentCondition{
						{
							Type:   appsv1.DeploymentProgressing,
							Reason: "ProgressDeadlineExceeded",
						},
					},
				},
			}
			err := client.Create(context.TODO(), deployment)
			Expect(err).NotTo(HaveOccurred())
			msg, ready, err := reconciler.isDeploymentReady(context.TODO(), &nimServiceKey)
			Expect(err).ToNot(HaveOccurred())
			Expect(ready).To(Equal(false))
			Expect(msg).To(Equal(fmt.Sprintf("deployment %q exceeded its progress deadline", deployment.Name)))
		})

		It("Waiting for deployment rollout to finish: new replicas are coming up", func() {
			nimServiceKey := types.NamespacedName{Name: nimService.Name, Namespace: nimService.Namespace}
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-nimservice",
					Namespace: "default",
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: &[]int32{4}[0],
				},
				Status: appsv1.DeploymentStatus{
					UpdatedReplicas: 1,
				},
			}
			err := client.Create(context.TODO(), deployment)
			Expect(err).NotTo(HaveOccurred())
			msg, ready, err := reconciler.isDeploymentReady(context.TODO(), &nimServiceKey)
			Expect(err).ToNot(HaveOccurred())
			Expect(ready).To(Equal(false))
			Expect(msg).To(Equal(fmt.Sprintf("Waiting for deployment %q rollout to finish: %d out of %d new replicas have been updated...\n", deployment.Name, deployment.Status.UpdatedReplicas, *deployment.Spec.Replicas)))
		})

		It("Waiting for deployment rollout to finish: old replicas are pending termination", func() {
			nimServiceKey := types.NamespacedName{Name: nimService.Name, Namespace: nimService.Namespace}
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-nimservice",
					Namespace: "default",
				},
				Status: appsv1.DeploymentStatus{
					UpdatedReplicas: 1,
					Replicas:        4,
				},
			}
			err := client.Create(context.TODO(), deployment)
			Expect(err).NotTo(HaveOccurred())
			msg, ready, err := reconciler.isDeploymentReady(context.TODO(), &nimServiceKey)
			Expect(err).ToNot(HaveOccurred())
			Expect(ready).To(Equal(false))
			Expect(msg).To(Equal(fmt.Sprintf("Waiting for deployment %q rollout to finish: %d old replicas are pending termination...\n", deployment.Name, deployment.Status.Replicas-deployment.Status.UpdatedReplicas)))
		})

		It("Waiting for deployment rollout to finish:", func() {
			nimServiceKey := types.NamespacedName{Name: nimService.Name, Namespace: nimService.Namespace}
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-nimservice",
					Namespace: "default",
				},
				Status: appsv1.DeploymentStatus{
					UpdatedReplicas:   4,
					AvailableReplicas: 1,
				},
			}
			err := client.Create(context.TODO(), deployment)
			Expect(err).NotTo(HaveOccurred())
			msg, ready, err := reconciler.isDeploymentReady(context.TODO(), &nimServiceKey)
			Expect(err).ToNot(HaveOccurred())
			Expect(ready).To(Equal(false))
			Expect(msg).To(Equal(fmt.Sprintf("Waiting for deployment %q rollout to finish: %d of %d updated replicas are available...\n", deployment.Name, deployment.Status.AvailableReplicas, deployment.Status.UpdatedReplicas)))
		})

		It("Deployment successfully rolled out", func() {
			nimServiceKey := types.NamespacedName{Name: nimService.Name, Namespace: nimService.Namespace}
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-nimservice",
					Namespace: "default",
				},
				Status: appsv1.DeploymentStatus{
					UpdatedReplicas:   4,
					AvailableReplicas: 4,
				},
			}
			err := client.Create(context.TODO(), deployment)
			Expect(err).NotTo(HaveOccurred())
			msg, ready, err := reconciler.isDeploymentReady(context.TODO(), &nimServiceKey)
			Expect(err).ToNot(HaveOccurred())
			Expect(ready).To(Equal(true))
			Expect(msg).To(Equal(fmt.Sprintf("deployment %q successfully rolled out\n", deployment.Name)))
		})
	})

	Describe("LWS environment variable creation for multi-node inferencing NIMService", func() {
		It("should create environment variables for the LWS", func() {
			nimService := &appsv1alpha1.NIMService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-nimservice",
					Namespace: "default",
				},
				Spec: appsv1alpha1.NIMServiceSpec{
					Expose: appsv1alpha1.Expose{
						Service: appsv1alpha1.Service{Type: corev1.ServiceTypeLoadBalancer, Port: ptr.To[int32](8123), Annotations: map[string]string{"annotation-key-specific": "service"}},
					},
					MultiNode: &appsv1alpha1.NimServiceMultiNodeConfig{
						Size:       2,
						GPUSPerPod: 8,
					},
				},
			}

			leaderEnv := utils.SortKeys(nimService.GetLWSLeaderEnv())
			workerEnv := utils.SortKeys(nimService.GetLWSWorkerEnv())

			Expect(reflect.DeepEqual(leaderEnv, []corev1.EnvVar{
				{
					Name:  "NIM_CACHE_PATH",
					Value: "/model-store",
				},
				{
					Name: "NGC_API_KEY",
					ValueFrom: &corev1.EnvVarSource{
						SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "",
							},
							Key: "NGC_API_KEY",
						},
					},
				},
				{
					Name:  "OUTLINES_CACHE_DIR",
					Value: "/tmp/outlines",
				},
				{
					Name:  "NIM_SERVER_PORT",
					Value: "8123",
				},
				{
					Name:  "NIM_HTTP_API_PORT",
					Value: "8123",
				},
				{
					Name:  "NIM_JSONL_LOGGING",
					Value: "1",
				},
				{
					Name:  "NIM_LOG_LEVEL",
					Value: "INFO",
				},
				{
					Name:  "NIM_MPI_ALLOW_RUN_AS_ROOT",
					Value: "0",
				},
				{
					Name:  "NIM_NUM_COMPUTE_NODES",
					Value: "2",
				},
				{
					Name:  "NIM_MULTI_NODE",
					Value: "1",
				},
				{
					Name:  "NIM_TENSOR_PARALLEL_SIZE",
					Value: "8",
				},
				{
					Name:  "NIM_PIPELINE_PARALLEL_SIZE",
					Value: "2",
				},
				{
					Name: "NIM_NODE_RANK",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.labels['leaderworkerset.sigs.k8s.io/worker-index']",
						},
					},
				},
				{
					Name:  "NIM_LEADER_ROLE",
					Value: "1",
				},
				{
					Name:  "OMPI_MCA_orte_keep_fqdn_hostnames",
					Value: "true",
				},
				{
					Name:  "OMPI_MCA_plm_rsh_args",
					Value: "-o ConnectionAttempts=20",
				},
				{
					Name:  "GPUS_PER_NODE",
					Value: "8",
				},
				{
					Name:  "CLUSTER_START_TIMEOUT",
					Value: "300",
				},
				{
					Name: "CLUSTER_SIZE",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.annotations['leaderworkerset.sigs.k8s.io/size']",
						},
					},
				},
				{
					Name: "GROUP_INDEX",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.labels['leaderworkerset.sigs.k8s.io/group-index']",
						},
					},
				},
			})).To(BeTrue())

			Expect(reflect.DeepEqual(workerEnv, []corev1.EnvVar{
				{
					Name:  "NIM_CACHE_PATH",
					Value: "/model-store",
				},
				{
					Name: "NGC_API_KEY",
					ValueFrom: &corev1.EnvVarSource{
						SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "",
							},
							Key: "NGC_API_KEY",
						},
					},
				},
				{
					Name:  "OUTLINES_CACHE_DIR",
					Value: "/tmp/outlines",
				},
				{
					Name:  "NIM_SERVER_PORT",
					Value: "8123",
				},
				{
					Name:  "NIM_HTTP_API_PORT",
					Value: "8123",
				},
				{
					Name:  "NIM_JSONL_LOGGING",
					Value: "1",
				},
				{
					Name:  "NIM_LOG_LEVEL",
					Value: "INFO",
				},
				{
					Name:  "NIM_MPI_ALLOW_RUN_AS_ROOT",
					Value: "0",
				},
				{
					Name:  "NIM_NUM_COMPUTE_NODES",
					Value: "2",
				},
				{
					Name:  "NIM_MULTI_NODE",
					Value: "1",
				},
				{
					Name:  "NIM_TENSOR_PARALLEL_SIZE",
					Value: "8",
				},
				{
					Name:  "NIM_PIPELINE_PARALLEL_SIZE",
					Value: "2",
				},
				{
					Name: "NIM_NODE_RANK",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.labels['leaderworkerset.sigs.k8s.io/worker-index']",
						},
					},
				},
				{
					Name:  "NIM_LEADER_ROLE",
					Value: "0",
				},
				{
					Name: "LEADER_NAME",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.annotations['leaderworkerset.sigs.k8s.io/leader-name']",
						},
					},
				},
				{
					Name: "NAMESPACE",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.namespace",
						},
					},
				},
				{
					Name: "LWS_NAME",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.labels['leaderworkerset.sigs.k8s.io/name']",
						},
					},
				},
			})).To(BeTrue())
		})
	})

	Describe("LWS deployment for multi-node inferencing NIMService", func() {
		AfterEach(func() {
			lws := &lwsv1.LeaderWorkerSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-nimservice-lws",
					Namespace: "default",
				},
			}
			err := client.Delete(context.TODO(), lws)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should report ready when LWS is available", func() {
			lws := &lwsv1.LeaderWorkerSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-nimservice-lws",
					Namespace: "default",
				},
				Status: lwsv1.LeaderWorkerSetStatus{
					Conditions: []metav1.Condition{
						{
							Type:   string(lwsv1.LeaderWorkerSetAvailable),
							Status: metav1.ConditionTrue,
						},
					},
				},
			}
			err := client.Create(context.TODO(), lws)
			Expect(err).NotTo(HaveOccurred())
			msg, ready, err := reconciler.isLeaderWorkerSetReady(context.TODO(), nimService)
			Expect(err).ToNot(HaveOccurred())
			Expect(ready).To(Equal(true))
			Expect(msg).To(Equal(fmt.Sprintf("leaderworkerset %q is ready", lws.Name)))
		})
		It("should report not ready when LWS is not available", func() {
			lws := &lwsv1.LeaderWorkerSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-nimservice-lws",
					Namespace: "default",
				},
				Status: lwsv1.LeaderWorkerSetStatus{
					Conditions: []metav1.Condition{
						{
							Type:   string(lwsv1.LeaderWorkerSetProgressing),
							Status: metav1.ConditionTrue,
						},
						{
							Type:   string(lwsv1.LeaderWorkerSetAvailable),
							Status: metav1.ConditionFalse,
						},
					},
				},
			}
			err := client.Create(context.TODO(), lws)
			Expect(err).NotTo(HaveOccurred())
			msg, ready, err := reconciler.isLeaderWorkerSetReady(context.TODO(), nimService)
			Expect(err).ToNot(HaveOccurred())
			Expect(ready).To(Equal(false))
			Expect(msg).To(Equal(fmt.Sprintf("leaderworkerset %q is not ready", lws.Name)))
		})
	})
	Describe("update model status on NIMService", func() {
		BeforeEach(func() {
			ingress := &networkingv1.Ingress{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-nimservice",
					Namespace: "default",
				},
				Spec: nimService.GetIngressSpec(),
			}
			_ = client.Create(context.TODO(), ingress)
		})
		AfterEach(func() {
			// Clean up the Service instance
			svc := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-nimservice",
					Namespace: "default",
				},
			}
			_ = client.Delete(context.TODO(), svc)
		})

		It("should fail when NIMService is unreachable", func() {
			svc := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-nimservice",
					Namespace: "default",
				},
				Spec: corev1.ServiceSpec{
					Type:      corev1.ServiceTypeClusterIP,
					ClusterIP: "bad.host", // not intercepted by testServer.
					Ports: []corev1.ServicePort{
						{
							Port: 8123,
							Name: "service-port",
						},
					},
				},
			}
			_ = client.Create(context.TODO(), svc)
			err := reconciler.updateModelStatus(context.Background(), nimService)
			Expect(err).To(HaveOccurred())
			Expect(nimService.Status.Model).To(BeNil())
		})

		It("should fail when models response is unmarshallable", func() {
			testServer.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/models" {
					w.WriteHeader(http.StatusOK)
					_, err := w.Write([]byte(`{"data": "invalid response"}`))
					Expect(err).ToNot(HaveOccurred())
				} else {
					w.WriteHeader(http.StatusNotFound)
				}
			})
			svc := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-nimservice",
					Namespace: "default",
				},
				Spec: corev1.ServiceSpec{
					Type:      corev1.ServiceTypeClusterIP,
					ClusterIP: "127.0.0.1",
					Ports: []corev1.ServicePort{
						{
							Port: 8123,
							Name: "service-port",
						},
					},
				},
			}
			_ = client.Create(context.TODO(), svc)
			err := reconciler.updateModelStatus(context.Background(), nimService)
			Expect(err).To(HaveOccurred())
			Expect(nimService.Status.Model).To(BeNil())
		})

		It("should have empty model name when it cannot be inferred", func() {
			testServer.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/models" {
					w.WriteHeader(http.StatusOK)
					// Set dummy object type for model.
					_, err := w.Write([]byte(`{"object": "list", "data":[{"id": "dummy-model", "object": "dummy", "root": "dummy-model", "parent": null}]}`))
					Expect(err).ToNot(HaveOccurred())
				} else {
					w.WriteHeader(http.StatusNotFound)
				}
			})
			svc := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-nimservice",
					Namespace: "default",
				},
				Spec: corev1.ServiceSpec{
					Type:      corev1.ServiceTypeClusterIP,
					ClusterIP: "127.0.0.1",
					Ports: []corev1.ServicePort{
						{
							Port: 8123,
							Name: "service-port",
						},
					},
				},
			}
			_ = client.Create(context.TODO(), svc)
			err := reconciler.updateModelStatus(context.Background(), nimService)
			Expect(err).ToNot(HaveOccurred())
			Expect(nimService.Status.Model).ToNot(BeNil())
			Expect(nimService.Status.Model.Name).ToNot(BeEmpty())
		})

		It("should set model status on NIMService", func() {
			svc := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-nimservice",
					Namespace: "default",
				},
				Spec: corev1.ServiceSpec{
					Type:      corev1.ServiceTypeClusterIP,
					ClusterIP: "127.0.0.1",
					Ports: []corev1.ServicePort{
						{
							Port: 8123,
							Name: "service-port",
						},
					},
				},
			}
			_ = client.Create(context.TODO(), svc)
			err := reconciler.updateModelStatus(context.Background(), nimService)
			Expect(err).ToNot(HaveOccurred())
			modelStatus := nimService.Status.Model
			Expect(modelStatus).ToNot(BeNil())
			Expect(modelStatus.ClusterEndpoint).To(Equal("127.0.0.1:8123"))
			Expect(modelStatus.ExternalEndpoint).To(Equal("test-nimservice.default.example.com"))
			Expect(modelStatus.Name).To(Equal("dummy-model"))
		})

		It("should succeed when nimservice has lora adapter models attached", func() {
			testServer.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/models" {
					w.WriteHeader(http.StatusOK)
					// Set dummy object type for model.
					_, err := w.Write([]byte(`{"object": "list", "data":[{"id": "dummy-model-adapter1", "object": "model", "root": "dummy-model", "parent": null}, {"id": "dummy-model-adapter2", "object": "model", "root": "dummy-model", "parent": null}, {"id": "dummy-model", "object": "model", "root": "dummy-model", "parent": null}]}`))
					Expect(err).ToNot(HaveOccurred())
				} else {
					w.WriteHeader(http.StatusNotFound)
				}
			})
			svc := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-nimservice",
					Namespace: "default",
				},
				Spec: corev1.ServiceSpec{
					Type:      corev1.ServiceTypeClusterIP,
					ClusterIP: "127.0.0.1",
					Ports: []corev1.ServicePort{
						{
							Port: 8123,
							Name: "service-port",
						},
					},
				},
			}
			_ = client.Create(context.TODO(), svc)
			err := reconciler.updateModelStatus(context.Background(), nimService)
			Expect(err).ToNot(HaveOccurred())
			modelStatus := nimService.Status.Model
			Expect(modelStatus).ToNot(BeNil())
			Expect(modelStatus.ClusterEndpoint).To(Equal("127.0.0.1:8123"))
			Expect(modelStatus.ExternalEndpoint).To(Equal("test-nimservice.default.example.com"))
			Expect(modelStatus.Name).To(Equal("dummy-model"))
		})

		Context("when nimservice only supports /v1/metadata", func() {
			It("should succeed when nimservice only supports /v1/metadata", func() {
				testServer.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/v1/models":
						w.WriteHeader(http.StatusNotFound)
					case "/v1/metadata":
						w.WriteHeader(http.StatusOK)
						_, err := w.Write([]byte(`{"modelInfo": [{"shortName": "dummy-model:dummy-version", "modelUrl": "ngc://org/team/dummy-model:dummy-version"}]}`))
						Expect(err).ToNot(HaveOccurred())
					default:
						w.WriteHeader(http.StatusNotFound)
					}
				})
				svc := &corev1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-nimservice",
						Namespace: "default",
					},
					Spec: corev1.ServiceSpec{
						Type:      corev1.ServiceTypeClusterIP,
						ClusterIP: "127.0.0.1",
						Ports: []corev1.ServicePort{
							{
								Port: 8123,
								Name: "service-port",
							},
						},
					},
				}
				_ = client.Create(context.TODO(), svc)
				err := reconciler.updateModelStatus(context.Background(), nimService)
				Expect(err).ToNot(HaveOccurred())
				modelStatus := nimService.Status.Model
				Expect(modelStatus).ToNot(BeNil())
				Expect(modelStatus.ClusterEndpoint).To(Equal("127.0.0.1:8123"))
				Expect(modelStatus.ExternalEndpoint).To(Equal("test-nimservice.default.example.com"))
				Expect(modelStatus.Name).To(Equal("dummy-model"))

			})
		})

		It("should add NIM_MODEL_NAME environment variable when NIMCache is Universal NIM", func() {
			// Create a NIMCache with ModelEndpoint to make it Universal NIM
			modelEndpoint := "https://api.ngc.nvidia.com/v2/models/nvidia/nim-llama2-7b/versions/1.0.0"
			universalNimCache := &appsv1alpha1.NIMCache{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-universal-nimcache",
					Namespace: "default",
				},
				Spec: appsv1alpha1.NIMCacheSpec{
					Source: appsv1alpha1.NIMSource{
						NGC: &appsv1alpha1.NGCSource{
							ModelPuller:   "test-container",
							PullSecret:    "my-secret",
							ModelEndpoint: &modelEndpoint,
						},
					},
					Storage: appsv1alpha1.NIMCacheStorage{
						PVC: appsv1alpha1.PersistentVolumeClaim{
							Create:       ptr.To[bool](true),
							StorageClass: "standard",
							Size:         "1Gi",
							SubPath:      "subPath",
						},
					},
				},
				Status: appsv1alpha1.NIMCacheStatus{
					State: appsv1alpha1.NimCacheStatusReady,
					PVC:   "test-universal-nimcache-pvc",
					Profiles: []appsv1alpha1.NIMProfile{{
						Name:   "test-profile",
						Config: map[string]string{"tp": "4"}},
					},
				},
			}
			Expect(client.Create(context.TODO(), universalNimCache)).To(Succeed())

			// Create PVC for the universal NIMCache
			universalPVC := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-universal-nimcache-pvc",
					Namespace: "default",
				},
			}
			Expect(client.Create(context.TODO(), universalPVC)).To(Succeed())

			// Create a new NIMService instance for this test
			testNimService := &appsv1alpha1.NIMService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-universal-nimservice",
					Namespace: "default",
				},
				Spec: appsv1alpha1.NIMServiceSpec{
					Labels:      map[string]string{"app": "test-app"},
					Annotations: map[string]string{"annotation-key": "annotation-value"},
					Image: appsv1alpha1.Image{
						Repository:  "nvcr.io/nvidia/nim-llm",
						Tag:         "v0.1.0",
						PullPolicy:  "IfNotPresent",
						PullSecrets: []string{"ngc-secret"},
					},
					Storage: appsv1alpha1.NIMServiceStorage{
						PVC: appsv1alpha1.PersistentVolumeClaim{
							Name:         "test-pvc",
							StorageClass: "standard",
							Size:         "1Gi",
							Create:       ptr.To[bool](true),
						},
						NIMCache: appsv1alpha1.NIMCacheVolSpec{
							Name: "test-universal-nimcache",
						},
					},
					Env: []corev1.EnvVar{
						{
							Name:  "custom-env",
							Value: "custom-value",
						},
					},
					Resources: &corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("500m"),
							corev1.ResourceMemory: resource.MustParse("128Mi"),
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("250m"),
							corev1.ResourceMemory: resource.MustParse("64Mi"),
						},
					},
					NodeSelector: map[string]string{"disktype": "ssd"},
					Tolerations: []corev1.Toleration{{
						Key:      "key1",
						Operator: corev1.TolerationOpEqual,
						Value:    "value1",
						Effect:   corev1.TaintEffectNoSchedule,
					}},
					Expose: appsv1alpha1.Expose{
						Service: appsv1alpha1.Service{Type: corev1.ServiceTypeLoadBalancer, Port: ptr.To[int32](8123), Annotations: map[string]string{"annotation-key-specific": "service"}},
					},
					RuntimeClassName: "nvidia",
				},
			}

			namespacedName := types.NamespacedName{Name: testNimService.Name, Namespace: testNimService.Namespace}
			Expect(client.Create(context.TODO(), testNimService)).To(Succeed())

			result, err := reconciler.reconcileNIMService(context.TODO(), testNimService)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))

			// Deployment should be created
			deployment := &appsv1.Deployment{}
			err = client.Get(context.TODO(), namespacedName, deployment)
			Expect(err).NotTo(HaveOccurred())
			Expect(deployment.Name).To(Equal(testNimService.GetName()))
			Expect(deployment.Namespace).To(Equal(testNimService.GetNamespace()))

			// Verify that NIM_MODEL_NAME environment variable is added
			container := deployment.Spec.Template.Spec.Containers[0]
			Expect(container.Env).To(ContainElement(corev1.EnvVar{
				Name:  "NIM_MODEL_NAME",
				Value: "/model-store",
			}), "NIM_MODEL_NAME environment variable should be present and set to /model-store")

			// Verify that other environment variables are still present
			var customEnv *corev1.EnvVar
			for _, env := range container.Env {
				if env.Name == "custom-env" {
					customEnv = &env
					break
				}
			}
			Expect(customEnv).NotTo(BeNil(), "Custom environment variables should still be present")
			Expect(customEnv.Value).To(Equal("custom-value"))
		})

		It("should not add NIM_MODEL_NAME environment variable when NIMCache is not Universal NIM", func() {
			// Create a regular NIMCache (not Universal NIM)
			regularNimCache := &appsv1alpha1.NIMCache{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-regular-nimcache",
					Namespace: "default",
				},
				Spec: appsv1alpha1.NIMCacheSpec{
					Source: appsv1alpha1.NIMSource{
						NGC: &appsv1alpha1.NGCSource{
							ModelPuller: "test-container",
							PullSecret:  "my-secret",
							// No ModelEndpoint, so it's not Universal NIM
						},
					},
					Storage: appsv1alpha1.NIMCacheStorage{
						PVC: appsv1alpha1.PersistentVolumeClaim{
							Create:       ptr.To[bool](true),
							StorageClass: "standard",
							Size:         "1Gi",
							SubPath:      "subPath",
						},
					},
				},
				Status: appsv1alpha1.NIMCacheStatus{
					State: appsv1alpha1.NimCacheStatusReady,
					PVC:   "test-regular-nimcache-pvc",
					Profiles: []appsv1alpha1.NIMProfile{{
						Name:   "test-profile",
						Config: map[string]string{"tp": "4"}},
					},
				},
			}
			Expect(client.Create(context.TODO(), regularNimCache)).To(Succeed())

			// Create PVC for the regular NIMCache
			regularPVC := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-regular-nimcache-pvc",
					Namespace: "default",
				},
			}
			Expect(client.Create(context.TODO(), regularPVC)).To(Succeed())

			// Create a new NIMService instance for this test
			testNimService := &appsv1alpha1.NIMService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-regular-nimservice",
					Namespace: "default",
				},
				Spec: appsv1alpha1.NIMServiceSpec{
					Labels:      map[string]string{"app": "test-app"},
					Annotations: map[string]string{"annotation-key": "annotation-value"},
					Image: appsv1alpha1.Image{
						Repository:  "nvcr.io/nvidia/nim-llm",
						Tag:         "v0.1.0",
						PullPolicy:  "IfNotPresent",
						PullSecrets: []string{"ngc-secret"},
					},
					Storage: appsv1alpha1.NIMServiceStorage{
						PVC: appsv1alpha1.PersistentVolumeClaim{
							Name:         "test-pvc",
							StorageClass: "standard",
							Size:         "1Gi",
							Create:       ptr.To[bool](true),
						},
						NIMCache: appsv1alpha1.NIMCacheVolSpec{
							Name: "test-regular-nimcache",
						},
					},
					Env: []corev1.EnvVar{
						{
							Name:  "custom-env",
							Value: "custom-value",
						},
					},
					Resources: &corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("500m"),
							corev1.ResourceMemory: resource.MustParse("128Mi"),
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("250m"),
							corev1.ResourceMemory: resource.MustParse("64Mi"),
						},
					},
					NodeSelector: map[string]string{"disktype": "ssd"},
					Tolerations: []corev1.Toleration{{
						Key:      "key1",
						Operator: corev1.TolerationOpEqual,
						Value:    "value1",
						Effect:   corev1.TaintEffectNoSchedule,
					}},
					Expose: appsv1alpha1.Expose{
						Service: appsv1alpha1.Service{Type: corev1.ServiceTypeLoadBalancer, Port: ptr.To[int32](8123), Annotations: map[string]string{"annotation-key-specific": "service"}},
					},
					RuntimeClassName: "nvidia",
				},
			}

			namespacedName := types.NamespacedName{Name: testNimService.Name, Namespace: testNimService.Namespace}
			Expect(client.Create(context.TODO(), testNimService)).To(Succeed())

			result, err := reconciler.reconcileNIMService(context.TODO(), testNimService)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))

			// Deployment should be created
			deployment := &appsv1.Deployment{}
			err = client.Get(context.TODO(), namespacedName, deployment)
			Expect(err).NotTo(HaveOccurred())
			Expect(deployment.Name).To(Equal(testNimService.GetName()))
			Expect(deployment.Namespace).To(Equal(testNimService.GetNamespace()))

			// Verify that NIM_MODEL_NAME environment variable is NOT added
			container := deployment.Spec.Template.Spec.Containers[0]
			Expect(container.Env).NotTo(ContainElement(corev1.EnvVar{Name: "NIM_MODEL_NAME"}), "NIM_MODEL_NAME environment variable should not be present for non-Universal NIM")

			// Verify that other environment variables are still present
			var customEnv *corev1.EnvVar
			for _, env := range container.Env {
				if env.Name == "custom-env" {
					customEnv = &env
					break
				}
			}
			Expect(customEnv).NotTo(BeNil(), "Custom environment variables should still be present")
			Expect(customEnv.Value).To(Equal("custom-value"))
		})

		It("should not add NIM_MODEL_NAME environment variable for multi-node non-Universal NIM deployment", func() {
			// Create a non-Universal NIM NIMCache
			regularNimCache := &appsv1alpha1.NIMCache{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-multinode-regular-nimcache",
					Namespace: "default",
				},
				Spec: appsv1alpha1.NIMCacheSpec{
					Source: appsv1alpha1.NIMSource{
						NGC: &appsv1alpha1.NGCSource{
							ModelPuller: "test-container",
							PullSecret:  "my-secret",
							// No ModelEndpoint, so it's not Universal NIM
						},
					},
					Storage: appsv1alpha1.NIMCacheStorage{
						PVC: appsv1alpha1.PersistentVolumeClaim{
							Create:       ptr.To[bool](true),
							StorageClass: "standard",
							Size:         "1Gi",
							SubPath:      "subPath",
						},
					},
				},
				Status: appsv1alpha1.NIMCacheStatus{
					State: appsv1alpha1.NimCacheStatusReady,
					PVC:   "test-multinode-regular-nimcache-pvc",
					Profiles: []appsv1alpha1.NIMProfile{{
						Name:   "test-profile",
						Config: map[string]string{"tp": "4"}},
					},
				},
			}
			Expect(client.Create(context.TODO(), regularNimCache)).To(Succeed())

			// Create PVC for the regular NIMCache
			regularPVC := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-multinode-regular-nimcache-pvc",
					Namespace: "default",
				},
			}
			Expect(client.Create(context.TODO(), regularPVC)).To(Succeed())

			// Create a multi-node NIMService instance
			testNimService := &appsv1alpha1.NIMService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-multinode-regular-nimservice",
					Namespace: "default",
				},
				Spec: appsv1alpha1.NIMServiceSpec{
					Image: appsv1alpha1.Image{
						Repository:  "nvcr.io/nvidia/nim-llm",
						Tag:         "v0.1.0",
						PullPolicy:  "IfNotPresent",
						PullSecrets: []string{"ngc-secret"},
					},
					Storage: appsv1alpha1.NIMServiceStorage{
						PVC: appsv1alpha1.PersistentVolumeClaim{
							Name:         "test-pvc",
							StorageClass: "standard",
							Size:         "1Gi",
							Create:       ptr.To[bool](true),
						},
						NIMCache: appsv1alpha1.NIMCacheVolSpec{
							Name: "test-multinode-regular-nimcache",
						},
					},
					Expose: appsv1alpha1.Expose{
						Service: appsv1alpha1.Service{Type: corev1.ServiceTypeLoadBalancer, Port: ptr.To[int32](8123)},
					},
					MultiNode: &appsv1alpha1.NimServiceMultiNodeConfig{
						BackendType: appsv1alpha1.NIMBackendTypeLWS,
						Size:        2,
						GPUSPerPod:  2,
					},
				},
			}

			Expect(client.Create(context.TODO(), testNimService)).To(Succeed())

			result, err := reconciler.reconcileNIMService(context.TODO(), testNimService)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))

			// LeaderWorkerSet should be created instead of Deployment
			lws := &lwsv1.LeaderWorkerSet{}
			lwsNamespacedName := types.NamespacedName{Name: testNimService.GetLWSName(), Namespace: testNimService.Namespace}
			err = client.Get(context.TODO(), lwsNamespacedName, lws)
			Expect(err).NotTo(HaveOccurred())

			// Verify that NIM_MODEL_NAME environment variable is NOT added to leader and worker containers
			leaderContainer := lws.Spec.LeaderWorkerTemplate.LeaderTemplate.Spec.Containers[0]
			Expect(leaderContainer.Env).NotTo(ContainElement(corev1.EnvVar{Name: "NIM_MODEL_NAME"}), "NIM_MODEL_NAME environment variable should not be present in leader container for multi-node non-Universal NIM")

			workerContainer := lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers[0]
			Expect(workerContainer.Env).NotTo(ContainElement(corev1.EnvVar{Name: "NIM_MODEL_NAME"}), "NIM_MODEL_NAME environment variable should not be present in worker container for multi-node non-Universal NIM")
		})

		It("should respect user-provided NIM_MODEL_NAME environment variable over default for Universal NIM", func() {
			// Create a Universal NIM NIMCache
			modelEndpoint := "https://api.ngc.nvidia.com/v2/models/nvidia/nim-llama2-7b/versions/1q.0.0"
			universalNimCache := &appsv1alpha1.NIMCache{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-custom-universal-nimcache",
					Namespace: "default",
				},
				Spec: appsv1alpha1.NIMCacheSpec{
					Source: appsv1alpha1.NIMSource{
						NGC: &appsv1alpha1.NGCSource{
							ModelPuller:   "test-container",
							PullSecret:    "my-secret",
							ModelEndpoint: &modelEndpoint,
						},
					},
					Storage: appsv1alpha1.NIMCacheStorage{
						PVC: appsv1alpha1.PersistentVolumeClaim{
							Create:       ptr.To[bool](true),
							StorageClass: "standard",
							Size:         "1Gi",
							SubPath:      "subPath",
						},
					},
				},
				Status: appsv1alpha1.NIMCacheStatus{
					State: appsv1alpha1.NimCacheStatusReady,
					PVC:   "test-custom-universal-nimcache-pvc",
					Profiles: []appsv1alpha1.NIMProfile{{
						Name:   "test-profile",
						Config: map[string]string{"tp": "4"}},
					},
				},
			}
			Expect(client.Create(context.TODO(), universalNimCache)).To(Succeed())

			// Create PVC for the Universal NIMCache
			universalPVC := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-custom-universal-nimcache-pvc",
					Namespace: "default",
				},
			}
			Expect(client.Create(context.TODO(), universalPVC)).To(Succeed())

			// Create a new NIMService instance with custom NIM_MODEL_NAME
			testNimService := &appsv1alpha1.NIMService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-custom-universal-nimservice",
					Namespace: "default",
				},
				Spec: appsv1alpha1.NIMServiceSpec{
					Env: []corev1.EnvVar{
						{
							Name:  "NIM_MODEL_NAME",
							Value: "/custom-model-path",
						},
						{
							Name:  "CUSTOM_ENV",
							Value: "custom-value",
						},
					},
					Image: appsv1alpha1.Image{
						Repository:  "nvcr.io/nvidia/nim-llm",
						Tag:         "v0.1.0",
						PullPolicy:  "IfNotPresent",
						PullSecrets: []string{"ngc-secret"},
					},
					Storage: appsv1alpha1.NIMServiceStorage{
						PVC: appsv1alpha1.PersistentVolumeClaim{
							Name:         "test-pvc",
							StorageClass: "standard",
							Size:         "1Gi",
							Create:       ptr.To[bool](true),
						},
						NIMCache: appsv1alpha1.NIMCacheVolSpec{
							Name: "test-custom-universal-nimcache",
						},
					},
					Expose: appsv1alpha1.Expose{
						Service: appsv1alpha1.Service{Type: corev1.ServiceTypeLoadBalancer, Port: ptr.To[int32](8123)},
					},
				},
			}

			namespacedName := types.NamespacedName{Name: testNimService.Name, Namespace: testNimService.Namespace}
			Expect(client.Create(context.TODO(), testNimService)).To(Succeed())

			result, err := reconciler.reconcileNIMService(context.TODO(), testNimService)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))

			// Deployment should be created
			deployment := &appsv1.Deployment{}
			err = client.Get(context.TODO(), namespacedName, deployment)
			Expect(err).NotTo(HaveOccurred())

			// Verify that user-provided NIM_MODEL_NAME takes precedence over default
			container := deployment.Spec.Template.Spec.Containers[0]
			Expect(container.Env).To(ContainElement(corev1.EnvVar{
				Name:  "NIM_MODEL_NAME",
				Value: "/custom-model-path",
			}), "User-provided NIM_MODEL_NAME environment variable should take precedence over default")

			// Verify that other user-provided environment variables are still present
			Expect(container.Env).To(ContainElement(corev1.EnvVar{
				Name:  "CUSTOM_ENV",
				Value: "custom-value",
			}), "Other user-provided environment variables should be present")

			// Verify that the default value is NOT present
			Expect(container.Env).NotTo(ContainElement(corev1.EnvVar{
				Name:  "NIM_MODEL_NAME",
				Value: "/model-store",
			}), "Default NIM_MODEL_NAME value should not be present when user provides custom value")
		})

		It("should respect user-provided NIM_MODEL_NAME environment variable in multi-node Universal NIM deployment", func() {
			// Create a Universal NIM NIMCache
			modelEndpoint := "https://api.ngc.nvidia.com/v2/models/nvidia/nim-llama2-7b/versions/1.0.0"
			universalNimCache := &appsv1alpha1.NIMCache{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-custom-multinode-universal-nimcache",
					Namespace: "default",
				},
				Spec: appsv1alpha1.NIMCacheSpec{
					Source: appsv1alpha1.NIMSource{
						NGC: &appsv1alpha1.NGCSource{
							ModelPuller:   "test-container",
							PullSecret:    "my-secret",
							ModelEndpoint: &modelEndpoint,
						},
					},
					Storage: appsv1alpha1.NIMCacheStorage{
						PVC: appsv1alpha1.PersistentVolumeClaim{
							Create:       ptr.To[bool](true),
							StorageClass: "standard",
							Size:         "1Gi",
							SubPath:      "subPath",
						},
					},
				},
				Status: appsv1alpha1.NIMCacheStatus{
					State: appsv1alpha1.NimCacheStatusReady,
					PVC:   "test-custom-multinode-universal-nimcache-pvc",
					Profiles: []appsv1alpha1.NIMProfile{{
						Name:   "test-profile",
						Config: map[string]string{"tp": "4"}},
					},
				},
			}
			Expect(client.Create(context.TODO(), universalNimCache)).To(Succeed())

			// Create PVC for the Universal NIMCache
			universalPVC := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-custom-multinode-universal-nimcache-pvc",
					Namespace: "default",
				},
			}
			Expect(client.Create(context.TODO(), universalPVC)).To(Succeed())

			// Create a multi-node NIMService instance with custom NIM_MODEL_NAME
			testNimService := &appsv1alpha1.NIMService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-custom-multinode-universal-nimservice",
					Namespace: "default",
				},
				Spec: appsv1alpha1.NIMServiceSpec{
					Env: []corev1.EnvVar{
						{
							Name:  "NIM_MODEL_NAME",
							Value: "/custom-multinode-model-path",
						},
					},
					Image: appsv1alpha1.Image{
						Repository:  "nvcr.io/nvidia/nim-llm",
						Tag:         "v0.1.0",
						PullPolicy:  "IfNotPresent",
						PullSecrets: []string{"ngc-secret"},
					},
					Storage: appsv1alpha1.NIMServiceStorage{
						PVC: appsv1alpha1.PersistentVolumeClaim{
							Name:         "test-pvc",
							StorageClass: "standard",
							Size:         "1Gi",
							Create:       ptr.To[bool](true),
						},
						NIMCache: appsv1alpha1.NIMCacheVolSpec{
							Name: "test-custom-multinode-universal-nimcache",
						},
					},
					Expose: appsv1alpha1.Expose{
						Service: appsv1alpha1.Service{Type: corev1.ServiceTypeLoadBalancer, Port: ptr.To[int32](8123)},
					},
					MultiNode: &appsv1alpha1.NimServiceMultiNodeConfig{
						BackendType: appsv1alpha1.NIMBackendTypeLWS,
						Size:        2,
						GPUSPerPod:  2,
					},
				},
			}

			Expect(client.Create(context.TODO(), testNimService)).To(Succeed())

			result, err := reconciler.reconcileNIMService(context.TODO(), testNimService)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))

			// LeaderWorkerSet should be created instead of Deployment
			lws := &lwsv1.LeaderWorkerSet{}
			lwsNamespacedName := types.NamespacedName{Name: testNimService.GetLWSName(), Namespace: testNimService.Namespace}
			err = client.Get(context.TODO(), lwsNamespacedName, lws)
			Expect(err).NotTo(HaveOccurred())

			// Verify that user-provided NIM_MODEL_NAME takes precedence in both leader and worker containers
			leaderContainer := lws.Spec.LeaderWorkerTemplate.LeaderTemplate.Spec.Containers[0]
			Expect(leaderContainer.Env).To(ContainElement(corev1.EnvVar{
				Name:  "NIM_MODEL_NAME",
				Value: "/custom-multinode-model-path",
			}), "User-provided NIM_MODEL_NAME environment variable should take precedence in leader container")

			workerContainer := lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers[0]
			Expect(workerContainer.Env).To(ContainElement(corev1.EnvVar{
				Name:  "NIM_MODEL_NAME",
				Value: "/custom-multinode-model-path",
			}), "User-provided NIM_MODEL_NAME environment variable should take precedence in worker container")

			// Verify that the default value is NOT present in either container
			Expect(leaderContainer.Env).NotTo(ContainElement(corev1.EnvVar{
				Name:  "NIM_MODEL_NAME",
				Value: "/model-store",
			}), "Default NIM_MODEL_NAME value should not be present in leader container when user provides custom value")

			Expect(workerContainer.Env).NotTo(ContainElement(corev1.EnvVar{
				Name:  "NIM_MODEL_NAME",
				Value: "/model-store",
			}), "Default NIM_MODEL_NAME value should not be present in worker container when user provides custom value")
		})
	})

	Describe("getNIMModelEndpoints", func() {
		var (
			svc     *corev1.Service
			ingress *networkingv1.Ingress
		)
		BeforeEach(func() {
			svc = &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-nimservice",
					Namespace: "default",
				},
				Spec: corev1.ServiceSpec{
					Type:           corev1.ServiceTypeLoadBalancer,
					ClusterIP:      "127.0.0.1",
					LoadBalancerIP: "10.1.1.1",
					Ports: []corev1.ServicePort{
						{
							Port: 8123,
							Name: "service-port",
						},
					},
				},
				Status: corev1.ServiceStatus{
					LoadBalancer: corev1.LoadBalancerStatus{
						Ingress: []corev1.LoadBalancerIngress{{IP: "10.1.1.1"}},
					},
				},
			}
			_ = client.Create(context.TODO(), svc)
			ingress = &networkingv1.Ingress{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-nimservice",
					Namespace: "default",
				},
				Spec: nimService.GetIngressSpec(),
				Status: networkingv1.IngressStatus{
					LoadBalancer: networkingv1.IngressLoadBalancerStatus{
						Ingress: []networkingv1.IngressLoadBalancerIngress{{IP: "10.1.1.2", Hostname: ""}},
					},
				},
			}
			_ = client.Create(context.TODO(), ingress)
		})

		AfterEach(func() {
			_ = client.Delete(context.TODO(), svc)
			_ = client.Delete(context.TODO(), ingress)
		})

		It("should return err when service is missing", func() {
			_ = client.Delete(context.TODO(), svc)
			_, _, err := reconciler.getNIMModelEndpoints(context.TODO(), nimService)
			Expect(err).To(HaveOccurred())
			Expect(errors.IsNotFound(err)).To(BeTrue())
			Expect(err).Should(MatchError("services \"test-nimservice\" not found"))
		})

		It("should return err when ingress is missing", func() {
			_ = client.Delete(context.TODO(), ingress)
			_, _, err := reconciler.getNIMModelEndpoints(context.TODO(), nimService)
			Expect(err).To(HaveOccurred())
			Expect(errors.IsNotFound(err)).To(BeTrue())
			Expect(err).Should(MatchError("ingresses.networking.k8s.io \"test-nimservice\" not found"))
		})

		It("should return only svc endpoints when ingress is disabled", func() {
			nimService.Spec.Expose.Ingress.Enabled = ptr.To(false)
			internal, external, err := reconciler.getNIMModelEndpoints(context.TODO(), nimService)
			Expect(err).ToNot(HaveOccurred())
			Expect(internal).To(Equal("127.0.0.1:8123"))
			Expect(external).To(Equal("10.1.1.1:8123"))
		})

		It("should return hostname from ingress rules as external endpoint", func() {
			internal, external, err := reconciler.getNIMModelEndpoints(context.TODO(), nimService)
			Expect(err).ToNot(HaveOccurred())
			Expect(internal).To(Equal("127.0.0.1:8123"))
			Expect(external).To(Equal("test-nimservice.default.example.com"))
		})

		It("should return ingress loadbalancer ip as external endpoint", func() {
			nimService.Spec.Expose.Ingress.Spec.Rules[0].Host = ""
			ingress.Spec.Rules[0].Host = ""
			_ = client.Update(context.TODO(), ingress)
			internal, external, err := reconciler.getNIMModelEndpoints(context.TODO(), nimService)
			Expect(err).ToNot(HaveOccurred())
			Expect(internal).To(Equal("127.0.0.1:8123"))
			Expect(external).To(Equal("10.1.1.2"))
		})

		It("should return ingress loadbalancer hostname as external endpoint", func() {
			nimService.Spec.Expose.Ingress.Spec.Rules[0].Host = ""
			ingress.Spec.Rules[0].Host = ""
			_ = client.Update(context.TODO(), ingress)
			ingress.Status = networkingv1.IngressStatus{
				LoadBalancer: networkingv1.IngressLoadBalancerStatus{
					Ingress: []networkingv1.IngressLoadBalancerIngress{{IP: "", Hostname: "test-nimservice.default.example.com"}},
				},
			}
			_ = client.Status().Update(context.TODO(), ingress)
			internal, external, err := reconciler.getNIMModelEndpoints(context.TODO(), nimService)
			Expect(err).ToNot(HaveOccurred())
			Expect(internal).To(Equal("127.0.0.1:8123"))
			Expect(external).To(Equal("test-nimservice.default.example.com"))
		})
	})

	Describe("getNIMCacheProfile", func() {
		It("should return nil when NIMCache is not used", func() {
			nimService.Spec.Storage.NIMCache.Name = ""
			profile, err := reconciler.getNIMCacheProfile(context.TODO(), nimService, "some-profile")
			Expect(err).To(BeNil())
			Expect(profile).To(BeNil())
		})

		It("should return an error when NIMCache is not found", func() {
			nimService.Spec.Storage.NIMCache.Name = "non-existent-cache"
			_, err := reconciler.getNIMCacheProfile(context.TODO(), nimService, "some-profile")
			Expect(err).To(HaveOccurred())
		})

		It("should return an error when NIMCache is not ready", func() {
			nimService.Spec.Storage.NIMCache.Name = "test-nimcache"
			nimCache.Status = appsv1alpha1.NIMCacheStatus{
				State: appsv1alpha1.NimCacheStatusPending,
			}

			// Update nimCache status
			Expect(reconciler.Client.Status().Update(context.TODO(), nimCache)).To(Succeed())
			_, err := reconciler.getNIMCacheProfile(context.TODO(), nimService, "test-profile")
			Expect(err).To(HaveOccurred())
		})

		It("should return nil when NIMCache profile is not found", func() {
			nimService.Spec.Storage.NIMCache.Name = "test-nimcache"
			profile, err := reconciler.getNIMCacheProfile(context.TODO(), nimService, "non-existent-profile")
			Expect(err).To(BeNil())
			Expect(profile).To(BeNil())
		})

		It("should return the profile if found in NIMCache", func() {
			nimService.Spec.Storage.NIMCache.Name = "test-nimcache"
			profile, err := reconciler.getNIMCacheProfile(context.TODO(), nimService, "test-profile")
			Expect(err).To(BeNil())
			Expect(profile.Name).To(Equal("test-profile"))
		})
	})

	Describe("getTensorParallelismByProfile", func() {
		It("should return tensor parallelism value if exists", func() {
			profile := &appsv1alpha1.NIMProfile{
				Name:   "test-profile",
				Config: map[string]string{"tp": "4"},
			}

			tensorParallelism, err := utils.GetTensorParallelismByProfileTags(profile.Config)
			Expect(err).To(BeNil())
			Expect(tensorParallelism).To(Equal("4"))
		})

		It("should return empty string if tensor parallelism does not exist", func() {
			profile := &appsv1alpha1.NIMProfile{
				Name:   "test-profile",
				Config: map[string]string{},
			}
			tensorParallelism, err := utils.GetTensorParallelismByProfileTags(profile.Config)
			Expect(err).To(BeNil())
			Expect(tensorParallelism).To(BeEmpty())
		})
	})

	Describe("addGPUResources", func() {
		It("should not provide GPU resources if user has already provided them", func() {
			profile := &appsv1alpha1.NIMProfile{
				Name:   "test-profile",
				Config: map[string]string{"tp": "4"},
			}

			// Initialize deployment params with user-provided GPU resources
			nimService.Spec.Resources = &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceName("nvidia.com/gpu"): resource.MustParse("8"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceName("nvidia.com/gpu"): resource.MustParse("8"),
				},
			}

			resources, err := reconciler.addGPUResources(context.TODO(), nimService, profile)
			Expect(err).ToNot(HaveOccurred())
			Expect(resources).ToNot(BeNil())

			// Ensure the user-provided GPU resources (8) are retained and not overridden
			Expect(resources.Requests).To(HaveKeyWithValue(corev1.ResourceName("nvidia.com/gpu"), resource.MustParse("8")))
			Expect(resources.Limits).To(HaveKeyWithValue(corev1.ResourceName("nvidia.com/gpu"), resource.MustParse("8")))
		})

		It("should assign GPU resources when tensor parallelism is provided", func() {
			profile := &appsv1alpha1.NIMProfile{
				Name:   "test-profile",
				Config: map[string]string{"tp": "4"},
			}

			resources, err := reconciler.addGPUResources(context.TODO(), nimService, profile)
			Expect(err).ToNot(HaveOccurred())
			Expect(resources).ToNot(BeNil())

			Expect(resources.Requests).To(HaveKeyWithValue(corev1.ResourceName("nvidia.com/gpu"), resource.MustParse("4")))
			Expect(resources.Limits).To(HaveKeyWithValue(corev1.ResourceName("nvidia.com/gpu"), resource.MustParse("4")))
		})

		It("should respect non GPU resources after adding GPU resources", func() {
			profile := &appsv1alpha1.NIMProfile{
				Name:   "test-profile",
				Config: map[string]string{},
			}

			resources, err := reconciler.addGPUResources(context.TODO(), nimService, profile)
			Expect(err).ToNot(HaveOccurred())
			Expect(resources).ToNot(BeNil())

			Expect(resources.Requests).To(HaveKeyWithValue(corev1.ResourceCPU, resource.MustParse("250m")))
			Expect(resources.Requests).To(HaveKeyWithValue(corev1.ResourceMemory, resource.MustParse("64Mi")))
			Expect(resources.Limits).To(HaveKeyWithValue(corev1.ResourceCPU, resource.MustParse("500m")))
			Expect(resources.Limits).To(HaveKeyWithValue(corev1.ResourceMemory, resource.MustParse("128Mi")))
		})

		It("should assign 1 GPU resource if tensor parallelism is not provided", func() {
			profile := &appsv1alpha1.NIMProfile{
				Name:   "test-profile",
				Config: map[string]string{},
			}

			resources, err := reconciler.addGPUResources(context.TODO(), nimService, profile)
			Expect(err).ToNot(HaveOccurred())
			Expect(resources).ToNot(BeNil())

			Expect(resources.Requests).To(HaveKeyWithValue(corev1.ResourceName("nvidia.com/gpu"), resource.MustParse("1")))
			Expect(resources.Limits).To(HaveKeyWithValue(corev1.ResourceName("nvidia.com/gpu"), resource.MustParse("1")))
		})

		It("should assign GPU resource equal to multiNode.GPUSPerPod in multi-node deployment", func() {
			nimService.Spec.MultiNode = &appsv1alpha1.NimServiceMultiNodeConfig{
				GPUSPerPod: 2,
			}
			profile := &appsv1alpha1.NIMProfile{
				Name:   "test-profile",
				Config: map[string]string{"tp": "4"},
			}

			resources, err := reconciler.addGPUResources(context.TODO(), nimService, profile)
			Expect(err).ToNot(HaveOccurred())
			Expect(resources).ToNot(BeNil())

			Expect(resources.Requests).To(HaveKeyWithValue(corev1.ResourceName("nvidia.com/gpu"), resource.MustParse("2")))
			Expect(resources.Limits).To(HaveKeyWithValue(corev1.ResourceName("nvidia.com/gpu"), resource.MustParse("2")))
		})

		It("should return an error if tensor parallelism cannot be parsed", func() {
			profile := &appsv1alpha1.NIMProfile{
				Name:   "test-profile",
				Config: map[string]string{"tp": "invalid"},
			}

			_, err := reconciler.addGPUResources(context.TODO(), nimService, profile)
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("Reconcile NIMService with Proxy Setting", func() {

		BeforeEach(func() {
			nimService.Spec.Proxy = &appsv1alpha1.ProxySpec{
				HttpProxy:     "http://proxy:1000",
				HttpsProxy:    "https://proxy:1000",
				NoProxy:       "http://no-proxy",
				CertConfigMap: "custom-ca-configmap",
			}

		})

		It("should create deployment with appropriate parameters", func() {
			namespacedName := types.NamespacedName{Name: nimService.Name, Namespace: nimService.Namespace}
			err := client.Create(context.TODO(), nimService)
			Expect(err).NotTo(HaveOccurred())

			result, err := reconciler.reconcileNIMService(context.TODO(), nimService)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))

			// Deployment should be created
			deployment := &appsv1.Deployment{}
			err = client.Get(context.TODO(), namespacedName, deployment)
			Expect(err).NotTo(HaveOccurred())
			Expect(deployment.Name).To(Equal(nimService.GetName()))
			Expect(deployment.Namespace).To(Equal(nimService.GetNamespace()))
			Expect(deployment.Annotations["annotation-key"]).To(Equal("annotation-value"))
			Expect(deployment.Spec.Template.Spec.Containers[0].Name).To(Equal(nimService.GetContainerName()))
			Expect(deployment.Spec.Template.Spec.Containers[0].Image).To(Equal(nimService.GetImage()))
			Expect(deployment.Spec.Template.Spec.Containers[0].ReadinessProbe).To(Equal(nimService.Spec.ReadinessProbe.Probe))
			Expect(deployment.Spec.Template.Spec.Containers[0].LivenessProbe).To(Equal(nimService.Spec.LivenessProbe.Probe))
			Expect(deployment.Spec.Template.Spec.Containers[0].StartupProbe).To(Equal(nimService.Spec.StartupProbe.Probe))
			Expect(*deployment.Spec.Template.Spec.RuntimeClassName).To(Equal(nimService.Spec.RuntimeClassName))

			// Verify CertConfig volume and mounts
			Expect(deployment.Spec.Template.Spec.Volumes).To(ContainElement(
				corev1.Volume{
					Name: "custom-ca",
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "custom-ca-configmap",
							},
						},
					},
				},
			))

			Expect(deployment.Spec.Template.Spec.Volumes).To(ContainElement(
				corev1.Volume{
					Name: "ca-cert-volume",
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{},
					},
				},
			))

			Expect(deployment.Spec.Template.Spec.Containers[0].VolumeMounts).To(ContainElement(
				corev1.VolumeMount{
					Name:      "ca-cert-volume",
					MountPath: "/etc/ssl",
				},
			))

			Expect(deployment.Spec.Template.Spec.InitContainers[0].VolumeMounts).To(ContainElement(
				corev1.VolumeMount{
					Name:      "ca-cert-volume",
					MountPath: "/ca-certs",
				},
			))

			Expect(deployment.Spec.Template.Spec.InitContainers[0].VolumeMounts).To(ContainElement(
				corev1.VolumeMount{
					Name:      "custom-ca",
					MountPath: "/custom",
				},
			))
			Expect(deployment.Spec.Template.Spec.InitContainers[0].Command).To(ContainElements(k8sutil.GetUpdateCaCertInitContainerCommand()))
			Expect(deployment.Spec.Template.Spec.InitContainers[0].SecurityContext).To(Equal(k8sutil.GetUpdateCaCertInitContainerSecurityContext()))

			// Expected environment variables
			expectedEnvs := map[string]string{
				"HTTP_PROXY":             "http://proxy:1000",
				"HTTPS_PROXY":            "https://proxy:1000",
				"NO_PROXY":               "http://no-proxy",
				"http_proxy":             "http://proxy:1000",
				"https_proxy":            "https://proxy:1000",
				"no_proxy":               "http://no-proxy",
				"NIM_SDK_USE_NATIVE_TLS": "1",
			}

			// Verify each custom environment variable
			for key, value := range expectedEnvs {
				var found bool
				for _, envVar := range deployment.Spec.Template.Spec.Containers[0].Env {
					if envVar.Name == key && envVar.Value == value {
						found = true
						break
					}
				}
				Expect(found).To(BeTrue(), "Expected environment variable %s=%s not found", key, value)
			}

		})
	})
})
