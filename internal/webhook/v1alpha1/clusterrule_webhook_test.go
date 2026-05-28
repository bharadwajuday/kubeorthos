/*
Copyright 2026.

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

package v1alpha1

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	auditv1alpha1 "kubeorthos/api/v1alpha1"
)

var _ = Describe("ClusterRule Webhook", func() {
	var (
		ctx context.Context
	)

	BeforeEach(func() {
		ctx = context.Background()
	})

	Context("When creating ClusterRule under Validating Webhook", func() {
		It("Should admit creation if all fields are valid", func() {
			rule := &auditv1alpha1.ClusterRule{
				ObjectMeta: metav1.ObjectMeta{
					Name: "valid-rule",
				},
				Spec: auditv1alpha1.ClusterRuleSpec{
					Action: auditv1alpha1.ActionAudit,
					ExpectedNodeConfig: auditv1alpha1.ExpectedNodeConfig{
						KubeletVersion: "v1.31.0",
					},
					NodeSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"kubernetes.io/os": "linux",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, rule)).To(Succeed())

			// Clean up
			Expect(k8sClient.Delete(ctx, rule)).To(Succeed())
		})

		It("Should reject creation if NodeSelector is invalid", func() {
			rule := &auditv1alpha1.ClusterRule{
				ObjectMeta: metav1.ObjectMeta{
					Name: "invalid-selector-rule",
				},
				Spec: auditv1alpha1.ClusterRuleSpec{
					Action: auditv1alpha1.ActionAudit,
					ExpectedNodeConfig: auditv1alpha1.ExpectedNodeConfig{
						KubeletVersion: "v1.31.0",
					},
					NodeSelector: &metav1.LabelSelector{
						MatchExpressions: []metav1.LabelSelectorRequirement{
							{
								Key:      "invalid-op-key",
								Operator: "InvalidOperator",
							},
						},
					},
				},
			}
			err := k8sClient.Create(ctx, rule)
			Expect(err).To(HaveOccurred())
			statusErr, ok := err.(*apierrors.StatusError)
			Expect(ok).To(BeTrue(), "Expected a StatusError")
			Expect(statusErr.ErrStatus.Message).To(ContainSubstring("not a valid label selector operator"))
		})

		It("Should reject creation if ExpectedNodeConfig is empty", func() {
			rule := &auditv1alpha1.ClusterRule{
				ObjectMeta: metav1.ObjectMeta{
					Name: "empty-config-rule",
				},
				Spec: auditv1alpha1.ClusterRuleSpec{
					Action:             auditv1alpha1.ActionAudit,
					ExpectedNodeConfig: auditv1alpha1.ExpectedNodeConfig{
						// all fields empty
					},
				},
			}
			err := k8sClient.Create(ctx, rule)
			Expect(err).To(HaveOccurred())
			statusErr, ok := err.(*apierrors.StatusError)
			Expect(ok).To(BeTrue(), "Expected a StatusError")
			Expect(statusErr.ErrStatus.Message).To(ContainSubstring("expectedNodeConfig cannot be empty"))
		})
	})

	Context("When updating ClusterRule under Validating Webhook", func() {
		It("Should reject update if NodeSelector becomes invalid", func() {
			rule := &auditv1alpha1.ClusterRule{
				ObjectMeta: metav1.ObjectMeta{
					Name: "update-test-rule",
				},
				Spec: auditv1alpha1.ClusterRuleSpec{
					Action: auditv1alpha1.ActionAudit,
					ExpectedNodeConfig: auditv1alpha1.ExpectedNodeConfig{
						KubeletVersion: "v1.31.0",
					},
				},
			}
			Expect(k8sClient.Create(ctx, rule)).To(Succeed())

			// Update with invalid selector
			rule.Spec.NodeSelector = &metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{
						Key:      "invalid-op-key",
						Operator: "InvalidOperator",
					},
				},
			}
			err := k8sClient.Update(ctx, rule)
			Expect(err).To(HaveOccurred())
			statusErr, ok := err.(*apierrors.StatusError)
			Expect(ok).To(BeTrue(), "Expected a StatusError")
			Expect(statusErr.ErrStatus.Message).To(ContainSubstring("not a valid label selector operator"))

			// Clean up
			Expect(k8sClient.Delete(ctx, rule)).To(Succeed())
		})
	})
})
