// Copyright (c) 2022 SAP SE or an SAP affiliate company. All rights reserved. This file is licensed under the Apache Software License, v. 2 except as noted otherwise in the LICENSE file
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package vpa_test

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	gardencorev1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	resourcesv1alpha1 "github.com/gardener/gardener/pkg/apis/resources/v1alpha1"
	"github.com/gardener/gardener/pkg/client/kubernetes"
	"github.com/gardener/gardener/pkg/operation/botanist/component"
	. "github.com/gardener/gardener/pkg/operation/botanist/component/vpa"
	gutil "github.com/gardener/gardener/pkg/utils/gardener"
	kutil "github.com/gardener/gardener/pkg/utils/kubernetes"
	"github.com/gardener/gardener/pkg/utils/retry"
	retryfake "github.com/gardener/gardener/pkg/utils/retry/fake"
	secretsmanager "github.com/gardener/gardener/pkg/utils/secrets/manager"
	fakesecretsmanager "github.com/gardener/gardener/pkg/utils/secrets/manager/fake"
	"github.com/gardener/gardener/pkg/utils/test"
	. "github.com/gardener/gardener/pkg/utils/test/matchers"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/runtime/serializer/json"
	"k8s.io/apimachinery/pkg/util/intstr"
	vpaautoscalingv1 "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var _ = Describe("VPA", func() {
	var (
		ctx = context.TODO()

		namespace    = "some-namespace"
		secretNameCA = "ca"

		genericTokenKubeconfigSecretName = "generic-token-kubeconfig"
		pathGenericKubeconfig            = "/var/run/secrets/gardener.cloud/shoot/generic-kubeconfig/kubeconfig"

		values = Values{
			SecretNameServerCA: secretNameCA,
		}

		c         client.Client
		sm        secretsmanager.Interface
		component component.DeployWaiter

		imageAdmissionController = "some-image:for-admission-controller"
		imageExporter            = "some-image:for-exporter"
		imageRecommender         = "some-image:for-recommender"
		imageUpdater             = "some-image:for-updater"

		valuesAdmissionController ValuesAdmissionController
		valuesExporter            ValuesExporter
		valuesRecommender         ValuesRecommender
		valuesUpdater             ValuesUpdater

		vpaUpdateModeAuto   = vpaautoscalingv1.UpdateModeAuto
		vpaControlledValues = vpaautoscalingv1.ContainerControlledValuesRequestsOnly

		networkPolicyProtocol = corev1.ProtocolTCP
		networkPolicyPort     = intstr.FromInt(10250)

		webhookFailurePolicy      = admissionregistrationv1.Ignore
		webhookMatchPolicy        = admissionregistrationv1.Exact
		webhookReinvocationPolicy = admissionregistrationv1.NeverReinvocationPolicy
		webhookSideEffects        = admissionregistrationv1.SideEffectClassNone
		webhookScope              = admissionregistrationv1.AllScopes

		managedResourceName   string
		managedResource       *resourcesv1alpha1.ManagedResource
		managedResourceSecret *corev1.Secret

		serviceExporter            *corev1.Service
		serviceAccountExporter     *corev1.ServiceAccount
		clusterRoleExporter        *rbacv1.ClusterRole
		clusterRoleBindingExporter *rbacv1.ClusterRoleBinding
		deploymentExporter         *appsv1.Deployment
		vpaExporter                *vpaautoscalingv1.VerticalPodAutoscaler

		serviceAccountUpdater     *corev1.ServiceAccount
		clusterRoleUpdater        *rbacv1.ClusterRole
		clusterRoleBindingUpdater *rbacv1.ClusterRoleBinding
		shootAccessSecretUpdater  *corev1.Secret
		deploymentUpdaterFor      func(bool, *metav1.Duration, *metav1.Duration, *int32, *float64, *float64) *appsv1.Deployment
		vpaUpdater                *vpaautoscalingv1.VerticalPodAutoscaler

		serviceAccountRecommender                    *corev1.ServiceAccount
		clusterRoleRecommenderMetricsReader          *rbacv1.ClusterRole
		clusterRoleBindingRecommenderMetricsReader   *rbacv1.ClusterRoleBinding
		clusterRoleRecommenderCheckpointActor        *rbacv1.ClusterRole
		clusterRoleBindingRecommenderCheckpointActor *rbacv1.ClusterRoleBinding
		shootAccessSecretRecommender                 *corev1.Secret
		deploymentRecommenderFor                     func(bool, *metav1.Duration, *float64) *appsv1.Deployment
		vpaRecommender                               *vpaautoscalingv1.VerticalPodAutoscaler

		serviceAccountAdmissionController     *corev1.ServiceAccount
		clusterRoleAdmissionController        *rbacv1.ClusterRole
		clusterRoleBindingAdmissionController *rbacv1.ClusterRoleBinding
		shootAccessSecretAdmissionController  *corev1.Secret
		serviceAdmissionController            *corev1.Service
		networkPolicyAdmissionController      *networkingv1.NetworkPolicy
		deploymentAdmissionControllerFor      func(bool) *appsv1.Deployment
		vpaAdmissionController                *vpaautoscalingv1.VerticalPodAutoscaler

		clusterRoleGeneralActor               *rbacv1.ClusterRole
		clusterRoleBindingGeneralActor        *rbacv1.ClusterRoleBinding
		clusterRoleGeneralTargetReader        *rbacv1.ClusterRole
		clusterRoleBindingGeneralTargetReader *rbacv1.ClusterRoleBinding
		mutatingWebhookConfiguration          *admissionregistrationv1.MutatingWebhookConfiguration
	)

	BeforeEach(func() {
		c = fakeclient.NewClientBuilder().WithScheme(kubernetes.SeedScheme).Build()
		sm = fakesecretsmanager.New(c, namespace)

		valuesAdmissionController = ValuesAdmissionController{
			Image:    imageAdmissionController,
			Replicas: 4,
		}
		valuesExporter = ValuesExporter{
			Image: imageExporter,
		}
		valuesRecommender = ValuesRecommender{
			Image:    imageRecommender,
			Replicas: 2,
		}
		valuesUpdater = ValuesUpdater{
			Image:    imageUpdater,
			Replicas: 3,
		}

		component = New(c, namespace, sm, values)
		managedResourceName = ""

		By("creating secrets managed outside of this package for whose secretsmanager.Get() will be called")
		Expect(c.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "ca", Namespace: namespace}})).To(Succeed())
		Expect(c.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "generic-token-kubeconfig", Namespace: namespace}})).To(Succeed())

		serviceExporter = &corev1.Service{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "v1",
				Kind:       "Service",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "vpa-exporter",
				Namespace: namespace,
				Labels: map[string]string{
					"app":                 "vpa-exporter",
					"gardener.cloud/role": "vpa",
				},
			},
			Spec: corev1.ServiceSpec{
				Type:            corev1.ServiceTypeClusterIP,
				SessionAffinity: corev1.ServiceAffinityNone,
				Selector:        map[string]string{"app": "vpa-exporter"},
				Ports: []corev1.ServicePort{{
					Name:       "metrics",
					Protocol:   corev1.ProtocolTCP,
					Port:       9570,
					TargetPort: intstr.FromInt(9570),
				}},
			},
		}
		serviceAccountExporter = &corev1.ServiceAccount{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "v1",
				Kind:       "ServiceAccount",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "vpa-exporter",
				Namespace: namespace,
			},
			AutomountServiceAccountToken: pointer.Bool(false),
		}
		clusterRoleExporter = &rbacv1.ClusterRole{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "rbac.authorization.k8s.io/v1",
				Kind:       "ClusterRole",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: "gardener.cloud:vpa:target:exporter",
				Labels: map[string]string{
					"gardener.cloud/role": "vpa",
				},
			},
			Rules: []rbacv1.PolicyRule{{
				APIGroups: []string{"autoscaling.k8s.io"},
				Resources: []string{"verticalpodautoscalers"},
				Verbs:     []string{"get", "watch", "list"},
			}},
		}
		clusterRoleBindingExporter = &rbacv1.ClusterRoleBinding{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "rbac.authorization.k8s.io/v1",
				Kind:       "ClusterRoleBinding",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: "gardener.cloud:vpa:target:exporter",
				Labels: map[string]string{
					"gardener.cloud/role": "vpa",
				},
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "ClusterRole",
				Name:     "gardener.cloud:vpa:target:exporter",
			},
			Subjects: []rbacv1.Subject{{
				Kind:      "ServiceAccount",
				Name:      "vpa-exporter",
				Namespace: namespace,
			}},
		}
		deploymentExporter = &appsv1.Deployment{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "vpa-exporter",
				Namespace: namespace,
				Labels: map[string]string{
					"app":                 "vpa-exporter",
					"gardener.cloud/role": "vpa",
				},
			},
			Spec: appsv1.DeploymentSpec{
				Replicas:             pointer.Int32(1),
				RevisionHistoryLimit: pointer.Int32(2),
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"app": "vpa-exporter",
					},
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app":                 "vpa-exporter",
							"gardener.cloud/role": "vpa",
						},
					},
					Spec: corev1.PodSpec{
						ServiceAccountName: "vpa-exporter",
						Containers: []corev1.Container{{
							Name:            "exporter",
							Image:           imageExporter,
							ImagePullPolicy: corev1.PullIfNotPresent,
							Command: []string{
								"/usr/local/bin/vpa-exporter",
								"--port=9570",
							},
							Ports: []corev1.ContainerPort{{
								Name:          "metrics",
								ContainerPort: 9570,
								Protocol:      corev1.ProtocolTCP,
							}},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("30m"),
									corev1.ResourceMemory: resource.MustParse("50Mi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceMemory: resource.MustParse("200Mi"),
								},
							},
						}},
					},
				},
			},
		}
		vpaExporter = &vpaautoscalingv1.VerticalPodAutoscaler{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "autoscaling.k8s.io/v1",
				Kind:       "VerticalPodAutoscaler",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "vpa-exporter-vpa",
				Namespace: namespace,
			},
			Spec: vpaautoscalingv1.VerticalPodAutoscalerSpec{
				TargetRef: &autoscalingv1.CrossVersionObjectReference{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Name:       "vpa-exporter",
				},
				UpdatePolicy: &vpaautoscalingv1.PodUpdatePolicy{UpdateMode: &vpaUpdateModeAuto},
				ResourcePolicy: &vpaautoscalingv1.PodResourcePolicy{
					ContainerPolicies: []vpaautoscalingv1.ContainerResourcePolicy{
						{
							ContainerName: "*",
							MinAllowed: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("30m"),
								corev1.ResourceMemory: resource.MustParse("50Mi"),
							},
						},
					},
				},
			},
		}

		serviceAccountUpdater = &corev1.ServiceAccount{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "v1",
				Kind:       "ServiceAccount",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "vpa-updater",
				Namespace: namespace,
				Labels: map[string]string{
					"gardener.cloud/role": "vpa",
				},
			},
			AutomountServiceAccountToken: pointer.Bool(false),
		}
		clusterRoleUpdater = &rbacv1.ClusterRole{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "rbac.authorization.k8s.io/v1",
				Kind:       "ClusterRole",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: "gardener.cloud:vpa:target:evictioner",
				Labels: map[string]string{
					"gardener.cloud/role": "vpa",
				},
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{"apps", "extensions"},
					Resources: []string{"replicasets"},
					Verbs:     []string{"get"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"pods/eviction"},
					Verbs:     []string{"create"},
				},
			},
		}
		clusterRoleBindingUpdater = &rbacv1.ClusterRoleBinding{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "rbac.authorization.k8s.io/v1",
				Kind:       "ClusterRoleBinding",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: "gardener.cloud:vpa:target:evictioner",
				Labels: map[string]string{
					"gardener.cloud/role": "vpa",
				},
				Annotations: map[string]string{
					"resources.gardener.cloud/delete-on-invalid-update": "true",
				},
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "ClusterRole",
				Name:     "gardener.cloud:vpa:target:evictioner",
			},
			Subjects: []rbacv1.Subject{{
				Kind:      "ServiceAccount",
				Name:      "vpa-updater",
				Namespace: namespace,
			}},
		}
		shootAccessSecretUpdater = &corev1.Secret{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "v1",
				Kind:       "Secret",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "shoot-access-vpa-updater",
				Namespace: namespace,
				Labels: map[string]string{
					"resources.gardener.cloud/purpose": "token-requestor",
				},
				Annotations: map[string]string{
					"serviceaccount.resources.gardener.cloud/name":      "vpa-updater",
					"serviceaccount.resources.gardener.cloud/namespace": "kube-system",
				},
			},
			Type: corev1.SecretTypeOpaque,
		}
		deploymentUpdaterFor = func(
			withServiceAccount bool,
			interval *metav1.Duration,
			evictAfterOOMThreshold *metav1.Duration,
			evictionRateBurst *int32,
			evictionRateLimit *float64,
			evictionTolerance *float64,
		) *appsv1.Deployment {
			var (
				flagEvictionToleranceValue      = "0.500000"
				flagEvictionRateBurstValue      = "1"
				flagEvictionRateLimitValue      = "-1.000000"
				flagEvictAfterOomThresholdValue = "10m0s"
				flagUpdaterIntervalValue        = "1m0s"
			)

			if interval != nil {
				flagUpdaterIntervalValue = interval.Duration.String()
			}
			if evictAfterOOMThreshold != nil {
				flagEvictAfterOomThresholdValue = evictAfterOOMThreshold.Duration.String()
			}
			if evictionRateBurst != nil {
				flagEvictionRateBurstValue = fmt.Sprintf("%d", *evictionRateBurst)
			}
			if evictionRateLimit != nil {
				flagEvictionRateLimitValue = fmt.Sprintf("%f", *evictionRateLimit)
			}
			if evictionTolerance != nil {
				flagEvictionToleranceValue = fmt.Sprintf("%f", *evictionTolerance)
			}

			obj := &appsv1.Deployment{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "vpa-updater",
					Namespace: namespace,
					Labels: map[string]string{
						"app":                 "vpa-updater",
						"gardener.cloud/role": "vpa",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Replicas:             pointer.Int32(3),
					RevisionHistoryLimit: pointer.Int32(2),
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"app": "vpa-updater",
						},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								"app":                 "vpa-updater",
								"gardener.cloud/role": "vpa",
								"networking.gardener.cloud/from-prometheus":    "allowed",
								"networking.gardener.cloud/to-dns":             "allowed",
								"networking.gardener.cloud/to-shoot-apiserver": "allowed",
							},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{
								Name:            "updater",
								Image:           imageUpdater,
								ImagePullPolicy: corev1.PullIfNotPresent,
								Command:         []string{"./updater"},
								Args: []string{
									"--min-replicas=1",
									fmt.Sprintf("--eviction-tolerance=%s", flagEvictionToleranceValue),
									fmt.Sprintf("--eviction-rate-burst=%s", flagEvictionRateBurstValue),
									fmt.Sprintf("--eviction-rate-limit=%s", flagEvictionRateLimitValue),
									fmt.Sprintf("--evict-after-oom-threshold=%s", flagEvictAfterOomThresholdValue),
									fmt.Sprintf("--updater-interval=%s", flagUpdaterIntervalValue),
									"--stderrthreshold=info",
									"--v=2",
								},
								Ports: []corev1.ContainerPort{
									{
										Name:          "server",
										ContainerPort: 8080,
									},
									{
										Name:          "metrics",
										ContainerPort: 8943,
									},
								},
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse("30m"),
										corev1.ResourceMemory: resource.MustParse("200Mi"),
									},
									Limits: corev1.ResourceList{
										corev1.ResourceMemory: resource.MustParse("4Gi"),
									},
								},
							}},
						},
					},
				},
			}

			if withServiceAccount {
				obj.Spec.Template.Spec.ServiceAccountName = serviceAccountUpdater.Name
				obj.Spec.Template.Spec.Containers[0].Env = append(obj.Spec.Template.Spec.Containers[0].Env, corev1.EnvVar{
					Name: "NAMESPACE",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.namespace",
						},
					},
				})
			} else {
				obj.Labels["gardener.cloud/role"] = "controlplane"
				obj.Spec.Template.Spec.AutomountServiceAccountToken = pointer.Bool(false)
				obj.Spec.Template.Spec.Containers[0].Command = append(obj.Spec.Template.Spec.Containers[0].Command, "--kubeconfig="+pathGenericKubeconfig)

				Expect(gutil.InjectGenericKubeconfig(obj, genericTokenKubeconfigSecretName, shootAccessSecretUpdater.Name)).To(Succeed())
			}

			return obj
		}
		vpaUpdater = &vpaautoscalingv1.VerticalPodAutoscaler{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "autoscaling.k8s.io/v1",
				Kind:       "VerticalPodAutoscaler",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "vpa-updater",
				Namespace: namespace,
			},
			Spec: vpaautoscalingv1.VerticalPodAutoscalerSpec{
				TargetRef: &autoscalingv1.CrossVersionObjectReference{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Name:       "vpa-updater",
				},
				UpdatePolicy: &vpaautoscalingv1.PodUpdatePolicy{UpdateMode: &vpaUpdateModeAuto},
				ResourcePolicy: &vpaautoscalingv1.PodResourcePolicy{
					ContainerPolicies: []vpaautoscalingv1.ContainerResourcePolicy{
						{
							ContainerName:    "*",
							ControlledValues: &vpaControlledValues,
							MinAllowed: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("10m"),
								corev1.ResourceMemory: resource.MustParse("50Mi"),
							},
						},
					},
				},
			},
		}

		serviceAccountRecommender = &corev1.ServiceAccount{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "v1",
				Kind:       "ServiceAccount",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "vpa-recommender",
				Namespace: namespace,
				Labels: map[string]string{
					"gardener.cloud/role": "vpa",
				},
			},
			AutomountServiceAccountToken: pointer.Bool(false),
		}
		clusterRoleRecommenderMetricsReader = &rbacv1.ClusterRole{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "rbac.authorization.k8s.io/v1",
				Kind:       "ClusterRole",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: "gardener.cloud:vpa:target:metrics-reader",
				Labels: map[string]string{
					"gardener.cloud/role": "vpa",
				},
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{"metrics.k8s.io"},
					Resources: []string{"pods"},
					Verbs:     []string{"get", "list"},
				},
			},
		}
		clusterRoleBindingRecommenderMetricsReader = &rbacv1.ClusterRoleBinding{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "rbac.authorization.k8s.io/v1",
				Kind:       "ClusterRoleBinding",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: "gardener.cloud:vpa:target:metrics-reader",
				Labels: map[string]string{
					"gardener.cloud/role": "vpa",
				},
				Annotations: map[string]string{
					"resources.gardener.cloud/delete-on-invalid-update": "true",
				},
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "ClusterRole",
				Name:     "gardener.cloud:vpa:target:metrics-reader",
			},
			Subjects: []rbacv1.Subject{{
				Kind:      "ServiceAccount",
				Name:      "vpa-recommender",
				Namespace: namespace,
			}},
		}
		clusterRoleRecommenderCheckpointActor = &rbacv1.ClusterRole{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "rbac.authorization.k8s.io/v1",
				Kind:       "ClusterRole",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: "gardener.cloud:vpa:target:checkpoint-actor",
				Labels: map[string]string{
					"gardener.cloud/role": "vpa",
				},
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{"poc.autoscaling.k8s.io"},
					Resources: []string{"verticalpodautoscalercheckpoints"},
					Verbs:     []string{"get", "list", "watch", "create", "patch", "delete"},
				},
				{
					APIGroups: []string{"autoscaling.k8s.io"},
					Resources: []string{"verticalpodautoscalercheckpoints"},
					Verbs:     []string{"get", "list", "watch", "create", "patch", "delete"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"namespaces"},
					Verbs:     []string{"get", "list"},
				},
			},
		}
		clusterRoleBindingRecommenderCheckpointActor = &rbacv1.ClusterRoleBinding{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "rbac.authorization.k8s.io/v1",
				Kind:       "ClusterRoleBinding",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: "gardener.cloud:vpa:target:checkpoint-actor",
				Labels: map[string]string{
					"gardener.cloud/role": "vpa",
				},
				Annotations: map[string]string{
					"resources.gardener.cloud/delete-on-invalid-update": "true",
				},
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "ClusterRole",
				Name:     "gardener.cloud:vpa:target:checkpoint-actor",
			},
			Subjects: []rbacv1.Subject{{
				Kind:      "ServiceAccount",
				Name:      "vpa-recommender",
				Namespace: namespace,
			}},
		}
		shootAccessSecretRecommender = &corev1.Secret{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "v1",
				Kind:       "Secret",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "shoot-access-vpa-recommender",
				Namespace: namespace,
				Labels: map[string]string{
					"resources.gardener.cloud/purpose": "token-requestor",
				},
				Annotations: map[string]string{
					"serviceaccount.resources.gardener.cloud/name":      "vpa-recommender",
					"serviceaccount.resources.gardener.cloud/namespace": "kube-system",
				},
			},
			Type: corev1.SecretTypeOpaque,
		}
		deploymentRecommenderFor = func(
			withServiceAccount bool,
			interval *metav1.Duration,
			recommendationMarginFraction *float64,
		) *appsv1.Deployment {
			var (
				flagRecommendationMarginFraction = "0.150000"
				flagRecommenderIntervalValue     = "1m0s"
			)

			if interval != nil {
				flagRecommenderIntervalValue = interval.Duration.String()
			}
			if recommendationMarginFraction != nil {
				flagRecommendationMarginFraction = fmt.Sprintf("%f", *recommendationMarginFraction)
			}

			obj := &appsv1.Deployment{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "vpa-recommender",
					Namespace: namespace,
					Labels: map[string]string{
						"app":                 "vpa-recommender",
						"gardener.cloud/role": "vpa",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Replicas:             pointer.Int32(2),
					RevisionHistoryLimit: pointer.Int32(2),
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"app": "vpa-recommender",
						},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								"app":                 "vpa-recommender",
								"gardener.cloud/role": "vpa",
								"networking.gardener.cloud/from-prometheus":    "allowed",
								"networking.gardener.cloud/to-dns":             "allowed",
								"networking.gardener.cloud/to-shoot-apiserver": "allowed",
							},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{
								Name:            "recommender",
								Image:           imageRecommender,
								ImagePullPolicy: corev1.PullIfNotPresent,
								Command:         []string{"./recommender"},
								Args: []string{
									"--v=3",
									"--stderrthreshold=info",
									"--pod-recommendation-min-cpu-millicores=5",
									"--pod-recommendation-min-memory-mb=10",
									fmt.Sprintf("--recommendation-margin-fraction=%s", flagRecommendationMarginFraction),
									fmt.Sprintf("--recommender-interval=%s", flagRecommenderIntervalValue),
									"--kube-api-qps=100",
									"--kube-api-burst=120",
								},
								Ports: []corev1.ContainerPort{
									{
										Name:          "server",
										ContainerPort: 8080,
									},
									{
										Name:          "metrics",
										ContainerPort: 8942,
									},
								},
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse("30m"),
										corev1.ResourceMemory: resource.MustParse("200Mi"),
									},
									Limits: corev1.ResourceList{
										corev1.ResourceMemory: resource.MustParse("4Gi"),
									},
								},
							}},
						},
					},
				},
			}

			if withServiceAccount {
				obj.Spec.Template.Spec.ServiceAccountName = serviceAccountRecommender.Name
			} else {
				obj.Labels["gardener.cloud/role"] = "controlplane"
				obj.Spec.Template.Spec.AutomountServiceAccountToken = pointer.Bool(false)
				obj.Spec.Template.Spec.Containers[0].Command = append(obj.Spec.Template.Spec.Containers[0].Command, "--kubeconfig="+pathGenericKubeconfig)

				Expect(gutil.InjectGenericKubeconfig(obj, genericTokenKubeconfigSecretName, shootAccessSecretRecommender.Name)).To(Succeed())
			}

			return obj
		}
		vpaRecommender = &vpaautoscalingv1.VerticalPodAutoscaler{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "autoscaling.k8s.io/v1",
				Kind:       "VerticalPodAutoscaler",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "vpa-recommender",
				Namespace: namespace,
			},
			Spec: vpaautoscalingv1.VerticalPodAutoscalerSpec{
				TargetRef: &autoscalingv1.CrossVersionObjectReference{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Name:       "vpa-recommender",
				},
				UpdatePolicy: &vpaautoscalingv1.PodUpdatePolicy{UpdateMode: &vpaUpdateModeAuto},
				ResourcePolicy: &vpaautoscalingv1.PodResourcePolicy{
					ContainerPolicies: []vpaautoscalingv1.ContainerResourcePolicy{
						{
							ContainerName:    "*",
							ControlledValues: &vpaControlledValues,
							MinAllowed: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("10m"),
								corev1.ResourceMemory: resource.MustParse("40Mi"),
							},
						},
					},
				},
			},
		}

		serviceAccountAdmissionController = &corev1.ServiceAccount{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "v1",
				Kind:       "ServiceAccount",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "vpa-admission-controller",
				Namespace: namespace,
				Labels: map[string]string{
					"gardener.cloud/role": "vpa",
				},
			},
			AutomountServiceAccountToken: pointer.Bool(false),
		}
		clusterRoleAdmissionController = &rbacv1.ClusterRole{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "rbac.authorization.k8s.io/v1",
				Kind:       "ClusterRole",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: "gardener.cloud:vpa:target:admission-controller",
				Labels: map[string]string{
					"gardener.cloud/role": "vpa",
				},
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"pods", "configmaps", "nodes", "limitranges"},
					Verbs:     []string{"get", "list", "watch"},
				},
				{
					APIGroups: []string{"admissionregistration.k8s.io"},
					Resources: []string{"mutatingwebhookconfigurations"},
					Verbs:     []string{"create", "delete", "get", "list"},
				},
				{
					APIGroups: []string{"poc.autoscaling.k8s.io"},
					Resources: []string{"verticalpodautoscalers"},
					Verbs:     []string{"get", "list", "watch"},
				},
				{
					APIGroups: []string{"autoscaling.k8s.io"},
					Resources: []string{"verticalpodautoscalers"},
					Verbs:     []string{"get", "list", "watch"},
				},
				{
					APIGroups: []string{"coordination.k8s.io"},
					Resources: []string{"leases"},
					Verbs:     []string{"create", "update", "get", "list", "watch"},
				},
			},
		}
		clusterRoleBindingAdmissionController = &rbacv1.ClusterRoleBinding{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "rbac.authorization.k8s.io/v1",
				Kind:       "ClusterRoleBinding",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: "gardener.cloud:vpa:target:admission-controller",
				Labels: map[string]string{
					"gardener.cloud/role": "vpa",
				},
				Annotations: map[string]string{
					"resources.gardener.cloud/delete-on-invalid-update": "true",
				},
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "ClusterRole",
				Name:     "gardener.cloud:vpa:target:admission-controller",
			},
			Subjects: []rbacv1.Subject{{
				Kind:      "ServiceAccount",
				Name:      "vpa-admission-controller",
				Namespace: namespace,
			}},
		}
		shootAccessSecretAdmissionController = &corev1.Secret{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "v1",
				Kind:       "Secret",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "shoot-access-vpa-admission-controller",
				Namespace: namespace,
				Labels: map[string]string{
					"resources.gardener.cloud/purpose": "token-requestor",
				},
				Annotations: map[string]string{
					"serviceaccount.resources.gardener.cloud/name":      "vpa-admission-controller",
					"serviceaccount.resources.gardener.cloud/namespace": "kube-system",
				},
			},
			Type: corev1.SecretTypeOpaque,
		}
		serviceAdmissionController = &corev1.Service{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "v1",
				Kind:       "Service",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "vpa-webhook",
				Namespace: namespace,
			},
			Spec: corev1.ServiceSpec{
				Selector: map[string]string{"app": "vpa-admission-controller"},
				Ports: []corev1.ServicePort{{
					Port:       443,
					TargetPort: intstr.FromInt(10250),
				}},
			},
		}
		networkPolicyAdmissionController = &networkingv1.NetworkPolicy{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "networking.k8s.io/v1",
				Kind:       "NetworkPolicy",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:        "allow-kube-apiserver-to-vpa-admission-controller",
				Namespace:   namespace,
				Annotations: map[string]string{"gardener.cloud/description": "Allows Egress from shoot's kube-apiserver pods to the VPA admission controller."},
			},
			Spec: networkingv1.NetworkPolicySpec{
				PodSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{
						"app":                 "kubernetes",
						"role":                "apiserver",
						"gardener.cloud/role": "controlplane",
					},
				},
				Egress: []networkingv1.NetworkPolicyEgressRule{{
					To: []networkingv1.NetworkPolicyPeer{{
						PodSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"app": "vpa-admission-controller",
							},
						},
					}},
					Ports: []networkingv1.NetworkPolicyPort{{
						Protocol: &networkPolicyProtocol,
						Port:     &networkPolicyPort,
					}},
				}},
				PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			},
		}
		deploymentAdmissionControllerFor = func(withServiceAccount bool) *appsv1.Deployment {
			obj := &appsv1.Deployment{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "vpa-admission-controller",
					Namespace: namespace,
					Labels: map[string]string{
						"app":                 "vpa-admission-controller",
						"gardener.cloud/role": "vpa",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Replicas:             pointer.Int32(4),
					RevisionHistoryLimit: pointer.Int32(2),
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"app": "vpa-admission-controller",
						},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								"app":                 "vpa-admission-controller",
								"gardener.cloud/role": "vpa",
								"networking.gardener.cloud/from-shoot-apiserver": "allowed",
								"networking.gardener.cloud/to-dns":               "allowed",
								"networking.gardener.cloud/to-shoot-apiserver":   "allowed",
							},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{
								Name:            "admission-controller",
								Image:           imageAdmissionController,
								ImagePullPolicy: corev1.PullIfNotPresent,
								Command:         []string{"./admission-controller"},
								Args: []string{
									"--v=2",
									"--stderrthreshold=info",
									"--client-ca-file=/etc/tls-certs/bundle.crt",
									"--tls-cert-file=/etc/tls-certs/tls.crt",
									"--tls-private-key=/etc/tls-certs/tls.key",
									"--address=:8944",
									"--port=10250",
									"--register-webhook=false",
								},
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse("30m"),
										corev1.ResourceMemory: resource.MustParse("200Mi"),
									},
									Limits: corev1.ResourceList{
										corev1.ResourceMemory: resource.MustParse("3Gi"),
									},
								},
								Ports: []corev1.ContainerPort{{
									ContainerPort: 10250,
								}},
								VolumeMounts: []corev1.VolumeMount{{
									Name:      "vpa-tls-certs",
									MountPath: "/etc/tls-certs",
									ReadOnly:  true,
								}},
							}},
							Volumes: []corev1.Volume{{
								Name: "vpa-tls-certs",
								VolumeSource: corev1.VolumeSource{
									Projected: &corev1.ProjectedVolumeSource{
										DefaultMode: pointer.Int32(420),
										Sources: []corev1.VolumeProjection{
											{
												Secret: &corev1.SecretProjection{
													LocalObjectReference: corev1.LocalObjectReference{
														Name: "ca",
													},
													Items: []corev1.KeyToPath{{
														Key:  "bundle.crt",
														Path: "bundle.crt",
													}},
												},
											},
											{
												Secret: &corev1.SecretProjection{
													LocalObjectReference: corev1.LocalObjectReference{
														Name: "vpa-admission-controller-server",
													},
													Items: []corev1.KeyToPath{
														{
															Key:  "tls.crt",
															Path: "tls.crt",
														},
														{
															Key:  "tls.key",
															Path: "tls.key",
														},
													},
												},
											},
										},
									},
								},
							}},
						},
					},
				},
			}

			if withServiceAccount {
				obj.Spec.Template.Spec.ServiceAccountName = serviceAccountAdmissionController.Name
				obj.Spec.Template.Spec.Containers[0].Env = append(obj.Spec.Template.Spec.Containers[0].Env, corev1.EnvVar{
					Name: "NAMESPACE",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.namespace",
						},
					},
				})
			} else {
				obj.Labels["gardener.cloud/role"] = "controlplane"
				obj.Spec.Template.Spec.AutomountServiceAccountToken = pointer.Bool(false)
				obj.Spec.Template.Spec.Containers[0].Env = append(obj.Spec.Template.Spec.Containers[0].Env,
					corev1.EnvVar{
						Name:  "KUBERNETES_SERVICE_HOST",
						Value: "kube-apiserver",
					},
					corev1.EnvVar{
						Name:  "KUBERNETES_SERVICE_PORT",
						Value: strconv.Itoa(443),
					},
				)
				obj.Spec.Template.Spec.Volumes = append(obj.Spec.Template.Spec.Volumes, corev1.Volume{
					Name: "shoot-access",
					VolumeSource: corev1.VolumeSource{
						Projected: &corev1.ProjectedVolumeSource{
							DefaultMode: pointer.Int32(420),
							Sources: []corev1.VolumeProjection{
								{
									Secret: &corev1.SecretProjection{
										LocalObjectReference: corev1.LocalObjectReference{
											Name: "ca",
										},
										Items: []corev1.KeyToPath{{
											Key:  "bundle.crt",
											Path: "ca.crt",
										}},
									},
								},
								{
									Secret: &corev1.SecretProjection{
										LocalObjectReference: corev1.LocalObjectReference{
											Name: "shoot-access-vpa-admission-controller",
										},
										Items: []corev1.KeyToPath{{
											Key:  "token",
											Path: "token",
										}},
										Optional: pointer.Bool(false),
									},
								},
							},
						},
					},
				})
				obj.Spec.Template.Spec.Containers[0].VolumeMounts = append(obj.Spec.Template.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
					Name:      "shoot-access",
					MountPath: "/var/run/secrets/kubernetes.io/serviceaccount",
					ReadOnly:  true,
				})
			}

			return obj
		}
		vpaAdmissionController = &vpaautoscalingv1.VerticalPodAutoscaler{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "autoscaling.k8s.io/v1",
				Kind:       "VerticalPodAutoscaler",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "vpa-admission-controller",
				Namespace: namespace,
			},
			Spec: vpaautoscalingv1.VerticalPodAutoscalerSpec{
				TargetRef: &autoscalingv1.CrossVersionObjectReference{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Name:       "vpa-admission-controller",
				},
				UpdatePolicy: &vpaautoscalingv1.PodUpdatePolicy{UpdateMode: &vpaUpdateModeAuto},
				ResourcePolicy: &vpaautoscalingv1.PodResourcePolicy{
					ContainerPolicies: []vpaautoscalingv1.ContainerResourcePolicy{
						{
							ContainerName:    "*",
							ControlledValues: &vpaControlledValues,
							MinAllowed: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("50m"),
								corev1.ResourceMemory: resource.MustParse("100Mi"),
							},
						},
					},
				},
			},
		}

		clusterRoleGeneralActor = &rbacv1.ClusterRole{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "rbac.authorization.k8s.io/v1",
				Kind:       "ClusterRole",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: "gardener.cloud:vpa:target:actor",
				Labels: map[string]string{
					"gardener.cloud/role": "vpa",
				},
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"pods", "nodes", "limitranges"},
					Verbs:     []string{"get", "list", "watch"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"events"},
					Verbs:     []string{"get", "list", "watch", "create"},
				},
				{
					APIGroups: []string{"poc.autoscaling.k8s.io"},
					Resources: []string{"verticalpodautoscalers"},
					Verbs:     []string{"get", "list", "watch", "patch"},
				},
				{
					APIGroups: []string{"autoscaling.k8s.io"},
					Resources: []string{"verticalpodautoscalers"},
					Verbs:     []string{"get", "list", "watch", "patch"},
				},
				{
					APIGroups: []string{"coordination.k8s.io"},
					Resources: []string{"leases"},
					Verbs:     []string{"get", "list", "watch"},
				},
			},
		}
		clusterRoleBindingGeneralActor = &rbacv1.ClusterRoleBinding{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "rbac.authorization.k8s.io/v1",
				Kind:       "ClusterRoleBinding",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: "gardener.cloud:vpa:target:actor",
				Labels: map[string]string{
					"gardener.cloud/role": "vpa",
				},
				Annotations: map[string]string{
					"resources.gardener.cloud/delete-on-invalid-update": "true",
				},
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "ClusterRole",
				Name:     "gardener.cloud:vpa:target:actor",
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      "vpa-recommender",
					Namespace: namespace,
				},
				{
					Kind:      "ServiceAccount",
					Name:      "vpa-updater",
					Namespace: namespace,
				},
			},
		}
		clusterRoleGeneralTargetReader = &rbacv1.ClusterRole{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "rbac.authorization.k8s.io/v1",
				Kind:       "ClusterRole",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: "gardener.cloud:vpa:target:target-reader",
				Labels: map[string]string{
					"gardener.cloud/role": "vpa",
				},
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{"*"},
					Resources: []string{"*/scale"},
					Verbs:     []string{"get", "watch"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"replicationcontrollers"},
					Verbs:     []string{"get", "list", "watch"},
				},
				{
					APIGroups: []string{"apps"},
					Resources: []string{"daemonsets", "deployments", "replicasets", "statefulsets"},
					Verbs:     []string{"get", "list", "watch"},
				},
				{
					APIGroups: []string{"batch"},
					Resources: []string{"jobs", "cronjobs"},
					Verbs:     []string{"get", "list", "watch"},
				},
				{
					APIGroups: []string{"druid.gardener.cloud"},
					Resources: []string{"etcds", "etcds/scale"},
					Verbs:     []string{"get", "list", "watch"},
				},
			},
		}
		clusterRoleBindingGeneralTargetReader = &rbacv1.ClusterRoleBinding{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "rbac.authorization.k8s.io/v1",
				Kind:       "ClusterRoleBinding",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: "gardener.cloud:vpa:target:target-reader",
				Labels: map[string]string{
					"gardener.cloud/role": "vpa",
				},
				Annotations: map[string]string{
					"resources.gardener.cloud/delete-on-invalid-update": "true",
				},
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "ClusterRole",
				Name:     "gardener.cloud:vpa:target:target-reader",
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      "vpa-admission-controller",
					Namespace: namespace,
				},
				{
					Kind:      "ServiceAccount",
					Name:      "vpa-recommender",
					Namespace: namespace,
				},
				{
					Kind:      "ServiceAccount",
					Name:      "vpa-updater",
					Namespace: namespace,
				},
			},
		}
		mutatingWebhookConfiguration = &admissionregistrationv1.MutatingWebhookConfiguration{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "admissionregistration.k8s.io/v1",
				Kind:       "MutatingWebhookConfiguration",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:   "vpa-webhook-config-target",
				Labels: map[string]string{"remediation.webhook.shoot.gardener.cloud/exclude": "true"},
			},
			Webhooks: []admissionregistrationv1.MutatingWebhook{{
				Name:                    "vpa.k8s.io",
				AdmissionReviewVersions: []string{"v1"},
				ClientConfig: admissionregistrationv1.WebhookClientConfig{
					URL: pointer.String(fmt.Sprintf("https://vpa-webhook.%s:443", namespace)),
				},
				FailurePolicy:      &webhookFailurePolicy,
				MatchPolicy:        &webhookMatchPolicy,
				ReinvocationPolicy: &webhookReinvocationPolicy,
				SideEffects:        &webhookSideEffects,
				TimeoutSeconds:     pointer.Int32(10),
				Rules: []admissionregistrationv1.RuleWithOperations{
					{
						Rule: admissionregistrationv1.Rule{
							APIGroups:   []string{""},
							APIVersions: []string{"v1"},
							Resources:   []string{"pods"},
							Scope:       &webhookScope,
						},
						Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.Create},
					},
					{
						Rule: admissionregistrationv1.Rule{
							APIGroups:   []string{"autoscaling.k8s.io"},
							APIVersions: []string{"*"},
							Resources:   []string{"verticalpodautoscalers"},
							Scope:       &webhookScope,
						},
						Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.Create, admissionregistrationv1.Update},
					},
				},
			}},
		}
	})

	JustBeforeEach(func() {
		managedResource = &resourcesv1alpha1.ManagedResource{
			ObjectMeta: metav1.ObjectMeta{
				Name:      managedResourceName,
				Namespace: namespace,
			},
		}
		managedResourceSecret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "managedresource-" + managedResource.Name,
				Namespace: namespace,
			},
		}
	})

	Describe("#Deploy", func() {
		Context("cluster type seed", func() {
			BeforeEach(func() {
				component = New(c, namespace, sm, Values{
					ClusterType:         ClusterTypeSeed,
					Enabled:             true,
					SecretNameServerCA:  secretNameCA,
					AdmissionController: valuesAdmissionController,
					Exporter:            valuesExporter,
					Recommender:         valuesRecommender,
					Updater:             valuesUpdater,
				})
				managedResourceName = "vpa"
			})

			It("should successfully deploy all resources", func() {
				Expect(c.Get(ctx, client.ObjectKeyFromObject(managedResource), managedResource)).To(MatchError(apierrors.NewNotFound(schema.GroupResource{Group: resourcesv1alpha1.SchemeGroupVersion.Group, Resource: "managedresources"}, managedResource.Name)))
				Expect(c.Get(ctx, client.ObjectKeyFromObject(managedResourceSecret), managedResourceSecret)).To(MatchError(apierrors.NewNotFound(schema.GroupResource{Group: corev1.SchemeGroupVersion.Group, Resource: "secrets"}, managedResourceSecret.Name)))

				Expect(component.Deploy(ctx)).To(Succeed())

				Expect(c.Get(ctx, client.ObjectKeyFromObject(managedResource), managedResource)).To(Succeed())
				Expect(managedResource).To(DeepEqual(&resourcesv1alpha1.ManagedResource{
					TypeMeta: metav1.TypeMeta{
						APIVersion: resourcesv1alpha1.SchemeGroupVersion.String(),
						Kind:       "ManagedResource",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:            managedResourceName,
						Namespace:       namespace,
						ResourceVersion: "1",
					},
					Spec: resourcesv1alpha1.ManagedResourceSpec{
						Class: pointer.String("seed"),
						SecretRefs: []corev1.LocalObjectReference{{
							Name: managedResourceSecret.Name,
						}},
						KeepObjects: pointer.Bool(false),
					},
				}))

				Expect(c.Get(ctx, client.ObjectKeyFromObject(managedResourceSecret), managedResourceSecret)).To(Succeed())
				Expect(managedResourceSecret.Type).To(Equal(corev1.SecretTypeOpaque))
				Expect(managedResourceSecret.Data).To(HaveLen(29))

				By("checking vpa-exporter resources")
				clusterRoleExporter.Name = replaceTargetSubstrings(clusterRoleExporter.Name)
				clusterRoleBindingExporter.Name = replaceTargetSubstrings(clusterRoleBindingExporter.Name)
				clusterRoleBindingExporter.RoleRef.Name = replaceTargetSubstrings(clusterRoleBindingExporter.RoleRef.Name)

				Expect(string(managedResourceSecret.Data["service__"+namespace+"__vpa-exporter.yaml"])).To(Equal(serialize(serviceExporter)))
				Expect(string(managedResourceSecret.Data["serviceaccount__"+namespace+"__vpa-exporter.yaml"])).To(Equal(serialize(serviceAccountExporter)))
				Expect(string(managedResourceSecret.Data["clusterrole____gardener.cloud_vpa_source_exporter.yaml"])).To(Equal(serialize(clusterRoleExporter)))
				Expect(string(managedResourceSecret.Data["clusterrolebinding____gardener.cloud_vpa_source_exporter.yaml"])).To(Equal(serialize(clusterRoleBindingExporter)))
				Expect(string(managedResourceSecret.Data["deployment__"+namespace+"__vpa-exporter.yaml"])).To(Equal(serialize(deploymentExporter)))
				Expect(string(managedResourceSecret.Data["verticalpodautoscaler__"+namespace+"__vpa-exporter-vpa.yaml"])).To(Equal(serialize(vpaExporter)))

				By("checking vpa-updater resources")
				clusterRoleUpdater.Name = replaceTargetSubstrings(clusterRoleUpdater.Name)
				clusterRoleBindingUpdater.Name = replaceTargetSubstrings(clusterRoleBindingUpdater.Name)
				clusterRoleBindingUpdater.RoleRef.Name = replaceTargetSubstrings(clusterRoleBindingUpdater.RoleRef.Name)

				deploymentUpdater := deploymentUpdaterFor(true, nil, nil, nil, nil, nil)
				dropNetworkingLabels(deploymentUpdater.Spec.Template.Labels)

				Expect(string(managedResourceSecret.Data["serviceaccount__"+namespace+"__vpa-updater.yaml"])).To(Equal(serialize(serviceAccountUpdater)))
				Expect(string(managedResourceSecret.Data["clusterrole____gardener.cloud_vpa_source_evictioner.yaml"])).To(Equal(serialize(clusterRoleUpdater)))
				Expect(string(managedResourceSecret.Data["clusterrolebinding____gardener.cloud_vpa_source_evictioner.yaml"])).To(Equal(serialize(clusterRoleBindingUpdater)))
				Expect(string(managedResourceSecret.Data["deployment__"+namespace+"__vpa-updater.yaml"])).To(Equal(serialize(deploymentUpdater)))
				Expect(string(managedResourceSecret.Data["verticalpodautoscaler__"+namespace+"__vpa-updater.yaml"])).To(Equal(serialize(vpaUpdater)))

				By("checking vpa-recommender resources")
				clusterRoleRecommenderMetricsReader.Name = replaceTargetSubstrings(clusterRoleRecommenderMetricsReader.Name)
				clusterRoleBindingRecommenderMetricsReader.Name = replaceTargetSubstrings(clusterRoleBindingRecommenderMetricsReader.Name)
				clusterRoleBindingRecommenderMetricsReader.RoleRef.Name = replaceTargetSubstrings(clusterRoleBindingRecommenderMetricsReader.RoleRef.Name)
				clusterRoleRecommenderCheckpointActor.Name = replaceTargetSubstrings(clusterRoleRecommenderCheckpointActor.Name)
				clusterRoleBindingRecommenderCheckpointActor.Name = replaceTargetSubstrings(clusterRoleBindingRecommenderCheckpointActor.Name)
				clusterRoleBindingRecommenderCheckpointActor.RoleRef.Name = replaceTargetSubstrings(clusterRoleBindingRecommenderCheckpointActor.RoleRef.Name)

				deploymentRecommender := deploymentRecommenderFor(true, nil, nil)
				dropNetworkingLabels(deploymentRecommender.Spec.Template.Labels)

				Expect(string(managedResourceSecret.Data["serviceaccount__"+namespace+"__vpa-recommender.yaml"])).To(Equal(serialize(serviceAccountRecommender)))
				Expect(string(managedResourceSecret.Data["clusterrole____gardener.cloud_vpa_source_metrics-reader.yaml"])).To(Equal(serialize(clusterRoleRecommenderMetricsReader)))
				Expect(string(managedResourceSecret.Data["clusterrolebinding____gardener.cloud_vpa_source_metrics-reader.yaml"])).To(Equal(serialize(clusterRoleBindingRecommenderMetricsReader)))
				Expect(string(managedResourceSecret.Data["clusterrole____gardener.cloud_vpa_source_checkpoint-actor.yaml"])).To(Equal(serialize(clusterRoleRecommenderCheckpointActor)))
				Expect(string(managedResourceSecret.Data["clusterrolebinding____gardener.cloud_vpa_source_checkpoint-actor.yaml"])).To(Equal(serialize(clusterRoleBindingRecommenderCheckpointActor)))
				Expect(string(managedResourceSecret.Data["deployment__"+namespace+"__vpa-recommender.yaml"])).To(Equal(serialize(deploymentRecommender)))
				Expect(string(managedResourceSecret.Data["verticalpodautoscaler__"+namespace+"__vpa-recommender.yaml"])).To(Equal(serialize(vpaRecommender)))

				By("checking vpa-admission-controller resources")
				clusterRoleAdmissionController.Name = replaceTargetSubstrings(clusterRoleAdmissionController.Name)
				clusterRoleBindingAdmissionController.Name = replaceTargetSubstrings(clusterRoleBindingAdmissionController.Name)
				clusterRoleBindingAdmissionController.RoleRef.Name = replaceTargetSubstrings(clusterRoleBindingAdmissionController.RoleRef.Name)

				deploymentAdmissionController := deploymentAdmissionControllerFor(true)
				dropNetworkingLabels(deploymentAdmissionController.Spec.Template.Labels)

				Expect(string(managedResourceSecret.Data["serviceaccount__"+namespace+"__vpa-admission-controller.yaml"])).To(Equal(serialize(serviceAccountAdmissionController)))
				Expect(string(managedResourceSecret.Data["clusterrole____gardener.cloud_vpa_source_admission-controller.yaml"])).To(Equal(serialize(clusterRoleAdmissionController)))
				Expect(string(managedResourceSecret.Data["clusterrolebinding____gardener.cloud_vpa_source_admission-controller.yaml"])).To(Equal(serialize(clusterRoleBindingAdmissionController)))
				Expect(string(managedResourceSecret.Data["service__"+namespace+"__vpa-webhook.yaml"])).To(Equal(serialize(serviceAdmissionController)))
				Expect(string(managedResourceSecret.Data["deployment__"+namespace+"__vpa-admission-controller.yaml"])).To(Equal(serialize(deploymentAdmissionController)))
				Expect(string(managedResourceSecret.Data["verticalpodautoscaler__"+namespace+"__vpa-admission-controller.yaml"])).To(Equal(serialize(vpaAdmissionController)))

				By("checking general resources")
				clusterRoleGeneralActor.Name = replaceTargetSubstrings(clusterRoleGeneralActor.Name)
				clusterRoleGeneralTargetReader.Name = replaceTargetSubstrings(clusterRoleGeneralTargetReader.Name)
				clusterRoleBindingGeneralActor.Name = replaceTargetSubstrings(clusterRoleBindingGeneralActor.Name)
				clusterRoleBindingGeneralActor.RoleRef.Name = replaceTargetSubstrings(clusterRoleBindingGeneralActor.RoleRef.Name)
				clusterRoleBindingGeneralTargetReader.Name = replaceTargetSubstrings(clusterRoleBindingGeneralTargetReader.Name)
				clusterRoleBindingGeneralTargetReader.RoleRef.Name = replaceTargetSubstrings(clusterRoleBindingGeneralTargetReader.RoleRef.Name)
				mutatingWebhookConfiguration.Name = strings.Replace(mutatingWebhookConfiguration.Name, "-target", "-source", -1)
				mutatingWebhookConfiguration.Webhooks[0].ClientConfig = admissionregistrationv1.WebhookClientConfig{
					Service: &admissionregistrationv1.ServiceReference{
						Name:      "vpa-webhook",
						Namespace: namespace,
						Port:      pointer.Int32(443),
					},
				}

				Expect(string(managedResourceSecret.Data["clusterrole____gardener.cloud_vpa_source_actor.yaml"])).To(Equal(serialize(clusterRoleGeneralActor)))
				Expect(string(managedResourceSecret.Data["clusterrolebinding____gardener.cloud_vpa_source_actor.yaml"])).To(Equal(serialize(clusterRoleBindingGeneralActor)))
				Expect(string(managedResourceSecret.Data["clusterrole____gardener.cloud_vpa_source_target-reader.yaml"])).To(Equal(serialize(clusterRoleGeneralTargetReader)))
				Expect(string(managedResourceSecret.Data["clusterrolebinding____gardener.cloud_vpa_source_target-reader.yaml"])).To(Equal(serialize(clusterRoleBindingGeneralTargetReader)))
				Expect(string(managedResourceSecret.Data["mutatingwebhookconfiguration____vpa-webhook-config-source.yaml"])).To(Equal(serialize(mutatingWebhookConfiguration)))
			})

			It("should successfully deploy only vpa-exporter if not enabled", func() {
				component = New(c, namespace, sm, Values{
					ClusterType:         ClusterTypeSeed,
					Enabled:             false,
					SecretNameServerCA:  secretNameCA,
					AdmissionController: valuesAdmissionController,
					Exporter:            valuesExporter,
					Recommender:         valuesRecommender,
					Updater:             valuesUpdater,
				})

				Expect(component.Deploy(ctx)).To(Succeed())

				Expect(c.Get(ctx, client.ObjectKeyFromObject(managedResource), managedResource)).To(Succeed())
				Expect(managedResource).To(DeepEqual(&resourcesv1alpha1.ManagedResource{
					TypeMeta: metav1.TypeMeta{
						APIVersion: resourcesv1alpha1.SchemeGroupVersion.String(),
						Kind:       "ManagedResource",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:            managedResourceName,
						Namespace:       namespace,
						ResourceVersion: "1",
					},
					Spec: resourcesv1alpha1.ManagedResourceSpec{
						Class: pointer.String("seed"),
						SecretRefs: []corev1.LocalObjectReference{{
							Name: managedResourceSecret.Name,
						}},
						KeepObjects: pointer.Bool(false),
					},
				}))

				Expect(c.Get(ctx, client.ObjectKeyFromObject(managedResourceSecret), managedResourceSecret)).To(Succeed())
				Expect(managedResourceSecret.Type).To(Equal(corev1.SecretTypeOpaque))
				Expect(managedResourceSecret.Data).To(HaveLen(6))

				By("checking vpa-exporter resources")
				clusterRoleExporter.Name = replaceTargetSubstrings(clusterRoleExporter.Name)
				clusterRoleBindingExporter.Name = replaceTargetSubstrings(clusterRoleBindingExporter.Name)
				clusterRoleBindingExporter.RoleRef.Name = replaceTargetSubstrings(clusterRoleBindingExporter.RoleRef.Name)

				Expect(string(managedResourceSecret.Data["service__"+namespace+"__vpa-exporter.yaml"])).To(Equal(serialize(serviceExporter)))
				Expect(string(managedResourceSecret.Data["serviceaccount__"+namespace+"__vpa-exporter.yaml"])).To(Equal(serialize(serviceAccountExporter)))
				Expect(string(managedResourceSecret.Data["clusterrole____gardener.cloud_vpa_source_exporter.yaml"])).To(Equal(serialize(clusterRoleExporter)))
				Expect(string(managedResourceSecret.Data["clusterrolebinding____gardener.cloud_vpa_source_exporter.yaml"])).To(Equal(serialize(clusterRoleBindingExporter)))
				Expect(string(managedResourceSecret.Data["deployment__"+namespace+"__vpa-exporter.yaml"])).To(Equal(serialize(deploymentExporter)))
				Expect(string(managedResourceSecret.Data["verticalpodautoscaler__"+namespace+"__vpa-exporter-vpa.yaml"])).To(Equal(serialize(vpaExporter)))
			})

			It("should successfully deploy with special configuration", func() {
				valuesRecommender.Interval = &metav1.Duration{Duration: 3 * time.Hour}
				valuesRecommender.RecommendationMarginFraction = pointer.Float64(8.91)

				valuesUpdater.Interval = &metav1.Duration{Duration: 4 * time.Hour}
				valuesUpdater.EvictAfterOOMThreshold = &metav1.Duration{Duration: 5 * time.Hour}
				valuesUpdater.EvictionRateBurst = pointer.Int32(1)
				valuesUpdater.EvictionRateLimit = pointer.Float64(2.34)
				valuesUpdater.EvictionTolerance = pointer.Float64(5.67)

				component = New(c, namespace, sm, Values{
					ClusterType:         ClusterTypeSeed,
					Enabled:             true,
					SecretNameServerCA:  secretNameCA,
					AdmissionController: valuesAdmissionController,
					Exporter:            valuesExporter,
					Recommender:         valuesRecommender,
					Updater:             valuesUpdater,
				})

				Expect(component.Deploy(ctx)).To(Succeed())

				Expect(c.Get(ctx, client.ObjectKeyFromObject(managedResource), managedResource)).To(Succeed())
				Expect(managedResource).To(DeepEqual(&resourcesv1alpha1.ManagedResource{
					TypeMeta: metav1.TypeMeta{
						APIVersion: resourcesv1alpha1.SchemeGroupVersion.String(),
						Kind:       "ManagedResource",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:            managedResourceName,
						Namespace:       namespace,
						ResourceVersion: "1",
					},
					Spec: resourcesv1alpha1.ManagedResourceSpec{
						Class: pointer.String("seed"),
						SecretRefs: []corev1.LocalObjectReference{{
							Name: managedResourceSecret.Name,
						}},
						KeepObjects: pointer.Bool(false),
					},
				}))

				Expect(c.Get(ctx, client.ObjectKeyFromObject(managedResourceSecret), managedResourceSecret)).To(Succeed())

				deploymentUpdater := deploymentUpdaterFor(
					true,
					valuesUpdater.Interval,
					valuesUpdater.EvictAfterOOMThreshold,
					valuesUpdater.EvictionRateBurst,
					valuesUpdater.EvictionRateLimit,
					valuesUpdater.EvictionTolerance,
				)
				dropNetworkingLabels(deploymentUpdater.Spec.Template.Labels)

				deploymentRecommender := deploymentRecommenderFor(
					true,
					valuesRecommender.Interval,
					valuesRecommender.RecommendationMarginFraction,
				)
				dropNetworkingLabels(deploymentRecommender.Spec.Template.Labels)

				Expect(string(managedResourceSecret.Data["deployment__"+namespace+"__vpa-updater.yaml"])).To(Equal(serialize(deploymentUpdater)))
				Expect(string(managedResourceSecret.Data["deployment__"+namespace+"__vpa-recommender.yaml"])).To(Equal(serialize(deploymentRecommender)))
			})

			It("should delete the legacy resources", func() {
				legacyExporterClusterRole := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "gardener.cloud:vpa:seed:exporter"}}
				Expect(c.Create(ctx, legacyExporterClusterRole)).To(Succeed())

				legacyExporterClusterRoleBinding := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "gardener.cloud:vpa:seed:exporter"}}
				Expect(c.Create(ctx, legacyExporterClusterRoleBinding)).To(Succeed())

				legacyUpdaterClusterRole := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "gardener.cloud:vpa:seed:evictioner"}}
				Expect(c.Create(ctx, legacyUpdaterClusterRole)).To(Succeed())

				legacyUpdaterClusterRoleBinding := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "gardener.cloud:vpa:seed:evictioner"}}
				Expect(c.Create(ctx, legacyUpdaterClusterRoleBinding)).To(Succeed())

				legacyRecommenderClusterRoleMetricsReader := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "gardener.cloud:vpa:seed:metrics-reader"}}
				Expect(c.Create(ctx, legacyRecommenderClusterRoleMetricsReader)).To(Succeed())

				legacyRecommenderClusterRoleBindingMetricsReader := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "gardener.cloud:vpa:seed:metrics-reader"}}
				Expect(c.Create(ctx, legacyRecommenderClusterRoleBindingMetricsReader)).To(Succeed())

				legacyRecommenderClusterRoleCheckpointActor := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "gardener.cloud:vpa:seed:checkpoint-actor"}}
				Expect(c.Create(ctx, legacyRecommenderClusterRoleCheckpointActor)).To(Succeed())

				legacyRecommenderClusterRoleBindingCheckpointActor := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "gardener.cloud:vpa:seed:checkpoint-actor"}}
				Expect(c.Create(ctx, legacyRecommenderClusterRoleBindingCheckpointActor)).To(Succeed())

				legacyAdmissionControllerClusterRole := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "gardener.cloud:vpa:seed:admission-controller"}}
				Expect(c.Create(ctx, legacyAdmissionControllerClusterRole)).To(Succeed())

				legacyAdmissionControllerClusterRoleBinding := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "gardener.cloud:vpa:seed:admission-controller"}}
				Expect(c.Create(ctx, legacyAdmissionControllerClusterRoleBinding)).To(Succeed())

				legacyTLSCertsSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "vpa-tls-certs", Namespace: namespace}}
				Expect(c.Create(ctx, legacyTLSCertsSecret)).To(Succeed())

				legacyGeneralClusterRoleActor := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "gardener.cloud:vpa:seed:actor"}}
				Expect(c.Create(ctx, legacyGeneralClusterRoleActor)).To(Succeed())

				legacyGeneralClusterRoleBindingActor := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "gardener.cloud:vpa:seed:actor"}}
				Expect(c.Create(ctx, legacyGeneralClusterRoleBindingActor)).To(Succeed())

				legacyGeneralClusterRoleTargetReader := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "gardener.cloud:vpa:seed:target-reader"}}
				Expect(c.Create(ctx, legacyGeneralClusterRoleTargetReader)).To(Succeed())

				legacyGeneralClusterRoleBindingTargetReader := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "gardener.cloud:vpa:seed:target-reader"}}
				Expect(c.Create(ctx, legacyGeneralClusterRoleBindingTargetReader)).To(Succeed())

				legacyMutatingWebhookConfiguration := &admissionregistrationv1.MutatingWebhookConfiguration{ObjectMeta: metav1.ObjectMeta{Name: "vpa-webhook-config-seed"}}
				Expect(c.Create(ctx, legacyMutatingWebhookConfiguration)).To(Succeed())

				Expect(component.Deploy(ctx)).To(Succeed())

				Expect(c.Get(ctx, client.ObjectKeyFromObject(legacyExporterClusterRole), &rbacv1.ClusterRole{})).To(BeNotFoundError())
				Expect(c.Get(ctx, client.ObjectKeyFromObject(legacyExporterClusterRoleBinding), &rbacv1.ClusterRoleBinding{})).To(BeNotFoundError())
				Expect(c.Get(ctx, client.ObjectKeyFromObject(legacyUpdaterClusterRole), &rbacv1.ClusterRole{})).To(BeNotFoundError())
				Expect(c.Get(ctx, client.ObjectKeyFromObject(legacyUpdaterClusterRoleBinding), &rbacv1.ClusterRoleBinding{})).To(BeNotFoundError())
				Expect(c.Get(ctx, client.ObjectKeyFromObject(legacyRecommenderClusterRoleMetricsReader), &rbacv1.ClusterRole{})).To(BeNotFoundError())
				Expect(c.Get(ctx, client.ObjectKeyFromObject(legacyRecommenderClusterRoleBindingMetricsReader), &rbacv1.ClusterRoleBinding{})).To(BeNotFoundError())
				Expect(c.Get(ctx, client.ObjectKeyFromObject(legacyRecommenderClusterRoleCheckpointActor), &rbacv1.ClusterRole{})).To(BeNotFoundError())
				Expect(c.Get(ctx, client.ObjectKeyFromObject(legacyRecommenderClusterRoleBindingCheckpointActor), &rbacv1.ClusterRoleBinding{})).To(BeNotFoundError())
				Expect(c.Get(ctx, client.ObjectKeyFromObject(legacyAdmissionControllerClusterRole), &rbacv1.ClusterRole{})).To(BeNotFoundError())
				Expect(c.Get(ctx, client.ObjectKeyFromObject(legacyAdmissionControllerClusterRoleBinding), &rbacv1.ClusterRoleBinding{})).To(BeNotFoundError())
				Expect(c.Get(ctx, client.ObjectKeyFromObject(legacyTLSCertsSecret), &corev1.Secret{})).To(BeNotFoundError())
				Expect(c.Get(ctx, client.ObjectKeyFromObject(legacyGeneralClusterRoleActor), &rbacv1.ClusterRole{})).To(BeNotFoundError())
				Expect(c.Get(ctx, client.ObjectKeyFromObject(legacyGeneralClusterRoleBindingActor), &rbacv1.ClusterRoleBinding{})).To(BeNotFoundError())
				Expect(c.Get(ctx, client.ObjectKeyFromObject(legacyGeneralClusterRoleTargetReader), &rbacv1.ClusterRole{})).To(BeNotFoundError())
				Expect(c.Get(ctx, client.ObjectKeyFromObject(legacyGeneralClusterRoleBindingTargetReader), &rbacv1.ClusterRoleBinding{})).To(BeNotFoundError())
				Expect(c.Get(ctx, client.ObjectKeyFromObject(legacyMutatingWebhookConfiguration), &admissionregistrationv1.MutatingWebhookConfiguration{})).To(BeNotFoundError())
			})
		})

		Context("cluster type shoot", func() {
			BeforeEach(func() {
				component = New(c, namespace, sm, Values{
					ClusterType:         ClusterTypeShoot,
					Enabled:             true,
					SecretNameServerCA:  secretNameCA,
					AdmissionController: valuesAdmissionController,
					Exporter:            valuesExporter,
					Recommender:         valuesRecommender,
					Updater:             valuesUpdater,
				})
				managedResourceName = "shoot-core-vpa"
			})

			It("should successfully deploy all resources", func() {
				Expect(c.Get(ctx, client.ObjectKeyFromObject(managedResource), managedResource)).To(MatchError(apierrors.NewNotFound(schema.GroupResource{Group: resourcesv1alpha1.SchemeGroupVersion.Group, Resource: "managedresources"}, managedResource.Name)))
				Expect(c.Get(ctx, client.ObjectKeyFromObject(managedResourceSecret), managedResourceSecret)).To(MatchError(apierrors.NewNotFound(schema.GroupResource{Group: corev1.SchemeGroupVersion.Group, Resource: "secrets"}, managedResourceSecret.Name)))

				Expect(component.Deploy(ctx)).To(Succeed())

				Expect(c.Get(ctx, client.ObjectKeyFromObject(managedResource), managedResource)).To(Succeed())
				Expect(managedResource).To(DeepEqual(&resourcesv1alpha1.ManagedResource{
					TypeMeta: metav1.TypeMeta{
						APIVersion: resourcesv1alpha1.SchemeGroupVersion.String(),
						Kind:       "ManagedResource",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:            managedResourceName,
						Namespace:       namespace,
						ResourceVersion: "1",
						Labels:          map[string]string{"origin": "gardener"},
					},
					Spec: resourcesv1alpha1.ManagedResourceSpec{
						InjectLabels: map[string]string{"shoot.gardener.cloud/no-cleanup": "true"},
						SecretRefs: []corev1.LocalObjectReference{{
							Name: managedResourceSecret.Name,
						}},
						KeepObjects: pointer.Bool(false),
					},
				}))

				Expect(c.Get(ctx, client.ObjectKeyFromObject(managedResourceSecret), managedResourceSecret)).To(Succeed())
				Expect(managedResourceSecret.Type).To(Equal(corev1.SecretTypeOpaque))
				Expect(managedResourceSecret.Data).To(HaveLen(15))

				By("checking vpa-exporter application resources do not exist")
				Expect(managedResourceSecret.Data["serviceaccount__"+namespace+"__vpa-exporter.yaml"]).To(BeNil())
				Expect(managedResourceSecret.Data["clusterrole____gardener.cloud_vpa_target_exporter.yaml"]).To(BeNil())
				Expect(managedResourceSecret.Data["clusterrolebinding____gardener.cloud_vpa_target_exporter.yaml"]).To(BeNil())

				By("checking vpa-exporter runtime resources do not exist")
				Expect(c.Get(ctx, client.ObjectKeyFromObject(serviceExporter), &corev1.Service{})).To(BeNotFoundError())
				Expect(c.Get(ctx, client.ObjectKeyFromObject(deploymentExporter), &appsv1.Deployment{})).To(BeNotFoundError())
				Expect(c.Get(ctx, client.ObjectKeyFromObject(vpaExporter), &vpaautoscalingv1.VerticalPodAutoscaler{})).To(BeNotFoundError())

				By("checking vpa-updater application resources")
				clusterRoleBindingUpdater.Subjects[0].Namespace = "kube-system"

				Expect(string(managedResourceSecret.Data["clusterrole____gardener.cloud_vpa_target_evictioner.yaml"])).To(Equal(serialize(clusterRoleUpdater)))
				Expect(string(managedResourceSecret.Data["clusterrolebinding____gardener.cloud_vpa_target_evictioner.yaml"])).To(Equal(serialize(clusterRoleBindingUpdater)))

				By("checking vpa-updater runtime resources")
				secret := &corev1.Secret{}
				Expect(c.Get(ctx, client.ObjectKeyFromObject(shootAccessSecretUpdater), secret)).To(Succeed())
				shootAccessSecretUpdater.ResourceVersion = "1"
				Expect(secret).To(Equal(shootAccessSecretUpdater))

				deployment := &appsv1.Deployment{}
				Expect(c.Get(ctx, kutil.Key(namespace, "vpa-updater"), deployment)).To(Succeed())
				deploymentUpdater := deploymentUpdaterFor(false, nil, nil, nil, nil, nil)
				deploymentUpdater.ResourceVersion = "1"
				Expect(deployment).To(Equal(deploymentUpdater))

				vpa := &vpaautoscalingv1.VerticalPodAutoscaler{}
				Expect(c.Get(ctx, client.ObjectKeyFromObject(vpaUpdater), vpa)).To(Succeed())
				vpaUpdater.ResourceVersion = "1"
				Expect(vpa).To(Equal(vpaUpdater))

				By("checking vpa-recommender application resources")
				clusterRoleBindingRecommenderMetricsReader.Subjects[0].Namespace = "kube-system"
				clusterRoleBindingRecommenderCheckpointActor.Subjects[0].Namespace = "kube-system"

				Expect(string(managedResourceSecret.Data["clusterrole____gardener.cloud_vpa_target_metrics-reader.yaml"])).To(Equal(serialize(clusterRoleRecommenderMetricsReader)))
				Expect(string(managedResourceSecret.Data["clusterrolebinding____gardener.cloud_vpa_target_metrics-reader.yaml"])).To(Equal(serialize(clusterRoleBindingRecommenderMetricsReader)))
				Expect(string(managedResourceSecret.Data["clusterrole____gardener.cloud_vpa_target_checkpoint-actor.yaml"])).To(Equal(serialize(clusterRoleRecommenderCheckpointActor)))
				Expect(string(managedResourceSecret.Data["clusterrolebinding____gardener.cloud_vpa_target_checkpoint-actor.yaml"])).To(Equal(serialize(clusterRoleBindingRecommenderCheckpointActor)))

				By("checking vpa-recommender runtime resources")
				secret = &corev1.Secret{}
				Expect(c.Get(ctx, client.ObjectKeyFromObject(shootAccessSecretRecommender), secret)).To(Succeed())
				shootAccessSecretRecommender.ResourceVersion = "1"
				Expect(secret).To(Equal(shootAccessSecretRecommender))

				deployment = &appsv1.Deployment{}
				Expect(c.Get(ctx, kutil.Key(namespace, "vpa-recommender"), deployment)).To(Succeed())
				deploymentRecommender := deploymentRecommenderFor(false, nil, nil)
				deploymentRecommender.ResourceVersion = "1"
				Expect(deployment).To(Equal(deploymentRecommender))

				vpa = &vpaautoscalingv1.VerticalPodAutoscaler{}
				Expect(c.Get(ctx, client.ObjectKeyFromObject(vpaRecommender), vpa)).To(Succeed())
				vpaRecommender.ResourceVersion = "1"
				Expect(vpa).To(Equal(vpaRecommender))

				By("checking vpa-admission-controller application resources")
				clusterRoleBindingAdmissionController.Subjects[0].Namespace = "kube-system"

				Expect(string(managedResourceSecret.Data["clusterrole____gardener.cloud_vpa_target_admission-controller.yaml"])).To(Equal(serialize(clusterRoleAdmissionController)))
				Expect(string(managedResourceSecret.Data["clusterrolebinding____gardener.cloud_vpa_target_admission-controller.yaml"])).To(Equal(serialize(clusterRoleBindingAdmissionController)))

				By("checking vpa-admission-controller runtime resources")
				secret = &corev1.Secret{}
				Expect(c.Get(ctx, client.ObjectKeyFromObject(shootAccessSecretAdmissionController), secret)).To(Succeed())
				shootAccessSecretAdmissionController.ResourceVersion = "1"
				Expect(secret).To(Equal(shootAccessSecretAdmissionController))

				service := &corev1.Service{}
				Expect(c.Get(ctx, client.ObjectKeyFromObject(serviceAdmissionController), service)).To(Succeed())
				serviceAdmissionController.ResourceVersion = "1"
				Expect(service).To(Equal(serviceAdmissionController))

				networkPolicy := &networkingv1.NetworkPolicy{}
				Expect(c.Get(ctx, client.ObjectKeyFromObject(networkPolicyAdmissionController), networkPolicy)).To(Succeed())
				networkPolicyAdmissionController.ResourceVersion = "1"
				Expect(networkPolicy).To(Equal(networkPolicyAdmissionController))

				deployment = &appsv1.Deployment{}
				Expect(c.Get(ctx, kutil.Key(namespace, "vpa-admission-controller"), deployment)).To(Succeed())
				deploymentAdmissionController := deploymentAdmissionControllerFor(false)
				deploymentAdmissionController.ResourceVersion = "1"
				Expect(deployment).To(Equal(deploymentAdmissionController))

				vpa = &vpaautoscalingv1.VerticalPodAutoscaler{}
				Expect(c.Get(ctx, client.ObjectKeyFromObject(vpaAdmissionController), vpa)).To(Succeed())
				vpaAdmissionController.ResourceVersion = "1"
				Expect(vpa).To(Equal(vpaAdmissionController))

				By("checking general application resources")
				clusterRoleBindingGeneralActor.Subjects[0].Namespace = "kube-system"
				clusterRoleBindingGeneralActor.Subjects[1].Namespace = "kube-system"
				clusterRoleBindingGeneralTargetReader.Subjects[0].Namespace = "kube-system"
				clusterRoleBindingGeneralTargetReader.Subjects[1].Namespace = "kube-system"
				clusterRoleBindingGeneralTargetReader.Subjects[2].Namespace = "kube-system"

				Expect(string(managedResourceSecret.Data["clusterrole____gardener.cloud_vpa_target_actor.yaml"])).To(Equal(serialize(clusterRoleGeneralActor)))
				Expect(string(managedResourceSecret.Data["clusterrolebinding____gardener.cloud_vpa_target_actor.yaml"])).To(Equal(serialize(clusterRoleBindingGeneralActor)))
				Expect(string(managedResourceSecret.Data["clusterrole____gardener.cloud_vpa_target_target-reader.yaml"])).To(Equal(serialize(clusterRoleGeneralTargetReader)))
				Expect(string(managedResourceSecret.Data["clusterrolebinding____gardener.cloud_vpa_target_target-reader.yaml"])).To(Equal(serialize(clusterRoleBindingGeneralTargetReader)))
				Expect(string(managedResourceSecret.Data["mutatingwebhookconfiguration____vpa-webhook-config-target.yaml"])).To(Equal(serialize(mutatingWebhookConfiguration)))
				Expect(string(managedResourceSecret.Data["crd-verticalpodautoscalercheckpoints.yaml"])).To(Equal(crdVPACheckpoints))
				Expect(string(managedResourceSecret.Data["crd-verticalpodautoscalers.yaml"])).To(Equal(crdVPA))
			})

			It("should delete the legacy resources", func() {
				legacyTLSCertsSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "vpa-tls-certs", Namespace: namespace}}
				Expect(c.Create(ctx, legacyTLSCertsSecret)).To(Succeed())

				Expect(component.Deploy(ctx)).To(Succeed())

				Expect(c.Get(ctx, client.ObjectKeyFromObject(legacyTLSCertsSecret), &corev1.Secret{})).To(BeNotFoundError())
			})
		})
	})

	Describe("#Destroy", func() {
		Context("cluster type seed", func() {
			BeforeEach(func() {
				component = New(c, namespace, nil, Values{ClusterType: ClusterTypeSeed})
				managedResourceName = "vpa"
			})

			It("should successfully destroy all resources", func() {
				Expect(c.Create(ctx, managedResource)).To(Succeed())
				Expect(c.Create(ctx, managedResourceSecret)).To(Succeed())

				Expect(component.Destroy(ctx)).To(Succeed())

				Expect(c.Get(ctx, client.ObjectKeyFromObject(managedResource), managedResource)).To(MatchError(apierrors.NewNotFound(schema.GroupResource{Group: resourcesv1alpha1.SchemeGroupVersion.Group, Resource: "managedresources"}, managedResource.Name)))
				Expect(c.Get(ctx, client.ObjectKeyFromObject(managedResourceSecret), managedResourceSecret)).To(MatchError(apierrors.NewNotFound(schema.GroupResource{Group: corev1.SchemeGroupVersion.Group, Resource: "secrets"}, managedResourceSecret.Name)))
			})
		})

		Context("cluster type shoot", func() {
			BeforeEach(func() {
				component = New(c, namespace, nil, Values{ClusterType: ClusterTypeShoot})
				managedResourceName = "shoot-core-vpa"
			})

			It("should successfully destroy all resources", func() {
				Expect(c.Create(ctx, managedResource)).To(Succeed())
				Expect(c.Create(ctx, managedResourceSecret)).To(Succeed())

				By("creating vpa-exporter runtime resources")
				Expect(c.Create(ctx, serviceExporter)).To(Succeed())
				Expect(c.Create(ctx, deploymentExporter)).To(Succeed())
				Expect(c.Create(ctx, vpaExporter)).To(Succeed())

				By("creating vpa-updater runtime resources")
				Expect(c.Create(ctx, deploymentUpdaterFor(true, nil, nil, nil, nil, nil))).To(Succeed())
				Expect(c.Create(ctx, vpaUpdater)).To(Succeed())

				By("creating vpa-recommender runtime resources")
				Expect(c.Create(ctx, deploymentRecommenderFor(true, nil, nil))).To(Succeed())
				Expect(c.Create(ctx, vpaRecommender)).To(Succeed())

				By("creating vpa-admission-controller runtime resources")
				Expect(c.Create(ctx, serviceAdmissionController)).To(Succeed())
				Expect(c.Create(ctx, networkPolicyAdmissionController)).To(Succeed())
				Expect(c.Create(ctx, deploymentAdmissionControllerFor(true))).To(Succeed())
				Expect(c.Create(ctx, vpaAdmissionController)).To(Succeed())

				Expect(component.Destroy(ctx)).To(Succeed())

				Expect(c.Get(ctx, client.ObjectKeyFromObject(managedResource), managedResource)).To(MatchError(apierrors.NewNotFound(schema.GroupResource{Group: resourcesv1alpha1.SchemeGroupVersion.Group, Resource: "managedresources"}, managedResource.Name)))
				Expect(c.Get(ctx, client.ObjectKeyFromObject(managedResourceSecret), managedResourceSecret)).To(MatchError(apierrors.NewNotFound(schema.GroupResource{Group: corev1.SchemeGroupVersion.Group, Resource: "secrets"}, managedResourceSecret.Name)))

				By("checking vpa-exporter runtime resources were not deleted")
				Expect(c.Get(ctx, client.ObjectKeyFromObject(serviceExporter), &corev1.Service{})).To(Succeed())
				Expect(c.Get(ctx, client.ObjectKeyFromObject(deploymentExporter), &appsv1.Deployment{})).To(Succeed())
				Expect(c.Get(ctx, client.ObjectKeyFromObject(vpaExporter), &vpaautoscalingv1.VerticalPodAutoscaler{})).To(Succeed())

				By("checking vpa-updater runtime resources")
				Expect(c.Get(ctx, client.ObjectKeyFromObject(deploymentUpdaterFor(true, nil, nil, nil, nil, nil)), &appsv1.Deployment{})).To(BeNotFoundError())
				Expect(c.Get(ctx, client.ObjectKeyFromObject(vpaUpdater), &vpaautoscalingv1.VerticalPodAutoscaler{})).To(BeNotFoundError())

				By("checking vpa-recommender runtime resources")
				Expect(c.Get(ctx, client.ObjectKeyFromObject(deploymentRecommenderFor(true, nil, nil)), &appsv1.Deployment{})).To(BeNotFoundError())
				Expect(c.Get(ctx, client.ObjectKeyFromObject(vpaRecommender), &vpaautoscalingv1.VerticalPodAutoscaler{})).To(BeNotFoundError())

				By("checking vpa-admission-controller runtime resources")
				Expect(c.Get(ctx, client.ObjectKeyFromObject(serviceAdmissionController), &corev1.Service{})).To(BeNotFoundError())
				Expect(c.Get(ctx, client.ObjectKeyFromObject(networkPolicyAdmissionController), &networkingv1.NetworkPolicy{})).To(BeNotFoundError())
				Expect(c.Get(ctx, client.ObjectKeyFromObject(deploymentAdmissionControllerFor(true)), &appsv1.Deployment{})).To(BeNotFoundError())
				Expect(c.Get(ctx, client.ObjectKeyFromObject(vpaAdmissionController), &vpaautoscalingv1.VerticalPodAutoscaler{})).To(BeNotFoundError())
			})
		})
	})

	Context("waiting functions", func() {
		var (
			fakeOps   *retryfake.Ops
			resetVars func()
		)

		BeforeEach(func() {
			fakeOps = &retryfake.Ops{MaxAttempts: 1}
			resetVars = test.WithVars(
				&retry.Until, fakeOps.Until,
				&retry.UntilTimeout, fakeOps.UntilTimeout,
			)
		})

		AfterEach(func() {
			resetVars()
		})

		Describe("#Wait", func() {
			tests := func(managedResourceName string) {
				It("should fail because reading the ManagedResource fails", func() {
					Expect(component.Wait(ctx)).To(MatchError(ContainSubstring("not found")))
				})

				It("should fail because the ManagedResource doesn't become healthy", func() {
					fakeOps.MaxAttempts = 2

					Expect(c.Create(ctx, &resourcesv1alpha1.ManagedResource{
						ObjectMeta: metav1.ObjectMeta{
							Name:       managedResourceName,
							Namespace:  namespace,
							Generation: 1,
						},
						Status: resourcesv1alpha1.ManagedResourceStatus{
							ObservedGeneration: 1,
							Conditions: []gardencorev1beta1.Condition{
								{
									Type:   resourcesv1alpha1.ResourcesApplied,
									Status: gardencorev1beta1.ConditionFalse,
								},
								{
									Type:   resourcesv1alpha1.ResourcesHealthy,
									Status: gardencorev1beta1.ConditionFalse,
								},
							},
						},
					}))

					Expect(component.Wait(ctx)).To(MatchError(ContainSubstring("is not healthy")))
				})

				It("should successfully wait for the managed resource to become healthy", func() {
					fakeOps.MaxAttempts = 2

					Expect(c.Create(ctx, &resourcesv1alpha1.ManagedResource{
						ObjectMeta: metav1.ObjectMeta{
							Name:       managedResourceName,
							Namespace:  namespace,
							Generation: 1,
						},
						Status: resourcesv1alpha1.ManagedResourceStatus{
							ObservedGeneration: 1,
							Conditions: []gardencorev1beta1.Condition{
								{
									Type:   resourcesv1alpha1.ResourcesApplied,
									Status: gardencorev1beta1.ConditionTrue,
								},
								{
									Type:   resourcesv1alpha1.ResourcesHealthy,
									Status: gardencorev1beta1.ConditionTrue,
								},
							},
						},
					}))

					Expect(component.Wait(ctx)).To(Succeed())
				})
			}

			Context("cluster type seed", func() {
				BeforeEach(func() {
					component = New(c, namespace, nil, Values{ClusterType: ClusterTypeSeed})
				})

				tests("vpa")
			})

			Context("cluster type shoot", func() {
				BeforeEach(func() {
					component = New(c, namespace, nil, Values{ClusterType: ClusterTypeShoot})
				})

				tests("shoot-core-vpa")
			})
		})

		Describe("#WaitCleanup", func() {
			tests := func(managedResourceName string) {
				It("should fail when the wait for the managed resource deletion times out", func() {
					fakeOps.MaxAttempts = 2

					managedResource := &resourcesv1alpha1.ManagedResource{
						ObjectMeta: metav1.ObjectMeta{
							Name:      managedResourceName,
							Namespace: namespace,
						},
					}

					Expect(c.Create(ctx, managedResource)).To(Succeed())

					Expect(component.WaitCleanup(ctx)).To(MatchError(ContainSubstring("still exists")))
				})

				It("should not return an error when it's already removed", func() {
					Expect(component.WaitCleanup(ctx)).To(Succeed())
				})
			}

			Context("cluster type seed", func() {
				BeforeEach(func() {
					component = New(c, namespace, nil, Values{ClusterType: ClusterTypeSeed})
				})

				tests("vpa")
			})

			Context("cluster type shoot", func() {
				BeforeEach(func() {
					component = New(c, namespace, nil, Values{ClusterType: ClusterTypeShoot})
				})

				tests("shoot-core-vpa")
			})
		})
	})
})

