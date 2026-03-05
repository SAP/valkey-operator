/*
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and valkey-operator contributors
SPDX-License-Identifier: Apache-2.0
*/

package transformer

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/sap/valkey-operator/internal/tlsutil"
)

type objectTransformer struct {
	client client.Client
}

func NewObjectTransformer(k8sClient client.Client) *objectTransformer {
	return &objectTransformer{client: k8sClient}
}

func (t *objectTransformer) TransformObjects(namespace string, name string, objects []client.Object) ([]client.Object, error) {
	if err := t.ensureTLSSecret(context.Background(), namespace, name, objects); err != nil {
		return nil, err
	}
	for i := 0; i < len(objects); i++ {
		if statefulSet := asStatefulSet(objects[i]); statefulSet != nil {
			if len(statefulSet.Spec.Template.Spec.TopologySpreadConstraints) == 0 {
				statefulSet.Spec.Template.Spec.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{
					{
						MaxSkew:            1,
						TopologyKey:        "kubernetes.io/hostname",
						WhenUnsatisfiable:  corev1.ScheduleAnyway,
						NodeAffinityPolicy: &[]corev1.NodeInclusionPolicy{corev1.NodeInclusionPolicyHonor}[0],
						NodeTaintsPolicy:   &[]corev1.NodeInclusionPolicy{corev1.NodeInclusionPolicyHonor}[0],
					},
				}
			}
			for j := 0; j < len(statefulSet.Spec.Template.Spec.TopologySpreadConstraints); j++ {
				constraint := &statefulSet.Spec.Template.Spec.TopologySpreadConstraints[j]
				if constraint.LabelSelector == nil && len(constraint.MatchLabelKeys) == 0 {
					constraint.LabelSelector = statefulSet.Spec.Selector
					constraint.MatchLabelKeys = []string{"controller-revision-hash"}
				}
			}
			objects[i] = asUnstructurable(statefulSet)
		}
	}
	// TODO: set persistentVolumeClaimRetentionPolicy to Delete (available from 1.27; unless chart natively supports it)
	return objects, nil
}

func (t *objectTransformer) ensureTLSSecret(ctx context.Context, namespace, name string, objects []client.Object) error {
	secretName := tlsSecretName(name, objects)
	if secretName == "" {
		return nil
	}
	if secretName != fmt.Sprintf("valkey-%s-crt", name) {
		return nil
	}
	if t.client == nil {
		return fmt.Errorf("k8s client is nil")
	}

	tlsSecret := &corev1.Secret{}
	if err := t.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: secretName}, tlsSecret); err == nil {
		return nil
	} else if !apierrors.IsNotFound(err) {
		return err
	}

	serviceName := fmt.Sprintf("valkey-%s", name)
	caCert, tlsCert, tlsKey, err := tlsutil.GenerateSelfSignedCert(serviceName, namespace)
	if err != nil {
		return fmt.Errorf("failed to generate TLS certificate: %w", err)
	}

	tlsSecret = &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: namespace,
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"ca.crt":  caCert,
			"tls.crt": tlsCert,
			"tls.key": tlsKey,
		},
	}

	if err := t.client.Create(ctx, tlsSecret); err != nil {
		return fmt.Errorf("failed to create TLS secret: %w", err)
	}

	return nil
}

func tlsSecretName(name string, objects []client.Object) string {
	crtName := fmt.Sprintf("valkey-%s-crt", name)
	tlsName := fmt.Sprintf("valkey-%s-tls", name)
	for i := 0; i < len(objects); i++ {
		statefulSet := asStatefulSet(objects[i])
		if statefulSet == nil {
			continue
		}
		for _, volume := range statefulSet.Spec.Template.Spec.Volumes {
			if volume.Secret == nil {
				continue
			}
			if volume.Secret.SecretName == crtName || volume.Secret.SecretName == tlsName {
				return volume.Secret.SecretName
			}
		}
	}
	return ""
}

func asStatefulSet(object client.Object) *appsv1.StatefulSet {
	if statefulSet, ok := object.(*appsv1.StatefulSet); ok {
		return statefulSet
	}
	if object, ok := object.(*unstructured.Unstructured); ok && (object.GetObjectKind().GroupVersionKind() == schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "StatefulSet"}) {
		statefulSet := &appsv1.StatefulSet{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, statefulSet); err != nil {
			panic(err)
		}
		return statefulSet
	}
	return nil
}

func asUnstructurable(object client.Object) *unstructured.Unstructured {
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(object)
	if err != nil {
		panic(err)
	}
	return &unstructured.Unstructured{Object: m}
}
