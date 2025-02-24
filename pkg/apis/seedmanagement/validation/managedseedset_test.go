// Copyright (c) 2021 SAP SE or an SAP affiliate company. All rights reserved. This file is licensed under the Apache Software License, v. 2 except as noted otherwise in the LICENSE file
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

package validation_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
	gomegatypes "github.com/onsi/gomega/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/pointer"

	"github.com/gardener/gardener/pkg/apis/core"
	"github.com/gardener/gardener/pkg/apis/seedmanagement"
	. "github.com/gardener/gardener/pkg/apis/seedmanagement/validation"
)

var _ = Describe("ManagedSeedSet Validation Tests", func() {
	var (
		managedSeed = &seedmanagement.ManagedSeed{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{
					"foo": "bar",
				},
			},
			Spec: seedmanagement.ManagedSeedSpec{
				SeedTemplate: &core.SeedTemplate{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"foo": "bar",
						},
					},
				},
			},
		}
		shoot = &core.Shoot{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{
					"foo": "bar",
				},
			},
			Spec: core.ShootSpec{
				CloudProfileName: "foo",
				Kubernetes: core.Kubernetes{
					Version: "1.18.14",
				},
				Networking: core.Networking{
					Type: "foo",
				},
				Provider: core.Provider{
					Type: "foo",
					Workers: []core.Worker{
						{
							Name: "some-worker",
							Machine: core.Machine{
								Type:         "some-machine-type",
								Architecture: pointer.String("amd64"),
							},
							Maximum: 2,
							Minimum: 1,
						},
					},
				},
				Region:            "some-region",
				SecretBindingName: "shoot-operator-foo",
			},
		}

		managedSeedSet *seedmanagement.ManagedSeedSet
	)

	BeforeEach(func() {
		managedSeedSet = &seedmanagement.ManagedSeedSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:       name,
				Namespace:  namespace,
				Generation: 1,
			},
			Spec: seedmanagement.ManagedSeedSetSpec{
				Replicas: pointer.Int32(1),
				Selector: *metav1.SetAsLabelSelector(labels.Set{
					"foo": "bar",
				}),
				Template: seedmanagement.ManagedSeedTemplate{
					ObjectMeta: managedSeed.ObjectMeta,
					Spec:       managedSeed.Spec,
				},
				ShootTemplate: core.ShootTemplate{
					ObjectMeta: shoot.ObjectMeta,
					Spec:       shoot.Spec,
				},
				UpdateStrategy: &seedmanagement.UpdateStrategy{
					Type: updateStrategyTypePtr(seedmanagement.RollingUpdateStrategyType),
					RollingUpdate: &seedmanagement.RollingUpdateStrategy{
						Partition: pointer.Int32(0),
					},
				},
				RevisionHistoryLimit: pointer.Int32(10),
			},
			Status: seedmanagement.ManagedSeedSetStatus{
				ObservedGeneration: 1,
				Replicas:           1,
				ReadyReplicas:      1,
				NextReplicaNumber:  2,
				CurrentReplicas:    0,
				UpdatedReplicas:    1,
				CollisionCount:     pointer.Int32(1),
			},
		}
	})

	Describe("#ValidateManagedSeedSet", func() {
		It("should allow valid resources", func() {
			errorList := ValidateManagedSeedSet(managedSeedSet)

			Expect(errorList).To(HaveLen(0))
		})

		DescribeTable("ManagedSeedSet metadata",
			func(objectMeta metav1.ObjectMeta, matcher gomegatypes.GomegaMatcher) {
				managedSeedSet.ObjectMeta = objectMeta

				errorList := ValidateManagedSeedSet(managedSeedSet)

				Expect(errorList).To(matcher)
			},

			Entry("should forbid ManagedSeedSet with empty metadata",
				metav1.ObjectMeta{},
				ConsistOf(
					PointTo(MatchFields(IgnoreExtras, Fields{
						"Type":  Equal(field.ErrorTypeRequired),
						"Field": Equal("metadata.name"),
					})),
					PointTo(MatchFields(IgnoreExtras, Fields{
						"Type":  Equal(field.ErrorTypeRequired),
						"Field": Equal("metadata.namespace"),
					})),
					PointTo(MatchFields(IgnoreExtras, Fields{
						"Type":  Equal(field.ErrorTypeInvalid),
						"Field": Equal("metadata.namespace"),
					})),
				),
			),
			Entry("should forbid ManagedSeedSet with empty name",
				metav1.ObjectMeta{Name: "", Namespace: namespace},
				ConsistOf(PointTo(MatchFields(IgnoreExtras, Fields{
					"Type":  Equal(field.ErrorTypeRequired),
					"Field": Equal("metadata.name"),
				}))),
			),
			Entry("should allow ManagedSeedSet with '.' in the name",
				metav1.ObjectMeta{Name: "managedseedset.test", Namespace: namespace},
				BeEmpty(),
			),
			Entry("should forbid ManagedSeedSet with '_' in the name (not a DNS-1123 label compliant name)",
				metav1.ObjectMeta{Name: "managedseedset_test", Namespace: namespace},
				ConsistOf(PointTo(MatchFields(IgnoreExtras, Fields{
					"Type":  Equal(field.ErrorTypeInvalid),
					"Field": Equal("metadata.name"),
				}))),
			),
			Entry("should forbid ManagedSeedSet with namespace different from garden",
				metav1.ObjectMeta{Name: name, Namespace: "foo"},
				ConsistOf(PointTo(MatchFields(IgnoreExtras, Fields{
					"Type":  Equal(field.ErrorTypeInvalid),
					"Field": Equal("metadata.namespace"),
				}))),
			),
		)

		It("should forbid negative replicas, updateStrategy.rollingUpdate.partition, and revisionHistoryLimit", func() {
			managedSeedSet.Spec.Replicas = pointer.Int32(-1)
			managedSeedSet.Spec.UpdateStrategy.RollingUpdate.Partition = pointer.Int32(-1)
			managedSeedSet.Spec.RevisionHistoryLimit = pointer.Int32(-1)

			errorList := ValidateManagedSeedSet(managedSeedSet)

			Expect(errorList).To(ConsistOf(
				PointTo(MatchFields(IgnoreExtras, Fields{
					"Type":  Equal(field.ErrorTypeInvalid),
					"Field": Equal("spec.replicas"),
				})),
				PointTo(MatchFields(IgnoreExtras, Fields{
					"Type":  Equal(field.ErrorTypeInvalid),
					"Field": Equal("spec.updateStrategy.rollingUpdate.partition"),
				})),
				PointTo(MatchFields(IgnoreExtras, Fields{
					"Type":  Equal(field.ErrorTypeInvalid),
					"Field": Equal("spec.revisionHistoryLimit"),
				})),
			))
		})

		It("should forbid empty selector", func() {
			managedSeedSet.Spec.Selector = metav1.LabelSelector{}

			errorList := ValidateManagedSeedSet(managedSeedSet)

			Expect(errorList).To(ConsistOf(
				PointTo(MatchFields(IgnoreExtras, Fields{
					"Type":  Equal(field.ErrorTypeInvalid),
					"Field": Equal("spec.selector"),
				})),
			))
		})

		It("should forbid empty updateStrategy.type", func() {
			managedSeedSet.Spec.UpdateStrategy.Type = updateStrategyTypePtr("")

			errorList := ValidateManagedSeedSet(managedSeedSet)

			Expect(errorList).To(ConsistOf(
				PointTo(MatchFields(IgnoreExtras, Fields{
					"Type":  Equal(field.ErrorTypeRequired),
					"Field": Equal("spec.updateStrategy.type"),
				})),
			))
		})

		It("should forbid unsupported updateStrategy.type", func() {
			managedSeedSet.Spec.UpdateStrategy.Type = updateStrategyTypePtr("OnDelete")

			errorList := ValidateManagedSeedSet(managedSeedSet)

			Expect(errorList).To(ConsistOf(
				PointTo(MatchFields(IgnoreExtras, Fields{
					"Type":  Equal(field.ErrorTypeNotSupported),
					"Field": Equal("spec.updateStrategy.type"),
				})),
			))
		})

		It("should forbid templates if labels don't match selector", func() {
			managedSeedSet.Spec.Selector = *metav1.SetAsLabelSelector(labels.Set{
				"bar": "baz",
			})

			errorList := ValidateManagedSeedSet(managedSeedSet)

			Expect(errorList).To(ConsistOf(
				PointTo(MatchFields(IgnoreExtras, Fields{
					"Type":  Equal(field.ErrorTypeInvalid),
					"Field": Equal("spec.template.metadata.labels"),
				})),
				PointTo(MatchFields(IgnoreExtras, Fields{
					"Type":  Equal(field.ErrorTypeInvalid),
					"Field": Equal("spec.template.spec.seedTemplate.metadata.labels"),
				})),
				PointTo(MatchFields(IgnoreExtras, Fields{
					"Type":  Equal(field.ErrorTypeInvalid),
					"Field": Equal("spec.shootTemplate.metadata.labels"),
				})),
			))
		})

		It("should forbid empty or invalid fields in template", func() {
			managedSeedCopy := managedSeed.DeepCopy()
			managedSeedCopy.Spec.Shoot = &seedmanagement.Shoot{}
			managedSeedCopy.Spec.SeedTemplate.Name = "foo"
			managedSeedCopy.Spec.SeedTemplate.Spec.Networks.Nodes = pointer.String("")
			managedSeedSet.Spec.Template.Spec = managedSeedCopy.Spec

			errorList := ValidateManagedSeedSet(managedSeedSet)

			Expect(errorList).To(ConsistOf(
				PointTo(MatchFields(IgnoreExtras, Fields{
					"Type":  Equal(field.ErrorTypeForbidden),
					"Field": Equal("spec.template.spec.shoot"),
				})),
				PointTo(MatchFields(IgnoreExtras, Fields{
					"Type":  Equal(field.ErrorTypeForbidden),
					"Field": Equal("spec.template.spec.seedTemplate.metadata.name"),
				})),
				PointTo(MatchFields(IgnoreExtras, Fields{
					"Type":  Equal(field.ErrorTypeInvalid),
					"Field": Equal("spec.template.spec.seedTemplate.spec.networks.nodes"),
				})),
			))
		})

		It("should forbid empty or invalid fields in shootTemplate", func() {
			shootCopy := shoot.DeepCopy()
			shootCopy.Spec.Provider.Type = ""
			managedSeedSet.Spec.ShootTemplate.Spec = shootCopy.Spec

			errorList := ValidateManagedSeedSet(managedSeedSet)

			Expect(errorList).To(ConsistOf(
				PointTo(MatchFields(IgnoreExtras, Fields{
					"Type":  Equal(field.ErrorTypeRequired),
					"Field": Equal("spec.shootTemplate.spec.provider.type"),
				})),
			))
		})
	})

	Describe("#ValidateManagedSeedSetUpdate", func() {
		var (
			newManagedSeedSet *seedmanagement.ManagedSeedSet
		)

		BeforeEach(func() {
			newManagedSeedSet = managedSeedSet.DeepCopy()
			newManagedSeedSet.ResourceVersion = "1"
		})

		It("should allow valid updates", func() {
			errorList := ValidateManagedSeedSetUpdate(newManagedSeedSet, managedSeedSet)

			Expect(errorList).To(HaveLen(0))
		})

		It("should forbid changes to immutable metadata fields", func() {
			newManagedSeedSet.Name = name + "x"

			errorList := ValidateManagedSeedSetUpdate(newManagedSeedSet, managedSeedSet)

			Expect(errorList).To(ConsistOf(
				PointTo(MatchFields(IgnoreExtras, Fields{
					"Type":   Equal(field.ErrorTypeInvalid),
					"Field":  Equal("metadata.name"),
					"Detail": Equal("field is immutable"),
				})),
			))
		})

		It("should forbid changes to immutable spec fields", func() {
			newManagedSeedSet.Spec.Selector = *metav1.SetAsLabelSelector(labels.Set{
				"bar": "baz",
			})
			newManagedSeedSet.Spec.RevisionHistoryLimit = pointer.Int32(20)

			errorList := ValidateManagedSeedSetUpdate(newManagedSeedSet, managedSeedSet)

			Expect(errorList).To(ConsistOf(
				PointTo(MatchFields(IgnoreExtras, Fields{
					"Type":   Equal(field.ErrorTypeInvalid),
					"Field":  Equal("spec.selector"),
					"Detail": Equal("field is immutable"),
				})),
				PointTo(MatchFields(IgnoreExtras, Fields{
					"Type":   Equal(field.ErrorTypeInvalid),
					"Field":  Equal("spec.revisionHistoryLimit"),
					"Detail": Equal("field is immutable"),
				})),
				PointTo(MatchFields(IgnoreExtras, Fields{
					"Type":  Equal(field.ErrorTypeInvalid),
					"Field": Equal("spec.template.metadata.labels"),
				})),
				PointTo(MatchFields(IgnoreExtras, Fields{
					"Type":  Equal(field.ErrorTypeInvalid),
					"Field": Equal("spec.template.spec.seedTemplate.metadata.labels"),
				})),
				PointTo(MatchFields(IgnoreExtras, Fields{
					"Type":  Equal(field.ErrorTypeInvalid),
					"Field": Equal("spec.shootTemplate.metadata.labels"),
				})),
			))
		})

		It("should forbid changes to immutable fields in template", func() {
			newManagedSeedSet.Spec.Template.Spec.SeedTemplate = nil
			newManagedSeedSet.Spec.Template.Spec.Gardenlet = &seedmanagement.Gardenlet{}

			errorList := ValidateManagedSeedSetUpdate(newManagedSeedSet, managedSeedSet)

			Expect(errorList).To(ConsistOf(
				PointTo(MatchFields(IgnoreExtras, Fields{
					"Type":   Equal(field.ErrorTypeInvalid),
					"Field":  Equal("spec.template.spec"),
					"Detail": Equal("changing from seed template to gardenlet and vice versa is not allowed"),
				})),
			))
		})

		It("should forbid changes to immutable fields in shootTemplate", func() {
			shootCopy := shoot.DeepCopy()
			shootCopy.Spec.Region = "other-region"
			newManagedSeedSet.Spec.ShootTemplate.Spec = shootCopy.Spec

			errorList := ValidateManagedSeedSetUpdate(newManagedSeedSet, managedSeedSet)

			Expect(errorList).To(ConsistOf(
				PointTo(MatchFields(IgnoreExtras, Fields{
					"Type":   Equal(field.ErrorTypeInvalid),
					"Field":  Equal("spec.shootTemplate.spec.region"),
					"Detail": Equal("field is immutable"),
				})),
			))
		})
	})

	Describe("#ValidateManagedSeedSetStatusUpdate", func() {
		var (
			newManagedSeedSet *seedmanagement.ManagedSeedSet
		)

		BeforeEach(func() {
			newManagedSeedSet = managedSeedSet.DeepCopy()
			newManagedSeedSet.ResourceVersion = "1"
		})

		It("should allow valid status updates", func() {
			errorList := ValidateManagedSeedSetStatusUpdate(newManagedSeedSet, managedSeedSet)

			Expect(errorList).To(HaveLen(0))
		})

		It("should forbid negative integer fields", func() {
			newManagedSeedSet.Status.ObservedGeneration = -1
			newManagedSeedSet.Status.Replicas = -1
			newManagedSeedSet.Status.ReadyReplicas = -1
			newManagedSeedSet.Status.CurrentReplicas = -1
			newManagedSeedSet.Status.UpdatedReplicas = -1

			errorList := ValidateManagedSeedSetStatusUpdate(newManagedSeedSet, managedSeedSet)

			Expect(errorList).To(ConsistOf(
				PointTo(MatchFields(IgnoreExtras, Fields{
					"Type":  Equal(field.ErrorTypeInvalid),
					"Field": Equal("status.observedGeneration"),
				})),
				PointTo(MatchFields(IgnoreExtras, Fields{
					"Type":  Equal(field.ErrorTypeInvalid),
					"Field": Equal("status.replicas"),
				})),
				PointTo(MatchFields(IgnoreExtras, Fields{
					"Type":  Equal(field.ErrorTypeInvalid),
					"Field": Equal("status.readyReplicas"),
				})),
				PointTo(MatchFields(IgnoreExtras, Fields{
					"Type":  Equal(field.ErrorTypeInvalid),
					"Field": Equal("status.currentReplicas"),
				})),
				PointTo(MatchFields(IgnoreExtras, Fields{
					"Type":  Equal(field.ErrorTypeInvalid),
					"Field": Equal("status.updatedReplicas"),
				})),
			))
		})

		It("should forbid invalid number of ready, current, or update replicas", func() {
			newManagedSeedSet.Status.ReadyReplicas = 2
			newManagedSeedSet.Status.CurrentReplicas = 2
			newManagedSeedSet.Status.UpdatedReplicas = 2

			errorList := ValidateManagedSeedSetStatusUpdate(newManagedSeedSet, managedSeedSet)

			Expect(errorList).To(ConsistOf(
				PointTo(MatchFields(IgnoreExtras, Fields{
					"Type":  Equal(field.ErrorTypeInvalid),
					"Field": Equal("status.readyReplicas"),
				})),
				PointTo(MatchFields(IgnoreExtras, Fields{
					"Type":  Equal(field.ErrorTypeInvalid),
					"Field": Equal("status.currentReplicas"),
				})),
				PointTo(MatchFields(IgnoreExtras, Fields{
					"Type":  Equal(field.ErrorTypeInvalid),
					"Field": Equal("status.updatedReplicas"),
				})),
			))
		})

		It("should forbid decrementing the next replica number or the collision count", func() {
			newManagedSeedSet.Status.NextReplicaNumber = 1
			newManagedSeedSet.Status.CollisionCount = pointer.Int32(0)

			errorList := ValidateManagedSeedSetStatusUpdate(newManagedSeedSet, managedSeedSet)

			Expect(errorList).To(ConsistOf(
				PointTo(MatchFields(IgnoreExtras, Fields{
					"Type":  Equal(field.ErrorTypeInvalid),
					"Field": Equal("status.nextReplicaNumber"),
				})),
				PointTo(MatchFields(IgnoreExtras, Fields{
					"Type":  Equal(field.ErrorTypeInvalid),
					"Field": Equal("status.collisionCount"),
				})),
			))
		})

		It("should forbid invalid pending replica", func() {
			newManagedSeedSet.Status.PendingReplica = &seedmanagement.PendingReplica{
				Name:    "foo",
				Reason:  "unknown",
				Retries: pointer.Int32(-1),
			}

			errorList := ValidateManagedSeedSetStatusUpdate(newManagedSeedSet, managedSeedSet)

			Expect(errorList).To(ConsistOf(
				PointTo(MatchFields(IgnoreExtras, Fields{
					"Type":  Equal(field.ErrorTypeInvalid),
					"Field": Equal("status.pendingReplica.name"),
				})),
				PointTo(MatchFields(IgnoreExtras, Fields{
					"Type":  Equal(field.ErrorTypeNotSupported),
					"Field": Equal("status.pendingReplica.reason"),
				})),
				PointTo(MatchFields(IgnoreExtras, Fields{
					"Type":  Equal(field.ErrorTypeInvalid),
					"Field": Equal("status.pendingReplica.retries"),
				})),
			))
		})
	})
})

func updateStrategyTypePtr(v seedmanagement.UpdateStrategyType) *seedmanagement.UpdateStrategyType {
	return &v
}