func serialize(obj client.Object) string {
	var (
		scheme        = kubernetes.SeedScheme
		groupVersions []schema.GroupVersion
	)

	for k := range scheme.AllKnownTypes() {
		groupVersions = append(groupVersions, k.GroupVersion())
	}

	var (
		ser   = json.NewSerializerWithOptions(json.DefaultMetaFactory, scheme, scheme, json.SerializerOptions{Yaml: true, Pretty: false, Strict: false})
		codec = serializer.NewCodecFactory(scheme).CodecForVersions(ser, ser, schema.GroupVersions(groupVersions), schema.GroupVersions(groupVersions))
	)

	serializationYAML, err := runtime.Encode(codec, obj)
	Expect(err).NotTo(HaveOccurred())

	return string(serializationYAML)
}

func replaceTargetSubstrings(in string) string {
	return strings.Replace(in, ":target:", ":source:", -1)
}

func dropNetworkingLabels(labels map[string]string) {
	for k := range labels {
		if k == "networking.gardener.cloud/from-prometheus" {
			continue
		}

		if strings.HasPrefix(k, "networking.gardener.cloud/") {
			delete(labels, k)
		}
	}
}

const (
	crdVPACheckpoints = `---
# Source: https://github.com/kubernetes/autoscaler/blob/master/vertical-pod-autoscaler/deploy/vpa-v1-crd-gen.yaml
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: verticalpodautoscalercheckpoints.autoscaling.k8s.io
  annotations:
    api-approved.kubernetes.io: https://github.com/kubernetes/kubernetes/pull/63797
    resources.gardener.cloud/keep-object: "true"
  labels:
    gardener.cloud/role: vpa
spec:
  group: autoscaling.k8s.io
  names:
    kind: VerticalPodAutoscalerCheckpoint
    listKind: VerticalPodAutoscalerCheckpointList
    plural: verticalpodautoscalercheckpoints
    shortNames:
    - vpacheckpoint
    singular: verticalpodautoscalercheckpoint
  scope: Namespaced
  versions:
  - name: v1
    schema:
      openAPIV3Schema:
        description: VerticalPodAutoscalerCheckpoint is the checkpoint of the internal
          state of VPA that is used for recovery after recommender's restart.
        properties:
          apiVersion:
            description: 'APIVersion defines the versioned schema of this representation
              of an object. Servers should convert recognized schemas to the latest
              internal value, and may reject unrecognized values. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources'
            type: string
          kind:
            description: 'Kind is a string value representing the REST resource this
              object represents. Servers may infer this from the endpoint the client
              submits requests to. Cannot be updated. In CamelCase. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds'
            type: string
          metadata:
            type: object
          spec:
            description: 'Specification of the checkpoint. More info: https://git.k8s.io/community/contributors/devel/api-conventions.md#spec-and-status.'
            properties:
              containerName:
                description: Name of the checkpointed container.
                type: string
              vpaObjectName:
                description: Name of the VPA object that stored VerticalPodAutoscalerCheckpoint
                  object.
                type: string
            type: object
          status:
            description: Data of the checkpoint.
            properties:
              cpuHistogram:
                description: Checkpoint of histogram for consumption of CPU.
                properties:
                  bucketWeights:
                    description: Map from bucket index to bucket weight.
                    type: object
                    x-kubernetes-preserve-unknown-fields: true
                  referenceTimestamp:
                    description: Reference timestamp for samples collected within
                      this histogram.
                    format: date-time
                    nullable: true
                    type: string
                  totalWeight:
                    description: Sum of samples to be used as denominator for weights
                      from BucketWeights.
                    type: number
                type: object
              firstSampleStart:
                description: Timestamp of the fist sample from the histograms.
                format: date-time
                nullable: true
                type: string
              lastSampleStart:
                description: Timestamp of the last sample from the histograms.
                format: date-time
                nullable: true
                type: string
              lastUpdateTime:
                description: The time when the status was last refreshed.
                format: date-time
                nullable: true
                type: string
              memoryHistogram:
                description: Checkpoint of histogram for consumption of memory.
                properties:
                  bucketWeights:
                    description: Map from bucket index to bucket weight.
                    type: object
                    x-kubernetes-preserve-unknown-fields: true
                  referenceTimestamp:
                    description: Reference timestamp for samples collected within
                      this histogram.
                    format: date-time
                    nullable: true
                    type: string
                  totalWeight:
                    description: Sum of samples to be used as denominator for weights
                      from BucketWeights.
                    type: number
                type: object
              totalSamplesCount:
                description: Total number of samples in the histograms.
                type: integer
              version:
                description: Version of the format of the stored data.
                type: string
            type: object
        type: object
    served: true
    storage: true
  - name: v1beta2
    schema:
      openAPIV3Schema:
        description: VerticalPodAutoscalerCheckpoint is the checkpoint of the internal
          state of VPA that is used for recovery after recommender's restart.
        properties:
          apiVersion:
            description: 'APIVersion defines the versioned schema of this representation
              of an object. Servers should convert recognized schemas to the latest
              internal value, and may reject unrecognized values. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources'
            type: string
          kind:
            description: 'Kind is a string value representing the REST resource this
              object represents. Servers may infer this from the endpoint the client
              submits requests to. Cannot be updated. In CamelCase. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds'
            type: string
          metadata:
            type: object
          spec:
            description: 'Specification of the checkpoint. More info: https://git.k8s.io/community/contributors/devel/api-conventions.md#spec-and-status.'
            properties:
              containerName:
                description: Name of the checkpointed container.
                type: string
              vpaObjectName:
                description: Name of the VPA object that stored VerticalPodAutoscalerCheckpoint
                  object.
                type: string
            type: object
          status:
            description: Data of the checkpoint.
            properties:
              cpuHistogram:
                description: Checkpoint of histogram for consumption of CPU.
                properties:
                  bucketWeights:
                    description: Map from bucket index to bucket weight.
                    type: object
                    x-kubernetes-preserve-unknown-fields: true
                  referenceTimestamp:
                    description: Reference timestamp for samples collected within
                      this histogram.
                    format: date-time
                    nullable: true
                    type: string
                  totalWeight:
                    description: Sum of samples to be used as denominator for weights
                      from BucketWeights.
                    type: number
                type: object
              firstSampleStart:
                description: Timestamp of the fist sample from the histograms.
                format: date-time
                nullable: true
                type: string
              lastSampleStart:
                description: Timestamp of the last sample from the histograms.
                format: date-time
                nullable: true
                type: string
              lastUpdateTime:
                description: The time when the status was last refreshed.
                format: date-time
                nullable: true
                type: string
              memoryHistogram:
                description: Checkpoint of histogram for consumption of memory.
                properties:
                  bucketWeights:
                    description: Map from bucket index to bucket weight.
                    type: object
                    x-kubernetes-preserve-unknown-fields: true
                  referenceTimestamp:
                    description: Reference timestamp for samples collected within
                      this histogram.
                    format: date-time
                    nullable: true
                    type: string
                  totalWeight:
                    description: Sum of samples to be used as denominator for weights
                      from BucketWeights.
                    type: number
                type: object
              totalSamplesCount:
                description: Total number of samples in the histograms.
                type: integer
              version:
                description: Version of the format of the stored data.
                type: string
            type: object
        type: object
    served: true
    storage: false
`

	crdVPA = `---
# For backwards compatibility it's not possible to take the community based VPA definition since it tightens
# the validation too much. Instead, the CRD needs to retain and permit unknown fields as it used to be with
# the ` + "`v1beta1`" + ` CRD version.
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: verticalpodautoscalers.autoscaling.k8s.io
  annotations:
    api-approved.kubernetes.io: https://github.com/kubernetes/kubernetes/pull/63797
    resources.gardener.cloud/keep-object: "true"
  labels:
    gardener.cloud/role: vpa
spec:
  group: autoscaling.k8s.io
  names:
    kind: VerticalPodAutoscaler
    listKind: VerticalPodAutoscalerList
    plural: verticalpodautoscalers
    shortNames:
    - vpa
    singular: verticalpodautoscaler
  scope: Namespaced
  versions:
  - name: v1
    schema:
      openAPIV3Schema:
        type: object
        x-kubernetes-preserve-unknown-fields: true
        properties:
          spec:
            type: object
            x-kubernetes-preserve-unknown-fields: true
            required: []
            properties:
              targetRef:
                type: object
                x-kubernetes-preserve-unknown-fields: true
              updatePolicy:
                type: object
                x-kubernetes-preserve-unknown-fields: true
                properties:
                  updateMode:
                    type: string
              resourcePolicy:
                type: object
                x-kubernetes-preserve-unknown-fields: true
                properties:
                  containerPolicies:
                    type: array
                    items:
                      type: object
                      x-kubernetes-preserve-unknown-fields: true
                      properties:
                        containerName:
                          type: string
                        mode:
                          type: string
                          enum: ["Auto", "Off"]
                        minAllowed:
                          type: object
                          x-kubernetes-preserve-unknown-fields: true
                        maxAllowed:
                          type: object
                          x-kubernetes-preserve-unknown-fields: true
                        controlledResources:
                          type: array
                          items:
                            type: string
                            enum: ["cpu", "memory"]
    served: true
    storage: true
  - name: v1beta2
    schema:
      openAPIV3Schema:
        type: object
        x-kubernetes-preserve-unknown-fields: true
        properties:
          spec:
            type: object
            x-kubernetes-preserve-unknown-fields: true
            required: []
            properties:
              targetRef:
                type: object
                x-kubernetes-preserve-unknown-fields: true
              updatePolicy:
                type: object
                x-kubernetes-preserve-unknown-fields: true
                properties:
                  updateMode:
                    type: string
              resourcePolicy:
                type: object
                x-kubernetes-preserve-unknown-fields: true
                properties:
                  containerPolicies:
                    type: array
                    items:
                      type: object
                      x-kubernetes-preserve-unknown-fields: true
                      properties:
                        containerName:
                          type: string
                        mode:
                          type: string
                          enum: ["Auto", "Off"]
                        minAllowed:
                          type: object
                          x-kubernetes-preserve-unknown-fields: true
                        maxAllowed:
                          type: object
                          x-kubernetes-preserve-unknown-fields: true
                        controlledResources:
                          type: array
                          items:
                            type: string
                            enum: ["cpu", "memory"]
    served: true
    storage: false
`
)
